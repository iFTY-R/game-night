// Package application composes checkpoint worker dependencies and owns PostgreSQL shutdown.
package application

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/iFTY-R/game-night/apps/internal/checkpointstorage"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/apps/internal/runtimeinfo"
	"github.com/iFTY-R/game-night/apps/internal/serviceheartbeat"
	"github.com/iFTY-R/game-night/apps/worker/internal/adminjobs"
	"github.com/iFTY-R/game-night/apps/worker/internal/checkpoint"
	workerconfig "github.com/iFTY-R/game-night/apps/worker/internal/config"
	workerruntime "github.com/iFTY-R/game-night/apps/worker/internal/runtime"
	"github.com/iFTY-R/game-night/platform/admin"
	adminoperations "github.com/iFTY-R/game-night/platform/admin/operations"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
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
	runtime          *workerruntime.Runtime
	pool             *pgxpool.Pool
	heartbeat        *serviceheartbeat.Reporter
	heartbeatTimeout time.Duration
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
	scheduler, err := checkpoint.NewScheduler(
		postgres.NewAuditOutboxUnitOfWork(pool, auditService), auditService, checkpointPolicy, source,
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
	governance := postgres.NewAdminUserGovernanceRepository(pool)
	batchService, err := adminuser.NewService(adminuser.Config{
		Repository: postgres.NewAdminUserRepository(pool),
		Jobs:       postgres.NewAdminJobRepository(pool),
		Governance: governance,
		// The worker only needs the asynchronous erasure primitive, so it composes a narrow adapter instead of reopening HTTP-only command paths.
		SingleGovernance: newWorkerErasureGovernance(governance, postgres.NewProfileRepository(pool), piiProtector, source),
		Audit:            postgres.NewAdminUserAuditRecorder(pool, auditService, checkpointPolicy, source),
		Clock:            source,
	})
	if err != nil {
		return nil, errInitializeRuntime
	}
	batchDispatcher, err := adminjobs.New(batchService, adminjobs.Config{
		Owner: string(config.Runtime.InstanceID), BatchSize: config.Runtime.BatchSize,
	})
	if err != nil {
		return nil, errInitializeRuntime
	}
	application.runtime, err = workerruntime.NewWithAdminJobs(
		dispatcher, scheduler, rotation, cleanup, batchDispatcher, config.Runtime.PollInterval, logger,
	)
	if err != nil {
		return nil, errInitializeRuntime
	}
	operationsRepository := postgres.NewAdminOperationsRepository(pool)
	maintenance, err := operationsRepository.GetMaintenanceState(ctx)
	if err != nil {
		return nil, errInitializeRuntime
	}
	heartbeatClient, err := serviceheartbeat.NewHTTPClient(
		&http.Client{Timeout: config.Heartbeat.Timeout},
		config.Heartbeat.TargetURL,
		config.Heartbeat.Token,
	)
	if err != nil {
		return nil, errInitializeRuntime
	}
	processInfo, err := runtimeinfo.New(adminoperations.ServiceWorker, string(config.Runtime.InstanceID), config.Heartbeat.BuildVersion, source.Now())
	if err != nil {
		return nil, errInitializeRuntime
	}
	var maintenanceVersion atomic.Uint64
	maintenanceVersion.Store(maintenance.Version)
	application.heartbeat, err = serviceheartbeat.NewReporter(
		heartbeatClient,
		processInfo,
		workerHeartbeatSnapshot(pool, readiness, application.runtime, operationsRepository, &maintenanceVersion),
		config.Heartbeat.Interval,
		config.Heartbeat.Timeout,
	)
	if err != nil {
		return nil, errInitializeRuntime
	}
	application.heartbeatTimeout = config.Runtime.ShutdownTimeout
	return application, nil
}

// Run blocks until the process context is canceled while keeping all passes serial.
func (application *Application) Run(ctx context.Context) error {
	if application == nil || application.runtime == nil || application.heartbeat == nil {
		return errInvalidOptions
	}
	heartbeatContext, cancelHeartbeat := context.WithCancel(context.Background())
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		application.heartbeat.Run(heartbeatContext)
	}()
	runtimeErr := application.runtime.Run(ctx)
	cancelHeartbeat()
	timer := time.NewTimer(application.heartbeatTimeout)
	defer timer.Stop()
	select {
	case <-heartbeatDone:
	case <-timer.C:
	}
	return runtimeErr
}

// Close releases the worker database pool after the runtime has stopped claiming new work.
func (application *Application) Close() {
	if application != nil && application.pool != nil {
		application.pool.Close()
		application.pool = nil
	}
}

func workerHeartbeatSnapshot(
	pool *pgxpool.Pool,
	checkpointReadiness *checkpointstorage.Readiness,
	runtime *workerruntime.Runtime,
	repository interface {
		GetMaintenanceState(context.Context) (adminoperations.MaintenanceState, error)
	},
	maintenanceVersion *atomic.Uint64,
) serviceheartbeat.SnapshotFunc {
	return func(ctx context.Context) serviceheartbeat.Snapshot {
		components := runtime.HealthComponents()
		components["postgresql"] = adminoperations.HealthUnavailable
		if pool != nil && pool.Ping(ctx) == nil {
			components["postgresql"] = adminoperations.HealthHealthy
		}
		components["checkpoint"] = adminoperations.HealthUnavailable
		if checkpointReadiness != nil && checkpointReadiness.Ready(ctx) {
			components["checkpoint"] = adminoperations.HealthHealthy
		}
		maintenance, err := repository.GetMaintenanceState(ctx)
		components["maintenance"] = adminoperations.HealthUnavailable
		if err == nil {
			maintenanceVersion.Store(maintenance.Version)
			components["maintenance"] = adminoperations.HealthHealthy
		}
		healthy := 0
		for _, component := range components {
			if component == adminoperations.HealthHealthy {
				healthy++
			}
		}
		status := adminoperations.HealthUnavailable
		if healthy == len(components) {
			status = adminoperations.HealthHealthy
		} else if healthy > 0 {
			status = adminoperations.HealthDegraded
		}
		return serviceheartbeat.Snapshot{Status: status, Components: components, MaintenanceVersion: maintenanceVersion.Load()}
	}
}
