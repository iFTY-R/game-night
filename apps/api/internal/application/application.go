// Package application composes API dependencies and owns their shutdown order.
package application

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/api/internal/bootstrap"
	apiConfig "github.com/iFTY-R/game-night/apps/api/internal/config"
	"github.com/iFTY-R/game-night/apps/api/internal/server"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminauth"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	transporterrors "github.com/iFTY-R/game-night/apps/api/internal/transport/errors"
	identitytransport "github.com/iFTY-R/game-night/apps/api/internal/transport/identity"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/metrics"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	ratetransport "github.com/iFTY-R/game-night/apps/api/internal/transport/ratelimit"
	roomtransport "github.com/iFTY-R/game-night/apps/api/internal/transport/room"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/sensitive"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
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
const maximumDatabaseClockSkew = 5 * time.Minute

// Options supplies process-owned observers and the current durable checkpoint sink probe.
type Options struct {
	Logger         *slog.Logger
	Metrics        *prometheus.Registry
	CheckpointSink audit.SinkReadiness
}

// Application owns the listener and every closeable dependency created for it.
type Application struct {
	runtime *server.Runtime
	redis   *goredis.Client
	pool    *pgxpool.Pool
	argon2  *security.Argon2Service

	shutdownOnce sync.Once
	shutdownErr  error
}

