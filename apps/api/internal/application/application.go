// Package application composes API dependencies and owns their shutdown order.
package application

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/api/internal/bootstrap"
	apiConfig "github.com/iFTY-R/game-night/apps/api/internal/config"
	"github.com/iFTY-R/game-night/apps/api/internal/server"
	adminaudittransport "github.com/iFTY-R/game-night/apps/api/internal/transport/adminaudit"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminauth"
	adminoperationstransport "github.com/iFTY-R/game-night/apps/api/internal/transport/adminoperations"
	adminoverviewtransport "github.com/iFTY-R/game-night/apps/api/internal/transport/adminoverview"
	adminroomtransport "github.com/iFTY-R/game-night/apps/api/internal/transport/adminroom"
	adminusertransport "github.com/iFTY-R/game-night/apps/api/internal/transport/adminuser"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	transporterrors "github.com/iFTY-R/game-night/apps/api/internal/transport/errors"
	gametransport "github.com/iFTY-R/game-night/apps/api/internal/transport/game"
	identitytransport "github.com/iFTY-R/game-night/apps/api/internal/transport/identity"
	maintenancetransport "github.com/iFTY-R/game-night/apps/api/internal/transport/maintenance"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/metrics"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	ratetransport "github.com/iFTY-R/game-night/apps/api/internal/transport/ratelimit"
	roomtransport "github.com/iFTY-R/game-night/apps/api/internal/transport/room"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/sensitive"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/apps/internal/runtimeinfo"
	"github.com/iFTY-R/game-night/apps/internal/serviceheartbeat"
	"github.com/iFTY-R/game-night/platform/admin"
	adminauditdomain "github.com/iFTY-R/game-night/platform/admin/auditlog"
	adminoperationsdomain "github.com/iFTY-R/game-night/platform/admin/operations"
	adminroomdomain "github.com/iFTY-R/game-night/platform/admin/room"
	adminuserdomain "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/identifier"
	identitydomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	"github.com/iFTY-R/game-night/platform/profile"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	goredis "github.com/redis/go-redis/v9"
)

var (
	errInvalidOptions       = errors.New("invalid API application options")
	errInitializeKeyrings   = errors.New("initialize API keyrings")
	errInitializePostgreSQL = errors.New("initialize API PostgreSQL")
	errInitializeRedis      = errors.New("initialize API Redis")
	errInitializeClock      = errors.New("initialize API database clock")
	errInitializeServices   = errors.New("initialize API services")
	errInitializeBootstrap  = errors.New("initialize administrator bootstrap")
	errInitializeTransport  = errors.New("initialize API transport")
)

// maximumDatabaseClockSkew fails startup when infrastructure clocks are too far apart for security expiry decisions.
const (
	maximumDatabaseClockSkew = 5 * time.Minute
	// gameConnectionTicketTTL bounds the browser's upgrade window without becoming a reusable session credential.
	gameConnectionTicketTTL = 30 * time.Second
	// gameSessionLeaseTTL is shared with realtime ownership coordination even while module registration remains disabled.
	gameSessionLeaseTTL = 15 * time.Second
)

// GameRegistry supplies both room admission manifests and exact runtime modules for pause recovery.
type GameRegistry interface {
	roomdomain.ManifestRegistry
	gameruntime.Registry
}

// Options supplies process-owned observers, the durable checkpoint probe, and the immutable game registry.
type Options struct {
	Logger         *slog.Logger
	Metrics        *prometheus.Registry
	CheckpointSink audit.SinkReadiness
	// Registry is the startup-validated manifest source shared with the realtime runtime.
	Registry GameRegistry
}

// Application owns the listener and every closeable dependency created for it.
type Application struct {
	runtime         *server.Runtime
	redis           *goredis.Client
	pool            *pgxpool.Pool
	argon2          *security.Argon2Service
	heartbeat       *serviceheartbeat.Reporter
	heartbeatCancel context.CancelFunc
	heartbeatDone   chan struct{}

	heartbeatOnce sync.Once
	shutdownOnce  sync.Once
	shutdownErr   error
}

