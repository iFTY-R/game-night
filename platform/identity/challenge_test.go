package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestChallengePurposeClosure(t *testing.T) {
	valid := []ChallengePurpose{ChallengePurposeBootstrap, ChallengePurposeRecovery}
	for _, purpose := range valid {
		parsed, err := ParseChallengePurpose(purpose.String())
		if err != nil || parsed != purpose || !parsed.Valid() {
			t.Fatalf("parse %q = %v, valid=%t, err=%v", purpose.String(), parsed, parsed.Valid(), err)
		}
	}
	for _, value := range []string{"", "login", "bootstrap_extra"} {
		if _, err := ParseChallengePurpose(value); !errors.Is(err, challenge.ErrInvalidInput) {
			t.Fatalf("parse %q error = %v, want ErrInvalidInput", value, err)
		}
	}
}

func TestIdentityChallengeFixesAudienceAndAnonymousSubject(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newIdentityChallengeService(t, now)
	issued, err := service.Issue(
		ChallengePurposeBootstrap, "https://play.example", "flow_bootstrap", challenge.DefaultMaxAttempts,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := issued.Challenge.Snapshot()
	if snapshot.Binding.Audience != ChallengeAudience || snapshot.Binding.Subject.Bound() ||
		snapshot.Binding.Purpose != "identity.bootstrap" {
		t.Fatalf("unexpected identity binding: %+v", snapshot.Binding)
	}

	operationID, _ := secretresult.NewOperationID(bytesWithMarker(1))
	digest := sha256.Sum256([]byte("bootstrap request"))
	repository := &identityMemoryChallengeRepository{record: issued.Challenge}
	unitOfWork := identityMemoryChallengeUnitOfWork{repository: repository}
	consume := func(context.Context, ChallengeTransaction, Challenge, challenge.Authorization) (AuthorizedChallengeCompletion, error) {
		return NoReplayCompletion(), nil
	}
	if _, err := service.AuthorizePersistent(
		context.Background(),
		unitOfWork,
		ChallengePurposeRecovery,
		"https://play.example",
		"flow_bootstrap",
		issued.Credentials,
		operationID,
		digest,
		consume,
	); !errors.Is(err, challenge.ErrAuthentication) {
		t.Fatalf("cross-purpose error = %v, want ErrAuthentication", err)
	}
	if repository.record.Snapshot().AttemptCount != 1 {
		t.Fatalf("persisted attempt count = %d, want 1", repository.record.Snapshot().AttemptCount)
	}
	if _, err := service.AuthorizePersistent(
		context.Background(), unitOfWork, ChallengePurposeBootstrap, "https://play.example", "flow_bootstrap",
		issued.Credentials, operationID, digest,
		func(context.Context, ChallengeTransaction, Challenge, challenge.Authorization) (AuthorizedChallengeCompletion, error) {
			return AuthorizedChallengeCompletion{}, nil
		},
	); !errors.Is(err, challenge.ErrInvalidInput) {
		t.Fatalf("no-op first-use completion error = %v", err)
	}
	if repository.record.State(now) != challenge.StateActive || repository.record.Snapshot().AttemptCount != 1 {
		t.Fatalf("invalid completion changed challenge: %+v", repository.record.Snapshot())
	}
	crossDomainBinding := secretresult.Binding{
		Key: secretresult.Key{
			Scope:   secretresult.ScopeAdminTOTPEnrollment,
			ActorID: repository.record.Snapshot().ID, OperationID: operationID,
		},
		RequestDigest: digest, ResultType: secretresult.ResultTypeAdminTOTPEnrollment, ResultVersion: 1,
	}
	crossDomainResult, err := secretresult.NewAvailable(
		uuid.New(), crossDomainBinding,
		secretresult.EncryptedPayload{
			Ciphertext: []byte("ciphertext"), Nonce: []byte("nonce"),
			WrappedDataKey: []byte("wrapped"), KeyVersion: 1,
		},
		now, now.Add(time.Minute), now.Add(time.Minute+secretresult.MinimumTombstoneRetention),
	)
	if err != nil {
		t.Fatal(err)
	}
	crossDomainCompletion, err := NewReplayCompletion(crossDomainResult)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthorizePersistent(
		context.Background(), unitOfWork, ChallengePurposeBootstrap, "https://play.example", "flow_bootstrap",
		issued.Credentials, operationID, digest,
		func(context.Context, ChallengeTransaction, Challenge, challenge.Authorization) (AuthorizedChallengeCompletion, error) {
			return crossDomainCompletion, nil
		},
	); !errors.Is(err, challenge.ErrInvalidInput) {
		t.Fatalf("cross-domain completion error = %v", err)
	}
	authorization, err := service.AuthorizePersistent(
		context.Background(),
		unitOfWork,
		ChallengePurposeBootstrap,
		"https://play.example",
		"flow_bootstrap",
		issued.Credentials,
		operationID,
		digest,
		consume,
	)
	if err != nil || authorization.Kind() != challenge.AuthorizeFirstUse {
		t.Fatalf("authorization = %+v, err=%v", authorization, err)
	}

	snapshot.Binding.Audience = "admin_auth_api"
	if _, err := RestoreChallenge(snapshot); !errors.Is(err, challenge.ErrInvalidInput) {
		t.Fatalf("cross-audience restore error = %v, want ErrInvalidInput", err)
	}
}

type identityMemoryChallengeRepository struct {
	record Challenge
}

func (repository *identityMemoryChallengeRepository) Insert(_ context.Context, record Challenge) error {
	repository.record = record
	return nil
}

func (repository *identityMemoryChallengeRepository) GetForUpdate(_ context.Context, selector identifier.Selector) (Challenge, error) {
	if repository.record.Snapshot().Selector != selector {
		return Challenge{}, challenge.ErrNotFound
	}
	return repository.record, nil
}

func (repository *identityMemoryChallengeRepository) RecordFailureCAS(_ context.Context, record Challenge, at time.Time) (Challenge, error) {
	if repository.record.Snapshot().AttemptCount != record.Snapshot().AttemptCount {
		return Challenge{}, challenge.ErrConcurrentTransition
	}
	updated, err := record.RecordFailure(at)
	if err == nil {
		repository.record = updated
	}
	return updated, err
}

func (repository *identityMemoryChallengeRepository) ConsumeCAS(_ context.Context, record Challenge) (Challenge, error) {
	repository.record = record
	return record, nil
}

type identityMemoryChallengeTransaction struct {
	repository *identityMemoryChallengeRepository
}

func (transaction identityMemoryChallengeTransaction) Challenges() ChallengeRepository {
	return transaction.repository
}

func (identityMemoryChallengeTransaction) SecretResults() secretresult.Repository {
	return nil
}

type identityMemoryChallengeUnitOfWork struct {
	repository *identityMemoryChallengeRepository
}

func (unitOfWork identityMemoryChallengeUnitOfWork) Run(ctx context.Context, work ChallengeTransactionWork) error {
	transactionalRepository := &identityMemoryChallengeRepository{record: unitOfWork.repository.record}
	if err := work(ctx, identityMemoryChallengeTransaction{repository: transactionalRepository}); err != nil {
		return err
	}
	unitOfWork.repository.record = transactionalRepository.record
	return nil
}

func newIdentityChallengeService(t testing.TB, now time.Time) *ChallengeService {
	t.Helper()
	keyring, err := security.LoadHMACKeyring[security.UserChallengeKeyPurpose](identityKeyringPath(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewChallengeService(keyring, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func identityKeyringPath(t testing.TB, now time.Time) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	document := map[string]any{
		"active_version": 1,
		"keys": []map[string]any{{
			"version": 1, "key": base64.StdEncoding.EncodeToString(key), "not_before": now.Add(-time.Hour),
		}},
	}
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identity-challenge-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}

func bytesWithMarker(marker byte) []byte {
	value := make([]byte, 16)
	for index := range value {
		value[index] = marker
	}
	return value
}