// New builds the complete API graph before opening the listener. Partial failures release every acquired resource.
func New(ctx context.Context, config apiConfig.Config, options Options) (_ *Application, returnedErr error) {
	if ctx == nil || options.Logger == nil || options.Metrics == nil || options.CheckpointSink == nil {
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
	roomService, err := roomdomain.NewService(
		postgres.NewRoomRepository(pool), roomdomain.NewSecureCodeGenerator(), roomdomain.NewDisabledGameCatalog(), source,
	)
	if err != nil {
		return nil, errInitializeServices
	}
	application.argon2 = argon2Service
	auditService, checkpointPolicy, err := securityServices(keyrings, config.Shared, options.CheckpointSink)
	if err != nil {
		return nil, errInitializeServices
	}
	userService, adminService, adminIdentityService, err := domainServices(
		keyrings, source, pool, userLimiter, adminLimiter, argon2Service, auditService, checkpointPolicy,
	)
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
	handler, err := transportHandler(
		config.Shared, source, userService, roomService, adminService, adminIdentityService,
		metricRegistry, readiness, options.Logger, promhttp.HandlerFor(options.Metrics, promhttp.HandlerOpts{}),
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
	if application == nil || application.runtime == nil {
		return errInvalidOptions
	}
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
		application.shutdownErr = errors.Join(runtimeErr, application.closeDependencies())
	})
	return application.shutdownErr
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
) (*identitydomain.Service, *admin.Service, *admin.IdentityService, error) {
	userChallenges, err := identitydomain.NewChallengeService(keyrings.UserChallenge, source)
	if err != nil {
		return nil, nil, nil, err
	}
	adminChallenges, err := admin.NewChallengeService(keyrings.AdminChallenge, source)
	if err != nil {
		return nil, nil, nil, err
	}
	devices, err := identitydomain.NewDeviceService(keyrings.Device, source)
	if err != nil {
		return nil, nil, nil, err
	}
	envelope, err := secretresult.NewEnvelopeCipher(keyrings.ResultEnvelope)
	if err != nil {
		return nil, nil, nil, err
	}
	userResults, err := secretresult.NewServiceWithIdentityAccess(envelope, source, keyrings.Device, keyrings.UserChallenge)
	if err != nil {
		return nil, nil, nil, err
	}
	adminResults, err := secretresult.NewServiceWithAdminAccess(envelope, source, keyrings.AdminSession)
	if err != nil {
		return nil, nil, nil, err
	}
	identityRecovery, err := identitydomain.NewRecoveryCodeService(argon2Service)
	if err != nil {
		return nil, nil, nil, err
	}
	recoveryAttempts, err := identitydomain.NewRecoveryAttemptService(keyrings.UserChallenge, source)
	if err != nil {
		return nil, nil, nil, err
	}
	usernames, err := identifier.NewUsernameValidator(nil, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	identityService, err := identitydomain.NewServiceWithRecovery(
		userChallenges, devices, identityRecovery, recoveryAttempts, userResults,
		postgres.NewIdentityUnitOfWorkWithAudit(pool, auditService), userLimiter, usernames, source, auditService, checkpointPolicy,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	adminRecovery, err := admin.NewRecoveryCodeService(argon2Service)
	if err != nil {
		return nil, nil, nil, err
	}
	totpService, err := admin.NewTOTPService(keyrings.TOTP)
	if err != nil {
		return nil, nil, nil, err
	}
	sessions, err := admin.NewSessionService(keyrings.AdminSession, source)
	if err != nil {
		return nil, nil, nil, err
	}
	adminUnitOfWork := postgres.NewAdminUnitOfWorkWithAudit(pool, auditService)
	adminService, err := admin.NewService(admin.ServiceDependencies{
		Challenge: adminChallenges, Passwords: argon2Service, PasswordPolicy: admin.DefaultPasswordPolicy(),
		TOTP: totpService, Sessions: sessions, RecoveryCodes: adminRecovery, Results: adminResults,
		Clock: source, UnitOfWork: adminUnitOfWork, Limiter: adminLimiter,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	pii, err := profile.NewDefaultPIIProtector(keyrings.PII)
	if err != nil {
		return nil, nil, nil, err
	}
	adminIdentityService, err := admin.NewIdentityService(admin.IdentityServiceDependencies{
		Clock: source, UnitOfWork: adminUnitOfWork, Sessions: sessions, Authorizer: admin.NewAdminAuthorizer(),
		Limiter: adminLimiter, PII: pii, RecoveryCodes: identityRecovery, Results: adminResults,
		Audit: auditService, CheckpointHealth: checkpointPolicy,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return identityService, adminService, adminIdentityService, nil
}

func transportHandler(
	config sharedconfig.Config,
	source clock.Clock,
	userService *identitydomain.Service,
	roomService *roomdomain.Service,
	adminService *admin.Service,
	adminIdentityService *admin.IdentityService,
	metricRegistry *metrics.Registry,
	readiness *server.Readiness,
	logger *slog.Logger,
	metricsHandler http.Handler,
) (http.Handler, error) {
	userCookies, err := cookies.NewManager(source)
	if err != nil {
		return nil, err
	}
	adminCookies, err := cookies.NewManager(source)
	if err != nil {
		return nil, err
	}
	userOrigins, err := origin.NewUserValidator(config.Network.UserOrigins)
	if err != nil {
		return nil, err
	}
	adminOrigins, err := origin.NewAdminValidator(config.Network.AdminOrigins)
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
	roomAuthenticator, err := roomtransport.NewIdentityAuthenticator(userService)
	if err != nil {
		return nil, err
	}
	roomHandler, err := roomtransport.NewService(roomService, roomAuthenticator, userOrigins, userCSRF)
	if err != nil {
		return nil, err
	}
	adminContext, err := adminauth.NewContextInterceptor(adminOrigins, csrf.NewAdminValidator(), adminProxy)
	if err != nil {
		return nil, err
	}
	adminEffects, err := adminauth.NewCookieEffects(adminCookies)
	if err != nil {
		return nil, err
	}
	adminAuthHandler, err := admin.NewConnectAdminServiceWithCookieEffects(adminService, adminEffects)
	if err != nil {
		return nil, err
	}
	adminIdentityHandler, err := admin.NewConnectAdminIdentityService(adminIdentityService, adminService)
	if err != nil {
		return nil, err
	}
	userOperations := append(append([]string(nil), sensitive.IdentityOperations...), sensitive.RoomOperations...)
	userSensitive, err := sensitive.New(userOperations...)
	if err != nil {
		return nil, err
	}
	adminOperations := append(append([]string(nil), sensitive.AdminAuthOperations...), sensitive.AdminIdentityOperations...)
	adminSensitive, err := sensitive.New(adminOperations...)
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
		Identity: identityHandler, Room: roomHandler, Readiness: readiness,
		Interceptors: []connect.Interceptor{userSensitive.Interceptor(), userMetrics, transporterrors.Interceptor()},
	})
	if err != nil {
		return nil, err
	}
	adminSurface, err := server.NewAdminSurface(server.AdminSurfaceConfig{
		Auth: adminAuthHandler, Identity: adminIdentityHandler, Readiness: readiness,
		Interceptors: []connect.Interceptor{adminSensitive.Interceptor(), adminMetrics, transporterrors.Interceptor(), adminContext},
	})
	if err != nil {
		return nil, err
	}
	return server.NewHandler(server.HandlerConfig{User: userSurface, Admin: adminSurface, Metrics: metricsHandler})
}
