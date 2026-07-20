// Package owner coordinates Redis liveness leases with PostgreSQL ownership fencing.
package owner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
)

var (
	// ErrInvalidConfig rejects an owner that cannot renew well before its Redis lease expires.
	ErrInvalidConfig = errors.New("invalid realtime owner configuration")
	// ErrOwnedElsewhere tells the internal router to use the address returned by Redis instead.
	ErrOwnedElsewhere = errors.New("game session is owned by another realtime instance")
	// ErrOwnershipUnavailable keeps commands fail closed while a claim is absent or incomplete.
	ErrOwnershipUnavailable = errors.New("game session ownership is unavailable")
	// ErrLeaseLost cancels commands as soon as renewal or token comparison fails.
	ErrLeaseLost = errors.New("game session lease lost")
	// ErrClosed rejects new claims after the realtime process starts draining.
	ErrClosed = errors.New("realtime owner manager is closed")
)

// LeaseCoordinator owns only short-lived Redis routing state; it never authorizes a state write.
type LeaseCoordinator interface {
	AcquireSessionLease(context.Context, uuid.UUID, string, string) (redisstore.SessionLease, bool, error)
	PromoteSessionLease(context.Context, redisstore.SessionLease, uint64) (redisstore.SessionLease, bool, error)
	RenewSessionLease(context.Context, redisstore.SessionLease) (bool, error)
	ReleaseSessionLease(context.Context, redisstore.SessionLease) (bool, error)
}

// SessionStore advances the authoritative PostgreSQL fencing epoch after a Redis claim succeeds.
type SessionStore interface {
	Get(context.Context, uuid.UUID) (gameruntime.Session, error)
	AcquireOwnershipCAS(context.Context, gameruntime.Session, gameruntime.Session) (gameruntime.Session, error)
}

// Runtime executes only commands whose epoch is supplied by this manager.
type Runtime interface {
	HandleAction(context.Context, gameruntime.ActionCommand) (gameruntime.ActionResult, error)
	HandleTimer(context.Context, gameruntime.DueTimer, uint64) (gameruntime.TimerCommitResult, error)
	HandleSystem(context.Context, gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error)
}

// FanoutPublisher wakes subscribers only after PostgreSQL has committed a new authoritative version.
type FanoutPublisher interface {
	PublishSessionFanout(context.Context, redisstore.SessionFanoutEvent) error
}

// Config identifies one routable process and leaves enough time for a failed renewal to be observed.
type Config struct {
	InstanceID    string
	Address       string
	LeaseTTL      time.Duration
	RenewInterval time.Duration
}

// Manager is the single process-local authority for held lease tokens and their PostgreSQL epochs.
type Manager struct {
	coordinator LeaseCoordinator
	sessions    SessionStore
	runtime     Runtime
	fanout      FanoutPublisher
	clock       clock.Clock
	config      Config

	lifecycleCtx    context.Context
	cancelLifecycle context.CancelCauseFunc

	mu       sync.Mutex
	held     map[uuid.UUID]*heldSession
	claims   map[uuid.UUID]*claimCall
	running  bool
	closed   bool
	closeErr error
}

type heldSession struct {
	lease  redisstore.SessionLease
	epoch  uint64
	ctx    context.Context
	cancel context.CancelCauseFunc
}

type claimCall struct {
	done  chan struct{}
	state *heldSession
	err   error
}

// NewManager validates the lease timing before accepting any session claim.
func NewManager(
	coordinator LeaseCoordinator,
	sessions SessionStore,
	runtime Runtime,
	fanout FanoutPublisher,
	source clock.Clock,
	config Config,
) (*Manager, error) {
	if coordinator == nil || sessions == nil || runtime == nil || fanout == nil || source == nil ||
		strings.TrimSpace(config.InstanceID) != config.InstanceID || config.InstanceID == "" ||
		strings.TrimSpace(config.Address) != config.Address || config.Address == "" ||
		config.LeaseTTL < redisstore.MinimumSessionLeaseTTL || config.LeaseTTL > redisstore.MaximumSessionLeaseTTL ||
		config.RenewInterval <= 0 || config.RenewInterval > config.LeaseTTL/2 {
		return nil, ErrInvalidConfig
	}
	lifecycleCtx, cancelLifecycle := context.WithCancelCause(context.Background())
	return &Manager{
		coordinator:     coordinator,
		sessions:        sessions,
		runtime:         runtime,
		fanout:          fanout,
		clock:           source,
		config:          config,
		lifecycleCtx:    lifecycleCtx,
		cancelLifecycle: cancelLifecycle,
		held:            make(map[uuid.UUID]*heldSession),
		claims:          make(map[uuid.UUID]*claimCall),
	}, nil
}

