package loadtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/realtime/internal/subscription"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// errPostgresUnavailable is the stable cause used to prove projection failures reach every affected subscriber.
var errPostgresUnavailable = errors.New("load gate injected PostgreSQL outage")

// Run executes the capacity, reconnect, dependency-loss, recovery, and draining phases as one bounded scenario.
func Run(ctx context.Context, config Config) (Report, error) {
	if ctx == nil || config.validate() != nil {
		return Report{}, ErrInvalidConfig
	}
	scenarioCtx, cancelScenario := context.WithTimeout(ctx, config.ScenarioTimeout)
	defer cancelScenario()

	topology, err := buildTopology(config)
	if err != nil {
		return Report{}, err
	}
	authorizer := &loadAuthorizer{}
	runtime := newLoadRuntime(topology.sessions)
	hub, err := subscription.NewHub(authorizer, runtime, subscription.HubConfig{
		ReconcileInterval: config.ReconcileInterval,
		ProjectionTimeout: config.ProjectionTimeout,
	})
	if err != nil {
		return Report{}, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), config.ProjectionTimeout)
		defer cancel()
		_ = hub.Close(closeCtx, subscription.ErrHubClosed)
	}()

	phase := &phaseClock{}
	connections, err := connectSubscribers(scenarioCtx, hub, authorizer, topology.subscribers, 1, phase)
	if err != nil {
		return Report{}, err
	}

	phase.start()
	if err := runtime.advanceAll(2); err != nil {
		return Report{}, err
	}
	if err := notifySessions(hub, topology.sessionIDs, 2); err != nil {
		return Report{}, err
	}
	fanoutDeliveries, err := awaitVersion(scenarioCtx, connections.sinks, 2)
	if err != nil {
		return Report{}, err
	}
	hotDeliveries := selectHotSpectatorDeliveries(topology.subscribers, fanoutDeliveries)

	if _, err := closeConnections(scenarioCtx, connections); err != nil {
		return Report{}, err
	}
	phase.start()
	connections, err = connectSubscribers(scenarioCtx, hub, authorizer, topology.subscribers, 2, phase)
	if err != nil {
		return Report{}, err
	}
	if err := runtime.advanceAll(3); err != nil {
		return Report{}, err
	}
	if err := notifySessions(hub, topology.sessionIDs, 3); err != nil {
		return Report{}, err
	}
	reconnectDeliveries, err := awaitVersion(scenarioCtx, connections.sinks, 3)
	if err != nil {
		return Report{}, err
	}

	runCtx, cancelRun := context.WithCancel(scenarioCtx)
	defer cancelRun()
	runDone := make(chan error, 1)
	go func() { runDone <- hub.Run(runCtx) }()

	// Redis Pub/Sub is deliberately bypassed; the Hub's PostgreSQL reconciliation ticker must repair the gap.
	phase.start()
	if err := runtime.advanceAll(4); err != nil {
		return Report{}, err
	}
	redisDeliveries, err := awaitVersion(scenarioCtx, connections.sinks, 4)
	if err != nil {
		return Report{}, err
	}

	// A projection read failure must close every affected subscriber without emitting an uncommitted update.
	runtime.setFailure(errPostgresUnavailable)
	phase.start()
	if err := notifySessions(hub, topology.sessionIDs, 4); err != nil {
		return Report{}, err
	}
	postgresFailureClosures, err := awaitClosures(scenarioCtx, connections.sinks)
	if err != nil {
		return Report{}, err
	}
	uncommittedUpdates := countVersion(connections.sinks, 5)
	runtime.setFailure(nil)

	phase.start()
	connections, err = connectSubscribers(scenarioCtx, hub, authorizer, topology.subscribers, 4, phase)
	if err != nil {
		return Report{}, err
	}
	if err := runtime.advanceAll(5); err != nil {
		return Report{}, err
	}
	if err := notifySessions(hub, topology.sessionIDs, 5); err != nil {
		return Report{}, err
	}
	postgresRecoveryDeliveries, err := awaitVersion(scenarioCtx, connections.sinks, 5)
	if err != nil {
		return Report{}, err
	}

	phase.start()
	closeCtx, cancelClose := context.WithTimeout(scenarioCtx, config.ProjectionTimeout)
	err = hub.Close(closeCtx, subscription.ErrHubClosed)
	cancelClose()
	if err != nil {
		return Report{}, err
	}
	drainingClosures, err := awaitClosures(scenarioCtx, connections.sinks)
	if err != nil {
		return Report{}, err
	}
	select {
	case runErr := <-runDone:
		if runErr != nil {
			return Report{}, runErr
		}
	case <-scenarioCtx.Done():
		return Report{}, scenarioCtx.Err()
	}

	leaseMetric, ownershipFaults, err := runOwnershipFaults(scenarioCtx, config.Targets.LeaseTransferP95)
	if err != nil {
		return Report{}, err
	}
	metrics := Metrics{
		Fanout:           summarizeLatency(deliveryDurations(fanoutDeliveries), config.Targets.FanoutP95),
		HotSpectator:     summarizeLatency(deliveryDurations(hotDeliveries), config.Targets.HotSpectatorP95),
		Reconnect:        summarizeLatency(deliveryDurations(reconnectDeliveries), config.Targets.ReconnectP95),
		RedisRecovery:    summarizeLatency(deliveryDurations(redisDeliveries), config.Targets.RedisRecoveryP95),
		PostgresRecovery: summarizeLatency(deliveryDurations(postgresRecoveryDeliveries), config.Targets.PostgresRecoveryP95),
		Draining:         summarizeLatency(closureDurations(drainingClosures), config.Targets.DrainingP95),
		LeaseTransfer:    leaseMetric,
	}
	faults := FaultChecks{
		RedisNotificationLossRecovered:  len(redisDeliveries) == len(topology.subscribers),
		RedisLeaseFailureClosed:         ownershipFaults.redisFailureClosed,
		PostgresProjectionFailureClosed: closuresMatch(postgresFailureClosures, errPostgresUnavailable),
		PostgresOwnershipFailureClosed:  ownershipFaults.postgresFailureClosed,
		LeaseTransferredWithNewEpoch:    ownershipFaults.leaseTransferred,
		DrainingClosedAllSubscribers:    closuresMatch(drainingClosures, subscription.ErrHubClosed),
		UncommittedUpdates:              uncommittedUpdates,
	}
	report := Report{
		SchemaVersion:    ReportSchemaVersion,
		Scenario:         ScenarioName,
		Players:          config.Players,
		Sessions:         config.Sessions,
		HotSpectators:    config.HotSpectators,
		TotalSubscribers: len(topology.subscribers),
		Metrics:          metrics,
		Faults:           faults,
	}
	report.Success = metrics.passed() && faults.passed()
	return report, nil
}

