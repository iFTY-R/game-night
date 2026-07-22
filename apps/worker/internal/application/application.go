// Package application composes checkpoint worker dependencies and owns PostgreSQL shutdown.
package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/iFTY-R/game-night/apps/internal/checkpointstorage"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/apps/worker/internal/checkpoint"
	workerconfig "github.com/iFTY-R/game-night/apps/worker/internal/config"
	workerruntime "github.com/iFTY-R/game-night/apps/worker/internal/runtime"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/keyrotation"
	"github.com/iFTY-R/game-night/platform/persistence/postgres"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	checkpointProbeInterval = 15 * time.Second
	checkpointProbeTimeout  = 2 * time.Second
)

var (
	errInvalidOptions       = errors.New("invalid worker application options")
	errInitializeKeyrings   = errors.New("initialize worker keyrings")
	errInitializePostgreSQL = errors.New("initialize worker PostgreSQL")
	errInitializeSink       = errors.New("initialize worker checkpoint sink")
	errInitializeRuntime    = errors.New("initialize worker runtime")
)

// Application owns the polling runtime and the worker-role database pool.
type Application struct {
	runtime *workerruntime.Runtime
	pool    *pgxpool.Pool
}

// New builds the complete worker graph and checks sink readiness before any consumer lease can be claimed.
func New(ctx context.Context, config workerconfig.Config, logger *slog.Logger) (_ *Application, returnedErr error) {
	if ctx == nil || logger == nil {
		return nil, errInvalidOptions
	}
	source := clock.System{}
	keyrings, err := security.LoadOperationsKeyrings(config.Shared.Keyrings.SecurityPaths(), source.Now())
	if err != nil {
		return nil, errInitializeKeyrings
	}
	auditService, err := audit.NewService(keyrings.Audit)
	if err != nil {
		return nil, errInitializeKeyrings
	}
	sink, err := checkpointstorage.Build(ctx, config.CheckpointStorage)
	if err != nil {
		return nil, errInitializeSink
	}
	readiness, err := checkpointstorage.NewReadiness(
		config.Shared.Environment, sink, checkpointProbeInterval, checkpointProbeTimeout,
	)
	if err != nil || !readiness.Ready(ctx) {
		return nil, errInitializeSink
	}
	pool, err := postgres.OpenPool(ctx, postgres.PoolConfig{
		DatabaseURL: config.Shared.PostgreSQL.DSN, Schema: config.Shared.PostgreSQL.Schema,
		MinConnections: config.Shared.PostgreSQL.MinConnections, MaxConnections: config.Shared.PostgreSQL.MaxConnections,
		MaxConnectionAge:  config.Shared.PostgreSQL.MaxConnectionLifetime,
		MaxConnectionIdle: config.Shared.PostgreSQL.MaxConnectionIdleTime,
		HealthCheckPeriod: config.Shared.PostgreSQL.HealthCheckPeriod,
	})
	if err != nil {
		return nil, errInitializePostgreSQL
	}
	application := &Application{pool: pool}
	defer func() {
		if returnedErr != nil {
			application.Close()
		}
	}()
	if err = postgres.NewOperationsKeyringReferenceChecker(pool, keyrings).Check(ctx); err != nil {
		return nil, errInitializeKeyrings
	}
	piiProtector, err := profile.NewDefaultPIIProtector(keyrings.PII)
	if err != nil {
		return nil, errInitializeKeyrings
	}
	totpService, err := admin.NewTOTPService(keyrings.TOTP)
	if err != nil {
		return nil, errInitializeKeyrings
	}
	checkpointPolicy, err := audit.NewCheckpointHealthPolicyWithThresholds(
		config.Shared.Environment == sharedconfig.EnvironmentProduction,
		readiness,
		uint64(config.Shared.Checkpoint.MaxEvents),
		config.Shared.Checkpoint.MaxInterval,
	)
	if err != nil {
		return nil, errInitializeRuntime
	}
	dispatcher, err := checkpoint.NewDispatcher(checkpoint.Config{
		Owner: config.Runtime.InstanceID, LeaseDuration: config.Runtime.LeaseDuration, BatchSize: config.Runtime.BatchSize,
	}, postgres.NewOutboxUnitOfWork(pool), sink, auditService, source)
	if err != nil {
		return nil, errInitializeRuntime
	}
	rotation, err := keyrotation.NewService(keyrotation.Config{
		Owner: config.Runtime.InstanceID, LeaseDuration: config.Runtime.LeaseDuration, BatchSize: config.Runtime.BatchSize,
	}, postgres.NewKeyRotationUnitOfWork(pool, auditService), piiProtector, totpService, auditService, checkpointPolicy, source)
	if err != nil {
		return nil, errInitializeRuntime
	}
	cleanup := postgres.NewExpiryCleanup(pool, config.Runtime.RoomIdleTimeout)
	application.runtime, err = workerruntime.NewWithOperations(dispatcher, rotation, cleanup, config.Runtime.PollInterval, logger)
	if err != nil {
		return nil, errInitializeRuntime
	}
	return application, nil
}

// Run blocks until the process context is canceled while keeping all passes serial.
func (application *Application) Run(ctx context.Context) error {
	if application == nil || application.runtime == nil {
		return errInvalidOptions
	}
	return application.runtime.Run(ctx)
}

// Close releases the worker database pool after the runtime has stopped claiming new work.
func (application *Application) Close() {
	if application != nil && application.pool != nil {
		application.pool.Close()
		application.pool = nil
	}
}