// Run renews every held token. A lost token cancels its in-flight commands without stopping other sessions.
func (manager *Manager) Run(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrInvalidConfig
	}
	manager.mu.Lock()
	if manager.closed || manager.running {
		manager.mu.Unlock()
		return ErrClosed
	}
	manager.running = true
	manager.mu.Unlock()

	ticker := time.NewTicker(manager.config.RenewInterval)
	defer ticker.Stop()
	defer func() {
		manager.mu.Lock()
		manager.running = false
		manager.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return manager.Close(context.Background())
		case <-manager.lifecycleCtx.Done():
			return nil
		case <-ticker.C:
			manager.renewHeld(ctx)
		}
	}
}

// EnsureOwned performs Redis claim first and PostgreSQL epoch CAS second, coalescing concurrent claims per session.
func (manager *Manager) EnsureOwned(ctx context.Context, sessionID uuid.UUID) (uint64, error) {
	if manager == nil || ctx == nil || sessionID == uuid.Nil {
		return 0, ErrOwnershipUnavailable
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return 0, ErrClosed
	}
	if state := manager.held[sessionID]; state != nil && state.ctx.Err() == nil {
		epoch := state.epoch
		manager.mu.Unlock()
		return epoch, nil
	}
	if pending := manager.claims[sessionID]; pending != nil {
		manager.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-pending.done:
			if pending.err != nil {
				return 0, pending.err
			}
			return pending.state.epoch, nil
		}
	}
	pending := &claimCall{done: make(chan struct{})}
	manager.claims[sessionID] = pending
	manager.mu.Unlock()

	state, err := manager.claim(ctx, sessionID)
	manager.mu.Lock()
	delete(manager.claims, sessionID)
	if err == nil && !manager.closed {
		manager.held[sessionID] = state
	} else if err == nil {
		state.cancel(ErrClosed)
		err = errors.Join(ErrClosed, manager.release(context.Background(), state.lease))
	}
	pending.state, pending.err = state, err
	close(pending.done)
	manager.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return state.epoch, nil
}

// HandleAction replaces every caller-supplied epoch with the process-local held fencing token.
func (manager *Manager) HandleAction(ctx context.Context, command gameruntime.ActionCommand) (gameruntime.ActionResult, error) {
	state, err := manager.owned(command.SessionID)
	if err != nil {
		return gameruntime.ActionResult{}, err
	}
	command.OwnershipEpoch = state.epoch
	ownedCtx, cancel := commandContext(ctx, state.ctx)
	defer cancel()
	result, err := manager.runtime.HandleAction(ownedCtx, command)
	if err != nil {
		return gameruntime.ActionResult{}, err
	}
	if !manager.stillOwns(command.SessionID, state) {
		return gameruntime.ActionResult{}, ErrLeaseLost
	}
	if err := manager.publish(ownedCtx, result.Session); err != nil {
		return gameruntime.ActionResult{}, err
	}
	return result, nil
}

// HandleTimer fences one persisted due timer with the epoch held by this process.
func (manager *Manager) HandleTimer(ctx context.Context, due gameruntime.DueTimer) (gameruntime.TimerCommitResult, error) {
	state, err := manager.owned(due.SessionID)
	if err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	ownedCtx, cancel := commandContext(ctx, state.ctx)
	defer cancel()
	result, err := manager.runtime.HandleTimer(ownedCtx, due, state.epoch)
	if err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	if !manager.stillOwns(due.SessionID, state) {
		return gameruntime.TimerCommitResult{}, ErrLeaseLost
	}
	if err := manager.publish(ownedCtx, result.Session); err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	return result, nil
}

// HandleSystem fences room-outbox, platform, and host lifecycle work with the held owner epoch.
func (manager *Manager) HandleSystem(ctx context.Context, command gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error) {
	state, err := manager.owned(command.SessionID)
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	command.OwnershipEpoch = state.epoch
	ownedCtx, cancel := commandContext(ctx, state.ctx)
	defer cancel()
	result, err := manager.runtime.HandleSystem(ownedCtx, command)
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	if !manager.stillOwns(command.SessionID, state) {
		return gameruntime.SystemCommitResult{}, ErrLeaseLost
	}
	if err := manager.publish(ownedCtx, result.Session); err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	return result, nil
}

