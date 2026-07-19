package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/ratelimit/ratelimittest"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIdentityServicePostgresResponseLossReplaysBootstrapAndOnboardingSecrets(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	service := integrationIdentityService(t, fixture)

	flowID := challenge.RequestFlowID("flow_" + uuid.NewString())
	begin, err := service.BeginIdentityBootstrap(ctx, identityDomain.BeginIdentityBootstrapCommand{
		CanonicalOrigin: "https://play.example.test", RequestFlowID: flowID, ClientIP: "203.0.113.31",
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapCommand := identityDomain.BootstrapIdentityCommand{
		CanonicalOrigin: "https://play.example.test", RequestFlowID: flowID,
		ChallengeCredentials: begin.Credentials, OperationID: integrationIdentityOperationID(t, 0x21),
		ClientIP: "203.0.113.31", DeviceLabel: "Phone",
	}
	firstBootstrap, err := service.BootstrapIdentity(ctx, bootstrapCommand)
	if err != nil {
		t.Fatal(err)
	}
	secondBootstrap, err := service.BootstrapIdentity(ctx, bootstrapCommand)
	if err != nil {
		t.Fatal(err)
	}
	if firstBootstrap.Operation.ResultID != secondBootstrap.Operation.ResultID ||
		firstBootstrap.DeviceSecrets.Token() != secondBootstrap.DeviceSecrets.Token() ||
		firstBootstrap.DeviceSecrets.CSRFToken() != secondBootstrap.DeviceSecrets.CSRFToken() || !secondBootstrap.Operation.Replayed {
		t.Fatal("PostgreSQL bootstrap response-loss retry did not replay the exact committed result")
	}
	assertIdentityTableCount(t, ctx, fixture, "users", 1)
	assertIdentityTableCount(t, ctx, fixture, "device_credentials", 1)
	assertIdentityTableCount(t, ctx, fixture, "secret_operation_results", 1)

	onboardingCommand := identityDomain.CompleteOnboardingCommand{
		DeviceToken: firstBootstrap.DeviceSecrets.Token(), CSRFToken: firstBootstrap.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.31", Username: "Alice9", OperationID: integrationIdentityOperationID(t, 0x22),
	}
	firstOnboarding, err := service.CompleteOnboarding(ctx, onboardingCommand)
	if err != nil {
		t.Fatal(err)
	}
	secondOnboarding, err := service.CompleteOnboarding(ctx, onboardingCommand)
	if err != nil {
		t.Fatal(err)
	}
	if firstOnboarding.Operation.ResultID != secondOnboarding.Operation.ResultID ||
		firstOnboarding.RecoveryCode != secondOnboarding.RecoveryCode || !secondOnboarding.Operation.Replayed {
		t.Fatal("PostgreSQL onboarding response-loss retry did not replay the exact recovery code")
	}
	assertIdentityTableCount(t, ctx, fixture, "username_claims", 1)
	assertIdentityTableCount(t, ctx, fixture, "user_recovery_credentials", 1)
	assertIdentityTableCount(t, ctx, fixture, "secret_operation_results", 2)

	conflict := bootstrapCommand
	conflict.DeviceLabel = "Other Phone"
	if _, err := service.BootstrapIdentity(ctx, conflict); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("different-digest bootstrap retry error = %v", err)
	}
	assertIdentityTableCount(t, ctx, fixture, "users", 1)
	assertIdentityTableCount(t, ctx, fixture, "device_credentials", 1)
	assertIdentityTableCount(t, ctx, fixture, "secret_operation_results", 2)
}

