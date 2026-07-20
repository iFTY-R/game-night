package subscription

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

var (
	ErrInvalidHubConfig    = errors.New("invalid realtime subscription hub configuration")
	ErrInvalidSubscription = errors.New("invalid realtime subscription")
	ErrHubClosed           = errors.New("realtime subscription hub is closed")
	ErrUnsafeUpdate        = errors.New("unsafe realtime subscription update")
)

// ProjectionRuntime returns only module-projected viewer data paired with the exact authoritative cursor used.
type ProjectionRuntime interface {
	ProjectCurrent(context.Context, uuid.UUID, game.Viewer) (gameruntime.Session, game.Projection, error)
	ProjectEventsCurrent(context.Context, uuid.UUID, uint64, game.Viewer) (gameruntime.Session, game.EventProjection, game.Projection, bool, error)
}

// AuthorizationRefresher periodically rechecks current room role, frozen seat, and host authority.
type AuthorizationRefresher interface {
	Refresh(context.Context, Authorization) (RefreshResult, error)
}

// Sink is a connection-owned bounded writer; Send must return an error when the client cannot keep up.
type Sink interface {
	Send(context.Context, Update) error
	Close(error)
}

// HubConfig bounds reconciliation frequency and every PostgreSQL/projection/write cycle.
type HubConfig struct {
	ReconcileInterval time.Duration
	ProjectionTimeout time.Duration
}

// Update contains only viewer-safe module output and public session metadata required by the wire adapter.
type Update struct {
	SessionID    uuid.UUID
	StateVersion uint64
	VersionKey   game.VersionKey
	Host         bool
	Projection   game.Projection
	Delta        game.EventProjection
}

// Snapshot reports whether this update replaces the complete viewer state.
func (update Update) Snapshot() bool {
	return update.Projection.Valid() && !update.Delta.Valid()
}

// Valid requires exactly one safe projection shape and never permits a zero authoritative cursor.
func (update Update) Valid() bool {
	return update.SessionID != uuid.Nil && update.StateVersion > 0 && update.VersionKey.Valid() &&
		(update.Projection.Valid() != update.Delta.Valid())
}

// Hub coalesces Redis wake-ups per connection and owns periodic PostgreSQL reconciliation.
type Hub struct {
	authorizer AuthorizationRefresher
	runtime    ProjectionRuntime
	config     HubConfig

	lifecycleCtx context.Context
	cancelHub    context.CancelCauseFunc

	mu        sync.Mutex
	sessions  map[uuid.UUID]map[uuid.UUID]*subscriber
	byID      map[uuid.UUID]*subscriber
	running   bool
	closed    bool
	waitGroup sync.WaitGroup
}

type subscriber struct {
	id            uuid.UUID
	hub           *Hub
	authorization Authorization
	sink          Sink
	ctx           context.Context
	cancel        context.CancelCauseFunc
	wake          chan struct{}
}

// Handle permits the transport to remove one connection without exposing hub internals.
type Handle struct {
	once   sync.Once
	cancel context.CancelCauseFunc
}

// NewHub creates an idle hub; Run adds periodic reconciliation while Register and Notify remain immediately usable.
func NewHub(authorizer AuthorizationRefresher, runtime ProjectionRuntime, config HubConfig) (*Hub, error) {
	if authorizer == nil || runtime == nil || config.ReconcileInterval < 10*time.Millisecond ||
		config.ReconcileInterval > 5*time.Minute || config.ProjectionTimeout < 100*time.Millisecond ||
		config.ProjectionTimeout > time.Minute {
		return nil, ErrInvalidHubConfig
	}
	lifecycleCtx, cancelHub := context.WithCancelCause(context.Background())
	return &Hub{
		authorizer: authorizer, runtime: runtime, config: config,
		lifecycleCtx: lifecycleCtx, cancelHub: cancelHub,
		sessions: make(map[uuid.UUID]map[uuid.UUID]*subscriber), byID: make(map[uuid.UUID]*subscriber),
	}, nil
}

// Register starts one serial projection worker and immediately reconciles events committed after the grant cursor.
func (hub *Hub) Register(authorization Authorization, sink Sink) (*Handle, error) {
	if hub == nil || sink == nil || !validAuthorization(authorization) {
		return nil, ErrInvalidSubscription
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil, ErrHubClosed
	}
	ctx, cancel := context.WithCancelCause(hub.lifecycleCtx)
	subscription := &subscriber{
		id: uuid.New(), hub: hub, authorization: authorization, sink: sink,
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
	}
	hub.byID[subscription.id] = subscription
	if hub.sessions[authorization.SessionID] == nil {
		hub.sessions[authorization.SessionID] = make(map[uuid.UUID]*subscriber)
	}
	hub.sessions[authorization.SessionID][subscription.id] = subscription
	hub.waitGroup.Add(1)
	hub.mu.Unlock()
	go subscription.run()
	subscription.notify()
	return &Handle{cancel: cancel}, nil
}

// Close removes the connection and cancels any in-flight authorization, projection, or sink write.
func (handle *Handle) Close(cause error) {
	if handle == nil || handle.cancel == nil {
		return
	}
	if cause == nil {
		cause = context.Canceled
	}
	handle.once.Do(func() { handle.cancel(cause) })
}

// Notify coalesces a secret-free Redis wake-up for every local viewer of the committed session.
func (hub *Hub) Notify(event redisstore.SessionFanoutEvent) error {
	if hub == nil || !event.Valid() {
		return ErrInvalidSubscription
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return ErrHubClosed
	}
	subscriptions := make([]*subscriber, 0, len(hub.sessions[event.SessionID]))
	for _, subscription := range hub.sessions[event.SessionID] {
		subscriptions = append(subscriptions, subscription)
	}
	hub.mu.Unlock()
	for _, subscription := range subscriptions {
		subscription.notify()
	}
	return nil
}

