package postgres

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
)

func TestIdentityAssistedRecoveryRepositoryConsumesAndRevokesActiveGrant(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := uuid.New()
	firstGrantID := uuid.New()
	secondGrantID := uuid.New()
	firstSelector, err := identifier.NewSelector(bytes.Repeat([]byte{0x81}, identityDomain.AssistedRecoverySelectorBytes))
	if err != nil {
		t.Fatal(err)
	}
	secondSelector, err := identifier.NewSelector(bytes.Repeat([]byte{0x82}, identityDomain.AssistedRecoverySelectorBytes))
	if err != nil {
		t.Fatal(err)
	}
	var adminID uuid.UUID
	if err := fixture.Pool.QueryRow(ctx, "SELECT admin_id FROM admin_accounts WHERE singleton_id = 1").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Pool.Exec(ctx, `
		INSERT INTO users (user_id, status, created_at, updated_at)
		VALUES ($1, 'onboarding', $2, $2)
	`, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Pool.Exec(ctx, `
		INSERT INTO admin_assisted_recovery_grants (
			assisted_grant_id, user_id, selector, secret_hash, purpose, status,
			attempt_count, max_attempts, created_by_admin_id, created_at, expires_at
		) VALUES (
			$1, $2, $3, $4, 'identity.assisted_recovery', 'active', 0, 5, $5, $6, $7
		)
	`, firstGrantID, userID, firstSelector.Value(), persistenceRecoveryPHC, adminID, now,
		now.Add(identityDomain.AssistedRecoveryTTL))
	if err != nil {
		t.Fatal(err)
	}

	resultBinding := integrationResultBinding(t, userID, 0x83)
	result := integrationAvailableResult(t, uuid.New(), resultBinding, now, now.Add(4*time.Minute))
	unitOfWork := NewIdentityUnitOfWork(fixture.Pool)
	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		grant, consumeErr := transaction.AssistedRecoveryGrants().GetForUpdate(ctx, firstGrantID, userID)
		if consumeErr != nil {
			return consumeErr
		}
		consumed, consumeErr := grant.Consume(result.Snapshot().ID, now.Add(time.Minute))
		if consumeErr != nil {
			return consumeErr
		}
		if _, consumeErr = transaction.SecretResults().InsertAvailable(ctx, result); consumeErr != nil {
			return consumeErr
		}
		_, consumeErr = transaction.AssistedRecoveryGrants().ConsumeCAS(ctx, grant, consumed)
		return consumeErr
	}); err != nil {
		t.Fatal(err)
	}

	secondCreatedAt := now.Add(2 * time.Minute)
	_, err = fixture.Pool.Exec(ctx, `
		INSERT INTO admin_assisted_recovery_grants (
			assisted_grant_id, user_id, selector, secret_hash, purpose, status,
			attempt_count, max_attempts, created_by_admin_id, created_at, expires_at
		) VALUES (
			$1, $2, $3, $4, 'identity.assisted_recovery', 'active', 0, 5, $5, $6, $7
		)
	`, secondGrantID, userID, secondSelector.Value(), persistenceRecoveryPHC, adminID,
		secondCreatedAt, secondCreatedAt.Add(identityDomain.AssistedRecoveryTTL))
	if err != nil {
		t.Fatal(err)
	}
	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		if _, lockErr := transaction.Users().GetForUpdate(ctx, userID); lockErr != nil {
			return lockErr
		}
		revoked, revokeErr := transaction.AssistedRecoveryGrants().RevokeActiveForUser(
			ctx, userID, uuid.Nil, now.Add(3*time.Minute),
		)
		if revokeErr == nil && (len(revoked) != 1 || revoked[0] != secondGrantID) {
			t.Fatalf("revoked assisted grants = %v, want [%s]", revoked, secondGrantID)
		}
		return revokeErr
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityDeviceRepositoryListsAndRevokesWithoutExposingSecrets(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := uuid.New()
	preservedID := uuid.New()
	bulkRevokedID := uuid.New()
	directRevokedID := uuid.New()
	_, err := fixture.Pool.Exec(ctx, `
		INSERT INTO users (user_id, status, created_at, updated_at)
		VALUES ($1, 'onboarding', $2, $2)
	`, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Pool.Exec(ctx, `
		INSERT INTO device_credentials (
			credential_id, user_id, secret_hash, secret_key_version, csrf_hash, generation, label,
			created_at, last_seen_at, rotated_at, idle_expires_at, absolute_expires_at
		) VALUES
			($1, $2, decode(repeat('11', 32), 'hex'), 1, decode(repeat('21', 32), 'hex'), 1, 'Preserved', $3, $3, $3, $4, $5),
			($6, $2, decode(repeat('12', 32), 'hex'), 1, decode(repeat('22', 32), 'hex'), 1, 'Bulk revoked', $3, $3, $3, $4, $5),
			($7, $2, decode(repeat('13', 32), 'hex'), 1, decode(repeat('23', 32), 'hex'), 1, 'Direct revoked', $3, $3, $3, $4, $5)
	`, preservedID, userID, now, now.Add(identityDomain.DeviceIdleTTL),
		now.Add(identityDomain.DeviceAbsoluteTTL), bulkRevokedID, directRevokedID)
	if err != nil {
		t.Fatal(err)
	}

	unitOfWork := NewIdentityUnitOfWork(fixture.Pool)
	listedAt := now.Add(time.Hour)
	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		request, listErr := identityDomain.NewDeviceListRequest(
			userID, false, identityDomain.DevicePageCursor{}, identityDomain.MaximumDevicePageSize, listedAt,
		)
		if listErr != nil {
			return listErr
		}
		devices, listErr := transaction.Devices().List(ctx, request)
		if listErr == nil && len(devices) != 3 {
			t.Fatalf("initial active devices = %d, want 3", len(devices))
		}
		return listErr
	}); err != nil {
		t.Fatal(err)
	}

	revokedAt := now.Add(2 * time.Hour)
	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		target, revokeErr := transaction.Devices().GetForUpdate(ctx, directRevokedID)
		if revokeErr != nil {
			return revokeErr
		}
		revoked, revokeErr := target.Revoke(identityDomain.DeviceRevokeUserRequested, revokedAt)
		if revokeErr != nil {
			return revokeErr
		}
		_, revokeErr = transaction.Devices().RevokeCAS(ctx, target, revoked)
		return revokeErr
	}); err != nil {
		t.Fatal(err)
	}
	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		revoked, revokeErr := transaction.Devices().RevokeOtherActiveForRecovery(
			ctx, userID, preservedID, revokedAt.Add(time.Minute),
		)
		if revokeErr == nil && (len(revoked) != 1 || revoked[0].CredentialID != bulkRevokedID) {
			t.Fatalf("bulk revoked devices = %+v, want only %s", revoked, bulkRevokedID)
		}
		return revokeErr
	}); err != nil {
		t.Fatal(err)
	}

	if err := unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		activeRequest, listErr := identityDomain.NewDeviceListRequest(
			userID, false, identityDomain.DevicePageCursor{}, identityDomain.MaximumDevicePageSize, revokedAt.Add(2*time.Minute),
		)
		if listErr != nil {
			return listErr
		}
		active, listErr := transaction.Devices().List(ctx, activeRequest)
		if listErr != nil {
			return listErr
		}
		allRequest, listErr := identityDomain.NewDeviceListRequest(
			userID, true, identityDomain.DevicePageCursor{}, identityDomain.MaximumDevicePageSize, revokedAt.Add(2*time.Minute),
		)
		if listErr != nil {
			return listErr
		}
		all, listErr := transaction.Devices().List(ctx, allRequest)
		if listErr == nil && (len(active) != 1 || active[0].CredentialID != preservedID || len(all) != 3) {
			t.Fatalf("device lists active=%+v all=%+v", active, all)
		}
		return listErr
	}); err != nil {
		t.Fatal(err)
	}
}