func TestIdentityServicePostgresRecoveryCommitsAndReplaysExactBundle(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	service := integrationIdentityService(t, fixture)
	bootstrap, onboarding := integrationOnboardIdentity(t, ctx, service, 0x71, 0x72, "RecoveryOwner9")
	begin := integrationBeginRecovery(t, ctx, service, onboarding.RecoveryCode, "recovery_commit")

	var beginDigest []byte
	if err := fixture.Pool.QueryRow(ctx, "SELECT request_digest FROM user_recovery_attempts").Scan(&beginDigest); err != nil {
		t.Fatal(err)
	}
	if beginDigest != nil {
		t.Fatalf("BeginRecovery persisted request digest %x, want NULL", beginDigest)
	}

	command := identityDomain.CompleteRecoveryCommand{
		CanonicalOrigin: "https://play.example.test", RecoveryGrant: begin.RecoveryGrant,
		OperationID: integrationIdentityOperationID(t, 0x73), DeviceLabel: "Recovered Phone",
		DevicePolicy: identityDomain.RecoveryDevicePolicyKeepOtherDevices, RequestID: "request-recovery-commit",
	}
	first, err := service.CompleteRecovery(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CompleteRecovery(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Operation.Replayed || first.Operation.ResultID != second.Operation.ResultID ||
		first.DeviceSecrets.Token() != second.DeviceSecrets.Token() || first.RecoveryCode != second.RecoveryCode ||
		first.User.Snapshot().ID != bootstrap.User.Snapshot().ID {
		t.Fatal("PostgreSQL recovery response-loss retry did not return the exact committed bundle")
	}

	var attemptStatus, sourceStatus string
	var requestDigest []byte
	var resultID uuid.UUID
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT attempt.status, attempt.request_digest, attempt.result_id, source.status
		FROM user_recovery_attempts AS attempt
		JOIN user_recovery_credentials AS source
		  ON source.recovery_credential_id = attempt.recovery_credential_id
	`).Scan(&attemptStatus, &requestDigest, &resultID, &sourceStatus); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != "consumed" || sourceStatus != "consumed" || len(requestDigest) != idempotency.DigestSize ||
		resultID != first.Operation.ResultID {
		t.Fatalf("committed recovery state: attempt=%s source=%s digest=%d result=%s",
			attemptStatus, sourceStatus, len(requestDigest), resultID)
	}
	assertIdentityRecoveryCommitCounts(t, ctx, fixture, 2, 2, 3, 1, 1)
}

func TestIdentityServicePostgresConcurrentRecoveryGrantHasOneWinner(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	service := integrationIdentityService(t, fixture)
	_, onboarding := integrationOnboardIdentity(t, ctx, service, 0x74, 0x75, "RecoveryRace9")
	begin := integrationBeginRecovery(t, ctx, service, onboarding.RecoveryCode, "recovery_race")
	commands := []identityDomain.CompleteRecoveryCommand{
		{
			CanonicalOrigin: "https://play.example.test", RecoveryGrant: begin.RecoveryGrant,
			OperationID: integrationIdentityOperationID(t, 0x76), DeviceLabel: "Race Winner",
			DevicePolicy: identityDomain.RecoveryDevicePolicyKeepOtherDevices, RequestID: "request-recovery-race-a",
		},
		{
			CanonicalOrigin: "https://play.example.test", RecoveryGrant: begin.RecoveryGrant,
			OperationID: integrationIdentityOperationID(t, 0x77), DeviceLabel: "Race Winner",
			DevicePolicy: identityDomain.RecoveryDevicePolicyKeepOtherDevices, RequestID: "request-recovery-race-b",
		},
	}
	type outcome struct {
		result identityDomain.CompleteRecoveryResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(commands))
	var waitGroup sync.WaitGroup
	for _, command := range commands {
		command := command
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			result, completeErr := service.CompleteRecovery(ctx, command)
			outcomes <- outcome{result: result, err: completeErr}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(outcomes)

	successes, rejected := 0, 0
	for result := range outcomes {
		switch {
		case result.err == nil && result.result.RecoveryCode != "":
			successes++
		case errors.Is(result.err, identityDomain.ErrRecoveryInvalid):
			rejected++
		default:
			t.Fatalf("unexpected concurrent recovery outcome: result=%#v err=%v", result.result.Operation, result.err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("concurrent recovery outcomes: success=%d rejected=%d", successes, rejected)
	}
	assertIdentityRecoveryCommitCounts(t, ctx, fixture, 2, 2, 3, 1, 1)
}

func TestIdentityServicePostgresConcurrentUsernameClaimHasOneWinner(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	service := integrationIdentityService(t, fixture)
	first := integrationBootstrapIdentity(t, ctx, service, 0x31)
	second := integrationBootstrapIdentity(t, ctx, service, 0x32)

	commands := []identityDomain.CompleteOnboardingCommand{
		{
			DeviceToken: first.DeviceSecrets.Token(), CSRFToken: first.DeviceSecrets.CSRFToken(),
			ClientIP: "203.0.113.32", Username: "Concurrent9", OperationID: integrationIdentityOperationID(t, 0x33),
		},
		{
			DeviceToken: second.DeviceSecrets.Token(), CSRFToken: second.DeviceSecrets.CSRFToken(),
			ClientIP: "203.0.113.33", Username: "ＣＯＮＣＵＲＲＥＮＴ９", OperationID: integrationIdentityOperationID(t, 0x34),
		},
	}
	start := make(chan struct{})
	results := make(chan error, len(commands))
	var waitGroup sync.WaitGroup
	for _, command := range commands {
		command := command
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, completeErr := service.CompleteOnboarding(ctx, command)
			results <- completeErr
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)

	successes, unavailable := 0, 0
	for resultErr := range results {
		switch {
		case resultErr == nil:
			successes++
		case errors.Is(resultErr, identityDomain.ErrUsernameUnavailable):
			unavailable++
		default:
			t.Fatalf("unexpected concurrent claim error: %v", resultErr)
		}
	}
	if successes != 1 || unavailable != 1 {
		t.Fatalf("concurrent claim outcomes: success=%d unavailable=%d", successes, unavailable)
	}
	assertIdentityTableCount(t, ctx, fixture, "username_claims", 1)
	assertIdentityTableCount(t, ctx, fixture, "user_recovery_credentials", 1)
	var activeUsers, onboardingUsers int
	if err := fixture.Pool.QueryRow(ctx, `
        SELECT count(*) FILTER (WHERE status = 'active'), count(*) FILTER (WHERE status = 'onboarding')
        FROM users
    `).Scan(&activeUsers, &onboardingUsers); err != nil {
		t.Fatal(err)
	}
	if activeUsers != 1 || onboardingUsers != 1 {
		t.Fatalf("concurrent user states: active=%d onboarding=%d", activeUsers, onboardingUsers)
	}
}

func TestIdentityPostgresConcurrentUsernameClaimMeetsAtClaimPoint(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	service := integrationIdentityService(t, fixture)
	identities := []identityDomain.BootstrapIdentityResult{
		integrationBootstrapIdentity(t, ctx, service, 0x41),
		integrationBootstrapIdentity(t, ctx, service, 0x42),
	}
	username, err := identifier.ParseUsername("Barrier9")
	if err != nil {
		t.Fatal(err)
	}

	unitOfWork := NewIdentityUnitOfWork(fixture.Pool)
	ready := make(chan error, len(identities))
	release := make(chan struct{})
	results := make(chan error, len(identities))
	var waitGroup sync.WaitGroup
	for _, identity := range identities {
		userID := identity.User.Snapshot().ID
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results <- unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
				user, getErr := transaction.Users().GetForUpdate(ctx, userID)
				ready <- getErr
				if getErr != nil {
					return getErr
				}
				<-release
				changedAt := user.Snapshot().CreatedAt.Add(time.Minute)
				claim, claimErr := identityDomain.NewActiveUsernameClaim(username, userID, changedAt)
				if claimErr != nil {
					return claimErr
				}
				if _, claimErr = transaction.UsernameClaims().Claim(ctx, claim, changedAt); claimErr != nil {
					return claimErr
				}
				next, transitionErr := user.CompleteOnboarding(username, changedAt)
				if transitionErr != nil {
					return transitionErr
				}
				_, transitionErr = transaction.Users().CompleteOnboardingCAS(ctx, user, next)
				return transitionErr
			})
		}()
	}
	var preparationErr error
	for range identities {
		preparationErr = errors.Join(preparationErr, <-ready)
	}
	close(release)
	waitGroup.Wait()
	close(results)
	if preparationErr != nil {
		t.Fatalf("prepare claim-point race: %v", preparationErr)
	}

	successes, unavailable := 0, 0
	for resultErr := range results {
		switch {
		case resultErr == nil:
			successes++
		case errors.Is(resultErr, identityDomain.ErrUsernameUnavailable):
			unavailable++
		default:
			t.Fatalf("unexpected claim-point race error: %v", resultErr)
		}
	}
	if successes != 1 || unavailable != 1 {
		t.Fatalf("claim-point outcomes: success=%d unavailable=%d", successes, unavailable)
	}
}

func TestIdentityServicePostgresChangeUsernameAndReclaimsExpiredReservation(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	runtime := newIntegrationIdentityRuntime(t, fixture)
	first := integrationBootstrapIdentity(t, ctx, runtime.service, 0x51)
	if _, err := runtime.service.CompleteOnboarding(ctx, identityDomain.CompleteOnboardingCommand{
		DeviceToken: first.DeviceSecrets.Token(), CSRFToken: first.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.51", Username: "Alice9", OperationID: integrationIdentityOperationID(t, 0x52),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.clock.Advance(identityDomain.UsernameChangeCooldown); err != nil {
		t.Fatal(err)
	}
	changed, err := runtime.service.ChangeUsername(ctx, identityDomain.ChangeUsernameCommand{
		DeviceToken: first.DeviceSecrets.Token(), CSRFToken: first.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.51", Username: "Bob9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.User.Snapshot().Username != "Bob9" {
		t.Fatalf("changed username = %q", changed.User.Snapshot().Username)
	}
	var oldStatus, newStatus string
	var oldReservedUntil time.Time
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT old_claim.status, old_claim.reserved_until, new_claim.status
		FROM username_claims AS old_claim
		JOIN username_claims AS new_claim ON new_claim.username_key = 'bob9'
		WHERE old_claim.username_key = 'alice9'
	`).Scan(&oldStatus, &oldReservedUntil, &newStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "reserved" || newStatus != "active" || !oldReservedUntil.Equal(runtime.clock.Now().Add(identityDomain.UsernameReservationTTL)) {
		t.Fatalf("claim states after rename: old=%s until=%s new=%s", oldStatus, oldReservedUntil, newStatus)
	}
	if _, err := runtime.service.ChangeUsername(ctx, identityDomain.ChangeUsernameCommand{
		DeviceToken: first.DeviceSecrets.Token(), CSRFToken: first.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.51", Username: "Carol9",
	}); !errors.Is(err, identityDomain.ErrUsernameChangeCooldown) {
		t.Fatalf("immediate second rename error = %v", err)
	}

	// Each snapshot is valid alone, but the adapter must reject a caller-crafted transition inside the cooldown.
	err = NewIdentityUnitOfWork(fixture.Pool).RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		user, getErr := transaction.Users().GetForUpdate(ctx, changed.User.Snapshot().ID)
		if getErr != nil {
			return getErr
		}
		carol, parseErr := identifier.ParseUsername("Carol9")
		if parseErr != nil {
			return parseErr
		}
		craftedAt := user.Snapshot().UsernameChangedAt.Add(24 * time.Hour)
		crafted, restoreErr := identityDomain.RestoreUser(identityDomain.UserSnapshot{
			ID: user.Snapshot().ID, Status: identityDomain.UserStatusActive,
			Username: carol.Display(), CurrentUsernameKey: carol.Key(), UsernameChangedAt: craftedAt,
			CreatedAt: user.Snapshot().CreatedAt, UpdatedAt: craftedAt,
		})
		if restoreErr != nil {
			return restoreErr
		}
		_, transitionErr := transaction.Users().ChangeUsernameCAS(ctx, user, crafted)
		return transitionErr
	})
	if !errors.Is(err, identityDomain.ErrUsernameChangeCooldown) {
		t.Fatalf("adapter cooldown reconstruction error = %v", err)
	}

	if _, err := runtime.clock.Advance(identityDomain.UsernameReservationTTL); err != nil {
		t.Fatal(err)
	}
	second := integrationBootstrapIdentity(t, ctx, runtime.service, 0x53)
	if _, err := runtime.service.CompleteOnboarding(ctx, identityDomain.CompleteOnboardingCommand{
		DeviceToken: second.DeviceSecrets.Token(), CSRFToken: second.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.52", Username: "Alice9", OperationID: integrationIdentityOperationID(t, 0x54),
	}); err != nil {
		t.Fatal(err)
	}
	var owner uuid.UUID
	var reclaimedStatus string
	var reservedUntil pgtype.Timestamptz
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT owner_user_id, status, reserved_until
		FROM username_claims WHERE username_key = 'alice9'
	`).Scan(&owner, &reclaimedStatus, &reservedUntil); err != nil {
		t.Fatal(err)
	}
	if owner != second.User.Snapshot().ID || reclaimedStatus != "active" || reservedUntil.Valid {
		t.Fatalf("expired reservation was not reclaimed: owner=%s status=%s reserved=%v", owner, reclaimedStatus, reservedUntil)
	}
}

func TestIdentityPostgresRejectsStaleDeviceCAS(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	runtime := newIntegrationIdentityRuntime(t, fixture)
	bootstrap := integrationBootstrapIdentity(t, ctx, runtime.service, 0x61)
	if _, err := runtime.clock.Advance(time.Second); err != nil {
		t.Fatal(err)
	}
	fresh, err := runtime.service.GetCurrentIdentity(ctx, identityDomain.GetCurrentIdentityCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(),
	})
	if err != nil {
		t.Fatal(err)
	}
	staleAuthorization, err := runtime.devices.Authenticate(bootstrap.Device, bootstrap.DeviceSecrets.Token())
	if err != nil {
		t.Fatal(err)
	}
	staleTouch, err := bootstrap.Device.Touch(staleAuthorization, runtime.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	err = NewIdentityUnitOfWork(fixture.Pool).RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		_, touchErr := transaction.Devices().TouchCAS(ctx, bootstrap.Device, staleTouch)
		return touchErr
	})
	if !errors.Is(err, identityDomain.ErrIdentityConcurrentTransition) {
		t.Fatalf("stale touch error = %v", err)
	}

	if _, err := runtime.clock.Advance(identityDomain.DeviceRotationInterval - time.Second); err != nil {
		t.Fatal(err)
	}
	rotationAuthorization, err := runtime.devices.Authenticate(fresh.Device, bootstrap.DeviceSecrets.Token())
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := runtime.devices.Rotate(fresh.Device, rotationAuthorization, bootstrap.DeviceSecrets.CSRFToken())
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork := NewIdentityUnitOfWork(fixture.Pool)
	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		_, rotateErr := transaction.Devices().RotateCAS(ctx, fresh.Device, rotated.Credential)
		return rotateErr
	}); err != nil {
		t.Fatal(err)
	}
	err = unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		_, rotateErr := transaction.Devices().RotateCAS(ctx, fresh.Device, rotated.Credential)
		return rotateErr
	})
	if !errors.Is(err, identityDomain.ErrIdentityConcurrentTransition) {
		t.Fatalf("stale rotation error = %v", err)
	}
}

func TestIdentityPostgresRollsBackWritesAfterCallbackFailure(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	userID := uuid.New()
	user, err := identityDomain.NewOnboardingUser(userID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("fail after identity write")
	err = NewIdentityUnitOfWork(fixture.Pool).RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		if _, insertErr := transaction.Users().Insert(ctx, user); insertErr != nil {
			return insertErr
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("callback failure = %v", err)
	}
	assertIdentityTableCount(t, ctx, fixture, "users", 0)
}

func TestIdentityPostgresOnboardingCASRejectsExpiredWindow(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	userID := uuid.New()
	current, err := identityDomain.NewOnboardingUser(userID, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	username, err := identifier.ParseUsername("Expired9")
	if err != nil {
		t.Fatal(err)
	}
	expiredAt := createdAt.Add(identityDomain.OnboardingTTL)
	next, err := identityDomain.RestoreUser(identityDomain.UserSnapshot{
		ID: userID, Status: identityDomain.UserStatusActive,
		Username: username.Display(), CurrentUsernameKey: username.Key(), UsernameChangedAt: expiredAt,
		CreatedAt: createdAt, UpdatedAt: expiredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork := NewIdentityUnitOfWork(fixture.Pool)
	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		_, insertErr := transaction.Users().Insert(ctx, current)
		return insertErr
	}); err != nil {
		t.Fatal(err)
	}
	err = unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		_, transitionErr := transaction.Users().CompleteOnboardingCAS(ctx, current, next)
		return transitionErr
	})
	if !errors.Is(err, identityDomain.ErrOnboardingExpired) {
		t.Fatalf("expired onboarding CAS error = %v", err)
	}
	_, err = sqlcgen.New(fixture.Pool).CompleteOnboardingUserCAS(ctx, sqlcgen.CompleteOnboardingUserCASParams{
		DisplayUsername: pgtype.Text{String: username.Display(), Valid: true},
		UsernameKey:     pgtype.Text{String: username.Key(), Valid: true},
		ChangedAt:       timeToPG(expiredAt), UserID: uuidToPG(userID),
		ExpectedUpdatedAt: timeToPG(current.Snapshot().UpdatedAt), ExpectedCreatedAt: timeToPG(createdAt),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired onboarding SQL error = %v", err)
	}
	var status string
	if err := fixture.Pool.QueryRow(ctx, "SELECT status FROM users WHERE user_id = $1", userID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "onboarding" {
		t.Fatalf("expired onboarding status = %s", status)
	}
}

type integrationIdentityRuntime struct {
	service *identityDomain.Service
	clock   *clock.Fake
	devices *identityDomain.DeviceService
}

func integrationIdentityService(t testing.TB, fixture *integrationtest.PostgresSchema) *identityDomain.Service {
	return newIntegrationIdentityRuntime(t, fixture).service
}

func newIntegrationIdentityRuntime(t testing.TB, fixture *integrationtest.PostgresSchema) integrationIdentityRuntime {
	t.Helper()
	now := databaseIntegrationTime(t, context.Background(), fixture)
	serviceClock := clock.NewFake(now)
	challengeKeyring := integrationChallengeKeyring[security.UserChallengeKeyPurpose](t, now)
	challengeService, err := identityDomain.NewChallengeService(challengeKeyring, serviceClock)
	if err != nil {
		t.Fatal(err)
	}
	deviceKeyring := integrationChallengeKeyring[security.DeviceHMACKeyPurpose](t, now)
	deviceService, err := identityDomain.NewDeviceService(deviceKeyring, serviceClock)
	if err != nil {
		t.Fatal(err)
	}
	resultCipher, err := secretresult.NewEnvelopeCipher(integrationResultAESKeyring(t, now))
	if err != nil {
		t.Fatal(err)
	}
	resultService, err := secretresult.NewServiceWithIdentityAccess(resultCipher, serviceClock, deviceKeyring, challengeKeyring)
	if err != nil {
		t.Fatal(err)
	}
	recoveryService, err := identityDomain.NewRecoveryCodeService(integrationRecoveryHasher{})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := identifier.NewUsernameValidator(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	recoveryAttemptService, err := identityDomain.NewRecoveryAttemptService(challengeKeyring, serviceClock)
	if err != nil {
		t.Fatal(err)
	}
	auditService, err := audit.NewService(newRepositoryAuditKeyring())
	if err != nil {
		t.Fatal(err)
	}
	service, err := identityDomain.NewServiceWithRecovery(
		challengeService, deviceService, recoveryService, recoveryAttemptService, resultService,
		NewIdentityUnitOfWorkWithAudit(fixture.Pool, auditService), ratelimittest.New(), validator, serviceClock, auditService,
	)
	if err != nil {
		t.Fatal(err)
	}
	return integrationIdentityRuntime{service: service, clock: serviceClock, devices: deviceService}
}

// integrationOnboardIdentity creates one active user and retains its initial device plus recovery authority.
func integrationOnboardIdentity(
	t testing.TB,
	ctx context.Context,
	service *identityDomain.Service,
	bootstrapMarker, onboardingMarker byte,
	username string,
) (identityDomain.BootstrapIdentityResult, identityDomain.CompleteOnboardingResult) {
	t.Helper()
	bootstrap := integrationBootstrapIdentity(t, ctx, service, bootstrapMarker)
	onboarding, err := service.CompleteOnboarding(ctx, identityDomain.CompleteOnboardingCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.41", Username: username,
		OperationID: integrationIdentityOperationID(t, onboardingMarker),
	})
	if err != nil {
		t.Fatal(err)
	}
	return bootstrap, onboarding
}

// integrationBeginRecovery consumes only the anonymous challenge and returns an unbound short-lived grant.
func integrationBeginRecovery(
	t testing.TB,
	ctx context.Context,
	service *identityDomain.Service,
	recoveryCode, flowSuffix string,
) identityDomain.BeginRecoveryResult {
	t.Helper()
	flowID := challenge.RequestFlowID("flow_" + flowSuffix)
	issued, err := service.BeginRecoveryChallenge(ctx, identityDomain.BeginRecoveryChallengeCommand{
		CanonicalOrigin: "https://play.example.test", RequestFlowID: flowID,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.BeginRecovery(ctx, identityDomain.BeginRecoveryCommand{
		CanonicalOrigin: "https://play.example.test", RequestFlowID: flowID,
		ChallengeCredentials: issued.Credentials, RecoveryCode: recoveryCode, ClientIP: "203.0.113.42",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// assertIdentityRecoveryCommitCounts proves the authoritative, secret-result, audit, and outbox writes committed together.
func assertIdentityRecoveryCommitCounts(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	devices, recoveries, secretResults, auditEvents, outboxEvents int,
) {
	t.Helper()
	var gotDevices, gotRecoveries, gotResults, gotAudit, gotOutbox int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM device_credentials),
			(SELECT count(*) FROM user_recovery_credentials),
			(SELECT count(*) FROM secret_operation_results),
			(SELECT count(*) FROM audit_events),
			(SELECT count(*) FROM outbox_events)
	`).Scan(&gotDevices, &gotRecoveries, &gotResults, &gotAudit, &gotOutbox); err != nil {
		t.Fatal(err)
	}
	if gotDevices != devices || gotRecoveries != recoveries || gotResults != secretResults ||
		gotAudit != auditEvents || gotOutbox != outboxEvents {
		t.Fatalf("recovery commit counts: devices=%d recoveries=%d results=%d audit=%d outbox=%d",
			gotDevices, gotRecoveries, gotResults, gotAudit, gotOutbox)
	}
}