// Run periodically wakes every connection so PostgreSQL repairs any Redis Pub/Sub notification loss.
func (hub *Hub) Run(ctx context.Context) error {
	if hub == nil || ctx == nil {
		return ErrInvalidHubConfig
	}
	hub.mu.Lock()
	if hub.closed || hub.running {
		hub.mu.Unlock()
		return ErrHubClosed
	}
	hub.running = true
	hub.mu.Unlock()
	defer func() {
		hub.mu.Lock()
		hub.running = false
		hub.mu.Unlock()
	}()
	ticker := time.NewTicker(hub.config.ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			hub.beginClose(ctx.Err())
			return nil
		case <-hub.lifecycleCtx.Done():
			return nil
		case <-ticker.C:
			hub.notifyAll()
		}
	}
}

// Close starts draining and waits for all per-connection workers to close their sinks.
func (hub *Hub) Close(ctx context.Context, cause error) error {
	if hub == nil {
		return nil
	}
	if ctx == nil {
		return ErrInvalidHubConfig
	}
	if cause == nil {
		cause = ErrHubClosed
	}
	hub.beginClose(cause)
	done := make(chan struct{})
	go func() {
		hub.waitGroup.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (hub *Hub) beginClose(cause error) {
	hub.mu.Lock()
	if !hub.closed {
		hub.closed = true
		hub.cancelHub(cause)
	}
	hub.mu.Unlock()
}

func (hub *Hub) notifyAll() {
	hub.mu.Lock()
	subscriptions := make([]*subscriber, 0, len(hub.byID))
	for _, subscription := range hub.byID {
		subscriptions = append(subscriptions, subscription)
	}
	hub.mu.Unlock()
	for _, subscription := range subscriptions {
		subscription.notify()
	}
}

func (hub *Hub) remove(subscription *subscriber) {
	hub.mu.Lock()
	delete(hub.byID, subscription.id)
	bySession := hub.sessions[subscription.authorization.SessionID]
	delete(bySession, subscription.id)
	if len(bySession) == 0 {
		delete(hub.sessions, subscription.authorization.SessionID)
	}
	hub.mu.Unlock()
}

func (subscription *subscriber) notify() {
	select {
	case subscription.wake <- struct{}{}:
	default:
	}
}

func (subscription *subscriber) run() {
	defer subscription.hub.waitGroup.Done()
	defer subscription.hub.remove(subscription)
	defer func() { subscription.sink.Close(context.Cause(subscription.ctx)) }()
	for {
		select {
		case <-subscription.ctx.Done():
			return
		case <-subscription.wake:
			if err := subscription.reconcile(); err != nil {
				subscription.cancel(err)
				return
			}
		}
	}
}

func (subscription *subscriber) reconcile() error {
	ctx, cancel := context.WithTimeout(subscription.ctx, subscription.hub.config.ProjectionTimeout)
	defer cancel()
	refreshed, err := subscription.hub.authorizer.Refresh(ctx, subscription.authorization)
	if err != nil {
		return err
	}
	current := refreshed.Authorization
	var update Update
	if refreshed.SnapshotRequired {
		session, projection, projectErr := subscription.hub.runtime.ProjectCurrent(ctx, current.SessionID, current.Viewer)
		if projectErr != nil {
			return projectErr
		}
		update = snapshotUpdate(session, current.Host, projection)
	} else {
		session, delta, projection, fallback, projectErr := subscription.hub.runtime.ProjectEventsCurrent(
			ctx, current.SessionID, current.Cursor, current.Viewer,
		)
		if projectErr != nil {
			return projectErr
		}
		if session.Snapshot().State.StateVersion == current.Cursor {
			subscription.authorization = current
			return nil
		}
		if fallback {
			update = snapshotUpdate(session, current.Host, projection)
		} else {
			update = deltaUpdate(session, current.Host, delta)
		}
	}
	if !update.Valid() {
		return ErrUnsafeUpdate
	}
	if err := subscription.sink.Send(ctx, update); err != nil {
		return err
	}
	current.Cursor = update.StateVersion
	current.CurrentVersion = update.StateVersion
	subscription.authorization = current
	return nil
}

func snapshotUpdate(session gameruntime.Session, host bool, projection game.Projection) Update {
	snapshot := session.Snapshot()
	return Update{
		SessionID: snapshot.ID, StateVersion: snapshot.State.StateVersion, VersionKey: snapshot.VersionKey,
		Host: host, Projection: cloneProjection(projection),
	}
}

func deltaUpdate(session gameruntime.Session, host bool, projection game.EventProjection) Update {
	snapshot := session.Snapshot()
	return Update{
		SessionID: snapshot.ID, StateVersion: snapshot.State.StateVersion, VersionKey: snapshot.VersionKey,
		Host: host, Delta: cloneEventProjection(projection),
	}
}

func cloneProjection(projection game.Projection) game.Projection {
	return game.Projection{
		View: projection.View.Clone(), AllowedActions: append([]game.Identifier(nil), projection.AllowedActions...),
	}
}

func cloneEventProjection(projection game.EventProjection) game.EventProjection {
	result := game.EventProjection{Messages: make([]game.Message, len(projection.Messages))}
	for index, message := range projection.Messages {
		result.Messages[index] = message.Clone()
	}
	return result
}

func validAuthorization(authorization Authorization) bool {
	return authorization.UserID != uuid.Nil && authorization.RoomID != uuid.Nil && authorization.SessionID != uuid.Nil &&
		authorization.Viewer.Valid() && authorization.Cursor > 0 && authorization.CurrentVersion >= authorization.Cursor &&
		authorization.RoomVersion > 0 && authorization.MembershipVersion > 0
}
