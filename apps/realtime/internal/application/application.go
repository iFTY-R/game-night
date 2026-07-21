// Package application composes the realtime process and owns its two listeners and dependencies.
package application

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/realtime/internal/config"
	"github.com/iFTY-R/game-night/apps/realtime/internal/fanout"
	"github.com/iFTY-R/game-night/apps/realtime/internal/owner"
	"github.com/iFTY-R/game-night/apps/realtime/internal/subscription"
	timerscheduler "github.com/iFTY-R/game-night/apps/realtime/internal/timer"
	"github.com/iFTY-R/game-night/apps/realtime/internal/transport/gamewebsocket"
	"github.com/iFTY-R/game-night/apps/realtime/internal/transport/internalgame"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1/realtimev1connect"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
)

const (
	gameConnectionTicketTTL  = 30 * time.Second
	maximumDatabaseClockSkew = 5 * time.Minute
)

var (
	errInvalidOptions       = errors.New("invalid realtime application options")
	errInitializePostgreSQL = errors.New("initialize realtime PostgreSQL")
	errInitializeRedis      = errors.New("initialize realtime Redis")
	errInitializeClock      = errors.New("initialize realtime database clock")
	errInitializeRuntime    = errors.New("initialize realtime runtime")
	errInitializeTimer      = errors.New("initialize realtime timer scheduler")
	errInitializeSubscriber = errors.New("initialize realtime subscription runtime")
	errInitializeFanout     = errors.New("initialize realtime durable fanout")
	errInitializeTransport  = errors.New("initialize realtime transport")
	errFanoutClosed         = errors.New("realtime fanout subscription closed")
)

// Options supplies process-owned observers and the startup-validated immutable game registry.
type Options struct {
	Logger   *slog.Logger
	Registry gameruntime.Registry
}

// Application owns both HTTP servers, the lease manager, and all external clients.
type Application struct {
	config         config.Config
	logger         *slog.Logger
	publicServer   *http.Server
	internalServer *http.Server
	owner          *owner.Manager
	timer          *timerscheduler.Scheduler
	hub            *subscription.Hub
	fanout         *redisstore.SessionFanoutSubscription
	durableFanout  *fanout.Dispatcher
	redis          *goredis.Client
	pool           *pgxpool.Pool

	closeOnce sync.Once
	closeErr  error
}

