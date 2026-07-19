package postgres

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5/pgtype"
)

// persistenceRecoveryPHC is structurally valid test data; repository tests never authenticate this hash.
const persistenceRecoveryPHC = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestIdentityRecoveryAttemptConsumeCASPersistsCompleteRequestBinding(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	current := recoveryAttemptFixture(t, now)
	next := consumedRecoveryAttempt(
		t, current, now.Add(time.Minute), uuid.MustParse("a5000000-0000-4000-8000-000000000001"), 0x44,
	)
	nextSnapshot := next.Snapshot()

	queries := &recoveryAttemptQueryRecorder{consumeRow: recoveryAttemptSQLRow(nextSnapshot)}
	stored, err := (&identityRecoveryAttemptRepository{queries: queries}).ConsumeCAS(
		context.Background(), current, next,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(queries.consumeParams.RequestDigest, nextSnapshot.Binding.RequestDigest[:]) {
		t.Fatalf("persisted request digest = %x, want %x", queries.consumeParams.RequestDigest, nextSnapshot.Binding.RequestDigest)
	}
	if !reflect.DeepEqual(stored.Snapshot(), nextSnapshot) {
		t.Fatalf("stored attempt = %+v, want %+v", stored.Snapshot(), nextSnapshot)
	}
}

func TestIdentityRecoveryAttemptMappingRejectsDigestStateMismatch(t *testing.T) {
	current := recoveryAttemptFixture(t, time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC))
	row := recoveryAttemptSQLRow(current.Snapshot())
	row.RequestDigest = bytes.Repeat([]byte{0x44}, 32)
	if _, err := identityRecoveryAttemptFromRow(row); !errors.Is(err, identityDomain.ErrIdentityIntegrity) {
		t.Fatalf("active attempt with digest error = %v", err)
	}

	consumed := consumedRecoveryAttempt(t, current, current.Snapshot().CreatedAt.Add(time.Minute), uuid.New(), 0x55)
	row = recoveryAttemptSQLRow(consumed.Snapshot())
	row.RequestDigest = nil
	if _, err := identityRecoveryAttemptFromRow(row); !errors.Is(err, identityDomain.ErrIdentityIntegrity) {
		t.Fatalf("consumed attempt without digest error = %v", err)
	}
}

