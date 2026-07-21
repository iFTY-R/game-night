package gameruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

var errTestSystemInboxCompletion = errors.New("injected system inbox completion failure")

func TestSystemInboxRetriesDurableReceiptWithoutRepeatingGameRule(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	_, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 41), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.clock.Advance(time.Second); err != nil {
		t.Fatal(err)
	}
	event, digest := systemInboxRevocationEvent(t, session, fixture.playerID, fixture.hostID, fixture.clock.Now())
	store := &runtimeSystemInboxStore{
		runtimeServiceAuthority: fixture.authority,
		record:                  restoreSystemInboxRecordForTest(t, event, digest),
		failCompletionOnce:      true,
	}
	executor := &runtimeSystemInboxExecutor{service: fixture.service, authority: fixture.authority}
	inbox, err := NewSystemInbox(fixture.registry, store, executor)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := inbox.Consume(t.Context(), event); !errors.Is(err, errTestSystemInboxCompletion) {
		t.Fatalf("first consume error=%v", err)
	}
	if fixture.module.systemCalls != 1 || store.record.Snapshot().Status != SystemInboxPending {
		t.Fatalf("system calls=%d record=%+v", fixture.module.systemCalls, store.record.Snapshot())
	}
	second, err := inbox.Consume(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.Record.Snapshot().Status != SystemInboxCompleted ||
		fixture.module.systemCalls != 1 || executor.handleCalls != 2 {
		t.Fatalf("second=%+v system calls=%d handle calls=%d", second, fixture.module.systemCalls, executor.handleCalls)
	}
	third, err := inbox.Consume(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Replayed || executor.handleCalls != 2 || fixture.module.systemCalls != 1 {
		t.Fatalf("third=%+v system calls=%d handle calls=%d", third, fixture.module.systemCalls, executor.handleCalls)
	}
}

func TestSystemInboxCompletesTerminalNoopWithoutOwnershipClaim(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	_, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 42), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	eventAt, err := fixture.clock.Advance(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	event, digest := systemInboxRevocationEvent(t, session, fixture.playerID, fixture.hostID, eventAt)
	cancelledAt, err := fixture.clock.Advance(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := session.Cancel(session.Snapshot().OwnershipEpoch, cancelledAt)
	if err != nil {
		t.Fatal(err)
	}
	fixture.authority.session = cancelled
	store := &runtimeSystemInboxStore{
		runtimeServiceAuthority: fixture.authority,
		record:                  restoreSystemInboxRecordForTest(t, event, digest),
	}
	executor := &runtimeSystemInboxExecutor{service: fixture.service, authority: fixture.authority}
	inbox, err := NewSystemInbox(fixture.registry, store, executor)
	if err != nil {
		t.Fatal(err)
	}

	result, err := inbox.Consume(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Record.Snapshot().Status != SystemInboxCompleted || executor.ensureCalls != 0 ||
		executor.handleCalls != 1 || fixture.module.systemCalls != 0 || fixture.authority.session.Snapshot().Status != StatusCancelled {
		t.Fatalf(
			"result=%+v ensure calls=%d handle calls=%d system calls=%d session=%+v",
			result, executor.ensureCalls, executor.handleCalls, fixture.module.systemCalls, fixture.authority.session.Snapshot(),
		)
	}
}

type runtimeSystemInboxStore struct {
	*runtimeServiceAuthority
	record             SystemInboxRecord
	failCompletionOnce bool
}

func (store *runtimeSystemInboxStore) GetSystemInbox(
	_ context.Context,
	key SystemInboxKey,
	digest idempotency.Digest,
) (SystemInboxRecord, error) {
	snapshot := store.record.Snapshot()
	if snapshot.Key != key {
		return SystemInboxRecord{}, ErrSystemInboxNotFound
	}
	if snapshot.PayloadDigest != digest {
		return SystemInboxRecord{}, idempotency.ErrConflict
	}
	return store.record, nil
}

func (store *runtimeSystemInboxStore) CompleteSystemInbox(
	_ context.Context,
	key SystemInboxKey,
	digest idempotency.Digest,
	stateVersion uint64,
	completedAt time.Time,
) (SystemInboxRecord, error) {
	if store.failCompletionOnce {
		store.failCompletionOnce = false
		return SystemInboxRecord{}, errTestSystemInboxCompletion
	}
	current := store.record.Snapshot()
	if current.Key != key || current.PayloadDigest != digest || current.Status != SystemInboxPending {
		return SystemInboxRecord{}, ErrGameSessionIntegrity
	}
	current.Status = SystemInboxCompleted
	current.CommittedStateVersion = stateVersion
	current.CompletedAt = completedAt
	record, err := RestoreSystemInboxRecord(current)
	if err != nil {
		return SystemInboxRecord{}, err
	}
	store.record = record
	return record, nil
}

type runtimeSystemInboxExecutor struct {
	service     *Service
	authority   *runtimeServiceAuthority
	ensureCalls int
	handleCalls int
}

func (executor *runtimeSystemInboxExecutor) EnsureOwned(_ context.Context, sessionID uuid.UUID) (uint64, error) {
	executor.ensureCalls++
	session, err := executor.authority.Get(context.Background(), sessionID)
	if err != nil {
		return 0, err
	}
	return session.Snapshot().OwnershipEpoch, nil
}

func (executor *runtimeSystemInboxExecutor) HandleSystem(ctx context.Context, command SystemCommand) (SystemCommitResult, error) {
	executor.handleCalls++
	return executor.service.HandleSystem(ctx, command)
}

func systemInboxRevocationEvent(
	t testing.TB,
	session Session,
	userID uuid.UUID,
	actorID uuid.UUID,
	at time.Time,
) (outbox.Event, idempotency.Digest) {
	t.Helper()
	event, err := roomDomain.NewParticipantRevokedEvent(roomDomain.ParticipantRevocationFact{
		EventID: uuid.New(), RoomID: session.Snapshot().RoomID, SessionID: session.Snapshot().ID, UserID: userID,
		ActorKind: roomDomain.RemovalActorHost, ActorID: actorID, Reason: roomDomain.RemovalReasonHostRemoved,
		MembershipVersion: 4, OccurredAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	digestBytes := sha256.Sum256(event.Snapshot().Payload)
	digest, err := idempotency.NewDigest(digestBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	return event, digest
}

func restoreSystemInboxRecordForTest(t testing.TB, event outbox.Event, digest idempotency.Digest) SystemInboxRecord {
	t.Helper()
	snapshot := event.Snapshot()
	record, err := RestoreSystemInboxRecord(SystemInboxSnapshot{
		Key:       SystemInboxKey{SessionID: snapshot.AggregateID, SourceEventID: snapshot.ID},
		EventType: snapshot.Type, PayloadDigest: digest, Status: SystemInboxPending, CreatedAt: snapshot.CreatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

var _ SystemInboxStore = (*runtimeSystemInboxStore)(nil)
var _ SystemInboxExecutor = (*runtimeSystemInboxExecutor)(nil)