// Close first cancels all commands, then compare-token releases every lease. It is idempotent.
func (manager *Manager) Close(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrInvalidConfig
	}
	manager.mu.Lock()
	if manager.closed {
		err := manager.closeErr
		manager.mu.Unlock()
		return err
	}
	manager.closed = true
	manager.cancelLifecycle(ErrClosed)
	states := make([]*heldSession, 0, len(manager.held))
	for sessionID, state := range manager.held {
		state.cancel(ErrClosed)
		states = append(states, state)
		delete(manager.held, sessionID)
	}
	manager.mu.Unlock()

	var closeErr error
	for _, state := range states {
		closeErr = errors.Join(closeErr, manager.release(ctx, state.lease))
	}
	manager.mu.Lock()
	manager.closeErr = closeErr
	manager.mu.Unlock()
	return closeErr
}

func (manager *Manager) claim(ctx context.Context, sessionID uuid.UUID) (*heldSession, error) {
	lease, acquired, err := manager.coordinator.AcquireSessionLease(ctx, sessionID, manager.config.InstanceID, manager.config.Address)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrOwnedElsewhere
	}
	current, err := manager.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, errors.Join(err, manager.release(context.Background(), lease))
	}
	snapshot := current.Snapshot()
	if snapshot.Status.Terminal() {
		return nil, errors.Join(gameruntime.ErrSessionTerminal, manager.release(context.Background(), lease))
	}
	at := manager.clock.Now().Round(0).UTC()
	if !at.After(snapshot.UpdatedAt) {
		at = snapshot.UpdatedAt.Add(time.Microsecond)
	}
	next, err := current.AcquireOwnership(snapshot.OwnershipEpoch, at)
	if err != nil {
		return nil, errors.Join(err, manager.release(context.Background(), lease))
	}
	owned, err := manager.sessions.AcquireOwnershipCAS(ctx, current, next)
	if err != nil {
		return nil, errors.Join(err, manager.release(context.Background(), lease))
	}
	promotedLease, promoted, err := manager.coordinator.PromoteSessionLease(ctx, lease, owned.Snapshot().OwnershipEpoch)
	if err != nil || !promoted {
		if err == nil {
			err = ErrLeaseLost
		}
		return nil, errors.Join(err, manager.release(context.Background(), lease))
	}
	lease = promotedLease
	leaseCtx, cancel := context.WithCancelCause(manager.lifecycleCtx)
	return &heldSession{lease: lease, epoch: owned.Snapshot().OwnershipEpoch, ctx: leaseCtx, cancel: cancel}, nil
}

func (manager *Manager) owned(sessionID uuid.UUID) (*heldSession, error) {
	if manager == nil || sessionID == uuid.Nil {
		return nil, ErrOwnershipUnavailable
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil, ErrClosed
	}
	state := manager.held[sessionID]
	if state == nil || state.ctx.Err() != nil {
		return nil, ErrOwnershipUnavailable
	}
	return state, nil
}

func (manager *Manager) stillOwns(sessionID uuid.UUID, expected *heldSession) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return !manager.closed && manager.held[sessionID] == expected && expected.ctx.Err() == nil
}

func (manager *Manager) renewHeld(ctx context.Context) {
	manager.mu.Lock()
	states := make([]*heldSession, 0, len(manager.held))
	for _, state := range manager.held {
		states = append(states, state)
	}
	manager.mu.Unlock()
	for _, state := range states {
		renewed, err := manager.coordinator.RenewSessionLease(ctx, state.lease)
		if err != nil || !renewed {
			manager.lose(state, ErrLeaseLost)
		}
	}
}

func (manager *Manager) lose(state *heldSession, cause error) {
	manager.mu.Lock()
	if manager.held[state.lease.SessionID] == state {
		delete(manager.held, state.lease.SessionID)
		state.cancel(cause)
	}
	manager.mu.Unlock()
}

func (manager *Manager) publish(ctx context.Context, session gameruntime.Session) error {
	snapshot := session.Snapshot()
	if snapshot.ID == uuid.Nil || snapshot.State.StateVersion == 0 {
		return gameruntime.ErrInvalidSessionInput
	}
	return manager.fanout.PublishSessionFanout(ctx, redisstore.SessionFanoutEvent{
		SessionID: snapshot.ID, StateVersion: snapshot.State.StateVersion,
	})
}

func (manager *Manager) release(ctx context.Context, lease redisstore.SessionLease) error {
	released, err := manager.coordinator.ReleaseSessionLease(ctx, lease)
	if err != nil {
		return err
	}
	if !released {
		return ErrLeaseLost
	}
	return nil
}

func commandContext(requestCtx, leaseCtx context.Context) (context.Context, context.CancelFunc) {
	if requestCtx == nil {
		requestCtx = context.Background()
	}
	ctx, cancelCause := context.WithCancelCause(requestCtx)
	stop := context.AfterFunc(leaseCtx, func() {
		cancelCause(context.Cause(leaseCtx))
	})
	return ctx, func() {
		stop()
		cancelCause(context.Canceled)
	}
}