func TestIdentityRecoveryAttemptRepositoryPersistsNullThenConsumedDigest(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := time.Now().UTC().Truncate(time.Microsecond)
	current := recoveryAttemptFixture(t, now)
	snapshot := current.Snapshot()
	sourceSelector, err := identifier.NewSelector(bytes.Repeat([]byte{0x88}, identityDomain.RecoverySelectorBytes))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Pool.Exec(ctx, `
		INSERT INTO users (user_id, status, created_at, updated_at)
		VALUES ($1, 'onboarding', $2, $2)
	`, snapshot.Binding.UserID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Pool.Exec(ctx, `
		INSERT INTO user_recovery_credentials (
			recovery_credential_id, user_id, selector, secret_hash, version, status, created_at
		) VALUES ($1, $2, $3, $4, 1, 'active', $5)
	`, snapshot.Binding.RecoveryCredentialID, snapshot.Binding.UserID, sourceSelector.Value(), persistenceRecoveryPHC, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Pool.Exec(ctx, `
		INSERT INTO anonymous_challenges (
			challenge_id, selector, secret_hash, secret_key_version, purpose, audience,
			origin_hash, request_flow_id, max_attempts, created_at, expires_at
		) VALUES (
			$1, 'integration-recovery-challenge', decode(repeat('11', 32), 'hex'), 1,
			'identity.recovery', 'identity.v1.IdentityService', $2, 'integration-recovery-flow', 5, $3, $4
		)
	`, snapshot.Binding.ChallengeID, snapshot.Binding.Origin[:], now, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	unitOfWork := NewIdentityUnitOfWork(fixture.Pool)
	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		_, insertErr := transaction.RecoveryAttempts().Insert(ctx, current)
		return insertErr
	}); err != nil {
		t.Fatal(err)
	}
	var initialDigest []byte
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT request_digest FROM user_recovery_attempts WHERE recovery_attempt_id = $1
	`, snapshot.ID).Scan(&initialDigest); err != nil {
		t.Fatal(err)
	}
	if initialDigest != nil {
		t.Fatalf("begin request digest = %x, want NULL", initialDigest)
	}

	resultBinding := integrationResultBinding(t, snapshot.Binding.UserID, 0x66)
	result := integrationAvailableResult(t, uuid.New(), resultBinding, now, now.Add(4*time.Minute))
	next := consumedRecoveryAttempt(t, current, now.Add(time.Minute), result.Snapshot().ID, 0x77)
	// UnitOfWork normalizes domain errors, so retain the active repository step for actionable failures.
	failedStep := "get recovery credential"
	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		source, getErr := transaction.RecoveryCredentials().GetForUpdate(
			ctx, snapshot.Binding.RecoveryCredentialID, snapshot.Binding.UserID, snapshot.Binding.RecoveryCredentialVersion,
		)
		if getErr != nil {
			return getErr
		}
		failedStep = "consume recovery credential domain state"
		consumedSource, getErr := source.Consume(now.Add(time.Minute))
		if getErr != nil {
			return getErr
		}
		failedStep = "persist consumed recovery credential"
		if _, getErr = transaction.RecoveryCredentials().ConsumeCAS(ctx, source, consumedSource); getErr != nil {
			return getErr
		}
		failedStep = "lock recovery attempt"
		locked, getErr := transaction.RecoveryAttempts().GetForUpdate(ctx, snapshot.Selector)
		if getErr != nil {
			return getErr
		}
		failedStep = "insert recovery result"
		if _, getErr = transaction.SecretResults().InsertAvailable(ctx, result); getErr != nil {
			return getErr
		}
		failedStep = "persist consumed recovery attempt"
		_, getErr = transaction.RecoveryAttempts().ConsumeCAS(ctx, locked, next)
		return getErr
	}); err != nil {
		t.Fatalf("%s: %v", failedStep, err)
	}
	var consumedDigest []byte
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT request_digest FROM user_recovery_attempts WHERE recovery_attempt_id = $1
	`, snapshot.ID).Scan(&consumedDigest); err != nil {
		t.Fatal(err)
	}
	consumedSnapshot := next.Snapshot()
	if !bytes.Equal(consumedDigest, consumedSnapshot.Binding.RequestDigest[:]) {
		t.Fatalf("consumed request digest = %x, want %x", consumedDigest, consumedSnapshot.Binding.RequestDigest)
	}
}