// New validates and connects the complete realtime graph before either listener is opened.
func New(ctx context.Context, cfg config.Config, options Options) (_ *Application, returnedErr error) {
	if ctx == nil || options.Logger == nil || options.Registry == nil {
		return nil, errInvalidOptions
	}
	pool, err := postgres.OpenPool(ctx, postgres.PoolConfig{
		DatabaseURL: cfg.Shared.PostgreSQL.DSN, Schema: cfg.Shared.PostgreSQL.Schema,
		MinConnections: cfg.Shared.PostgreSQL.MinConnections, MaxConnections: cfg.Shared.PostgreSQL.MaxConnections,
		MaxConnectionAge: cfg.Shared.PostgreSQL.MaxConnectionLifetime, MaxConnectionIdle: cfg.Shared.PostgreSQL.MaxConnectionIdleTime,
		HealthCheckPeriod: cfg.Shared.PostgreSQL.HealthCheckPeriod,
	})
	if err != nil {
		return nil, errInitializePostgreSQL
	}
	application := &Application{config: cfg, logger: options.Logger, pool: pool}
	defer func() {
		if returnedErr != nil {
			_ = application.Close(context.Background())
		}
	}()

	source, err := newDatabaseClock(ctx, pool)
	if err != nil {
		return nil, errInitializeClock
	}
	redisOptions, err := goredis.ParseURL(cfg.Shared.Redis.URL)
	if err != nil {
		return nil, errInitializeRedis
	}
	application.redis = goredis.NewClient(redisOptions)
	if err := application.redis.Ping(ctx).Err(); err != nil {
		return nil, errInitializeRedis
	}
	coordinator, err := redisstore.NewGameCoordinator(application.redis, redisstore.CoordinationConfig{
		KeyPrefix: cfg.Shared.Redis.KeyPrefix, Timeout: cfg.Shared.Redis.Timeout,
		TicketTTL: gameConnectionTicketTTL, LeaseTTL: cfg.Ownership.LeaseTTL,
	})
	if err != nil {
		return nil, errInitializeRedis
	}
	sessions := postgres.NewGameSessionRepository(pool)
	rooms := postgres.NewRoomRepository(pool)
	runtime, err := gameruntime.NewService(
		options.Registry, sessions, rooms, postgres.NewRoomGameSessionRepository(pool), source, gameruntime.SecureGenerator{},
	)
	if err != nil {
		return nil, errInitializeRuntime
	}
	application.owner, err = owner.NewManager(coordinator, sessions, runtime, coordinator, source, owner.Config{
		InstanceID: cfg.Ownership.InstanceID, Address: cfg.Ownership.AdvertisedURL,
		LeaseTTL: cfg.Ownership.LeaseTTL, RenewInterval: cfg.Ownership.RenewInterval,
	})
	if err != nil {
		return nil, errInitializeRuntime
	}
	application.timer, err = timerscheduler.NewScheduler(sessions, application.owner, source, options.Logger, timerscheduler.Config{
		ScanInterval: cfg.Timer.ScanInterval, OperationTimeout: cfg.Timer.OperationTimeout, BatchSize: cfg.Timer.BatchSize,
	})
	if err != nil {
		return nil, errInitializeTimer
	}
	authorizer, err := subscription.NewAuthorizer(coordinator, rooms, sessions, source, subscription.Config{
		MaximumGrantLifetime: gameConnectionTicketTTL,
	})
	if err != nil {
		return nil, errInitializeSubscriber
	}
	application.hub, err = subscription.NewHub(authorizer, runtime, subscription.HubConfig{
		ReconcileInterval: cfg.WebSocket.AuthorizationInterval, ProjectionTimeout: cfg.WebSocket.WriteTimeout,
	})
	if err != nil {
		return nil, errInitializeSubscriber
	}
	websocketAcceptor, err := gamewebsocket.NewAcceptor(cfg.Shared.Network.UserOrigins)
	if err != nil {
		return nil, errInitializeTransport
	}
	websocketHandler, err := gamewebsocket.NewHandler(
		authorizer,
		func(authorization subscription.Authorization, sink subscription.Sink) (gamewebsocket.Handle, error) {
			return application.hub.Register(authorization, sink)
		},
		websocketAcceptor,
		source,
		gamewebsocket.Config{
			AllowedOrigins: cfg.Shared.Network.UserOrigins,
			HelloTimeout:   cfg.WebSocket.HelloTimeout, WriteTimeout: cfg.WebSocket.WriteTimeout,
			PingInterval: cfg.WebSocket.PingInterval, MaxMessageBytes: cfg.WebSocket.MaxMessageBytes,
			QueueCapacity: cfg.WebSocket.SendQueueCapacity,
		},
	)
	if err != nil {
		return nil, errInitializeTransport
	}
	application.durableFanout, err = fanout.NewDispatcher(fanout.Config{
		Owner: outbox.LeaseOwner(cfg.Ownership.InstanceID), LeaseDuration: cfg.Fanout.LeaseDuration,
		PollInterval: cfg.Fanout.PollInterval, BatchSize: cfg.Fanout.BatchSize,
	}, postgres.NewOutboxUnitOfWork(pool), coordinator, source, options.Logger)
	if err != nil {
		return nil, errInitializeFanout
	}
	application.fanout, err = coordinator.SubscribeSessionFanout(ctx)
	if err != nil {
		return nil, errInitializeSubscriber
	}
	privateService, err := internalgame.NewService(runtime, application.owner, sessions, coordinator)
	if err != nil {
		return nil, errInitializeTransport
	}
	tokenInterceptor, err := internalgame.NewTokenInterceptor(cfg.InternalToken)
	if err != nil {
		return nil, errInitializeTransport
	}
	privatePath, privateHandler := realtimev1connect.NewOwnerServiceHandler(
		privateService,
		connect.WithInterceptors(tokenInterceptor, internalgame.ErrorInterceptor()),
	)
	internalMux := http.NewServeMux()
	internalMux.Handle(privatePath, privateHandler)
	internalMux.HandleFunc("GET /health/live", liveHandler)
	internalMux.Handle("GET /health/ready", application.readyHandler())
	publicMux := newPublicMux(websocketHandler, application.readyHandler())
	application.publicServer = newHTTPServer(cfg.Listener.PublicAddress, publicMux, true)
	application.internalServer = newHTTPServer(cfg.Listener.InternalAddress, internalMux, false)
	return application, nil
}

