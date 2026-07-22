package loadtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/realtime/internal/owner"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
)

// errRedisUnavailable is the stable cause used to prove lease acquisition fails before PostgreSQL CAS.
var errRedisUnavailable = errors.New("load gate injected Redis outage")

type ownershipFaultResult struct {
	leaseTransferred      bool
	redisFailureClosed    bool
	postgresFailureClosed bool
}

// runOwnershipFaults measures a clean lease handoff, then verifies Redis and PostgreSQL failures cannot claim ownership.
func runOwnershipFaults(ctx context.Context, target time.Duration) (LatencyMetric, ownershipFaultResult, error) {
	sessionID := stableUUID("load-ownership-session")
	roomID := stableUUID("load-ownership-room")
	userID := stableUUID("load-ownership-player")
	session, err := loadSession(sessionID, roomID, []gameruntime.Participant{{UserID: userID, SeatIndex: 0}}, 1)
	if err != nil {
		return LatencyMetric{}, ownershipFaultResult{}, err
	}
	coordinator := &sharedLeaseCoordinator{}
	store := &ownershipSessionStore{session: session}
	runtime := ownershipRuntime{store: store}
	publisher := ownershipPublisher{}
	startedAt := session.Snapshot().StartedAt

	managerA, err := newOwnershipManager(coordinator, store, runtime, publisher, "realtime-a", startedAt.Add(time.Second))
	if err != nil {
		return LatencyMetric{}, ownershipFaultResult{}, err
	}
	epochA, err := managerA.EnsureOwned(ctx, sessionID)
	if err != nil {
		return LatencyMetric{}, ownershipFaultResult{}, err
	}
	transferStarted := time.Now()
	if err := managerA.Close(ctx); err != nil {
		return LatencyMetric{}, ownershipFaultResult{}, err
	}
	managerB, err := newOwnershipManager(coordinator, store, runtime, publisher, "realtime-b", startedAt.Add(2*time.Second))
	if err != nil {
		return LatencyMetric{}, ownershipFaultResult{}, err
	}
	epochB, err := managerB.EnsureOwned(ctx, sessionID)
	if err != nil {
		return LatencyMetric{}, ownershipFaultResult{}, err
	}
	transferLatency := time.Since(transferStarted)
	if err := managerB.Close(ctx); err != nil {
		return LatencyMetric{}, ownershipFaultResult{}, err
	}

	store.setCASFailure(true)
	managerPostgres, err := newOwnershipManager(coordinator, store, runtime, publisher, "realtime-postgres-fault", startedAt.Add(3*time.Second))
	if err != nil {
		return LatencyMetric{}, ownershipFaultResult{}, err
	}
	epochBeforePostgresFault := store.epoch()
	_, postgresErr := managerPostgres.EnsureOwned(ctx, sessionID)
	postgresFailureClosed := errors.Is(postgresErr, errPostgresUnavailable) &&
		store.epoch() == epochBeforePostgresFault && coordinator.empty()
	_ = managerPostgres.Close(ctx)
	store.setCASFailure(false)

	coordinator.setAcquireFailure(true)
	managerRedis, err := newOwnershipManager(coordinator, store, runtime, publisher, "realtime-redis-fault", startedAt.Add(4*time.Second))
	if err != nil {
		return LatencyMetric{}, ownershipFaultResult{}, err
	}
	casCallsBeforeRedisFault := store.casCallCount()
	_, redisErr := managerRedis.EnsureOwned(ctx, sessionID)
	redisFailureClosed := errors.Is(redisErr, errRedisUnavailable) &&
		store.casCallCount() == casCallsBeforeRedisFault && coordinator.empty()
	_ = managerRedis.Close(ctx)
	coordinator.setAcquireFailure(false)

	return summarizeLatency([]time.Duration{transferLatency}, target), ownershipFaultResult{
		leaseTransferred:      epochA > 0 && epochB == epochA+1,
		redisFailureClosed:    redisFailureClosed,
		postgresFailureClosed: postgresFailureClosed,
	}, nil
}

func newOwnershipManager(
	coordinator owner.LeaseCoordinator,
	store owner.SessionStore,
	runtime owner.Runtime,
	publisher owner.FanoutPublisher,
	instanceID string,
	at time.Time,
) (*owner.Manager, error) {
	return owner.NewManager(coordinator, store, runtime, publisher, clock.NewFake(at), owner.Config{
		InstanceID:    instanceID,
		Address:       fmt.Sprintf("http://%s.internal:8091", instanceID),
		LeaseTTL:      time.Second,
		RenewInterval: 100 * time.Millisecond,
	})
}

