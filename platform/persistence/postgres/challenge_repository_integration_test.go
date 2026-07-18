package postgres

import (
	"bytes"
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
	"github.com/iFTY-R/game-night/internal/integrationtest"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestIdentityChallengeFailureAndNoResultConsumptionCommit(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := time.Now().UTC().Truncate(time.Microsecond)
	fakeClock := clock.NewFake(now)
	keyring := integrationChallengeKeyring[security.UserChallengeKeyPurpose](t, now)
	service, err := identityDomain.NewChallengeService(keyring, fakeClock)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Issue(
		identityDomain.ChallengePurposeRecovery,
		"https://play.example.test",
		"recovery_flow_1",
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork := NewIdentityChallengeUnitOfWork(fixture.Pool)
	if err := unitOfWork.Run(ctx, func(ctx context.Context, transaction identityDomain.ChallengeTransaction) error {
		return transaction.Challenges().Insert(ctx, issued.Challenge)
	}); err != nil {
		t.Fatal(err)
	}

	operationID := integrationOperationID(t, 0x71)
	digest := sha256.Sum256([]byte("recovery request"))
	badCredentials := issued.Credentials
	badCredentials.BodyProof += "invalid"
	_, authenticationError := service.AuthorizePersistent(
		ctx,
		unitOfWork,
		identityDomain.ChallengePurposeRecovery,
		"https://play.example.test",
		"recovery_flow_1",
		badCredentials,
		operationID,
		digest,
		func(context.Context, identityDomain.ChallengeTransaction, identityDomain.Challenge, challenge.Authorization) (identityDomain.AuthorizedChallengeCompletion, error) {
			t.Fatal("invalid proof unexpectedly reached authorized work")
			return identityDomain.AuthorizedChallengeCompletion{}, nil
		},
	)
	if !errors.Is(authenticationError, challenge.ErrAuthentication) {
		t.Fatalf("authentication error = %v", authenticationError)
	}

	if _, err := service.AuthorizePersistent(
		ctx,
		unitOfWork,
		identityDomain.ChallengePurposeRecovery,
		"https://play.example.test",
		"recovery_flow_1",
		issued.Credentials,
		operationID,
		digest,
		func(_ context.Context, _ identityDomain.ChallengeTransaction, current identityDomain.Challenge, _ challenge.Authorization) (identityDomain.AuthorizedChallengeCompletion, error) {
			if current.Snapshot().AttemptCount != 1 {
				t.Fatalf("committed attempt count = %d, want 1", current.Snapshot().AttemptCount)
			}
			return identityDomain.NoReplayCompletion(), nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthorizePersistent(
		ctx,
		unitOfWork,
		identityDomain.ChallengePurposeRecovery,
		"https://play.example.test",
		"recovery_flow_1",
		issued.Credentials,
		operationID,
		digest,
		func(context.Context, identityDomain.ChallengeTransaction, identityDomain.Challenge, challenge.Authorization) (identityDomain.AuthorizedChallengeCompletion, error) {
			t.Fatal("consumed no-result challenge unexpectedly reached authorized work")
			return identityDomain.AuthorizedChallengeCompletion{}, nil
		},
	); !errors.Is(err, challenge.ErrUnavailable) {
		t.Fatalf("consumed no-result challenge error = %v", err)
	}
}

func TestAdminChallengeAndSecretResultCommitExactReplayTogether(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := time.Now().UTC().Truncate(time.Microsecond)
	fakeClock := clock.NewFake(now)
	keyring := integrationChallengeKeyring[security.AdminChallengeKeyPurpose](t, now)
	service, err := adminDomain.NewChallengeService(keyring, fakeClock)
	if err != nil {
		t.Fatal(err)
	}
	var adminID uuid.UUID
	var adminVersion, passwordVersion int64
	if err := fixture.Pool.QueryRow(ctx, "SELECT admin_id, admin_version, password_version FROM admin_accounts WHERE singleton_id = 1").Scan(
		&adminID, &adminVersion, &passwordVersion,
	); err != nil {
		t.Fatal(err)
	}
	issued, err := service.Issue(
		adminDomain.ChallengePurposeTOTPEnrollment,
		adminID,
		adminVersion,
		passwordVersion,
		"https://admin.example.test",
		"totp_flow_1",
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork := NewAdminChallengeUnitOfWork(fixture.Pool)
	if err := unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.ChallengeTransaction) error {
		return transaction.Challenges().Insert(ctx, issued.Challenge)
	}); err != nil {
		t.Fatal(err)
	}

	operationID := integrationOperationID(t, 0x72)
	digest := sha256.Sum256([]byte("complete TOTP enrollment"))
	resultBinding := secretresult.Binding{
		Key: secretresult.Key{
			Scope: secretresult.ScopeAdminTOTPEnrollment, ActorID: issued.Challenge.Snapshot().ID, OperationID: operationID,
		},
		RequestDigest: digest, ResultType: secretresult.ResultTypeAdminTOTPEnrollment, ResultVersion: 1,
	}
	secretExpiresAt := now.Add(8 * time.Minute)
	result := integrationAvailableResult(t, uuid.New(), resultBinding, now, secretExpiresAt)
	if _, err := service.AuthorizePersistent(
		ctx,
		unitOfWork,
		adminDomain.ChallengePurposeTOTPEnrollment,
		adminID,
		adminVersion,
		passwordVersion,
		"https://admin.example.test",
		"totp_flow_1",
		issued.Credentials,
		operationID,
		digest,
		func(ctx context.Context, transaction adminDomain.ChallengeTransaction, _ adminDomain.Challenge, _ challenge.Authorization) (adminDomain.AuthorizedChallengeCompletion, error) {
			if _, insertErr := transaction.SecretResults().InsertAvailable(ctx, result); insertErr != nil {
				return adminDomain.AuthorizedChallengeCompletion{}, insertErr
			}
			return adminDomain.AuthorizedChallengeCompletion{}, nil
		},
	); !errors.Is(err, challenge.ErrInvalidInput) {
		t.Fatalf("empty first-use completion error = %v", err)
	}
	var rolledBackResults int
	var challengeStatus string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM secret_operation_results WHERE result_id = $1),
			(SELECT status FROM admin_challenges WHERE challenge_id = $2)
	`, result.Snapshot().ID, issued.Challenge.Snapshot().ID).Scan(&rolledBackResults, &challengeStatus); err != nil {
		t.Fatal(err)
	}
	if rolledBackResults != 0 || challengeStatus != "active" {
		t.Fatalf("invalid completion committed partial state: results=%d challenge=%s", rolledBackResults, challengeStatus)
	}

	firstAuthorization, err := service.AuthorizePersistent(
		ctx,
		unitOfWork,
		adminDomain.ChallengePurposeTOTPEnrollment,
		adminID,
		adminVersion,
		passwordVersion,
		"https://admin.example.test",
		"totp_flow_1",
		issued.Credentials,
		operationID,
		digest,
		func(ctx context.Context, transaction adminDomain.ChallengeTransaction, _ adminDomain.Challenge, _ challenge.Authorization) (adminDomain.AuthorizedChallengeCompletion, error) {
			stored, err := transaction.SecretResults().InsertAvailable(ctx, result)
			if err != nil {
				return adminDomain.AuthorizedChallengeCompletion{}, err
			}
			return adminDomain.NewReplayCompletion(stored)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if firstAuthorization.Kind() != challenge.AuthorizeFirstUse {
		t.Fatalf("first authorization = %+v", firstAuthorization)
	}

	var resolution secretresult.Resolution
	authorization, err := service.AuthorizePersistent(
		ctx,
		unitOfWork,
		adminDomain.ChallengePurposeTOTPEnrollment,
		adminID,
		adminVersion,
		passwordVersion,
		"https://admin.example.test",
		"totp_flow_1",
		issued.Credentials,
		operationID,
		digest,
		func(ctx context.Context, transaction adminDomain.ChallengeTransaction, _ adminDomain.Challenge, _ challenge.Authorization) (adminDomain.AuthorizedChallengeCompletion, error) {
			stored, err := transaction.SecretResults().GetByOperationForUpdate(ctx, resultBinding.Key)
			if err != nil {
				return adminDomain.AuthorizedChallengeCompletion{}, err
			}
			resolution, err = stored.Resolve(resultBinding, now.Add(time.Minute))
			if err != nil || resolution.Kind != secretresult.ReplayAvailable {
				t.Fatalf("secret result replay = %+v, err=%v", resolution, err)
			}
			return adminDomain.AuthorizedChallengeCompletion{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Kind() != challenge.AuthorizeExactReplay || authorization.ResultID() != result.Snapshot().ID ||
		!challenge.AuthorizesReplay(authorization, result.Snapshot().ID, now) {
		t.Fatalf("admin exact replay = %+v", authorization)
	}
}

func TestAdminChallengeRejectsStaleAccountGenerations(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := time.Now().UTC().Truncate(time.Microsecond)
	service, err := adminDomain.NewChallengeService(
		integrationChallengeKeyring[security.AdminChallengeKeyPurpose](t, now),
		clock.NewFake(now),
	)
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork := NewAdminChallengeUnitOfWork(fixture.Pool)

	cases := []struct {
		name          string
		requestFlowID challenge.RequestFlowID
		updateSQL     string
	}{
		{
			name:          "account version",
			requestFlowID: "stale_admin_version",
			updateSQL:     "UPDATE admin_accounts SET admin_version = admin_version + 1 WHERE singleton_id = 1",
		},
		{
			name:          "password version",
			requestFlowID: "stale_password_version",
			updateSQL:     "UPDATE admin_accounts SET password_version = password_version + 1 WHERE singleton_id = 1",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var adminID uuid.UUID
			var adminVersion, passwordVersion int64
			if err := fixture.Pool.QueryRow(ctx, `
                SELECT admin_id, admin_version, password_version
                FROM admin_accounts
                WHERE singleton_id = 1
            `).Scan(&adminID, &adminVersion, &passwordVersion); err != nil {
				t.Fatal(err)
			}
			issued, err := service.Issue(
				adminDomain.ChallengePurposeLogin,
				adminID,
				adminVersion,
				passwordVersion,
				"https://admin.example.test",
				testCase.requestFlowID,
				3,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.ChallengeTransaction) error {
				return transaction.Challenges().Insert(ctx, issued.Challenge)
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := fixture.Pool.Exec(ctx, testCase.updateSQL); err != nil {
				t.Fatal(err)
			}
			err = unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.ChallengeTransaction) error {
				_, getErr := transaction.Challenges().GetForUpdate(ctx, issued.Challenge.Snapshot().Selector)
				return getErr
			})
			if !errors.Is(err, challenge.ErrNotFound) {
				t.Fatalf("GetForUpdate stale-generation error = %v", err)
			}

			consumed, err := issued.Challenge.ConsumeWithoutReplay(now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			err = unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.ChallengeTransaction) error {
				_, consumeErr := transaction.Challenges().ConsumeCAS(ctx, consumed)
				return consumeErr
			})
			if !errors.Is(err, challenge.ErrConcurrentTransition) {
				t.Fatalf("ConsumeCAS stale-generation error = %v", err)
			}
			var storedStatus string
			if err := fixture.Pool.QueryRow(ctx, `
                SELECT status
                FROM admin_challenges
                WHERE challenge_id = $1
            `, issued.Challenge.Snapshot().ID).Scan(&storedStatus); err != nil {
				t.Fatal(err)
			}
			if storedStatus != "active" {
				t.Fatalf("stale challenge status = %s, want active", storedStatus)
			}
		})
	}
}

func TestAdminChallengeConsumeWaitsForAccountGenerationLock(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := time.Now().UTC().Truncate(time.Microsecond)
	service, err := adminDomain.NewChallengeService(
		integrationChallengeKeyring[security.AdminChallengeKeyPurpose](t, now),
		clock.NewFake(now),
	)
	if err != nil {
		t.Fatal(err)
	}
	var adminID uuid.UUID
	var adminVersion, passwordVersion int64
	if err := fixture.Pool.QueryRow(ctx, `
        SELECT admin_id, admin_version, password_version
        FROM admin_accounts
        WHERE singleton_id = 1
    `).Scan(&adminID, &adminVersion, &passwordVersion); err != nil {
		t.Fatal(err)
	}
	issued, err := service.Issue(
		adminDomain.ChallengePurposeLogin,
		adminID,
		adminVersion,
		passwordVersion,
		"https://admin.example.test",
		"generation_race",
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork := NewAdminChallengeUnitOfWork(fixture.Pool)
	if err := unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.ChallengeTransaction) error {
		return transaction.Challenges().Insert(ctx, issued.Challenge)
	}); err != nil {
		t.Fatal(err)
	}
	consumed, err := issued.Challenge.ConsumeWithoutReplay(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	generationChange, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = generationChange.Rollback(context.Background()) }()
	if _, err := generationChange.Exec(ctx, `
        UPDATE admin_accounts
        SET admin_version = admin_version + 1
        WHERE singleton_id = 1
    `); err != nil {
		t.Fatal(err)
	}

	// accountLockProbeTimeout only bounds the lock assertion; the account transaction deliberately remains open longer.
	const accountLockProbeTimeout = 250 * time.Millisecond
	lockProbeContext, cancelLockProbe := context.WithTimeout(ctx, accountLockProbeTimeout)
	err = unitOfWork.Run(lockProbeContext, func(ctx context.Context, transaction adminDomain.ChallengeTransaction) error {
		_, consumeErr := transaction.Challenges().ConsumeCAS(ctx, consumed)
		return consumeErr
	})
	cancelLockProbe()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ConsumeCAS did not wait for account generation lock: %v", err)
	}
	if err := generationChange.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.ChallengeTransaction) error {
		_, consumeErr := transaction.Challenges().ConsumeCAS(ctx, consumed)
		return consumeErr
	})
	if !errors.Is(err, challenge.ErrConcurrentTransition) {
		t.Fatalf("concurrent stale ConsumeCAS error = %v", err)
	}

	var storedStatus string
	if err := fixture.Pool.QueryRow(ctx, `
        SELECT status
        FROM admin_challenges
        WHERE challenge_id = $1
    `, issued.Challenge.Snapshot().ID).Scan(&storedStatus); err != nil {
		t.Fatal(err)
	}
	if storedStatus != "active" {
		t.Fatalf("concurrently stale challenge status = %s, want active", storedStatus)
	}
}

func integrationChallengeKeyring[P security.HMACKeyPurpose](t testing.TB, now time.Time) *security.HMACKeyring[P] {
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
	path := filepath.Join(t.TempDir(), "challenge-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	keyring, err := security.LoadHMACKeyring[P](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func integrationOperationID(t testing.TB, marker byte) secretresult.OperationID {
	t.Helper()
	operationID, err := secretresult.NewOperationID(bytes.Repeat([]byte{marker}, 16))
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}