// ListenAndServe opens both isolated listeners and runs lease renewal until shutdown or a server failure.
func (application *Application) ListenAndServe(ctx context.Context) error {
	if application == nil || ctx == nil || application.publicServer == nil || application.internalServer == nil ||
		application.owner == nil || application.timer == nil || application.hub == nil || application.fanout == nil ||
		application.durableFanout == nil {
		return errInvalidOptions
	}
	publicListener, err := net.Listen("tcp", application.publicServer.Addr)
	if err != nil {
		return err
	}
	internalListener, err := net.Listen("tcp", application.internalServer.Addr)
	if err != nil {
		_ = publicListener.Close()
		return err
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	errorsChannel := make(chan error, 7)
	go func() { errorsChannel <- normalizeServeError(application.publicServer.Serve(publicListener)) }()
	go func() { errorsChannel <- normalizeServeError(application.internalServer.Serve(internalListener)) }()
	go func() { errorsChannel <- application.owner.Run(runCtx) }()
	go func() { errorsChannel <- application.timer.Run(runCtx) }()
	go func() { errorsChannel <- application.hub.Run(runCtx) }()
	go func() { errorsChannel <- application.runFanout(runCtx) }()
	go func() { errorsChannel <- application.durableFanout.Run(runCtx) }()

	select {
	case <-ctx.Done():
		cancelRun()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), application.config.Listener.ShutdownTimeout)
		defer cancel()
		return application.Close(shutdownCtx)
	case err := <-errorsChannel:
		cancelRun()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), application.config.Listener.ShutdownTimeout)
		defer cancel()
		return errors.Join(err, application.Close(shutdownCtx))
	}
}

// Close stops new requests, cancels held commands, releases leases, and then closes external clients.
func (application *Application) Close(ctx context.Context) error {
	if application == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	application.closeOnce.Do(func() {
		if application.publicServer != nil {
			application.closeErr = errors.Join(application.closeErr, application.publicServer.Shutdown(ctx))
		}
		if application.internalServer != nil {
			application.closeErr = errors.Join(application.closeErr, application.internalServer.Shutdown(ctx))
		}
		if application.hub != nil {
			application.closeErr = errors.Join(application.closeErr, application.hub.Close(ctx, subscription.ErrHubClosed))
		}
		if application.fanout != nil {
			application.closeErr = errors.Join(application.closeErr, application.fanout.Close())
		}
		if application.owner != nil {
			application.closeErr = errors.Join(application.closeErr, application.owner.Close(ctx))
		}
		if application.redis != nil {
			application.closeErr = errors.Join(application.closeErr, application.redis.Close())
		}
		if application.pool != nil {
			application.pool.Close()
		}
	})
	return application.closeErr
}

func (application *Application) runFanout(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case message, ok := <-application.fanout.Messages():
			if !ok {
				return errFanoutClosed
			}
			if message == nil {
				application.logger.WarnContext(ctx, "discard nil game fanout message")
				continue
			}
			event, err := redisstore.ParseSessionFanoutEvent([]byte(message.Payload))
			if err != nil {
				application.logger.WarnContext(ctx, "discard invalid game fanout message", "error", err)
				continue
			}
			if err := application.hub.Notify(event); err != nil && !errors.Is(err, subscription.ErrHubClosed) {
				return err
			}
		}
	}
}

func (application *Application) readyHandler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if application.pool.Ping(ctx) != nil || application.redis.Ping(ctx).Err() != nil {
			http.Error(response, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
}

func liveHandler(response http.ResponseWriter, _ *http.Request) {
	response.WriteHeader(http.StatusNoContent)
}

// newPublicMux keeps the browser upgrade route exact and separate from the private Connect service listener.
func newPublicMux(websocketHandler, readyHandler http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /realtime/game", websocketHandler)
	mux.HandleFunc("GET /health/live", liveHandler)
	mux.Handle("GET /health/ready", readyHandler)
	return mux
}

func newHTTPServer(address string, handler http.Handler, websocket bool) *http.Server {
	server := &http.Server{
		Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20,
	}
	if !websocket {
		server.WriteTimeout = 30 * time.Second
	}
	return server
}

func normalizeServeError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type databaseClock struct{ offset time.Duration }

func (source databaseClock) Now() time.Time { return time.Now().Round(0).UTC().Add(source.offset) }

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
	midpoint := startedAt.Add(finishedAt.Sub(startedAt) / 2)
	offset := databaseNow.Round(0).UTC().Sub(midpoint)
	if offset > maximumDatabaseClockSkew || offset < -maximumDatabaseClockSkew {
		return nil, errInitializeClock
	}
	return databaseClock{offset: offset}, nil
}

var _ clock.Clock = databaseClock{}
