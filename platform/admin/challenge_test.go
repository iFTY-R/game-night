package admin

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
	valid := []ChallengePurpose{
		ChallengePurposeLogin,
		ChallengePurposeSetupPassword,
		ChallengePurposeTOTPEnrollment,
		ChallengePurposeMFA,
		ChallengePurposeRecovery,
	}
	for _, purpose := range valid {
		parsed, err := ParseChallengePurpose(purpose.String())
		if err != nil || parsed != purpose || !parsed.Valid() {
			t.Fatalf("parse %q = %v, valid=%t, err=%v", purpose.String(), parsed, parsed.Valid(), err)
		}
	}
	for _, value := range []string{"", "bootstrap", "mfa_extra"} {
		if _, err := ParseChallengePurpose(value); !errors.Is(err, challenge.ErrInvalidInput) {
			t.Fatalf("parse %q error = %v, want ErrInvalidInput", value, err)
		}
	}
}

func TestAdminChallengeBindsAudienceAndCredentialGenerations(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newAdminChallengeService(t, now)
	adminID := uuid.New()
	issued, err := service.Issue(
		ChallengePurposeMFA,
		adminID,
		7,
		3,
		"https://admin.example",
		"flow_mfa",
		challenge.DefaultMaxAttempts,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := issued.Challenge.Snapshot()
	if snapshot.Binding.Audience != ChallengeAudience || snapshot.Binding.Purpose != "admin.mfa" ||
		snapshot.Binding.Subject.ID != adminID || snapshot.Binding.Subject.Version != 7 ||
		snapshot.Binding.Subject.CredentialVersion != 3 {
		t.Fatalf("unexpected admin binding: %+v", snapshot.Binding)
	}

	operationID, _ := secretresult.NewOperationID(adminBytesWithMarker(1))
	digest := sha256.Sum256([]byte("mfa request"))
	repository := &adminMemoryChallengeRepository{record: issued.Challenge}
	unitOfWork := adminMemoryChallengeUnitOfWork{repository: repository}
	consume := func(context.Context, ChallengeTransaction, Challenge, challenge.Authorization) (AuthorizedChallengeCompletion, error) {
		return NoReplayCompletion(), nil
	}
	authorization, err := service.AuthorizePersistent(
		context.Background(),
		unitOfWork,
		ChallengePurposeMFA,
		adminID,
		7,
		3,
		"https://admin.example",
		"flow_mfa",
		issued.Credentials,
		operationID,
		digest,
		consume,
	)
	if err != nil || authorization.Kind() != challenge.AuthorizeFirstUse {
		t.Fatalf("authorization = %+v, err=%v", authorization, err)
	}

	tests := []struct {
		name            string
		candidateID     uuid.UUID
		adminVersion    int64
		passwordVersion int64
	}{
		{name: "admin id", candidateID: uuid.New(), adminVersion: 7, passwordVersion: 3},
		{name: "admin version", candidateID: adminID, adminVersion: 8, passwordVersion: 3},
		{name: "password version", candidateID: adminID, adminVersion: 7, passwordVersion: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.AuthorizePersistent(
				context.Background(),
				unitOfWork,
				ChallengePurposeMFA,
				test.candidateID,
				test.adminVersion,
				test.passwordVersion,
				"https://admin.example",
				"flow_mfa",
				issued.Credentials,
				operationID,
				digest,
				consume,
			)
			if !errors.Is(err, challenge.ErrAuthentication) {
				t.Fatalf("generation mismatch error = %v, want ErrAuthentication", err)
			}
		})
	}

	snapshot.Binding.Subject = challenge.SubjectBinding{}
	if _, err := RestoreChallenge(snapshot); !errors.Is(err, challenge.ErrInvalidInput) {
		t.Fatalf("anonymous admin restore error = %v, want ErrInvalidInput", err)
	}
}

type adminMemoryChallengeRepository struct {
	record Challenge
}

func (repository *adminMemoryChallengeRepository) Insert(_ context.Context, record Challenge) error {
	repository.record = record
	return nil
}

func (repository *adminMemoryChallengeRepository) GetForUpdate(_ context.Context, selector identifier.Selector) (Challenge, error) {
	if repository.record.Snapshot().Selector != selector {
		return Challenge{}, challenge.ErrNotFound
	}
	return repository.record, nil
}

func (repository *adminMemoryChallengeRepository) RecordFailureCAS(_ context.Context, record Challenge, at time.Time) (Challenge, error) {
	if repository.record.Snapshot().AttemptCount != record.Snapshot().AttemptCount {
		return Challenge{}, challenge.ErrConcurrentTransition
	}
	updated, err := record.RecordFailure(at)
	if err == nil {
		repository.record = updated
	}
	return updated, err
}

func (repository *adminMemoryChallengeRepository) ConsumeCAS(_ context.Context, record Challenge) (Challenge, error) {
	repository.record = record
	return record, nil
}

func (*adminMemoryChallengeRepository) RevokeActiveByAdminID(context.Context, uuid.UUID, time.Time) (int64, error) {
	return 0, nil
}

type adminMemoryChallengeTransaction struct {
	repository *adminMemoryChallengeRepository
}

func (transaction adminMemoryChallengeTransaction) Challenges() ChallengeRepository {
	return transaction.repository
}

func (adminMemoryChallengeTransaction) SecretResults() secretresult.Repository {
	return nil
}

type adminMemoryChallengeUnitOfWork struct {
	repository *adminMemoryChallengeRepository
}

func (unitOfWork adminMemoryChallengeUnitOfWork) Run(ctx context.Context, work ChallengeTransactionWork) error {
	transactionalRepository := &adminMemoryChallengeRepository{record: unitOfWork.repository.record}
	if err := work(ctx, adminMemoryChallengeTransaction{repository: transactionalRepository}); err != nil {
		return err
	}
	unitOfWork.repository.record = transactionalRepository.record
	return nil
}

func newAdminChallengeService(t testing.TB, now time.Time) *ChallengeService {
	t.Helper()
	keyring, err := security.LoadHMACKeyring[security.AdminChallengeKeyPurpose](adminKeyringPath(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewChallengeService(keyring, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func adminKeyringPath(t testing.TB, now time.Time) string {
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
	path := filepath.Join(t.TempDir(), "admin-challenge-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}

func adminBytesWithMarker(marker byte) []byte {
	value := make([]byte, 16)
	for index := range value {
		value[index] = marker
	}
	return value
}