type sharedLeaseCoordinator struct {
	mu          sync.Mutex
	lease       redisstore.SessionLease
	nextToken   uint64
	failAcquire bool
}

// AcquireSessionLease mirrors the token-hiding behavior of the Redis coordinator when another owner is active.
func (coordinator *sharedLeaseCoordinator) AcquireSessionLease(
	_ context.Context,
	sessionID uuid.UUID,
	instanceID, address string,
) (redisstore.SessionLease, bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.failAcquire {
		return redisstore.SessionLease{}, false, errRedisUnavailable
	}
	if coordinator.lease.Active() {
		current := coordinator.lease
		current.Token = ""
		return current, false, nil
	}
	coordinator.nextToken++
	coordinator.lease = redisstore.SessionLease{
		SessionID: sessionID,
		Owner:     instanceID,
		Address:   address,
		Token:     fmt.Sprintf("%022d", coordinator.nextToken),
	}
	return coordinator.lease, true, nil
}

func (coordinator *sharedLeaseCoordinator) PromoteSessionLease(
	_ context.Context,
	lease redisstore.SessionLease,
	epoch uint64,
) (redisstore.SessionLease, bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !sameLease(coordinator.lease, lease) {
		return redisstore.SessionLease{}, false, nil
	}
	coordinator.lease.Ready = true
	coordinator.lease.OwnershipEpoch = epoch
	return coordinator.lease, true, nil
}

func (coordinator *sharedLeaseCoordinator) RenewSessionLease(_ context.Context, lease redisstore.SessionLease) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return sameLease(coordinator.lease, lease), nil
}

func (coordinator *sharedLeaseCoordinator) ReleaseSessionLease(_ context.Context, lease redisstore.SessionLease) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !sameLease(coordinator.lease, lease) {
		return false, nil
	}
	coordinator.lease = redisstore.SessionLease{}
	return true, nil
}

func (coordinator *sharedLeaseCoordinator) setAcquireFailure(failed bool) {
	coordinator.mu.Lock()
	coordinator.failAcquire = failed
	coordinator.mu.Unlock()
}

func (coordinator *sharedLeaseCoordinator) empty() bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return !coordinator.lease.Active()
}

func sameLease(left, right redisstore.SessionLease) bool {
	return left.SessionID == right.SessionID && left.Owner == right.Owner && left.Address == right.Address &&
		left.Token != "" && left.Token == right.Token
}

type ownershipSessionStore struct {
	mu       sync.Mutex
	session  gameruntime.Session
	failCAS  bool
	casCalls int
}

func (store *ownershipSessionStore) Get(context.Context, uuid.UUID) (gameruntime.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.session, nil
}

// AcquireOwnershipCAS records ordering so the Redis failure test can prove PostgreSQL was never reached.
func (store *ownershipSessionStore) AcquireOwnershipCAS(
	_ context.Context,
	_ gameruntime.Session,
	next gameruntime.Session,
) (gameruntime.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.casCalls++
	if store.failCAS {
		return gameruntime.Session{}, errPostgresUnavailable
	}
	store.session = next
	return next, nil
}

func (store *ownershipSessionStore) setCASFailure(failed bool) {
	store.mu.Lock()
	store.failCAS = failed
	store.mu.Unlock()
}

func (store *ownershipSessionStore) epoch() uint64 {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.session.Snapshot().OwnershipEpoch
}

func (store *ownershipSessionStore) casCallCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.casCalls
}

type ownershipRuntime struct {
	store *ownershipSessionStore
}

func (runtime ownershipRuntime) HandleAction(context.Context, gameruntime.ActionCommand) (gameruntime.ActionResult, error) {
	session, _ := runtime.store.Get(context.Background(), uuid.Nil)
	return gameruntime.ActionResult{Session: session}, nil
}

func (runtime ownershipRuntime) HandleTimer(context.Context, gameruntime.DueTimer, uint64) (gameruntime.TimerCommitResult, error) {
	session, _ := runtime.store.Get(context.Background(), uuid.Nil)
	return gameruntime.TimerCommitResult{Session: session}, nil
}

func (runtime ownershipRuntime) HandleSystem(context.Context, gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error) {
	session, _ := runtime.store.Get(context.Background(), uuid.Nil)
	return gameruntime.SystemCommitResult{Session: session}, nil
}

type ownershipPublisher struct{}

func (ownershipPublisher) PublishSessionFanout(context.Context, redisstore.SessionFanoutEvent) error {
	return nil
}
