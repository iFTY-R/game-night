package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/idempotency"
)

const (
	// RepairCommandVersion lets future repair plan formats fail closed instead of reusing stale semantics.
	RepairCommandVersion uint64 = 1
	// DefaultRepairPreviewTTL bounds how long an operator can execute a dry-run snapshot.
	DefaultRepairPreviewTTL = 10 * time.Minute
)

// PreviewEmergencyRepairCommand selects one fixed repair family and target for server-side dry-run planning.
type PreviewEmergencyRepairCommand struct {
	TargetID   uuid.UUID
	RepairType RepairType
	Reason     string
}

// ExecuteEmergencyRepairCommand carries the immutable preview version plus one idempotent execution operation.
type ExecuteEmergencyRepairCommand struct {
	RepairID              uuid.UUID
	OperationID           idempotency.OperationID
	ExpectedRepairVersion uint64
	Reason                string
}

// PreviewEmergencyRepair creates a persisted dry-run plan for exactly one supported repair family.
func (service *Service) PreviewEmergencyRepair(ctx context.Context, actor admin.ActorContext, command PreviewEmergencyRepairCommand) (RepairOperation, error) {
	if service == nil || service.repository == nil || service.repairs == nil || service.clock == nil || ctx == nil ||
		command.TargetID == uuid.Nil || !validRepairType(command.RepairType) || !validRepairReason(command.Reason) {
		return RepairOperation{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionGamesRepair); err != nil {
		return RepairOperation{}, ErrPermissionDenied
	}
	now := service.clock.Now()
	plan, err := service.buildRepairPlan(ctx, actor.AdminID(), command, now)
	if err != nil {
		return RepairOperation{}, err
	}
	return service.repairs.CreateRepairOperation(ctx, CreateRepairOperationCommand{RepairOperation: plan})
}

// ExecuteEmergencyRepair revalidates elevation, repair version, TTL, and idempotency before side effects run.
func (service *Service) ExecuteEmergencyRepair(ctx context.Context, actor admin.ActorContext, command ExecuteEmergencyRepairCommand) (RepairOperation, error) {
	if service == nil || service.repairs == nil || service.clock == nil || ctx == nil ||
		command.RepairID == uuid.Nil || !command.OperationID.Valid() || command.ExpectedRepairVersion == 0 || !validRepairReason(command.Reason) {
		return RepairOperation{}, ErrInvalidInput
	}
	now := service.clock.Now()
	if err := actor.Require(admin.PermissionGamesRepair); err != nil {
		return RepairOperation{}, ErrPermissionDenied
	}
	if err := actor.RequireElevation(admin.ElevationScopeGamesEmergencyRepair, now); err != nil {
		return RepairOperation{}, ErrPermissionDenied
	}
	repair, err := service.repairs.GetRepairOperation(ctx, command.RepairID)
	if err != nil {
		return RepairOperation{}, err
	}
	requestDigest := repairExecuteDigest(repair, command)
	if repair.State == RepairStateExecuted {
		if repair.OperationID == command.OperationID.Value() && sameBytes(repair.RequestDigest, requestDigest[:]) {
			return repair, nil
		}
		return RepairOperation{}, ErrConflict
	}
	if repair.State != RepairStatePreviewed || repair.Version != command.ExpectedRepairVersion || !now.Before(repair.ExpiresAt) {
		return RepairOperation{}, ErrConflict
	}
	afterDigest, err := service.executeRepairEffect(ctx, repair, command, now)
	if err != nil {
		return RepairOperation{}, err
	}
	auditEventID, err := uuid.NewV7()
	if err != nil {
		return RepairOperation{}, ErrInvalidInput
	}
	return service.repairs.CompleteRepairOperation(ctx, CompleteRepairOperationCommand{
		RepairID: command.RepairID, OperationID: command.OperationID.Value(), RequestDigest: requestDigest[:],
		AuditEventID: auditEventID, AfterSnapshotDigest: afterDigest, Reason: command.Reason,
		State: RepairStateExecuted, ExpectedVersion: command.ExpectedRepairVersion, ExecutedAt: now,
	})
}

func (service *Service) executeRepairEffect(ctx context.Context, repair RepairOperation, command ExecuteEmergencyRepairCommand, now time.Time) ([]byte, error) {
	if repair.RepairType == RepairClearStaleOwnerLease {
		return service.executeClearStaleOwnerLease(ctx, repair, now)
	}
	if service.executor == nil {
		return nil, ErrInvalidInput
	}
	return service.executor.ExecuteEmergencyRepair(ctx, repair, command)
}

func (service *Service) executeClearStaleOwnerLease(ctx context.Context, repair RepairOperation, now time.Time) ([]byte, error) {
	if service.owners == nil || service.ownerFixes == nil || repair.TargetKind != RepairTargetKindGameSession {
		return nil, ErrInvalidInput
	}
	detail, err := service.repairGameDetail(ctx, repair.TargetID, now)
	if err != nil {
		return nil, err
	}
	game := detail.Summary
	owner := game.Owner
	currentDigest := stableDigest("game", game.SessionID.String(), game.Status, owner.Freshness, owner.OwnerInstance, owner.OwnerAddress, owner.OwnershipEpoch)
	if !sameBytes(currentDigest[:], repair.TargetDigest) {
		return nil, ErrConflict
	}
	cleared, err := service.ownerFixes.ClearStaleOwnerLease(ctx, owner)
	if err != nil {
		return nil, err
	}
	if !cleared {
		return nil, ErrConflict
	}
	afterDigest := stableDigest("owner-cleared", repair.TargetID.String(), repair.ExpectedOwnershipEpoch)
	return afterDigest[:], nil
}

// GetRepairOperation exposes the server-side repair plan without allowing the caller to mutate its after-state.
func (service *Service) GetRepairOperation(ctx context.Context, actor admin.ActorContext, repairID uuid.UUID) (RepairOperation, error) {
	if service == nil || service.repairs == nil || ctx == nil || repairID == uuid.Nil {
		return RepairOperation{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionGamesRepair); err != nil {
		return RepairOperation{}, ErrPermissionDenied
	}
	return service.repairs.GetRepairOperation(ctx, repairID)
}

func (service *Service) buildRepairPlan(ctx context.Context, adminID uuid.UUID, command PreviewEmergencyRepairCommand, now time.Time) (RepairOperation, error) {
	switch command.RepairType {
	case RepairClearStaleOwnerLease:
		return service.clearStaleOwnerLeasePlan(ctx, adminID, command, now)
	case RepairTerminateUnrecoverable:
		return service.terminateUnrecoverablePlan(ctx, adminID, command, now)
	case RepairRepairRoomGameLink:
		return service.repairRoomGameLinkPlan(ctx, adminID, command, now)
	default:
		return RepairOperation{}, ErrInvalidInput
	}
}

func (service *Service) clearStaleOwnerLeasePlan(ctx context.Context, adminID uuid.UUID, command PreviewEmergencyRepairCommand, now time.Time) (RepairOperation, error) {
	detail, err := service.repairGameDetail(ctx, command.TargetID, now)
	if err != nil {
		return RepairOperation{}, err
	}
	game := detail.Summary
	if game.Owner.Freshness != OwnerFreshnessStale && game.Owner.Freshness != OwnerFreshnessExpired ||
		game.Owner.OwnershipEpoch != game.OwnershipEpoch || game.Owner.OwnerInstance == "" || game.Owner.OwnerAddress == "" {
		return RepairOperation{}, ErrConflict
	}
	return newRepairOperation(adminID, command, RepairTargetKindGameSession, game.StateVersion, game.OwnershipEpoch, 0, 0,
		"clear stale realtime owner lease", []string{"delete the matching Redis owner lease only if owner/address/epoch still match"},
		stableDigest("game", game.SessionID.String(), game.Status, game.Owner.Freshness, game.Owner.OwnerInstance, game.Owner.OwnerAddress, game.Owner.OwnershipEpoch),
		now), nil
}

func (service *Service) terminateUnrecoverablePlan(ctx context.Context, adminID uuid.UUID, command PreviewEmergencyRepairCommand, now time.Time) (RepairOperation, error) {
	detail, err := service.repairGameDetail(ctx, command.TargetID, now)
	if err != nil {
		return RepairOperation{}, err
	}
	game := detail.Summary
	if game.Status != "active" && game.Status != "suspended" {
		return RepairOperation{}, ErrConflict
	}
	return newRepairOperation(adminID, command, RepairTargetKindGameSession, game.StateVersion, game.OwnershipEpoch, 0, 0,
		"terminate unrecoverable active game session", []string{"commit a reviewed terminal cancellation through the repair executor"},
		stableDigest("game", game.SessionID.String(), game.RoomID.String(), game.Status, game.StateVersion, game.OwnershipEpoch),
		now), nil
}

func (service *Service) repairRoomGameLinkPlan(ctx context.Context, adminID uuid.UUID, command PreviewEmergencyRepairCommand, now time.Time) (RepairOperation, error) {
	detail, err := service.repository.GetRoom(ctx, command.TargetID)
	if err != nil {
		return RepairOperation{}, err
	}
	room := detail.Summary
	if !hasRoomAnomaly(room.Anomalies, RoomAnomalyGameLinkMismatch) {
		return RepairOperation{}, ErrConflict
	}
	return newRepairOperation(adminID, command, RepairTargetKindRoom, 0, room.OwnershipEpoch, room.RoomVersion, room.MembershipVersion,
		"repair inconsistent room and game-session link", []string{"update only the reviewed room/session link fields via CAS"},
		stableDigest("room", room.RoomID.String(), room.ActiveSessionID.String(), room.ActiveGameID, room.Status, room.RoomVersion, room.MembershipVersion),
		now), nil
}

func (service *Service) repairGameDetail(ctx context.Context, sessionID uuid.UUID, sampledAt time.Time) (GameDetail, error) {
	detail, err := service.repository.GetGame(ctx, sessionID)
	if err != nil {
		return GameDetail{}, err
	}
	games := service.enrichGames(ctx, []GameSummary{detail.Summary}, sampledAt)
	detail.Summary = games[0]
	detail.SampledAt = sampledAt
	return detail, nil
}

func newRepairOperation(adminID uuid.UUID, command PreviewEmergencyRepairCommand, targetKind string, stateVersion, ownershipEpoch, roomVersion, membershipVersion uint64, summary string, effects []string, beforeDigest [sha256.Size]byte, now time.Time) RepairOperation {
	repairID, _ := uuid.NewV7()
	previewDigest := stableDigest("preview", string(command.RepairType), command.TargetID.String(), targetKind, RepairCommandVersion, stateVersion, ownershipEpoch, roomVersion, membershipVersion, hex.EncodeToString(beforeDigest[:]))
	return RepairOperation{
		RepairID: repairID, RepairType: command.RepairType, State: RepairStatePreviewed,
		TargetID: command.TargetID, TargetKind: targetKind, TargetDigest: beforeDigest[:], PreviewDigest: previewDigest[:],
		CommandVersion: RepairCommandVersion, ExpectedRoomVersion: roomVersion, ExpectedMembershipVersion: membershipVersion,
		ExpectedStateVersion: stateVersion, ExpectedOwnershipEpoch: ownershipEpoch, Summary: summary,
		IrreversibleEffects: append([]string(nil), effects...), BeforeSnapshotDigest: beforeDigest[:],
		RequestedByAdminID: adminID, Reason: strings.TrimSpace(command.Reason), Version: 1,
		CreatedAt: now, ExpiresAt: now.Add(DefaultRepairPreviewTTL),
	}
}

func repairExecuteDigest(repair RepairOperation, command ExecuteEmergencyRepairCommand) [sha256.Size]byte {
	return stableDigest("execute", repair.RepairID.String(), command.ExpectedRepairVersion, command.OperationID.Value(), strings.TrimSpace(command.Reason), hex.EncodeToString(repair.PreviewDigest))
}

func stableDigest(values ...any) [sha256.Size]byte {
	hasher := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(hasher, "%d:%v;", len(fmt.Sprint(value)), value)
	}
	return sha256.Sum256(hasher.Sum(nil))
}

func validRepairType(value RepairType) bool {
	return value == RepairClearStaleOwnerLease || value == RepairTerminateUnrecoverable || value == RepairRepairRoomGameLink
}

func validRepairReason(reason string) bool {
	trimmed := strings.TrimSpace(reason)
	length := utf8.RuneCountInString(trimmed)
	return utf8.ValidString(trimmed) && length >= 1 && length <= 512
}

func hasRoomAnomaly(values []RoomAnomalyFlag, target RoomAnomalyFlag) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sameBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