// New builds the complete API graph before opening the listener. Partial failures release every acquired resource.
func New(ctx context.Context, config apiConfig.Config, options Options) (_ *Application, returnedErr error) {
	if ctx == nil || options.Logger == nil || options.Metrics == nil || options.CheckpointSink == nil || options.Registry == nil {
		return nil, errInvalidOptions
	}
	var source clock.Clock = clock.System{}
	keyrings, err := security.LoadKeyrings(config.Shared.Keyrings.SecurityPaths(), source.Now())
	if err != nil {
		return nil, errInitializeKeyrings
	}

	pool, err := postgres.OpenPool(ctx, postgres.PoolConfig{
		DatabaseURL: config.Shared.PostgreSQL.DSN, Schema: config.Shared.PostgreSQL.Schema,
		MinConnections: config.Shared.PostgreSQL.MinConnections, MaxConnections: config.Shared.PostgreSQL.MaxConnections,
		MaxConnectionAge: config.Shared.PostgreSQL.MaxConnectionLifetime, MaxConnectionIdle: config.Shared.PostgreSQL.MaxConnectionIdleTime,
		HealthCheckPeriod: config.Shared.PostgreSQL.HealthCheckPeriod,
	})
	if err != nil {
		return nil, errInitializePostgreSQL
	}
	application := &Application{pool: pool}
	defer func() {
		if returnedErr != nil {
			_ = application.closeDependencies()
		}
	}()
	source, err = newDatabaseClock(ctx, pool)
	if err != nil {
		return nil, errInitializeClock
	}
	if err := postgres.NewKeyringReferenceChecker(pool, keyrings).Check(ctx); err != nil {
		return nil, errInitializeKeyrings
	}

	redisOptions, err := goredis.ParseURL(config.Shared.Redis.URL)
	if err != nil {
		return nil, errInitializeRedis
	}
	redisClient := goredis.NewClient(redisOptions)
	application.redis = redisClient
	if err := redisClient.Ping(ctx).Err(); err != nil {
		return nil, errInitializeRedis
	}

	operations := sensitive.AllOperations()
	metricRegistry, err := metrics.New(options.Metrics, operations...)
	if err != nil {
		return nil, errInitializeTransport
	}
	redisLimiter, err := redisstore.NewLimiter(redisClient, keyrings.RateLimit, redisstore.Config{
		KeyPrefix: config.Shared.Redis.KeyPrefix, Timeout: config.Shared.Redis.Timeout, Rules: redisstore.StandardRules(),
	})
	if err != nil {
		return nil, errInitializeRedis
	}
	gameCoordinator, err := redisstore.NewGameCoordinator(redisClient, redisstore.CoordinationConfig{
		KeyPrefix: config.Shared.Redis.KeyPrefix, Timeout: config.Shared.Redis.Timeout,
		TicketTTL: gameConnectionTicketTTL, LeaseTTL: gameSessionLeaseTTL,
	})
	if err != nil {
		return nil, errInitializeRedis
	}
	adminRoomOwners, err := redisstore.NewAdminRoomOwnerReader(redisClient, redisstore.CoordinationConfig{
		KeyPrefix: config.Shared.Redis.KeyPrefix, Timeout: config.Shared.Redis.Timeout,
	})
	if err != nil {
		return nil, errInitializeRedis
	}
	userLimiter, err := ratetransport.New(redisLimiter, metricRegistry)
	if err != nil {
		return nil, errInitializeRedis
	}
	adminLimiter, err := ratetransport.New(redisLimiter, metricRegistry)
	if err != nil {
		return nil, errInitializeRedis
	}

	argon2Service, err := security.NewArgon2Service(security.DefaultArgon2Params(), config.Argon2.Workers, config.Argon2.QueueCapacity)
	if err != nil {
		return nil, errInitializeServices
	}
	roomRepository := postgres.NewRoomRepository(pool)
	gameSessionRepository := postgres.NewGameSessionRepository(pool)
	ruleRepository := postgres.NewRuleRepository(pool)
	replayAccessRepository := postgres.NewReplayAccessRepository(pool)
	gameRuntime, err := gametransport.NewRemoteRuntime(&http.Client{Timeout: 30 * time.Second}, gametransport.RemoteRuntimeConfig{
		BootstrapURL: config.Realtime.BootstrapURL, PeerURLs: config.Realtime.PeerURLs,
		InternalToken: config.Realtime.InternalToken,
	})
	if err != nil {
		return nil, errInitializeServices
	}
	gameCatalog, err := roomdomain.NewRegisteredGameCatalog(options.Registry)
	if err != nil {
		return nil, errInitializeServices
	}
	roomService, err := roomdomain.NewService(roomRepository, roomdomain.NewSecureCodeGenerator(), source)
	if err != nil {
		return nil, errInitializeServices
	}
	gameGovernance, err := gameruntime.NewService(
		options.Registry, gameSessionRepository, roomRepository, postgres.NewRoomGameSessionRepository(pool), source, gameruntime.SecureGenerator{},
	)
	if err != nil {
		return nil, errInitializeServices
	}
	application.argon2 = argon2Service
	auditService, checkpointPolicy, err := securityServices(keyrings, config.Shared, options.CheckpointSink)
	if err != nil {
		return nil, errInitializeServices
	}
	userService, adminService, err := domainServices(
		keyrings, source, pool, userLimiter, adminLimiter, argon2Service, auditService, checkpointPolicy,
	)
	if err != nil {
		return nil, errInitializeServices
	}
	adminUserService, err := adminUserService(keyrings, source, pool, auditService, checkpointPolicy)
	if err != nil {
		return nil, errInitializeServices
	}
	adminAuditReader := postgres.NewAdminAuditReadRepository(pool, auditService)
	adminAuditQueryService, err := newAdminAuditService(keyrings, source, adminAuditReader)
	if err != nil {
		return nil, errInitializeServices
	}
	adminRoomService, err := adminroomdomain.NewService(adminroomdomain.Config{
		Repository: postgres.NewAdminRoomQueryRepository(pool),
		Rooms:      roomRepository,
		Owners:     adminRoomOwners,
		OwnerFixes: gameCoordinator,
		Repairs:    postgres.NewAdminRepairRepository(pool),
		Executor:   postgres.NewAdminEmergencyRepairExecutor(pool),
		Clock:      source,
	})
	if err != nil {
		return nil, errInitializeServices
	}
	bootstrapCoordinator, err := bootstrap.NewCoordinator(ctx, adminService, string(config.Shared.BootstrapSecretFile))
	if err != nil {
		return nil, errInitializeBootstrap
	}

	readiness, err := server.NewReadiness(server.ReadinessChecks{
		PostgreSQL: server.CheckFunc(pool.Ping),
		Redis:      server.CheckFunc(func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }),
		Keyring:    keyringChecker{keyrings: keyrings},
		Bootstrap:  bootstrapCoordinator,
		Checkpoint: checkpointChecker{
			unitOfWork: postgres.NewAuditOutboxUnitOfWork(pool, auditService), policy: checkpointPolicy, clock: source,
		},
	})
	if err != nil {
		return nil, errInitializeTransport
	}
	operationsRepository := postgres.NewAdminOperationsRepository(pool)
	presenceProjection, err := redisstore.NewAdminPresenceProjection(redisClient, redisstore.AdminPresenceConfig{
		KeyPrefix: config.Shared.Redis.KeyPrefix,
		Timeout:   config.Shared.Redis.Timeout,
		TTL:       redisstore.AdminPresenceTTL,
		Clock:     source,
	})
	if err != nil {
		return nil, errInitializeServices
	}
	operationsService, err := adminoperationsdomain.NewService(adminoperationsdomain.ServiceConfig{
		Repository: operationsRepository,
		Presence:   presenceProjection,
		Clock:      source,
		Probes: []adminoperationsdomain.DependencyProbe{
			{Kind: adminoperationsdomain.DependencyPostgreSQL, Check: pool.Ping},
			{Kind: adminoperationsdomain.DependencyRedis, Check: func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }},
			{Kind: adminoperationsdomain.DependencyCheckpointProgress, Check: readinessComponentProbe(readiness, "checkpoint")},
			{Kind: adminoperationsdomain.DependencyRealtimePresence, Check: func(ctx context.Context) error {
				_, probeErr := presenceProjection.ReadPresenceSummary(ctx)
				return probeErr
			}},
			{Kind: adminoperationsdomain.DependencyRateLimiter, Check: func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }},
		},
	})
	if err != nil {
		return nil, errInitializeServices
	}
	overviewService, err := adminoperationsdomain.NewOverviewService(adminoperationsdomain.OverviewServiceConfig{
		Repository: operationsRepository,
		Presence:   presenceProjection,
		Operations: operationsService,
		Audit:      adminAuditReader,
		Clock:      source,
	})
	if err != nil {
		return nil, errInitializeServices
	}
	cacheImpact, err := newAdminCacheImpactReader(operationsRepository, operationsService, presenceProjection, source)
	if err != nil {
		return nil, errInitializeServices
	}
	operationsCommandService, err := adminoperationsdomain.NewCommandService(adminoperationsdomain.CommandServiceConfig{
		Repository: operationsRepository, UnitOfWork: postgres.NewAdminOperationsUnitOfWork(pool, auditService),
		Audit: auditService, CheckpointHealth: checkpointPolicy, CacheImpact: cacheImpact, Clock: source,
	})
	if err != nil {
		return nil, errInitializeServices
	}
	maintenance, err := operationsRepository.GetMaintenanceState(ctx)
	if err != nil {
		return nil, errInitializeServices
	}
	heartbeatHandler, err := adminoperationstransport.NewHeartbeatHandler(operationsRepository, config.Heartbeat.Token, source)
	if err != nil {
		return nil, errInitializeTransport
	}
	heartbeatSink, err := serviceheartbeat.NewRepositorySink(operationsRepository, source)
	if err != nil {
		return nil, errInitializeServices
	}
	processInfo, err := runtimeinfo.New(adminoperationsdomain.ServiceAPI, config.InstanceID, config.Heartbeat.BuildVersion, source.Now())
	if err != nil {
		return nil, errInitializeServices
	}
	var maintenanceVersion atomic.Uint64
	maintenanceVersion.Store(maintenance.Version)
	application.heartbeat, err = serviceheartbeat.NewReporter(
		heartbeatSink,
		processInfo,
		apiHeartbeatSnapshot(readiness, operationsRepository, &maintenanceVersion),
		config.Heartbeat.Interval,
		config.Heartbeat.Timeout,
	)
	if err != nil {
		return nil, errInitializeServices
	}
	handler, err := transportHandler(
		config.Shared, source, userService, roomService, gameCatalog, gameRuntime, gameGovernance, gameSessionRepository, roomRepository,
		ruleRepository, replayAccessRepository, gameCoordinator, adminService, adminRoomService, adminUserService, adminAuditQueryService,
		operationsService, operationsCommandService, overviewService, operationsRepository,
		metricRegistry, readiness, options.Logger, promhttp.HandlerFor(options.Metrics, promhttp.HandlerOpts{}), heartbeatHandler,
	)
	if err != nil {
		return nil, errInitializeTransport
	}
	application.runtime, err = server.NewRuntime(config.Listener, handler)
	if err != nil {
		return nil, errInitializeTransport
	}
	return application, nil
}