type loadTopology struct {
	subscribers []subscriberSpec
	sessionIDs  []uuid.UUID
	sessions    map[uuid.UUID]gameruntime.Session
}

type subscriberSpec struct {
	userID       uuid.UUID
	roomID       uuid.UUID
	sessionID    uuid.UUID
	viewer       game.Viewer
	host         bool
	hotSpectator bool
}

// buildTopology assigns every player to one bounded room and concentrates all spectators in the first room.
func buildTopology(config Config) (loadTopology, error) {
	playersPerSession := config.Players / config.Sessions
	if playersPerSession < 1 || playersPerSession > int(game.MaximumParticipants) {
		return loadTopology{}, ErrInvalidConfig
	}
	topology := loadTopology{
		subscribers: make([]subscriberSpec, 0, config.Players+config.HotSpectators),
		sessionIDs:  make([]uuid.UUID, 0, config.Sessions),
		sessions:    make(map[uuid.UUID]gameruntime.Session, config.Sessions),
	}
	for sessionIndex := range config.Sessions {
		sessionID := stableUUID(fmt.Sprintf("load-session-%d", sessionIndex))
		roomID := stableUUID(fmt.Sprintf("load-room-%d", sessionIndex))
		participants := make([]gameruntime.Participant, 0, playersPerSession)
		for seatIndex := range playersPerSession {
			playerIndex := sessionIndex*playersPerSession + seatIndex
			userID := stableUUID(fmt.Sprintf("load-player-%d", playerIndex))
			viewer := game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(userID.String()), SeatIndex: uint32(seatIndex)}
			topology.subscribers = append(topology.subscribers, subscriberSpec{
				userID: userID, roomID: roomID, sessionID: sessionID, viewer: viewer, host: seatIndex == 0,
			})
			participants = append(participants, gameruntime.Participant{UserID: userID, SeatIndex: uint32(seatIndex)})
		}
		session, err := loadSession(sessionID, roomID, participants, 1)
		if err != nil {
			return loadTopology{}, err
		}
		topology.sessionIDs = append(topology.sessionIDs, sessionID)
		topology.sessions[sessionID] = session
	}
	hotSessionID := topology.sessionIDs[0]
	hotRoomID := topology.sessions[hotSessionID].Snapshot().RoomID
	for spectatorIndex := range config.HotSpectators {
		userID := stableUUID(fmt.Sprintf("load-spectator-%d", spectatorIndex))
		topology.subscribers = append(topology.subscribers, subscriberSpec{
			userID:       userID,
			roomID:       hotRoomID,
			sessionID:    hotSessionID,
			viewer:       game.Viewer{Kind: game.ViewerSpectator, UserID: game.Identifier(userID.String())},
			hotSpectator: true,
		})
	}
	return topology, nil
}