func consumedRecoveryAttempt(
	t testing.TB,
	current identityDomain.RecoveryAttempt,
	consumedAt time.Time,
	resultID uuid.UUID,
	digestByte byte,
) identityDomain.RecoveryAttempt {
	t.Helper()
	snapshot := current.Snapshot()
	snapshot.Binding.RequestDigestSet = true
	for index := range snapshot.Binding.RequestDigest {
		snapshot.Binding.RequestDigest[index] = digestByte
	}
	snapshot.Status = identityDomain.RecoveryAttemptConsumed
	snapshot.ConsumedAt = consumedAt
	snapshot.ResultID = resultID
	next, err := identityDomain.RestoreRecoveryAttempt(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func recoveryAttemptFixture(t testing.TB, now time.Time) identityDomain.RecoveryAttempt {
	t.Helper()
	selector, err := identifier.NewSelector(bytes.Repeat([]byte{0x11}, identityDomain.RecoveryGrantSelectorBytes))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := identityDomain.RestoreRecoveryAttempt(identityDomain.RecoveryAttemptSnapshot{
		ID:       uuid.MustParse("a4000000-0000-4000-8000-000000000001"),
		Selector: selector,
		GrantMAC: security.MAC[security.UserChallengeKeyPurpose]{KeyVersion: 1, Value: bytes.Repeat([]byte{0x22}, 32)},
		Binding: identityDomain.RecoveryAttemptBinding{
			UserID:                    uuid.MustParse("a1000000-0000-4000-8000-000000000001"),
			ChallengeID:               uuid.MustParse("a3000000-0000-4000-8000-000000000001"),
			Origin:                    challenge.OriginDigest(bytes.Repeat([]byte{0x33}, 32)),
			RecoveryCredentialID:      uuid.MustParse("a2000000-0000-4000-8000-000000000001"),
			RecoveryCredentialVersion: 1,
		},
		MaxAttempts: identityDomain.RecoveryAttemptMaxAttempts,
		Status:      identityDomain.RecoveryAttemptActive,
		CreatedAt:   now,
		ExpiresAt:   now.Add(identityDomain.RecoveryAttemptTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func recoveryAttemptSQLRow(snapshot identityDomain.RecoveryAttemptSnapshot) sqlcgen.UserRecoveryAttempt {
	row := sqlcgen.UserRecoveryAttempt{
		RecoveryAttemptID:         uuidToPG(snapshot.ID),
		GrantSelector:             snapshot.Selector.Value(),
		GrantSecretHash:           snapshot.GrantMAC.Value,
		GrantKeyVersion:           int32(snapshot.GrantMAC.KeyVersion),
		UserID:                    uuidToPG(snapshot.Binding.UserID),
		RecoveryCredentialID:      uuidToPG(snapshot.Binding.RecoveryCredentialID),
		RecoveryCredentialVersion: pgtype.Int8{Int64: int64(snapshot.Binding.RecoveryCredentialVersion), Valid: true},
		ChallengeID:               uuidToPG(snapshot.Binding.ChallengeID),
		OriginHash:                snapshot.Binding.Origin[:],
		Purpose:                   identityRecoveryAttemptPurpose,
		AttemptCount:              int32(snapshot.AttemptCount),
		MaxAttempts:               int32(snapshot.MaxAttempts),
		Status:                    string(snapshot.Status),
		CreatedAt:                 timeToPG(snapshot.CreatedAt),
		ExpiresAt:                 timeToPG(snapshot.ExpiresAt),
		ConsumedAt:                timeToPG(snapshot.ConsumedAt),
		RevokedAt:                 timeToPG(snapshot.RevokedAt),
		ResultID:                  uuidToPG(snapshot.ResultID),
	}
	if snapshot.Binding.RequestDigestSet {
		row.RequestDigest = bytes.Clone(snapshot.Binding.RequestDigest[:])
	}
	return row
}

type recoveryAttemptQueryRecorder struct {
	consumeParams sqlcgen.ConsumeUserRecoveryAttemptCASParams
	consumeRow    sqlcgen.UserRecoveryAttempt
}

func (*recoveryAttemptQueryRecorder) CreateUserRecoveryAttempt(
	context.Context, sqlcgen.CreateUserRecoveryAttemptParams,
) (sqlcgen.UserRecoveryAttempt, error) {
	panic("unexpected CreateUserRecoveryAttempt call")
}

func (*recoveryAttemptQueryRecorder) GetUserRecoveryAttemptBySelector(
	context.Context, sqlcgen.GetUserRecoveryAttemptBySelectorParams,
) (sqlcgen.UserRecoveryAttempt, error) {
	panic("unexpected GetUserRecoveryAttemptBySelector call")
}

func (*recoveryAttemptQueryRecorder) GetUserRecoveryAttemptForUpdate(
	context.Context, sqlcgen.GetUserRecoveryAttemptForUpdateParams,
) (sqlcgen.GetUserRecoveryAttemptForUpdateRow, error) {
	panic("unexpected GetUserRecoveryAttemptForUpdate call")
}

func (*recoveryAttemptQueryRecorder) RecordUserRecoveryAttemptFailureCAS(
	context.Context, sqlcgen.RecordUserRecoveryAttemptFailureCASParams,
) (sqlcgen.UserRecoveryAttempt, error) {
	panic("unexpected RecordUserRecoveryAttemptFailureCAS call")
}

func (queries *recoveryAttemptQueryRecorder) ConsumeUserRecoveryAttemptCAS(
	_ context.Context,
	params sqlcgen.ConsumeUserRecoveryAttemptCASParams,
) (sqlcgen.UserRecoveryAttempt, error) {
	queries.consumeParams = params
	return queries.consumeRow, nil
}

func (*recoveryAttemptQueryRecorder) RevokeUserRecoveryAttemptCAS(
	context.Context, sqlcgen.RevokeUserRecoveryAttemptCASParams,
) (sqlcgen.UserRecoveryAttempt, error) {
	panic("unexpected RevokeUserRecoveryAttemptCAS call")
}

var _ identityRecoveryAttemptQueries = (*recoveryAttemptQueryRecorder)(nil)
