package room

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestRepairServicePreviewCreatesImmutableDryRunPlan(t *testing.T) {
	now := time.Date(2026, 7, 26, 18, 0, 0, 0, time.UTC)
	targetID := uuid.New()
	repository := &memoryRepairRepository{
		games: map[uuid.UUID]GameDetail{targetID: {
			Summary: GameSummary{
				SessionID: targetID, RoomID: uuid.New(), Status: "active", StateVersion: 11, OwnershipEpoch: 7,
				Owner: OwnerLeaseSummary{
					SessionID: targetID, OwnerInstance: "realtime-a", OwnerAddress: "http://realtime-a.internal:8091",
					OwnershipEpoch: 7, Freshness: OwnerFreshnessExpired, ObservedAt: now, ExpiresAt: now.Add(-time.Second),
				},
			},
		}},
		repairs: map[uuid.UUID]RepairOperation{},
	}
	service := newRepairService(t, repository, nil, now)
	actor := newRoomTestActor(t, now, admin.PermissionGamesRepair, admin.PermissionGamesRead)

	repair, err := service.PreviewEmergencyRepair(context.Background(), actor, PreviewEmergencyRepairCommand{
		TargetID: targetID, RepairType: RepairClearStaleOwnerLease, Reason: "owner lease expired during operator investigation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repair.RepairType != RepairClearStaleOwnerLease || repair.State != RepairStatePreviewed ||
		repair.TargetKind != RepairTargetKindGameSession || repair.ExpectedStateVersion != 11 ||
		repair.ExpectedOwnershipEpoch != 7 || repair.ExpiresAt.Sub(repair.CreatedAt) != DefaultRepairPreviewTTL ||
		len(repair.PreviewDigest) != idempotency.DigestSize || repository.executeCalls != 0 {
		t.Fatalf("repair=%+v execute calls=%d", repair, repository.executeCalls)
	}
}

func TestRepairServicePreviewSupportsTerminateAndLinkRepairPlans(t *testing.T) {
	now := time.Date(2026, 7, 26, 18, 30, 0, 0, time.UTC)
	sessionID, roomID := uuid.New(), uuid.New()
	repository := &memoryRepairRepository{
		games: map[uuid.UUID]GameDetail{sessionID: {
			Summary: GameSummary{SessionID: sessionID, RoomID: roomID, Status: "suspended", StateVersion: 12, OwnershipEpoch: 8},
		}},
		rooms: map[uuid.UUID]RoomDetail{roomID: {
			Summary: RoomSummary{
				RoomID: roomID, Status: "playing", ActiveSessionID: sessionID, ActiveGameID: "liars-dice",
				RoomVersion: 5, MembershipVersion: 4, OwnershipEpoch: 8,
				Anomalies: []RoomAnomalyFlag{RoomAnomalyGameLinkMismatch},
			},
		}},
		repairs: map[uuid.UUID]RepairOperation{},
	}
	service := newRepairService(t, repository, nil, now)
	actor := newRoomTestActor(t, now, admin.PermissionGamesRepair, admin.PermissionGamesRead, admin.PermissionRoomsRead)

	terminate, err := service.PreviewEmergencyRepair(context.Background(), actor, PreviewEmergencyRepairCommand{
		TargetID: sessionID, RepairType: RepairTerminateUnrecoverable, Reason: "owner cannot recover suspended session",
	})
	if err != nil {
		t.Fatal(err)
	}
	link, err := service.PreviewEmergencyRepair(context.Background(), actor, PreviewEmergencyRepairCommand{
		TargetID: roomID, RepairType: RepairRepairRoomGameLink, Reason: "room points at inconsistent active game session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if terminate.ExpectedStateVersion != 12 || terminate.TargetKind != RepairTargetKindGameSession ||
		link.ExpectedRoomVersion != 5 || link.ExpectedMembershipVersion != 4 || link.TargetKind != RepairTargetKindRoom {
		t.Fatalf("terminate=%+v link=%+v", terminate, link)
	}
}

func TestRepairServiceExecuteRequiresEmergencyElevation(t *testing.T) {
	now := time.Date(2026, 7, 26, 19, 0, 0, 0, time.UTC)
	repairID := uuid.New()
	repository := &memoryRepairRepository{repairs: map[uuid.UUID]RepairOperation{
		repairID: newMemoryRepair(repairID, uuid.New(), now),
	}}
	service := newRepairService(t, repository, repository, now)
	actor := newRoomTestActor(t, now, admin.PermissionGamesRepair)

	_, err := service.ExecuteEmergencyRepair(context.Background(), actor, ExecuteEmergencyRepairCommand{
		RepairID: repairID, OperationID: repairOperationID(t, 1), ExpectedRepairVersion: 1,
		Reason: "execute reviewed emergency repair",
	})
	if !errors.Is(err, ErrPermissionDenied) || repository.executeCalls != 0 {
		t.Fatalf("execute error=%v calls=%d", err, repository.executeCalls)
	}
}

func TestRepairServiceExecuteIsVersionedAndIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	repairID := uuid.New()
	repository := &memoryRepairRepository{repairs: map[uuid.UUID]RepairOperation{
		repairID: newMemoryRepair(repairID, uuid.New(), now),
	}}
	service := newRepairService(t, repository, repository, now)
	actor := newElevatedRepairActor(t, now)
	command := ExecuteEmergencyRepairCommand{
		RepairID: repairID, OperationID: repairOperationID(t, 2), ExpectedRepairVersion: 1,
		Reason: "execute reviewed emergency repair",
	}

	executed, err := service.ExecuteEmergencyRepair(context.Background(), actor, command)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.ExecuteEmergencyRepair(context.Background(), actor, command)
	if err != nil {
		t.Fatal(err)
	}
	if executed.State != RepairStateExecuted || replayed.RepairID != executed.RepairID ||
		repository.executeCalls != 1 || len(executed.AfterSnapshotDigest) != idempotency.DigestSize ||
		executed.OperationID != command.OperationID.Value() {
		t.Fatalf("executed=%+v replayed=%+v calls=%d", executed, replayed, repository.executeCalls)
	}

	conflict := command
	conflict.OperationID = repairOperationID(t, 3)
	if _, err = service.ExecuteEmergencyRepair(context.Background(), actor, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
}

func TestRepairServiceExecuteRejectsExpiredPreview(t *testing.T) {
	now := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	repairID := uuid.New()
	expired := newMemoryRepair(repairID, uuid.New(), now.Add(-2*DefaultRepairPreviewTTL))
	repository := &memoryRepairRepository{repairs: map[uuid.UUID]RepairOperation{repairID: expired}}
	service := newRepairService(t, repository, repository, now)
	actor := newElevatedRepairActor(t, now)

	_, err := service.ExecuteEmergencyRepair(context.Background(), actor, ExecuteEmergencyRepairCommand{
		RepairID: repairID, OperationID: repairOperationID(t, 4), ExpectedRepairVersion: expired.Version,
		Reason: "execute expired emergency repair",
	})
	if !errors.Is(err, ErrConflict) || repository.executeCalls != 0 {
		t.Fatalf("expired error=%v calls=%d", err, repository.executeCalls)
	}
}

type memoryRepairRepository struct {
	games        map[uuid.UUID]GameDetail
	rooms        map[uuid.UUID]RoomDetail
	repairs      map[uuid.UUID]RepairOperation
	executeCalls int
}

func (repository *memoryRepairRepository) ListRooms(context.Context, RoomListQuery) ([]RoomSummary, error) {
	return nil, nil
}

func (repository *memoryRepairRepository) GetRoom(_ context.Context, roomID uuid.UUID) (RoomDetail, error) {
	detail, ok := repository.rooms[roomID]
	if !ok {
		return RoomDetail{}, ErrNotFound
	}
	return detail, nil
}

func (repository *memoryRepairRepository) ListGames(context.Context, GameListQuery) ([]GameSummary, error) {
	return nil, nil
}

func (repository *memoryRepairRepository) GetGame(_ context.Context, sessionID uuid.UUID) (GameDetail, error) {
	detail, ok := repository.games[sessionID]
	if !ok {
		return GameDetail{}, ErrNotFound
	}
	return detail, nil
}

func (repository *memoryRepairRepository) ReadOwners(_ context.Context, sessionIDs []uuid.UUID, _ time.Time) (map[uuid.UUID]OwnerLeaseSummary, error) {
	owners := make(map[uuid.UUID]OwnerLeaseSummary, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if detail, ok := repository.games[sessionID]; ok {
			owners[sessionID] = detail.Summary.Owner
		}
	}
	return owners, nil
}

func (repository *memoryRepairRepository) CreateRepairOperation(_ context.Context, command CreateRepairOperationCommand) (RepairOperation, error) {
	if repository.repairs == nil {
		repository.repairs = map[uuid.UUID]RepairOperation{}
	}
	repository.repairs[command.RepairID] = command.RepairOperation
	return command.RepairOperation, nil
}

func (repository *memoryRepairRepository) GetRepairOperation(_ context.Context, repairID uuid.UUID) (RepairOperation, error) {
	repair, ok := repository.repairs[repairID]
	if !ok {
		return RepairOperation{}, ErrNotFound
	}
	return repair, nil
}

func (repository *memoryRepairRepository) ExpireRepairOperation(_ context.Context, repairID uuid.UUID, expectedVersion uint64) (RepairOperation, error) {
	repair, ok := repository.repairs[repairID]
	if !ok || repair.Version != expectedVersion || repair.State != RepairStatePreviewed {
		return RepairOperation{}, ErrConflict
	}
	repair.State = RepairStateExpired
	repair.Version++
	repository.repairs[repairID] = repair
	return repair, nil
}

func (repository *memoryRepairRepository) CompleteRepairOperation(_ context.Context, command CompleteRepairOperationCommand) (RepairOperation, error) {
	repair, ok := repository.repairs[command.RepairID]
	if !ok || repair.State != RepairStatePreviewed || repair.Version != command.ExpectedVersion {
		return RepairOperation{}, ErrConflict
	}
	repair.State = command.State
	repair.OperationID = command.OperationID
	repair.RequestDigest = append([]byte(nil), command.RequestDigest...)
	repair.AuditEventID = command.AuditEventID
	repair.AfterSnapshotDigest = append([]byte(nil), command.AfterSnapshotDigest...)
	repair.Reason = command.Reason
	repair.ExecutedAt = command.ExecutedAt
	repair.Version++
	repository.repairs[command.RepairID] = repair
	return repair, nil
}

func (repository *memoryRepairRepository) ExecuteEmergencyRepair(context.Context, RepairOperation, ExecuteEmergencyRepairCommand) ([]byte, error) {
	repository.executeCalls++
	digest := stableDigest("after", repository.executeCalls)
	return digest[:], nil
}

func newRepairService(t testing.TB, repository *memoryRepairRepository, executor EmergencyRepairExecutor, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Config{
		Repository: repository, Repairs: repository, Executor: executor, Owners: repository, Clock: clock.NewFake(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newMemoryRepair(repairID, targetID uuid.UUID, now time.Time) RepairOperation {
	before := stableDigest("before", targetID)
	preview := stableDigest("preview", repairID)
	return RepairOperation{
		RepairID: repairID, RepairType: RepairTerminateUnrecoverable, State: RepairStatePreviewed,
		TargetID: targetID, TargetKind: RepairTargetKindGameSession, TargetDigest: before[:], PreviewDigest: preview[:],
		CommandVersion: RepairCommandVersion, ExpectedStateVersion: 7, ExpectedOwnershipEpoch: 3,
		Summary: "terminate unrecoverable active game session", IrreversibleEffects: []string{"commit terminal cancellation"},
		BeforeSnapshotDigest: before[:], RequestedByAdminID: uuid.New(), Reason: "preview reviewed repair",
		Version: 1, CreatedAt: now, ExpiresAt: now.Add(DefaultRepairPreviewTTL),
	}
}

func newElevatedRepairActor(t testing.TB, now time.Time) admin.ActorContext {
	t.Helper()
	adminID, sessionID := uuid.New(), uuid.New()
	session, err := admin.RestoreSession(admin.SessionSnapshot{
		ID: sessionID, AdminID: adminID, Selector: base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		SecretMAC: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash:  security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		Kind:      admin.SessionKindFull, AdminVersion: 1, PasswordVersion: 1, SessionVersion: 1,
		ClientIP: "203.0.113.10", UserAgent: "admin-room-test", MaxAttempts: 5,
		CreatedAt: now.Add(-time.Minute), LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	elevation, err := admin.NewElevation(session, 0, admin.ElevationScopeGamesEmergencyRepair, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := admin.NewPermissionSet(admin.PermissionGamesRepair)
	if err != nil {
		t.Fatal(err)
	}
	elevations, err := admin.NewElevationSet(elevation)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := admin.NewActorContext(adminID, sessionID, session, permissions, elevations, 0, "repair-execute", "http://127.0.0.1:4174", "203.0.113.10", "admin-room-test")
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func repairOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	operationID, err := idempotency.NewOperationID([]byte{marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker})
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

var _ QueryRepository = (*memoryRepairRepository)(nil)
var _ RepairRepository = (*memoryRepairRepository)(nil)
var _ EmergencyRepairExecutor = (*memoryRepairRepository)(nil)
var _ OwnerReader = (*memoryRepairRepository)(nil)
