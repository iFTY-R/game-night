package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/pressly/goose/v3"
)

const adminPersistenceTestHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestAdminUnitOfWorkReadsSingletonAndPreservesBootstrapCAS(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	unitOfWork := NewAdminUnitOfWork(fixture.Pool)
	if err := unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if account.Snapshot().Status != adminDomain.AccountStatusBootstrapPending || account.Snapshot().PasswordVersion != 0 {
			t.Fatalf("unexpected bootstrap account state: %+v", account.Snapshot())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdminSecurityMigrationResetsLegacySecurityState(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	database := openMigrationDatabase(t, ctx, fixture)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	migrationsDir := migrationDirectory(t)
	if err := goose.UpToContext(ctx, database, migrationsDir, 27); err != nil {
		t.Fatalf("apply legacy migrations: %v", err)
	}

	now := time.Date(2026, time.July, 25, 10, 30, 0, 0, time.UTC)
	adminID := uuid.New()
	sessionID := uuid.New()
	challengeID := uuid.New()
	pendingEnrollmentID := uuid.New()
	activeEnrollmentID := uuid.New()
	recoveryCodeID := uuid.New()
	assistedGrantID := uuid.New()
	userID := uuid.New()
	attemptID := uuid.New()

	if _, err := fixture.Pool.Exec(ctx, `
		INSERT INTO users (user_id, status, created_at, updated_at)
		VALUES ($1, 'active', $2, $2)
    `, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
        UPDATE admin_accounts
        SET admin_id = $1,
            status = 'recovery_pending',
            password_hash = $2,
            password_algorithm = 'argon2id',
            password_parameters = 'm=65536,t=3,p=2',
            password_version = 7,
            admin_version = 11,
            last_accepted_totp_step = 321,
            created_at = $3,
            updated_at = $3
        WHERE singleton_id = 1
    `, adminID, adminPersistenceTestHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO admin_sessions (
            session_id, admin_id, selector, secret_hash, secret_key_version, csrf_hash,
            kind, admin_version, password_version, attempt_count, max_attempts,
            created_at, last_seen_at, idle_expires_at, absolute_expires_at
        ) VALUES (
            $1, $2, 'legacy-session-selector', decode(repeat('01', 32), 'hex'), 1, decode(repeat('02', 32), 'hex'),
            'recovery_pending', 11, 7, 0, 5, $3, $3, $4, $5
        )
    `, sessionID, adminID, now.Add(-time.Hour), now.Add(time.Hour), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO admin_challenges (
            challenge_id, admin_id, selector, secret_hash, secret_key_version, purpose, audience,
            admin_version, password_version, origin_hash, request_flow_id, attempt_count, max_attempts,
            status, created_at, expires_at
        ) VALUES (
            $1, $2, 'legacy-challenge-selector', decode(repeat('03', 32), 'hex'), 1, 'password_login', 'browser',
            11, 7, decode(repeat('04', 32), 'hex'), 'legacy-flow', 0, 5, 'active', $3, $4
        )
    `, challengeID, adminID, now.Add(-30*time.Minute), now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO admin_totp_enrollments (
            enrollment_id, admin_id, ciphertext, nonce, key_version, status, admin_version, operation_id,
            created_at, expires_at
        ) VALUES (
            $1, $2, decode(repeat('05', 16), 'hex'), decode(repeat('06', 12), 'hex'), 1, 'pending', 11, 'pending-enrollment',
            $3, $4
        )
    `, pendingEnrollmentID, adminID, now.Add(-15*time.Minute), now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO admin_totp_enrollments (
            enrollment_id, admin_id, ciphertext, nonce, key_version, status, admin_version, operation_id,
            created_at, activated_at
        ) VALUES (
            $1, $2, decode(repeat('07', 16), 'hex'), decode(repeat('08', 12), 'hex'), 1, 'active', 11, 'active-enrollment',
            $3, $4
        )
    `, activeEnrollmentID, adminID, now.Add(-2*time.Hour), now.Add(-90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO admin_recovery_codes (
            recovery_code_id, admin_id, selector, secret_hash, set_version, status, created_at
        ) VALUES ($1, $2, 'legacy-recovery-selector', $3, 4, 'active', $4)
    `, recoveryCodeID, adminID, adminPersistenceTestHash, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO admin_assisted_recovery_grants (
            assisted_grant_id, user_id, selector, secret_hash, purpose, status, attempt_count, max_attempts,
            created_by_admin_id, created_at, expires_at
		) VALUES (
			$1, $2, 'legacy-assisted-selector', $3, 'identity.assisted_recovery', 'active', 0, 5, $4, $5, $6
        )
    `, assistedGrantID, userID, adminPersistenceTestHash, adminID, now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO user_recovery_attempts (
            attempt_id, user_id, challenge_id, assisted_grant_id, selector, secret_hash, status,
            created_at, expires_at, request_digest
        ) VALUES (
            $1, $2, NULL, $3, 'legacy-attempt-selector', $4, 'active', $5, $6, decode(repeat('09', 32), 'hex')
        )
    `, attemptID, userID, assistedGrantID, adminPersistenceTestHash, now.Add(-10*time.Minute), now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpByOneContext(ctx, database, migrationsDir); err != nil {
		t.Fatalf("apply security migration: %v", err)
	}

	var status string
	var passwordVersion, adminVersion int64
	if err := fixture.Pool.QueryRow(ctx, `
        SELECT status, password_version, admin_version
        FROM admin_accounts
        WHERE singleton_id = 1
    `).Scan(&status, &passwordVersion, &adminVersion); err != nil {
		t.Fatal(err)
	}
	if status != "setup_required" || passwordVersion != 7 || adminVersion <= 11 {
		t.Fatalf("unexpected migrated account state: status=%s password_version=%d admin_version=%d", status, passwordVersion, adminVersion)
	}

	assertColumnMissing(t, ctx, fixture, "admin_accounts", "last_accepted_totp_step")
	assertColumnPresent(t, ctx, fixture, "admin_totp_enrollments", "enrollment_version")
	assertColumnPresent(t, ctx, fixture, "admin_totp_enrollments", "replay_floor")
	assertColumnPresent(t, ctx, fixture, "admin_sessions", "session_version")
	assertColumnPresent(t, ctx, fixture, "admin_sessions", "client_ip")
	assertColumnPresent(t, ctx, fixture, "admin_sessions", "user_agent")
	assertTablePresent(t, ctx, fixture, "admin_assisted_recovery_grants")
	var assistedGrantStatus, recoveryAttemptStatus string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT status FROM admin_assisted_recovery_grants WHERE assisted_grant_id = $1
	`, assistedGrantID).Scan(&assistedGrantStatus); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT status FROM user_recovery_attempts WHERE recovery_attempt_id = $1
	`, attemptID).Scan(&recoveryAttemptStatus); err != nil {
		t.Fatal(err)
	}
	if assistedGrantStatus != "revoked" || recoveryAttemptStatus != "revoked" {
		t.Fatalf("legacy assisted recovery state: grant=%s attempt=%s", assistedGrantStatus, recoveryAttemptStatus)
	}
	assertTablePresent(t, ctx, fixture, "admin_elevation_grants")
	assertTablePresent(t, ctx, fixture, "admin_command_receipts")
}

func TestAdminSecurityMigrationHardensNewSecurityObjects(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	database := openMigrationDatabase(t, ctx, fixture)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, database, migrationDirectory(t), 28); err != nil {
		t.Fatalf("apply migrations through security rebuild: %v", err)
	}

	assertFunctionVolatility(t, ctx, fixture, "run_expiry_cleanup", "v")
	assertFunctionExecuteDeniedToPublic(t, ctx, fixture, "run_expiry_cleanup()")
	assertFunctionExecuteDeniedToPublic(
		t,
		ctx,
		fixture,
		"reset_admin_account(bytea, uuid, bytea, bytea, integer, timestamptz, text, text, text, uuid, bytea)",
	)
	assertTablePrivilegeDeniedToPublic(t, ctx, fixture, "admin_elevation_grants", "SELECT")
	assertTablePrivilegeDeniedToPublic(t, ctx, fixture, "admin_elevation_grants", "INSERT")
	assertTablePrivilegeDeniedToPublic(t, ctx, fixture, "admin_command_receipts", "SELECT")
	assertTablePrivilegeDeniedToPublic(t, ctx, fixture, "admin_command_receipts", "INSERT")
}

func TestAdminSessionEnrollmentElevationAndReceiptPersistence(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	unitOfWork := NewAdminUnitOfWork(fixture.Pool)
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

	if err := unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.Transaction) error {
		account, err := seedActiveAdminAccount(ctx, transaction.Accounts(), now)
		if err != nil {
			return err
		}

		pending, err := transaction.Enrollments().CreatePending(ctx, mustRestoreEnrollment(t, adminDomain.EnrollmentSnapshot{
			ID: uuid.New(), AdminID: account.Snapshot().ID, Ciphertext: bytes.Repeat([]byte{0x31}, 16), Nonce: bytes.Repeat([]byte{0x32}, 12),
			KeyVersion: 1, Status: adminDomain.EnrollmentStatusPending, AdminVersion: account.Snapshot().AdminVersion,
			EnrollmentVersion: 1, OperationID: "pending-enrollment-op", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
		}))
		if err != nil {
			return err
		}
		if pending.Snapshot().EnrollmentVersion != 1 {
			t.Fatalf("pending enrollment version = %d, want 1", pending.Snapshot().EnrollmentVersion)
		}

		nextAccount, err := transaction.Accounts().RecordMFAChangeCAS(ctx, account, now.Add(time.Minute))
		if err != nil {
			return err
		}
		active, err := transaction.Enrollments().ActivateCAS(ctx, pending, 12345, nextAccount.Snapshot().AdminVersion, now.Add(time.Minute))
		if err != nil {
			return err
		}
		if !active.Active() || active.Snapshot().ReplayFloor == nil || *active.Snapshot().ReplayFloor != 12345 {
			t.Fatalf("active enrollment replay floor = %+v", active.Snapshot())
		}

		accepted, err := transaction.Enrollments().AcceptTOTPCAS(ctx, active, 12346, now.Add(2*time.Minute))
		if err != nil {
			return err
		}
		if accepted.Snapshot().ReplayFloor == nil || *accepted.Snapshot().ReplayFloor != 12346 ||
			accepted.Snapshot().EnrollmentVersion != active.Snapshot().EnrollmentVersion+1 {
			t.Fatalf("accepted enrollment state = %+v", accepted.Snapshot())
		}

		for index, marker := range []byte{0x41, 0x42} {
			code := mustRestoreAdminRecoveryCode(t, adminDomain.RecoveryCodeSnapshot{
				ID: uuid.New(), AdminID: account.Snapshot().ID, Selector: mustSelector(t, marker),
				SecretHash: adminPersistenceTestHash, SetVersion: 7, Status: adminDomain.RecoveryCodeStatusActive,
				CreatedAt: now.Add(time.Duration(index+1) * time.Minute),
			})
			if err := transaction.RecoveryCodes().Insert(ctx, code); err != nil {
				return err
			}
		}
		recoveryState, err := transaction.RecoveryCodes().GetActiveSetState(ctx, account.Snapshot().ID)
		if err != nil {
			return err
		}
		if recoveryState.SetVersion != 7 || recoveryState.RemainingActive != 2 {
			t.Fatalf("recovery-code state = %+v, want version 7 with 2 active", recoveryState)
		}
		loadedCode, err := transaction.RecoveryCodes().FindActiveBySelector(ctx, mustSelector(t, 0x41))
		if err != nil {
			return err
		}
		if _, err = transaction.RecoveryCodes().ConsumeCAS(ctx, loadedCode, now.Add(4*time.Minute)); err != nil {
			return err
		}
		recoveryState, err = transaction.RecoveryCodes().GetActiveSetState(ctx, account.Snapshot().ID)
		if err != nil {
			return err
		}
		if recoveryState.SetVersion != 7 || recoveryState.RemainingActive != 1 {
			t.Fatalf("recovery-code state after consume = %+v, want version 7 with 1 active", recoveryState)
		}

		fullSession := mustRestoreSession(t, adminDomain.SessionSnapshot{
			ID:                uuid.New(),
			AdminID:           account.Snapshot().ID,
			Selector:          mustSelector(t, 0x7b),
			SecretMAC:         mustAdminSessionMAC(0x7c),
			CSRFHash:          mustAdminSessionMAC(0x7d),
			Kind:              adminDomain.SessionKindFull,
			AdminVersion:      nextAccount.Snapshot().AdminVersion,
			PasswordVersion:   nextAccount.Snapshot().PasswordVersion,
			SessionVersion:    1,
			ClientIP:          "198.51.100.20",
			UserAgent:         "persist-test",
			MaxAttempts:       5,
			CreatedAt:         now.Add(3 * time.Minute),
			LastSeenAt:        now.Add(3 * time.Minute),
			IdleExpiresAt:     now.Add(33 * time.Minute),
			AbsoluteExpiresAt: now.Add(12 * time.Hour),
		})
		if err := transaction.Sessions().Insert(ctx, fullSession); err != nil {
			return err
		}

		stored, err := transaction.Sessions().GetForUpdate(ctx, fullSession.Snapshot().Selector)
		if err != nil {
			return err
		}
		if stored.Snapshot().SessionVersion != 1 || stored.Snapshot().ClientIP != "198.51.100.20" || stored.Snapshot().UserAgent != "persist-test" {
			t.Fatalf("stored session metadata = %+v", stored.Snapshot())
		}
		byID, err := transaction.Sessions().GetByIDForUpdate(ctx, stored.Snapshot().ID)
		if err != nil {
			return err
		}
		if byID.Snapshot().ID != stored.Snapshot().ID {
			t.Fatalf("session-by-id mismatch: got=%s want=%s", byID.Snapshot().ID, stored.Snapshot().ID)
		}
		listed, err := transaction.Sessions().ListActiveForAdmin(ctx, stored.Snapshot().AdminID, now.Add(4*time.Minute))
		if err != nil {
			return err
		}
		if len(listed) != 1 {
			t.Fatalf("listed sessions = %d, want 1", len(listed))
		}

		touched, err := transaction.Sessions().TouchCAS(ctx, stored, now.Add(4*time.Minute), adminDomain.AdminFullSessionIdleTTL)
		if err != nil {
			return err
		}
		if touched.Snapshot().SessionVersion != 2 {
			t.Fatalf("touched session version = %d, want 2", touched.Snapshot().SessionVersion)
		}

		grant, err := adminDomain.NewElevation(
			touched,
			accepted.Snapshot().EnrollmentVersion,
			adminDomain.ElevationScopeSecurityRevokeSessions,
			now.Add(4*time.Minute),
			now.Add(9*time.Minute),
		)
		if err != nil {
			return err
		}
		storedGrant, err := transaction.Elevations().UpsertLive(ctx, grant)
		if err != nil {
			return err
		}
		if err := storedGrant.Validate(touched, accepted.Snapshot().EnrollmentVersion, adminDomain.ElevationScopeSecurityRevokeSessions, now.Add(5*time.Minute)); err != nil {
			return err
		}
		staleSession, err := transaction.Sessions().TouchCAS(ctx, touched, now.Add(5*time.Minute), adminDomain.AdminFullSessionIdleTTL)
		if err != nil {
			return err
		}
		staleGrant, err := transaction.Elevations().GetForSessionScope(ctx, staleSession.Snapshot().ID, adminDomain.ElevationScopeSecurityRevokeSessions, now.Add(5*time.Minute))
		if err != nil {
			return err
		}
		if err := staleGrant.Validate(staleSession, accepted.Snapshot().EnrollmentVersion, adminDomain.ElevationScopeSecurityRevokeSessions, now.Add(5*time.Minute)); !errors.Is(err, adminDomain.ErrElevationDenied) {
			t.Fatalf("stale grant validation error = %v, want ErrElevationDenied", err)
		}

		operationID := mustOperationID(t, 0x44)
		digest := mustDigest(t, 0x55)
		receipt, err := transaction.CommandReceipts().Save(ctx, adminDomain.CommandReceipt{
			AdminID: account.Snapshot().ID, OperationID: operationID, RequestDigest: digest,
			Command: "revoke_other_admin_sessions", TargetType: "admin", TargetID: account.Snapshot().ID.String(),
			ResultAdminVersion: nextAccount.Snapshot().AdminVersion, ResultPasswordVersion: nextAccount.Snapshot().PasswordVersion,
			ResultSessionVersion: staleSession.Snapshot().SessionVersion, ResultEnrollmentVersion: accepted.Snapshot().EnrollmentVersion,
			AuditEventID: uuid.New(), CreatedAt: now.Add(6 * time.Minute),
		})
		if err != nil {
			return err
		}
		loadedReceipt, err := transaction.CommandReceipts().Get(ctx, account.Snapshot().ID, operationID)
		if err != nil {
			return err
		}
		if loadedReceipt.OperationID != receipt.OperationID || loadedReceipt.RequestDigest != receipt.RequestDigest {
			t.Fatalf("loaded receipt = %+v, want %+v", loadedReceipt, receipt)
		}
		if _, err := transaction.CommandReceipts().Save(ctx, adminDomain.CommandReceipt{
			AdminID: account.Snapshot().ID, OperationID: operationID, RequestDigest: mustDigest(t, 0x66),
			Command: "revoke_other_admin_sessions", TargetType: "admin", TargetID: account.Snapshot().ID.String(),
			ResultAdminVersion: nextAccount.Snapshot().AdminVersion, ResultPasswordVersion: nextAccount.Snapshot().PasswordVersion,
			ResultSessionVersion: staleSession.Snapshot().SessionVersion, ResultEnrollmentVersion: accepted.Snapshot().EnrollmentVersion,
			AuditEventID: uuid.New(), CreatedAt: now.Add(6 * time.Minute),
		}); !errors.Is(err, adminDomain.ErrIdempotencyConflict) {
			t.Fatalf("receipt digest conflict error = %v, want ErrIdempotencyConflict", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdminSessionAndElevationCASOperations(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	unitOfWork := NewAdminUnitOfWork(fixture.Pool)
	now := time.Date(2026, time.July, 25, 16, 0, 0, 0, time.UTC)

	buildSession := func(marker byte, adminID uuid.UUID, at time.Time, adminVersion, passwordVersion int64) adminDomain.Session {
		return mustRestoreSession(t, adminDomain.SessionSnapshot{
			ID:                uuid.New(),
			AdminID:           adminID,
			Selector:          mustSelector(t, marker),
			SecretMAC:         mustAdminSessionMAC(marker + 1),
			CSRFHash:          mustAdminSessionMAC(marker + 2),
			Kind:              adminDomain.SessionKindFull,
			AdminVersion:      adminVersion,
			PasswordVersion:   passwordVersion,
			SessionVersion:    1,
			ClientIP:          "203.0.113.10",
			UserAgent:         "cas-test",
			MaxAttempts:       5,
			CreatedAt:         at,
			LastSeenAt:        at,
			IdleExpiresAt:     at.Add(30 * time.Minute),
			AbsoluteExpiresAt: at.Add(12 * time.Hour),
		})
	}

	if err := unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.Transaction) error {
		account, err := seedActiveAdminAccount(ctx, transaction.Accounts(), now.Add(-time.Minute))
		if err != nil {
			return err
		}

		preserved := buildSession(0x71, account.Snapshot().ID, now, account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion)
		singleRevoke := buildSession(0x81, account.Snapshot().ID, now.Add(time.Minute), account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion)
		bulkRevoke := buildSession(0x91, account.Snapshot().ID, now.Add(2*time.Minute), account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion)
		for _, session := range []adminDomain.Session{preserved, singleRevoke, bulkRevoke} {
			if err := transaction.Sessions().Insert(ctx, session); err != nil {
				return err
			}
		}

		loadedSingle, err := transaction.Sessions().GetByIDForUpdate(ctx, singleRevoke.Snapshot().ID)
		if err != nil {
			return err
		}
		revokedSingle, err := transaction.Sessions().RevokeCAS(ctx, loadedSingle, "manual_revoke", now.Add(3*time.Minute))
		if err != nil {
			return err
		}
		if revokedSingle.Snapshot().SessionVersion != loadedSingle.Snapshot().SessionVersion+1 || revokedSingle.Snapshot().RevokedAt.IsZero() {
			t.Fatalf("single-session revoke state = %+v", revokedSingle.Snapshot())
		}

		activeBeforeBulk, err := transaction.Sessions().ListActiveForAdmin(ctx, account.Snapshot().ID, now.Add(3*time.Minute))
		if err != nil {
			return err
		}
		if len(activeBeforeBulk) != 2 {
			t.Fatalf("active sessions before bulk revoke = %d, want 2", len(activeBeforeBulk))
		}

		grant, err := adminDomain.NewElevation(
			preserved,
			0,
			adminDomain.ElevationScopeSecurityDisableMFA,
			now.Add(3*time.Minute),
			now.Add(7*time.Minute),
		)
		if err != nil {
			return err
		}
		storedGrant, err := transaction.Elevations().UpsertLive(ctx, grant)
		if err != nil {
			return err
		}
		loadedGrant, err := transaction.Elevations().GetForSessionScope(
			ctx,
			preserved.Snapshot().ID,
			adminDomain.ElevationScopeSecurityDisableMFA,
			now.Add(4*time.Minute),
		)
		if err != nil {
			return err
		}
		if loadedGrant.Snapshot().Scope != storedGrant.Snapshot().Scope || loadedGrant.Snapshot().SessionID != storedGrant.Snapshot().SessionID {
			t.Fatalf("loaded grant = %+v, want %+v", loadedGrant.Snapshot(), storedGrant.Snapshot())
		}
		liveGrants, err := transaction.Elevations().ListLiveForSessions(
			ctx,
			account.Snapshot().ID,
			[]uuid.UUID{preserved.Snapshot().ID, bulkRevoke.Snapshot().ID},
			now.Add(4*time.Minute),
		)
		if err != nil {
			return err
		}
		if len(liveGrants) != 1 || liveGrants[0].Snapshot().SessionID != preserved.Snapshot().ID {
			t.Fatalf("live grants = %+v, want preserved-session grant", liveGrants)
		}
		revokedGrant, err := transaction.Elevations().RevokeCAS(ctx, loadedGrant, now.Add(4*time.Minute))
		if err != nil {
			return err
		}
		if revokedGrant.Snapshot().RevokedAt.IsZero() {
			t.Fatalf("revoked grant missing tombstone: %+v", revokedGrant.Snapshot())
		}
		if _, err := transaction.Elevations().GetForSessionScope(
			ctx,
			preserved.Snapshot().ID,
			adminDomain.ElevationScopeSecurityDisableMFA,
			now.Add(4*time.Minute),
		); !errors.Is(err, adminDomain.ErrElevationDenied) {
			t.Fatalf("revoked grant lookup error = %v, want ErrElevationDenied", err)
		}

		revokedOthers, err := transaction.Sessions().RevokeOtherActiveCAS(
			ctx,
			account.Snapshot().ID,
			preserved.Snapshot().ID,
			preserved.Snapshot().AdminVersion,
			preserved.Snapshot().SessionVersion,
			"bulk_revoke",
			now.Add(5*time.Minute),
		)
		if err != nil {
			return err
		}
		if len(revokedOthers) != 1 || revokedOthers[0].Snapshot().ID != bulkRevoke.Snapshot().ID {
			t.Fatalf("bulk-revoked sessions = %+v", revokedOthers)
		}
		if revokedOthers[0].Snapshot().SessionVersion != bulkRevoke.Snapshot().SessionVersion+1 {
			t.Fatalf("bulk-revoked session version = %d, want %d", revokedOthers[0].Snapshot().SessionVersion, bulkRevoke.Snapshot().SessionVersion+1)
		}

		activeAfterBulk, err := transaction.Sessions().ListActiveForAdmin(ctx, account.Snapshot().ID, now.Add(5*time.Minute))
		if err != nil {
			return err
		}
		if len(activeAfterBulk) != 1 || activeAfterBulk[0].Snapshot().ID != preserved.Snapshot().ID {
			t.Fatalf("active sessions after bulk revoke = %+v", activeAfterBulk)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func openMigrationDatabase(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
) *sql.DB {
	t.Helper()
	var currentUser string
	if err := fixture.Pool.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	return fixture.OpenSQLDB(t, map[string]string{
		"game_night.owner_role":        currentUser,
		"game_night.audit_writer_role": currentUser,
		"game_night.migration_role":    currentUser,
		"game_night.runtime_role":      currentUser,
		"game_night.worker_role":       currentUser,
	})
}

func migrationDirectory(t testing.TB) string {
	t.Helper()
	directory, err := filepath.Abs(filepath.Join("..", "..", "..", "infra", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func assertColumnPresent(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema, tableName, columnName string) {
	t.Helper()
	var exists bool
	if err := fixture.Pool.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1
            FROM information_schema.columns
            WHERE table_schema = current_schema()
              AND table_name = $1
              AND column_name = $2
        )
    `, tableName, columnName).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("expected %s.%s to exist", tableName, columnName)
	}
}

func assertColumnMissing(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema, tableName, columnName string) {
	t.Helper()
	var exists bool
	if err := fixture.Pool.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1
            FROM information_schema.columns
            WHERE table_schema = current_schema()
              AND table_name = $1
              AND column_name = $2
        )
    `, tableName, columnName).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatalf("expected %s.%s to be removed", tableName, columnName)
	}
}

func assertTablePresent(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema, tableName string) {
	t.Helper()
	var exists bool
	if err := fixture.Pool.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1
            FROM information_schema.tables
            WHERE table_schema = current_schema()
              AND table_name = $1
        )
    `, tableName).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("expected table %s to exist", tableName)
	}
}

func assertTableMissing(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema, tableName string) {
	t.Helper()
	var exists bool
	if err := fixture.Pool.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1
            FROM information_schema.tables
            WHERE table_schema = current_schema()
              AND table_name = $1
        )
    `, tableName).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatalf("expected table %s to be removed", tableName)
	}
}

func assertFunctionVolatility(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema, functionName, expected string) {
	t.Helper()
	var volatility string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT function_row.provolatile
		FROM pg_proc AS function_row
		JOIN pg_namespace AS namespace_row ON namespace_row.oid = function_row.pronamespace
		WHERE namespace_row.nspname = current_schema()
		  AND function_row.proname = $1
		  AND function_row.pronargs = 0
	`, functionName).Scan(&volatility); err != nil {
		t.Fatal(err)
	}
	if volatility != expected {
		t.Fatalf("function %s volatility = %s, want %s", functionName, volatility, expected)
	}
}

func assertTablePrivilegeDeniedToPublic(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	tableName,
	privilege string,
) {
	t.Helper()
	var allowed bool
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT has_table_privilege(
			'public',
			format('%I.%I', current_schema(), $1),
			$2
		)
	`, tableName, privilege).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatalf("expected PUBLIC to lack %s on %s", privilege, tableName)
	}
}

func assertFunctionExecuteDeniedToPublic(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	signature string,
) {
	t.Helper()
	var allowed bool
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT has_function_privilege(
			'public',
			format('%I.%s', current_schema(), $1::text),
			'EXECUTE'
		)
	`, signature).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatalf("expected PUBLIC to lack EXECUTE on %s", signature)
	}
}

func seedActiveAdminAccount(
	ctx context.Context,
	repository adminDomain.AccountRepository,
	at time.Time,
) (adminDomain.Account, error) {
	account, err := repository.GetForUpdate(ctx)
	if err != nil {
		return adminDomain.Account{}, err
	}
	bootstrapped, err := repository.BootstrapPasswordCAS(ctx, account, adminPersistenceTestHash, adminDomain.PasswordAlgorithmArgon2id, "m=65536,t=3,p=2", at)
	if err != nil {
		return adminDomain.Account{}, err
	}
	return repository.TransitionStatusCAS(ctx, bootstrapped, adminDomain.AccountStatusActive, at.Add(time.Second))
}

func mustRestoreEnrollment(t testing.TB, snapshot adminDomain.EnrollmentSnapshot) adminDomain.Enrollment {
	t.Helper()
	enrollment, err := adminDomain.RestoreEnrollment(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return enrollment
}

func mustRestoreSession(t testing.TB, snapshot adminDomain.SessionSnapshot) adminDomain.Session {
	t.Helper()
	session, err := adminDomain.RestoreSession(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func mustRestoreAdminRecoveryCode(t testing.TB, snapshot adminDomain.RecoveryCodeSnapshot) adminDomain.RecoveryCode {
	t.Helper()
	code, err := adminDomain.RestoreRecoveryCode(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func mustSelector(t testing.TB, marker byte) string {
	t.Helper()
	selector, err := identifier.NewSelector(bytes.Repeat([]byte{marker}, 16))
	if err != nil {
		t.Fatal(err)
	}
	return selector.Value()
}

func mustAdminSessionMAC(marker byte) security.MAC[security.AdminSessionKeyPurpose] {
	return security.MAC[security.AdminSessionKeyPurpose]{
		KeyVersion: 1,
		Value:      bytes.Repeat([]byte{marker}, 32),
	}
}

func mustDigest(t testing.TB, marker byte) idempotency.Digest {
	t.Helper()
	return mustDigestBytes(t, bytes.Repeat([]byte{marker}, idempotency.DigestSize))
}

func mustDigestBytes(t testing.TB, value []byte) idempotency.Digest {
	t.Helper()
	digest, err := idempotency.NewDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func mustOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	selector, err := identifier.NewSelector(bytes.Repeat([]byte{marker}, 16))
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := idempotency.ParseOperationID(selector.Value())
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}
