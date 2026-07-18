package postgres

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pressly/goose/v3"
)

// transactionIntegrationTimeout covers migration setup plus transaction visibility checks on CI.
const transactionIntegrationTimeout = 90 * time.Second

func TestTransactionRunnerUsesOneTransaction(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), transactionIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	runner := NewTransactionRunner(fixture.Pool)

	t.Run("query handle commits atomically", func(t *testing.T) {
		userID := uuid.New()
		err := runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
			if _, err := queries.CreateUser(ctx, createUserParams(userID)); err != nil {
				return err
			}
			if countUsers(t, ctx, fixture, userID) != 0 {
				t.Fatal("transactional write became visible before commit")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if countUsers(t, ctx, fixture, userID) != 1 {
			t.Fatal("committed transactional write is not visible")
		}
	})

	t.Run("callback error rolls back", func(t *testing.T) {
		userID := uuid.New()
		workError := errors.New("reject transaction")
		err := runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
			if _, err := queries.CreateUser(ctx, createUserParams(userID)); err != nil {
				return err
			}
			return workError
		})
		if !errors.Is(err, workError) {
			t.Fatalf("expected callback error, got %v", err)
		}
		if countUsers(t, ctx, fixture, userID) != 0 {
			t.Fatal("callback error did not roll back write")
		}
	})

	t.Run("panic rolls back", func(t *testing.T) {
		userID := uuid.New()
		func() {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("expected transaction callback panic")
				}
			}()
			_ = runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
				if _, err := queries.CreateUser(ctx, createUserParams(userID)); err != nil {
					return err
				}
				panic("rollback transaction")
			})
		}()
		if countUsers(t, ctx, fixture, userID) != 0 {
			t.Fatal("panic did not roll back write")
		}
	})

	t.Run("canceled commit returns error", func(t *testing.T) {
		userID := uuid.New()
		transactionContext, cancelTransaction := context.WithCancel(ctx)
		err := runner.Run(transactionContext, func(ctx context.Context, queries QueryHandle) error {
			if _, err := queries.CreateUser(ctx, createUserParams(userID)); err != nil {
				return err
			}
			cancelTransaction()
			return nil
		})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "commit") {
			t.Fatalf("expected commit error, got %v", err)
		}
		if countUsers(t, ctx, fixture, userID) != 0 {
			t.Fatal("failed commit left a visible write")
		}
	})
}

func applyTransactionTestMigrations(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema) {
	t.Helper()
	var currentUser string
	if err := fixture.Pool.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	database := fixture.OpenSQLDB(t, map[string]string{
		"game_night.owner_role":        currentUser,
		"game_night.audit_writer_role": currentUser,
		"game_night.migration_role":    currentUser,
		"game_night.runtime_role":      currentUser,
		"game_night.worker_role":       currentUser,
	})
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	migrationsDir, err := filepath.Abs(filepath.Join("..", "..", "..", "infra", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, database, migrationsDir); err != nil {
		t.Fatalf("apply transaction test migrations: %v", err)
	}
}

func createUserParams(userID uuid.UUID) sqlcgen.CreateUserParams {
	now := time.Now().UTC()
	return sqlcgen.CreateUserParams{
		UserID:    pgtype.UUID{Bytes: userID, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}
}

func countUsers(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema, userID uuid.UUID) int {
	t.Helper()
	var count int
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE user_id = $1", userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