func integrationBootstrapIdentity(
	t testing.TB,
	ctx context.Context,
	service *identityDomain.Service,
	marker byte,
) identityDomain.BootstrapIdentityResult {
	t.Helper()
	flowID := challenge.RequestFlowID("flow_" + uuid.NewString())
	begin, err := service.BeginIdentityBootstrap(ctx, identityDomain.BeginIdentityBootstrapCommand{
		CanonicalOrigin: "https://play.example.test", RequestFlowID: flowID, ClientIP: "203.0.113.40",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.BootstrapIdentity(ctx, identityDomain.BootstrapIdentityCommand{
		CanonicalOrigin: "https://play.example.test", RequestFlowID: flowID,
		ChallengeCredentials: begin.Credentials, OperationID: integrationIdentityOperationID(t, marker),
		ClientIP: "203.0.113.40", DeviceLabel: "Phone",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func integrationIdentityOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	value := make([]byte, 16)
	value[len(value)-1] = marker
	operationID, err := idempotency.NewOperationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

func integrationResultAESKeyring(t testing.TB, now time.Time) *security.AESKeyring[security.ResultEnvelopeKeyPurpose] {
	t.Helper()
	path := integrationSymmetricKeyringPath(t, now, "result-keyring.json")
	keyring, err := security.LoadAESKeyring[security.ResultEnvelopeKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func integrationSymmetricKeyringPath(t testing.TB, now time.Time, filename string) string {
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
	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}

type integrationRecoveryHasher struct{}

func (integrationRecoveryHasher) Hash(_ context.Context, input []byte) (string, error) {
	digest := sha256.Sum256(input)
	salt := make([]byte, 16)
	return "$argon2id$v=19$m=65536,t=3,p=2$" + base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(digest[:]), nil
}

// VerifyOrDummy follows one digest-and-compare path for both valid and malformed integration-test hashes.
func (integrationRecoveryHasher) VerifyOrDummy(_ context.Context, encoded string, input []byte) (bool, bool, error) {
	digest := sha256.Sum256(input)
	stored := make([]byte, sha256.Size)
	separator := strings.LastIndexByte(encoded, '$')
	parsed := separator >= 0
	if parsed {
		decoded, err := base64.RawStdEncoding.DecodeString(encoded[separator+1:])
		parsed = err == nil && len(decoded) == sha256.Size
		if parsed {
			copy(stored, decoded)
		}
	}
	return parsed && subtle.ConstantTimeCompare(stored, digest[:]) == 1, false, nil
}

func assertIdentityTableCount(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	table string,
	want int,
) {
	t.Helper()
	allowed := map[string]bool{
		"users": true, "device_credentials": true, "username_claims": true,
		"user_recovery_credentials": true, "secret_operation_results": true,
	}
	if !allowed[table] {
		t.Fatalf("unsupported identity table count %q", table)
	}
	var count int
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s count = %d, want %d", table, count, want)
	}
}