func stableUUID(value string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(value))
}

// loadSession restores deterministic active state so repeated CI runs differ only in measured scheduling latency.
func loadSession(sessionID, roomID uuid.UUID, participants []gameruntime.Participant, stateVersion uint64) (gameruntime.Session, error) {
	startedAt := time.Date(2026, time.July, 20, 20, 0, 0, 0, time.UTC)
	return gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: sessionID, RoomID: roomID,
		VersionKey:     game.VersionKey{GameID: "liars-dice", Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
		OwnershipEpoch: 1,
		Participants:   participants,
		State: game.Snapshot{
			SnapshotVersion: 1,
			StateVersion:    stateVersion,
			State:           game.Message{MessageType: "load.state", SchemaVersion: 1, Payload: []byte{byte(stateVersion)}},
		},
		Status:    gameruntime.StatusActive,
		StartedAt: startedAt,
		UpdatedAt: startedAt.Add(time.Duration(stateVersion-1) * time.Second),
	})
}

type loadAuthorizer struct {
	calls atomic.Int64
}

func (authorizer *loadAuthorizer) Refresh(_ context.Context, previous subscription.Authorization) (subscription.RefreshResult, error) {
	authorizer.calls.Add(1)
	return subscription.RefreshResult{Authorization: previous}, nil
}

type loadRuntime struct {
	mu       sync.RWMutex
	sessions map[uuid.UUID]gameruntime.Session
	failure  error
}

func newLoadRuntime(sessions map[uuid.UUID]gameruntime.Session) *loadRuntime {
	cloned := make(map[uuid.UUID]gameruntime.Session, len(sessions))
	for sessionID, session := range sessions {
		cloned[sessionID] = session
	}
	return &loadRuntime{sessions: cloned}
}

// ProjectCurrent serves the Hub's durable snapshot read and exposes the injected PostgreSQL failure unchanged.
func (runtime *loadRuntime) ProjectCurrent(_ context.Context, sessionID uuid.UUID, _ game.Viewer) (gameruntime.Session, game.Projection, error) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.failure != nil {
		return gameruntime.Session{}, game.Projection{}, runtime.failure
	}
	session, ok := runtime.sessions[sessionID]
	if !ok {
		return gameruntime.Session{}, game.Projection{}, gameruntime.ErrSessionNotFound
	}
	return session, projectionFor(session), nil
}