// databaseClock keeps process-side expiry and mutation timestamps aligned with PostgreSQL's authoritative timeline.
type databaseClock struct {
	// offset is the bounded database-minus-process duration sampled once before any domain service is created.
	offset time.Duration
}

// Now applies the startup calibration without retaining monotonic or process-local timezone metadata.
func (source databaseClock) Now() time.Time {
	return time.Now().Round(0).UTC().Add(source.offset)
}

// newDatabaseClock estimates offset at the network round-trip midpoint and rejects unsafe infrastructure skew.
func newDatabaseClock(ctx context.Context, pool *pgxpool.Pool) (clock.Clock, error) {
	if ctx == nil || pool == nil {
		return nil, errInitializeClock
	}
	startedAt := time.Now().Round(0).UTC()
	var databaseNow time.Time
	if err := pool.QueryRow(ctx, "SELECT pg_catalog.clock_timestamp()").Scan(&databaseNow); err != nil {
		return nil, errInitializeClock
	}
	finishedAt := time.Now().Round(0).UTC()
	return databaseClockFromSamples(startedAt, databaseNow, finishedAt)
}

// databaseClockFromSamples validates the observation window before deriving the midpoint offset.
func databaseClockFromSamples(startedAt, databaseNow, finishedAt time.Time) (clock.Clock, error) {
	startedAt, databaseNow, finishedAt = startedAt.Round(0).UTC(), databaseNow.Round(0).UTC(), finishedAt.Round(0).UTC()
	if startedAt.IsZero() || databaseNow.IsZero() || finishedAt.Before(startedAt) {
		return nil, errInitializeClock
	}
	midpoint := startedAt.Add(finishedAt.Sub(startedAt) / 2)
	offset := databaseNow.Sub(midpoint)
	if offset > maximumDatabaseClockSkew || offset < -maximumDatabaseClockSkew {
		return nil, errInitializeClock
	}
	return databaseClock{offset: offset}, nil
}

