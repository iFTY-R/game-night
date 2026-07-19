package identity

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestRecoveryCodeIssuePersistsOnlyHashAndReturnsCanonicalSecret(t *testing.T) {
	hasher := &recordingRecoveryHasher{hash: testRecoveryPHC}
	service, err := NewRecoveryCodeService(hasher)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	issued, err := service.Issue(context.Background(), uuid.New(), now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := issued.Credential.Snapshot()
	if snapshot.ID.Version() != 7 || snapshot.Version != 1 || snapshot.Status != RecoveryCredentialActive ||
		snapshot.SecretHash != hasher.hash {
		t.Fatalf("unexpected recovery snapshot: %+v", snapshot)
	}
	parsed, err := security.ParseToken(issued.Code, security.TokenPolicy{
		Version: RecoveryCodeVersion, MinSecretBytes: RecoverySecretBytes, MaxSecretBytes: RecoverySecretBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Selector != snapshot.Selector.Value() || len(parsed.Secret) != RecoverySecretBytes {
		t.Fatalf("unexpected recovery code shape: selector=%q secret=%d", parsed.Selector, len(parsed.Secret))
	}
	clear(parsed.Secret)
	if len(hasher.input) <= RecoverySecretBytes {
		t.Fatal("recovery hash input was not domain- and selector-bound")
	}
}

const testRecoveryPHC = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestRecoveryCodeIssuePropagatesBoundedHasherFailure(t *testing.T) {
	hashErr := errors.New("hasher busy")
	service, _ := NewRecoveryCodeService(&recordingRecoveryHasher{err: hashErr})
	if _, err := service.Issue(context.Background(), uuid.New(), time.Now()); !errors.Is(err, hashErr) {
		t.Fatalf("issue error = %v", err)
	}
}

func TestAssistedRecoveryIssueUses256BitSecretAndFixedTTL(t *testing.T) {
	hasher := &recordingRecoveryHasher{hash: testRecoveryPHC}
	service, err := NewRecoveryCodeService(hasher)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	userID, adminID := uuid.New(), uuid.New()
	issued, err := service.IssueAssisted(context.Background(), userID, adminID, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := issued.Grant.Snapshot()
	if snapshot.UserID != userID || snapshot.CreatedByAdminID != adminID || snapshot.Status != AssistedRecoveryGrantActive ||
		!snapshot.ExpiresAt.Equal(now.Add(AssistedRecoveryTTL)) || snapshot.SecretHash != testRecoveryPHC {
		t.Fatalf("unexpected assisted grant: %+v", snapshot)
	}
	parsed, err := security.ParseToken(issued.Code, security.TokenPolicy{Version: AssistedRecoveryCodeVersion, MinSecretBytes: AssistedRecoverySecretBytes, MaxSecretBytes: AssistedRecoverySecretBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(parsed.Secret)
	if parsed.Selector != snapshot.Selector.Value() || len(parsed.Secret) != AssistedRecoverySecretBytes || len(hasher.input) <= AssistedRecoverySecretBytes {
		t.Fatalf("unexpected assisted code shape: selector=%q secret=%d hash_input=%d", parsed.Selector, len(parsed.Secret), len(hasher.input))
	}
}

func TestRestoreRecoveryCredentialAcceptsValidStatusMatrix(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	base := validRecoverySnapshot(t, now)
	for _, test := range []struct {
		name   string
		status RecoveryCredentialStatus
		mutate func(*RecoveryCredentialSnapshot)
	}{
		{name: "active", status: RecoveryCredentialActive, mutate: func(*RecoveryCredentialSnapshot) {}},
		{name: "consumed", status: RecoveryCredentialConsumed, mutate: func(snapshot *RecoveryCredentialSnapshot) {
			snapshot.ConsumedAt = now.Add(time.Minute)
		}},
		{name: "revoked", status: RecoveryCredentialRevoked, mutate: func(snapshot *RecoveryCredentialSnapshot) {
			snapshot.RevokedAt = now.Add(time.Minute)
			snapshot.RevokeReason = "user_requested"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			snapshot.Status = test.status
			test.mutate(&snapshot)
			restored, err := RestoreRecoveryCredential(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if restored.Snapshot() != snapshot {
				t.Fatalf("restored snapshot = %+v, want %+v", restored.Snapshot(), snapshot)
			}
		})
	}
}

func TestRestoreRecoveryCredentialRejectsMalformedState(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*RecoveryCredentialSnapshot)
	}{
		{name: "malformed PHC", mutate: func(snapshot *RecoveryCredentialSnapshot) { snapshot.SecretHash = "not-a-phc" }},
		{name: "zero version", mutate: func(snapshot *RecoveryCredentialSnapshot) { snapshot.Version = 0 }},
		{name: "version exceeds database", mutate: func(snapshot *RecoveryCredentialSnapshot) {
			snapshot.Version = uint64(math.MaxInt64) + 1
		}},
		{name: "short selector", mutate: func(snapshot *RecoveryCredentialSnapshot) {
			snapshot.Selector, _ = identifier.NewSelector(make([]byte, RecoverySelectorBytes-1))
		}},
		{name: "active carries consumed time", mutate: func(snapshot *RecoveryCredentialSnapshot) {
			snapshot.ConsumedAt = now.Add(time.Minute)
		}},
		{name: "consumed before creation", mutate: func(snapshot *RecoveryCredentialSnapshot) {
			snapshot.Status = RecoveryCredentialConsumed
			snapshot.ConsumedAt = now.Add(-time.Microsecond)
		}},
		{name: "revoked before creation", mutate: func(snapshot *RecoveryCredentialSnapshot) {
			snapshot.Status = RecoveryCredentialRevoked
			snapshot.RevokedAt = now.Add(-time.Microsecond)
			snapshot.RevokeReason = "user_requested"
		}},
		{name: "revoked reason whitespace", mutate: func(snapshot *RecoveryCredentialSnapshot) {
			snapshot.Status = RecoveryCredentialRevoked
			snapshot.RevokedAt = now.Add(time.Minute)
			snapshot.RevokeReason = " user_requested "
		}},
		{name: "revoked reason too long", mutate: func(snapshot *RecoveryCredentialSnapshot) {
			snapshot.Status = RecoveryCredentialRevoked
			snapshot.RevokedAt = now.Add(time.Minute)
			snapshot.RevokeReason = strings.Repeat("x", 65)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validRecoverySnapshot(t, now)
			test.mutate(&snapshot)
			if _, err := RestoreRecoveryCredential(snapshot); !errors.Is(err, ErrInvalidRecoveryCredential) {
				t.Fatalf("restore error = %v", err)
			}
		})
	}
}

func validRecoverySnapshot(t testing.TB, now time.Time) RecoveryCredentialSnapshot {
	t.Helper()
	selector, err := identifier.NewSelector(make([]byte, RecoverySelectorBytes))
	if err != nil {
		t.Fatal(err)
	}
	return RecoveryCredentialSnapshot{
		ID: uuid.New(), UserID: uuid.New(), Selector: selector, SecretHash: testRecoveryPHC,
		Version: 1, Status: RecoveryCredentialActive, CreatedAt: now,
	}
}

type recordingRecoveryHasher struct {
	hash            string
	err             error
	input           []byte
	verifyHash      string
	verifyInput     []byte
	verifyMatched   bool
	verifyNeedsUp   bool
	verifyErr       error
	verifyCallCount int
}

func (hasher *recordingRecoveryHasher) Hash(_ context.Context, input []byte) (string, error) {
	hasher.input = append([]byte(nil), input...)
	return hasher.hash, hasher.err
}

func (hasher *recordingRecoveryHasher) VerifyOrDummy(_ context.Context, encoded string, input []byte) (bool, bool, error) {
	hasher.verifyHash = encoded
	hasher.verifyInput = append([]byte(nil), input...)
	hasher.verifyCallCount++
	return hasher.verifyMatched, hasher.verifyNeedsUp, hasher.verifyErr
}
