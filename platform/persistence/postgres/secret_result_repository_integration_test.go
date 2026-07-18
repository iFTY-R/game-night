package postgres

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/secretresult"
)

func TestSecretResultRepositoryIdempotencyAndTerminalErasure(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	unitOfWork := NewSecretResultUnitOfWork(fixture.Pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	binding := integrationResultBinding(t, uuid.New(), 0x11)
	available := integrationAvailableResult(t, uuid.New(), binding, now, now.Add(5*time.Minute))

	if err := unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		_, err := repository.InsertAvailable(ctx, available)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		stored, err := repository.GetByIDForUpdate(ctx, available.Snapshot().ID)
		if err != nil {
			return err
		}
		if stored.Snapshot().Binding != binding {
			t.Fatalf("result-id lookup binding = %+v, want %+v", stored.Snapshot().Binding, binding)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	mismatchedVersion := binding
	mismatchedVersion.ResultVersion++
	err := unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		_, confirmErr := repository.ConfirmCAS(ctx, secretresult.Confirmation{
			ResultID: available.Snapshot().ID, Binding: mismatchedVersion, ConfirmedAt: now.Add(time.Minute),
		})
		return confirmErr
	})
	if !errors.Is(err, secretresult.ErrConcurrentTransition) {
		t.Fatalf("result-version mismatch error = %v", err)
	}
	var status string
	var ciphertext, nonce, wrappedDataKey []byte
	if err := fixture.Pool.QueryRow(ctx, `
        SELECT status, ciphertext, nonce, wrapped_data_key
        FROM secret_operation_results
        WHERE result_id = $1
    `, available.Snapshot().ID).Scan(&status, &ciphertext, &nonce, &wrappedDataKey); err != nil {
		t.Fatal(err)
	}
	if status != string(secretresult.StatusAvailable) || len(ciphertext) == 0 || len(nonce) == 0 || len(wrappedDataKey) == 0 {
		t.Fatalf("version-mismatched confirmation changed secret result: status=%s ciphertext=%d nonce=%d wrapped-key=%d",
			status, len(ciphertext), len(nonce), len(wrappedDataKey))
	}

	if err := unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		stored, err := repository.GetByOperationForUpdate(ctx, binding.Key)
		if err != nil {
			return err
		}
		resolution, err := stored.Resolve(binding, now)
		if err != nil || resolution.Kind != secretresult.ReplayAvailable {
			t.Fatalf("exact replay resolution = %+v, err=%v", resolution, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	conflictingBinding := binding
	conflictingBinding.RequestDigest[0] ^= 0xff
	conflicting := integrationAvailableResult(t, uuid.New(), conflictingBinding, now, now.Add(5*time.Minute))
	err = unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		_, insertErr := repository.InsertAvailable(ctx, conflicting)
		return insertErr
	})
	if !errors.Is(err, secretresult.ErrIdempotencyConflict) {
		t.Fatalf("digest conflict error = %v", err)
	}

	isolatedBinding := binding
	isolatedBinding.Key.ActorID = uuid.New()
	isolated := integrationAvailableResult(t, uuid.New(), isolatedBinding, now, now.Add(5*time.Minute))
	if err := unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		_, insertErr := repository.InsertAvailable(ctx, isolated)
		return insertErr
	}); err != nil {
		t.Fatalf("different actor must own an independent operation key: %v", err)
	}

	if err := unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		stored, err := repository.GetByOperationForUpdate(ctx, binding.Key)
		if err != nil {
			return err
		}
		_, err = repository.ConfirmCAS(ctx, secretresult.Confirmation{
			ResultID: stored.Snapshot().ID, Binding: binding, ConfirmedAt: now.Add(time.Minute),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertStoredSecretResultState(t, ctx, unitOfWork, binding.Key, secretresult.StatusConfirmed)
}

func TestSecretResultConfirmAndExpiryCASHaveSingleTerminalWinner(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	unitOfWork := NewSecretResultUnitOfWork(fixture.Pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiresAt := now.Add(2 * time.Minute)
	binding := integrationResultBinding(t, uuid.New(), 0x33)
	available := integrationAvailableResult(t, uuid.New(), binding, now, expiresAt)
	if err := unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		_, err := repository.InsertAvailable(ctx, available)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		<-start
		results <- unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
			stored, err := repository.GetByOperationForUpdate(ctx, binding.Key)
			if err != nil {
				return err
			}
			_, err = repository.ConfirmCAS(ctx, secretresult.Confirmation{
				ResultID: stored.Snapshot().ID, Binding: binding, ConfirmedAt: expiresAt.Add(-time.Microsecond),
			})
			return err
		})
	}()
	go func() {
		defer waitGroup.Done()
		<-start
		results <- unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
			stored, err := repository.GetByOperationForUpdate(ctx, binding.Key)
			if err != nil {
				return err
			}
			_, err = repository.ExpireCAS(ctx, stored, expiresAt)
			return err
		})
	}()
	close(start)
	waitGroup.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, secretresult.ErrConcurrentTransition) {
			t.Fatalf("unexpected CAS loser error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("terminal CAS successes = %d, want 1", successes)
	}
	if err := unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		stored, err := repository.GetByOperationForUpdate(ctx, binding.Key)
		if err != nil {
			return err
		}
		snapshot := stored.Snapshot()
		if snapshot.Status != secretresult.StatusConfirmed && snapshot.Status != secretresult.StatusExpired {
			t.Fatalf("terminal status = %s", snapshot.Status)
		}
		if !snapshot.Payload.Empty() {
			t.Fatal("terminal winner did not erase all secret columns")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func integrationResultBinding(t testing.TB, actorID uuid.UUID, digestByte byte) secretresult.Binding {
	t.Helper()
	operationID, err := secretresult.NewOperationID(bytes.Repeat([]byte{0x55}, 16))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := secretresult.NewDigest(bytes.Repeat([]byte{digestByte}, secretresult.DigestSize))
	if err != nil {
		t.Fatal(err)
	}
	return secretresult.Binding{
		Key:           secretresult.Key{Scope: secretresult.ScopeIdentityBootstrap, ActorID: actorID, OperationID: operationID},
		RequestDigest: digest, ResultType: secretresult.ResultTypeIdentityDeviceCredential, ResultVersion: 1,
	}
}

func integrationAvailableResult(t testing.TB, resultID uuid.UUID, binding secretresult.Binding, completedAt, expiresAt time.Time) secretresult.Result {
	t.Helper()
	result, err := secretresult.NewAvailable(
		resultID,
		binding,
		secretresult.EncryptedPayload{Ciphertext: []byte("ciphertext"), Nonce: []byte("nonce"), WrappedDataKey: []byte("wrapped-key"), KeyVersion: 1},
		completedAt,
		expiresAt,
		expiresAt.Add(secretresult.MinimumTombstoneRetention),
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertStoredSecretResultState(t testing.TB, ctx context.Context, unitOfWork *SecretResultUnitOfWork, key secretresult.Key, status secretresult.Status) {
	t.Helper()
	if err := unitOfWork.Run(ctx, func(ctx context.Context, repository secretresult.Repository) error {
		stored, err := repository.GetByOperationForUpdate(ctx, key)
		if err != nil {
			return err
		}
		snapshot := stored.Snapshot()
		if snapshot.Status != status || !snapshot.Payload.Empty() {
			t.Fatalf("stored terminal result = %+v", snapshot)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