var _ clock.Clock = databaseClock{}

// ListenAndServe opens the configured API listener after the dependency graph is complete.
func (application *Application) ListenAndServe() error {
	if application == nil || application.runtime == nil || application.heartbeat == nil {
		return errInvalidOptions
	}
	application.heartbeatOnce.Do(func() {
		heartbeatContext, cancel := context.WithCancel(context.Background())
		application.heartbeatCancel = cancel
		application.heartbeatDone = make(chan struct{})
		go func() {
			defer close(application.heartbeatDone)
			application.heartbeat.Run(heartbeatContext)
		}()
	})
	return application.runtime.ListenAndServe()
}

// Shutdown drains HTTP first, then closes Redis, PostgreSQL, and finally the bounded Argon2 workers.
func (application *Application) Shutdown(ctx context.Context) error {
	if application == nil {
		return errInvalidOptions
	}
	application.shutdownOnce.Do(func() {
		var runtimeErr error
		if application.runtime != nil {
			runtimeErr = application.runtime.Shutdown(ctx)
			if runtimeErr != nil {
				runtimeErr = errors.Join(runtimeErr, application.runtime.Close())
			}
		}
		application.stopHeartbeat(ctx)
		application.shutdownErr = errors.Join(runtimeErr, application.closeDependencies())
	})
	return application.shutdownErr
}