// ProjectEventsCurrent models either a one-version delta or snapshot repair after a missed Redis notification.
func (runtime *loadRuntime) ProjectEventsCurrent(
	_ context.Context,
	sessionID uuid.UUID,
	after uint64,
	_ game.Viewer,
) (gameruntime.Session, game.EventProjection, game.Projection, bool, error) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.failure != nil {
		return gameruntime.Session{}, game.EventProjection{}, game.Projection{}, false, runtime.failure
	}
	session, ok := runtime.sessions[sessionID]
	if !ok {
		return gameruntime.Session{}, game.EventProjection{}, game.Projection{}, false, gameruntime.ErrSessionNotFound
	}
	stateVersion := session.Snapshot().State.StateVersion
	if after == stateVersion {
		return session, game.EventProjection{}, game.Projection{}, false, nil
	}
	if after > stateVersion {
		return gameruntime.Session{}, game.EventProjection{}, game.Projection{}, false, gameruntime.ErrStateVersionConflict
	}
	if after+1 != stateVersion {
		return session, game.EventProjection{}, projectionFor(session), true, nil
	}
	delta := game.EventProjection{Messages: []game.Message{{
		MessageType: "load.delta", SchemaVersion: 1, Payload: []byte{byte(stateVersion)},
	}}}
	return session, delta, game.Projection{}, false, nil
}

func (runtime *loadRuntime) advanceAll(stateVersion uint64) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	for sessionID, current := range runtime.sessions {
		snapshot := current.Snapshot()
		snapshot.State.StateVersion = stateVersion
		snapshot.State.State.Payload = []byte{byte(stateVersion)}
		snapshot.UpdatedAt = snapshot.StartedAt.Add(time.Duration(stateVersion-1) * time.Second)
		next, err := gameruntime.RestoreSession(snapshot)
		if err != nil {
			return err
		}
		runtime.sessions[sessionID] = next
	}
	return nil
}

func (runtime *loadRuntime) setFailure(err error) {
	runtime.mu.Lock()
	runtime.failure = err
	runtime.mu.Unlock()
}

func projectionFor(session gameruntime.Session) game.Projection {
	stateVersion := session.Snapshot().State.StateVersion
	return game.Projection{View: game.Message{
		MessageType: "load.view", SchemaVersion: 1, Payload: []byte{byte(stateVersion)},
	}}
}

type phaseClock struct {
	startedAt atomic.Int64
}

func (phase *phaseClock) start() {
	phase.startedAt.Store(time.Now().UnixNano())
}

func (phase *phaseClock) elapsed() time.Duration {
	startedAt := phase.startedAt.Load()
	if startedAt == 0 {
		return 0
	}
	return time.Since(time.Unix(0, startedAt))
}

type delivery struct {
	stateVersion uint64
	latency      time.Duration
}

type closure struct {
	cause   error
	latency time.Duration
}

type loadSink struct {
	phase      *phaseClock
	deliveries chan delivery
	closures   chan closure
}

func newLoadSink(phase *phaseClock) *loadSink {
	return &loadSink{phase: phase, deliveries: make(chan delivery, 8), closures: make(chan closure, 1)}
}

func (sink *loadSink) Send(ctx context.Context, update subscription.Update) error {
	record := delivery{stateVersion: update.StateVersion, latency: sink.phase.elapsed()}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case sink.deliveries <- record:
		return nil
	}
}

func (sink *loadSink) Close(cause error) {
	select {
	case sink.closures <- closure{cause: cause, latency: sink.phase.elapsed()}:
	default:
	}
}

type loadConnections struct {
	handles []*subscription.Handle
	sinks   []*loadSink
}

