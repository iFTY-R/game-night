package postgres

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeAuditQueries struct {
	readRow      sqlcgen.ReadAuditHeadRow
	readErr      error
	appendRow    sqlcgen.AppendAuditEventRow
	appendErr    error
	appendCalled bool
	listRows     []sqlcgen.AuditEventsRedacted
	listErr      error
}

func (fake *fakeAuditQueries) ReadAuditHead(context.Context, sqlcgen.ReadAuditHeadParams) (sqlcgen.ReadAuditHeadRow, error) {
	return fake.readRow, fake.readErr
}

func (*fakeAuditQueries) ReadAuditAnchor(context.Context, sqlcgen.ReadAuditAnchorParams) (sqlcgen.ReadAuditAnchorRow, error) {
	panic("unexpected ReadAuditAnchor")
}

func (fake *fakeAuditQueries) AppendAuditEvent(context.Context, sqlcgen.AppendAuditEventParams) (sqlcgen.AppendAuditEventRow, error) {
	fake.appendCalled = true
	return fake.appendRow, fake.appendErr
}

func (fake *fakeAuditQueries) ListAuditEvents(context.Context, sqlcgen.ListAuditEventsParams) ([]sqlcgen.AuditEventsRedacted, error) {
	return fake.listRows, fake.listErr
}

type repositoryAuditKeyring struct {
	private ed25519.PrivateKey
}

func newRepositoryAuditKeyring() *repositoryAuditKeyring {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	return &repositoryAuditKeyring{private: ed25519.NewKeyFromSeed(seed)}
}

func (*repositoryAuditKeyring) ActiveVersion() uint32 { return 1 }

func (keyring *repositoryAuditKeyring) Sign(payload []byte) (security.AuditSignature, error) {
	return security.AuditSignature{KeyVersion: 1, Value: ed25519.Sign(keyring.private, payload)}, nil
}

func (keyring *repositoryAuditKeyring) Verify(payload []byte, signature security.AuditSignature) bool {
	return signature.KeyVersion == 1 && ed25519.Verify(keyring.private.Public().(ed25519.PublicKey), payload, signature.Value)
}

func TestAuditRepositoryMapsHeadConflictWithoutLeakingDriverError(t *testing.T) {
	head, event, service := repositoryAuditFixture(t)
	privateError := &pgconn.PgError{Code: auditHeadConflictSQLState, Message: "private database detail"}
	repository := newAuditRepository(&fakeAuditQueries{appendErr: privateError}, service)

	_, err := repository.AppendEvent(context.Background(), audit.AppendRequest{ExpectedHead: head, Event: event})
	if !errors.Is(err, audit.ErrHeadConflict) {
		t.Fatalf("append error = %v, want head conflict", err)
	}
	if err.Error() == privateError.Error() {
		t.Fatal("repository leaked PostgreSQL diagnostic")
	}
}

func TestAuditRepositoryRejectsDatabaseAppendMismatch(t *testing.T) {
	head, event, service := repositoryAuditFixture(t)
	snapshot := event.Snapshot()
	repository := newAuditRepository(&fakeAuditQueries{appendRow: sqlcgen.AppendAuditEventRow{
		AppendedSequence: int64(snapshot.Event.Sequence + 1), AppendedHash: snapshot.EventHash.Bytes(),
	}}, service)

	if _, err := repository.AppendEvent(context.Background(), audit.AppendRequest{ExpectedHead: head, Event: event}); !errors.Is(err, audit.ErrIntegrity) {
		t.Fatalf("append mismatch error = %v, want integrity", err)
	}
}

func TestAuditRepositoryRejectsForgedSignatureBeforeQuery(t *testing.T) {
	head, event, service := repositoryAuditFixture(t)
	snapshot := event.Snapshot()
	snapshot.Signature[0] ^= 0xff
	forged, err := audit.RestoreSignedEvent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	queries := &fakeAuditQueries{}
	repository := newAuditRepository(queries, service)

	if _, err := repository.AppendEvent(context.Background(), audit.AppendRequest{ExpectedHead: head, Event: forged}); !errors.Is(err, audit.ErrIntegrity) {
		t.Fatalf("forged append error = %v, want integrity", err)
	}
	if queries.appendCalled {
		t.Fatal("forged event reached the database query")
	}
}

func TestAuditRepositoryListChecksRedundantColumns(t *testing.T) {
	_, event, service := repositoryAuditFixture(t)
	row := auditRow(event)
	row.ChainID = "tampered"
	repository := newAuditRepository(&fakeAuditQueries{listRows: []sqlcgen.AuditEventsRedacted{row}}, service)
	request, err := audit.NewListRequest(audit.ChainAdmin, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.List(context.Background(), request); !errors.Is(err, audit.ErrIntegrity) {
		t.Fatalf("list error = %v, want integrity", err)
	}

	row = auditRow(event)
	repository = newAuditRepository(&fakeAuditQueries{listRows: []sqlcgen.AuditEventsRedacted{row}}, service)
	events, err := repository.List(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || service.Verify(events[0]) != nil {
		t.Fatal("repository did not preserve a verifiable signed event")
	}
}

func TestAdminAuditChainAggregateIDMatchesOfflineResetContract(t *testing.T) {
	const expected = "9c26d493-92b3-59a5-a787-3a1a3df235aa"
	if actual := auditChainAggregateID(audit.ChainAdmin).String(); actual != expected {
		t.Fatalf("admin aggregate ID = %s, want %s", actual, expected)
	}
}

func repositoryAuditFixture(t testing.TB) (audit.Head, audit.SignedEvent, *audit.Service) {
	t.Helper()
	createdAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	head, err := audit.RestoreHead(audit.HeadSnapshot{
		ChainID: audit.ChainAdmin, Sequence: 0, Hash: audit.GenesisHash, UpdatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := audit.NewService(newRepositoryAuditKeyring())
	if err != nil {
		t.Fatal(err)
	}
	actorID := uuid.New()
	targetID := uuid.New()
	actor, err := audit.NewActor(audit.ActorAdmin, actorID.String())
	if err != nil {
		t.Fatal(err)
	}
	target, err := audit.NewTarget(audit.TargetUser, targetID.String())
	if err != nil {
		t.Fatal(err)
	}
	event, err := service.Prepare(head, audit.EventInput{
		EventID: uuid.New(), RequestID: "request-1", OccurredAt: createdAt.Add(time.Second),
		Actor: actor, Target: target, Action: audit.ActionUserSuspended, ReasonCode: "policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	return head, event, service
}

func auditRow(event audit.SignedEvent) sqlcgen.AuditEventsRedacted {
	snapshot := event.Snapshot()
	return sqlcgen.AuditEventsRedacted{
		ChainID: string(snapshot.Event.ChainID), Sequence: int64(snapshot.Event.Sequence),
		EventID: uuidToPG(snapshot.Event.EventID), PreviousHash: snapshot.Event.PreviousHash.Bytes(),
		CanonicalEvent: snapshot.CanonicalEvent, EventHash: snapshot.EventHash.Bytes(), Signature: snapshot.Signature,
		SigningKeyVersion: int32(snapshot.Event.SigningKeyVersion),
		CreatedAt:         pgtype.Timestamptz{Time: snapshot.Event.OccurredAt, Valid: true},
	}
}