func (application *Application) stopHeartbeat(ctx context.Context) {
	if application.heartbeatCancel == nil || application.heartbeatDone == nil {
		return
	}
	application.heartbeatCancel()
	select {
	case <-application.heartbeatDone:
	case <-ctx.Done():
	}
}

func (application *Application) closeDependencies() error {
	var closeErr error
	if application.redis != nil {
		if err := application.redis.Close(); err != nil {
			closeErr = errors.Join(closeErr, errors.New("close API Redis"))
		}
		application.redis = nil
	}
	if application.pool != nil {
		application.pool.Close()
		application.pool = nil
	}
	if application.argon2 != nil {
		application.argon2.Close()
		application.argon2 = nil
	}
	return closeErr
}

func securityServices(
	keyrings security.Keyrings,
	config sharedconfig.Config,
	sink audit.SinkReadiness,
) (*audit.Service, *audit.CheckpointHealthPolicy, error) {
	auditService, err := audit.NewService(keyrings.Audit)
	if err != nil {
		return nil, nil, err
	}
	policy, err := audit.NewCheckpointHealthPolicyWithThresholds(
		config.Environment == sharedconfig.EnvironmentProduction,
		sink,
		uint64(config.Checkpoint.MaxEvents),
		config.Checkpoint.MaxInterval,
	)
	if err != nil {
		return nil, nil, err
	}
	return auditService, policy, nil
}