// connectSubscribers waits for authorization refresh so timing starts only after all workers are active.
func connectSubscribers(
	ctx context.Context,
	hub *subscription.Hub,
	authorizer *loadAuthorizer,
	specs []subscriberSpec,
	cursor uint64,
	phase *phaseClock,
) (loadConnections, error) {
	connections := loadConnections{
		handles: make([]*subscription.Handle, 0, len(specs)),
		sinks:   make([]*loadSink, 0, len(specs)),
	}
	initialCalls := authorizer.calls.Load()
	for _, spec := range specs {
		sink := newLoadSink(phase)
		handle, err := hub.Register(subscription.Authorization{
			UserID: spec.userID, RoomID: spec.roomID, SessionID: spec.sessionID,
			Viewer: spec.viewer, Cursor: cursor, CurrentVersion: cursor,
			RoomVersion: 1, MembershipVersion: 1, Host: spec.host,
		}, sink)
		if err != nil {
			return loadConnections{}, err
		}
		connections.handles = append(connections.handles, handle)
		connections.sinks = append(connections.sinks, sink)
	}
	wantCalls := initialCalls + int64(len(specs))
	for authorizer.calls.Load() < wantCalls {
		select {
		case <-ctx.Done():
			return loadConnections{}, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	return connections, nil
}

func closeConnections(ctx context.Context, connections loadConnections) ([]closure, error) {
	for _, handle := range connections.handles {
		handle.Close(context.Canceled)
	}
	return awaitClosures(ctx, connections.sinks)
}

func notifySessions(hub *subscription.Hub, sessionIDs []uuid.UUID, stateVersion uint64) error {
	for _, sessionID := range sessionIDs {
		if err := hub.Notify(redisstore.SessionFanoutEvent{SessionID: sessionID, StateVersion: stateVersion}); err != nil {
			return err
		}
	}
	return nil
}

// awaitVersion requires exactly the requested committed version and rejects any future-version delivery.
func awaitVersion(ctx context.Context, sinks []*loadSink, stateVersion uint64) ([]delivery, error) {
	result := make([]delivery, len(sinks))
	for index, sink := range sinks {
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case record := <-sink.deliveries:
				if record.stateVersion < stateVersion {
					continue
				}
				if record.stateVersion != stateVersion {
					return nil, fmt.Errorf("unexpected realtime state version: got %d want %d", record.stateVersion, stateVersion)
				}
				result[index] = record
				goto nextSink
			}
		}
	nextSink:
	}
	return result, nil
}

func awaitClosures(ctx context.Context, sinks []*loadSink) ([]closure, error) {
	result := make([]closure, len(sinks))
	for index, sink := range sinks {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result[index] = <-sink.closures:
		}
	}
	return result, nil
}

func countVersion(sinks []*loadSink, stateVersion uint64) int {
	count := 0
	for _, sink := range sinks {
		for {
			select {
			case record := <-sink.deliveries:
				if record.stateVersion == stateVersion {
					count++
				}
			default:
				goto nextSink
			}
		}
	nextSink:
	}
	return count
}

func selectHotSpectatorDeliveries(specs []subscriberSpec, records []delivery) []delivery {
	result := make([]delivery, 0)
	for index, spec := range specs {
		if spec.hotSpectator {
			result = append(result, records[index])
		}
	}
	return result
}

func deliveryDurations(records []delivery) []time.Duration {
	result := make([]time.Duration, len(records))
	for index, record := range records {
		result[index] = record.latency
	}
	return result
}

func closureDurations(records []closure) []time.Duration {
	result := make([]time.Duration, len(records))
	for index, record := range records {
		result[index] = record.latency
	}
	return result
}

func closuresMatch(records []closure, target error) bool {
	if len(records) == 0 {
		return false
	}
	for _, record := range records {
		if !errors.Is(record.cause, target) {
			return false
		}
	}
	return true
}