func domainServices(
	keyrings security.Keyrings,
	source clock.Clock,
	pool *pgxpool.Pool,
	userLimiter, adminLimiter *ratetransport.Limiter,
	argon2Service *security.Argon2Service,
	auditService *audit.Service,
	checkpointPolicy *audit.CheckpointHealthPolicy,
) (*identitydomain.Service, *admin.Service, error) {
	userChallenges, err := identitydomain.NewChallengeService(keyrings.UserChallenge, source)
	if err != nil {
		return nil, nil, err
	}
	adminChallenges, err := admin.NewChallengeService(keyrings.AdminChallenge, source)
	if err != nil {
		return nil, nil, err
	}
	devices, err := identitydomain.NewDeviceService(keyrings.Device, source)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := secretresult.NewEnvelopeCipher(keyrings.ResultEnvelope)
	if err != nil {
		return nil, nil, err
	}
	userResults, err := secretresult.NewServiceWithIdentityAccess(envelope, source, keyrings.Device, keyrings.UserChallenge)
	if err != nil {
		return nil, nil, err
	}
	adminResults, err := secretresult.NewServiceWithAdminAccess(envelope, source, keyrings.AdminSession)
	if err != nil {
		return nil, nil, err
	}
	identityRecovery, err := identitydomain.NewRecoveryCodeService(argon2Service)
	if err != nil {
		return nil, nil, err
	}
	recoveryAttempts, err := identitydomain.NewRecoveryAttemptService(keyrings.UserChallenge, source)
	if err != nil {
		return nil, nil, err
	}
	usernames, err := identifier.NewUsernameValidator(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	identityService, err := identitydomain.NewServiceWithRecovery(
		userChallenges, devices, identityRecovery, recoveryAttempts, userResults,
		postgres.NewIdentityUnitOfWorkWithAudit(pool, auditService), userLimiter, usernames, source, auditService, checkpointPolicy,
	)
	if err != nil {
		return nil, nil, err
	}
	adminRecovery, err := admin.NewRecoveryCodeService(argon2Service)
	if err != nil {
		return nil, nil, err
	}
	totpService, err := admin.NewTOTPService(keyrings.TOTP)
	if err != nil {
		return nil, nil, err
	}
	sessions, err := admin.NewSessionService(keyrings.AdminSession, source)
	if err != nil {
		return nil, nil, err
	}
	adminUnitOfWork := postgres.NewAdminUnitOfWorkWithAudit(pool, auditService)
	adminService, err := admin.NewService(admin.ServiceDependencies{
		Challenge: adminChallenges, Passwords: argon2Service, PasswordPolicy: admin.DefaultPasswordPolicy(),
		TOTP: totpService, Sessions: sessions, RecoveryCodes: adminRecovery, Results: adminResults,
		Clock: source, UnitOfWork: adminUnitOfWork, Limiter: adminLimiter,
		Audit: auditService, CheckpointHealth: checkpointPolicy,
	})
	if err != nil {
		return nil, nil, err
	}
	return identityService, adminService, nil
}

func adminUserService(
	keyrings security.Keyrings,
	source clock.Clock,
	pool *pgxpool.Pool,
	auditService *audit.Service,
	checkpointPolicy *audit.CheckpointHealthPolicy,
) (*adminuserdomain.Service, error) {
	protector, err := profile.NewDefaultPIIProtector(keyrings.PII)
	if err != nil {
		return nil, err
	}
	cursor, err := adminuserdomain.NewCursorCodec(keyrings.AdminCursor)
	if err != nil {
		return nil, err
	}
	jobRepository := postgres.NewAdminJobRepository(pool)
	governanceRepository := postgres.NewAdminUserGovernanceRepository(pool)
	return adminuserdomain.NewService(adminuserdomain.Config{
		Repository:       postgres.NewAdminUserRepository(pool),
		Jobs:             jobRepository,
		Governance:       governanceRepository,
		UserCommands:     governanceRepository,
		SingleGovernance: governanceRepository,
		Profiles:         postgres.NewProfileRepository(pool),
		Protector:        protector,
		Audit: adminusertransport.NewAuditRecorder(
			auditService, postgres.NewAuditOutboxUnitOfWork(pool, auditService), checkpointPolicy, source,
		),
		Cursor: cursor,
		Clock:  source,
	})
}

// newAdminAuditService composes the read-only, signature-verifying audit service with management cursor authentication.
func newAdminAuditService(
	keyrings security.Keyrings,
	source clock.Clock,
	reader adminauditdomain.Reader,
) (*adminauditdomain.Service, error) {
	cursor, err := adminauditdomain.NewCursorCodec(keyrings.AdminCursor)
	if err != nil {
		return nil, err
	}
	return adminauditdomain.NewService(adminauditdomain.Config{
		Reader: reader,
		Cursor: cursor,
		Clock:  source,
	})
}

func transportHandler(
	config sharedconfig.Config,
	source clock.Clock,
	userService *identitydomain.Service,
	roomService *roomdomain.Service,
	gameCatalog roomdomain.GameCatalog,
	gameRuntime gametransport.Runtime,
	gameGovernance roomtransport.GameGovernance,
	gameSessions *postgres.GameSessionRepository,
	rooms *postgres.RoomRepository,
	rules *postgres.RuleRepository,
	replays *postgres.ReplayAccessRepository,
	gameCoordinator *redisstore.GameCoordinator,
	adminService *admin.Service,
	adminRoomService *adminroomdomain.Service,
	adminUserService *adminuserdomain.Service,
	adminAuditQueryService *adminauditdomain.Service,
	adminOperationsService *adminoperationsdomain.Service,
	adminOperationsCommandService *adminoperationsdomain.CommandService,
	adminOverviewService *adminoperationsdomain.OverviewService,
	adminOperationsRepository *postgres.AdminOperationsRepository,
	metricRegistry *metrics.Registry,
	readiness *server.Readiness,
	logger *slog.Logger,
	metricsHandler http.Handler,
	heartbeatHandler http.Handler,
) (http.Handler, error) {
	userCookies, err := cookies.NewManager(source)
	if err != nil {
		return nil, err
	}
	adminCookies, err := cookies.NewManager(source)
	if err != nil {
		return nil, err
	}
	userOrigins, err := origin.NewUserValidator()
	if err != nil {
		return nil, err
	}
	adminOrigins, err := origin.NewAdminValidator()
	if err != nil {
		return nil, err
	}
	userProxy, err := proxy.NewResolver(config.Network.TrustedProxies, metricRegistry)
	if err != nil {
		return nil, err
	}
	adminProxy, err := proxy.NewResolver(config.Network.TrustedProxies, metricRegistry)
	if err != nil {
		return nil, err
	}
	userCSRF := csrf.NewUserValidator()
	identityHandler, err := identitytransport.NewService(userService, userCookies, userOrigins, userCSRF, userProxy, source)
	if err != nil {
		return nil, err
	}
	gameAuthenticator, err := gametransport.NewIdentityAuthenticator(userService)
	if err != nil {
		return nil, err
	}
	roomAuthenticator, err := roomtransport.NewIdentityAuthenticator(userService)
	if err != nil {
		return nil, err
	}
	roomHandler, err := roomtransport.NewService(
		roomService, gameCatalog, gameRuntime, gameSessions, rooms, gameCoordinator,
		roomAuthenticator, userOrigins, userCSRF,
		roomtransport.WithRuleRepository(rules),
		roomtransport.WithRuleClock(source),
		roomtransport.WithGameGovernance(gameGovernance),
	)
	if err != nil {
		return nil, err
	}
	gameHandler, err := gametransport.NewService(
		gameRuntime, gameSessions, rooms, replays, gameAuthenticator, userOrigins, userCSRF,
		gameCoordinator, gameCoordinator, source, gameConnectionTicketTTL,
	)
	if err != nil {
		return nil, err
	}
	adminContext, err := adminauth.NewContextInterceptor(adminService, adminOrigins, csrf.NewAdminValidator(), adminProxy)
	if err != nil {
		return nil, err
	}
	adminEffects, err := adminauth.NewCookieEffects(adminCookies)
	if err != nil {
		return nil, err
	}
	adminAuthHandler, err := adminauth.NewService(adminService, adminEffects, readiness)
	if err != nil {
		return nil, err
	}
	adminRoomHandler, err := adminroomtransport.NewService(adminRoomService, source)
	if err != nil {
		return nil, err
	}
	adminUserHandler, err := adminusertransport.NewService(adminUserService, source)
	if err != nil {
		return nil, err
	}
	adminAuditHandler, err := adminaudittransport.NewService(adminAuditQueryService, source)
	if err != nil {
		return nil, err
	}
	adminOperationsHandler, err := adminoperationstransport.NewService(adminOperationsService, adminOperationsRepository, adminOperationsCommandService)
	if err != nil {
		return nil, err
	}
	adminOverviewHandler, err := adminoverviewtransport.NewService(adminOverviewService)
	if err != nil {
		return nil, err
	}
	userOperations := append(append([]string(nil), sensitive.IdentityOperations...), sensitive.RoomOperations...)
	userOperations = append(userOperations, sensitive.GameOperations...)
	userSensitive, err := sensitive.New(userOperations...)
	if err != nil {
		return nil, err
	}
	adminOperations := append([]string(nil), sensitive.AdminAuthOperations...)
	adminOperations = append(adminOperations, sensitive.AdminUserOperations...)
	adminOperations = append(adminOperations, sensitive.AdminRoomOperations...)
	adminOperations = append(adminOperations, sensitive.AdminAuditOperations...)
	adminOperations = append(adminOperations, sensitive.AdminOperationsOperations...)
	adminOperations = append(adminOperations, sensitive.AdminOverviewOperations...)
	adminSensitive, err := sensitive.New(adminOperations...)
	if err != nil {
		return nil, err
	}
	maintenanceInterceptor, err := maintenancetransport.NewInterceptor(adminOperationsRepository)
	if err != nil {
		return nil, err
	}
	userMetrics, err := metrics.NewUnaryInterceptor(logger, metricRegistry, userOperations...)
	if err != nil {
		return nil, err
	}
	adminMetrics, err := metrics.NewUnaryInterceptor(logger, metricRegistry, adminOperations...)
	if err != nil {
		return nil, err
	}
	userSurface, err := server.NewUserSurface(server.UserSurfaceConfig{
		Identity: identityHandler, Room: roomHandler, Game: gameHandler, Readiness: readiness,
		Interceptors: []connect.Interceptor{userSensitive.Interceptor(), userMetrics, transporterrors.Interceptor(), maintenanceInterceptor},
	})
	if err != nil {
		return nil, err
	}
	adminSurface, err := server.NewAdminSurface(server.AdminSurfaceConfig{
		Auth:         adminAuthHandler,
		User:         adminUserHandler,
		Room:         adminRoomHandler,
		Audit:        adminAuditHandler,
		Operations:   adminOperationsHandler,
		Overview:     adminOverviewHandler,
		Interceptors: []connect.Interceptor{adminSensitive.Interceptor(), adminMetrics, transporterrors.Interceptor(), adminContext},
	})
	if err != nil {
		return nil, err
	}
	return server.NewHandler(server.HandlerConfig{User: userSurface, Admin: adminSurface, Metrics: metricsHandler, Heartbeat: heartbeatHandler})
}

func readinessComponentProbe(readiness *server.Readiness, component string) func(context.Context) error {
	return func(ctx context.Context) error {
		if readiness == nil || readiness.RuntimeSnapshot(ctx, true).Components[component] != "ready" {
			return errDependencyUnavailable
		}
		return nil
	}
}

func apiHeartbeatSnapshot(
	readiness *server.Readiness,
	repository interface {
		GetMaintenanceState(context.Context) (adminoperationsdomain.MaintenanceState, error)
	},
	maintenanceVersion *atomic.Uint64,
) serviceheartbeat.SnapshotFunc {
	return func(ctx context.Context) serviceheartbeat.Snapshot {
		readinessSnapshot := readiness.RuntimeSnapshot(ctx, true)
		status := adminoperationsdomain.HealthHealthy
		if !readinessSnapshot.Ready {
			status = adminoperationsdomain.HealthDegraded
		}
		components := make(map[string]adminoperationsdomain.HealthStatus, len(readinessSnapshot.Components)+1)
		for code, componentStatus := range readinessSnapshot.Components {
			components[code] = adminoperationsdomain.HealthUnavailable
			if componentStatus == "ready" {
				components[code] = adminoperationsdomain.HealthHealthy
			}
		}
		maintenance, err := repository.GetMaintenanceState(ctx)
		if err != nil {
			components["maintenance"] = adminoperationsdomain.HealthUnavailable
			status = adminoperationsdomain.HealthDegraded
		} else {
			maintenanceVersion.Store(maintenance.Version)
			components["maintenance"] = adminoperationsdomain.HealthHealthy
		}
		return serviceheartbeat.Snapshot{Status: status, Components: components, MaintenanceVersion: maintenanceVersion.Load()}
	}
}
