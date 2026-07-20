package game

import "time"

const (
	// MaximumMessageBytes bounds one decoded game payload before engine-specific parsing.
	MaximumMessageBytes = 1 << 20
	// RandomSeedBytes is the fixed entropy input persisted with a deterministic command or timer execution.
	RandomSeedBytes = 32
	// maximumAllocatedIDs bounds runtime-preallocated identifiers consumed by one deterministic transition.
	maximumAllocatedIDs = 256
	// MaximumTransitionEvents bounds one atomic engine result before persistence allocates an event batch.
	MaximumTransitionEvents = 1024
	// MaximumTransitionTimers bounds timer replacement work committed with one engine result.
	MaximumTransitionTimers = 64
	// MaximumAllowedActions bounds one viewer projection and prevents malformed modules from inflating client payloads.
	MaximumAllowedActions = 128
)

// Message is an engine-owned protobuf payload carried without a platform-level union of every game type.
type Message struct {
	MessageType   Identifier
	SchemaVersion uint32
	Payload       []byte
}

// Clone prevents a module or runtime caller from retaining and mutating another layer's payload buffer.
func (message Message) Clone() Message {
	message.Payload = append([]byte(nil), message.Payload...)
	return message
}

// Valid performs common envelope checks; the registered game codec remains responsible for payload semantics.
func (message Message) Valid() bool {
	_, err := ParseIdentifier(string(message.MessageType))
	return err == nil && message.SchemaVersion > 0 && len(message.Payload) <= MaximumMessageBytes
}

// DeterministicContext contains every non-state input an engine may use for time, randomness, and new identifiers.
type DeterministicContext struct {
	Now          time.Time
	RandomSeed   [RandomSeedBytes]byte
	AllocatedIDs []Identifier
}

// Clone keeps the runtime's preallocated identifier sequence immutable across retries and replay.
func (execution DeterministicContext) Clone() DeterministicContext {
	execution.Now = execution.Now.Round(0).UTC()
	execution.AllocatedIDs = append([]Identifier(nil), execution.AllocatedIDs...)
	return execution
}

// Valid rejects process-local time metadata, duplicate IDs, and unbounded transition allocations.
func (execution DeterministicContext) Valid() bool {
	if execution.Now.IsZero() || execution.Now != execution.Now.Round(0).UTC() || len(execution.AllocatedIDs) > maximumAllocatedIDs {
		return false
	}
	seeded := false
	for _, value := range execution.RandomSeed {
		seeded = seeded || value != 0
	}
	if !seeded {
		return false
	}
	seen := make(map[Identifier]struct{}, len(execution.AllocatedIDs))
	for _, id := range execution.AllocatedIDs {
		if _, err := ParseIdentifier(string(id)); err != nil {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

// Participant is the immutable user and stable seat snapshot frozen by PartyRoom at session start.
type Participant struct {
	UserID    Identifier
	SeatIndex uint32
}

// Valid accepts a canonical platform identity; seat uniqueness and participant-count bounds are request-level rules.
func (participant Participant) Valid() bool {
	_, err := ParseIdentifier(string(participant.UserID))
	return err == nil
}

// SessionStartContext is trusted PartyRoom state that game configuration payloads cannot override.
type SessionStartContext struct {
	HostUserID   Identifier
	StartingSeat uint32
}

// Valid requires both the host identity and initial seat to exist in the same frozen participant snapshot.
func (start SessionStartContext) Valid(participants []Participant) bool {
	if _, err := ParseIdentifier(string(start.HostUserID)); err != nil {
		return false
	}
	hostFound, seatFound := false, false
	for _, participant := range participants {
		hostFound = hostFound || participant.UserID == start.HostUserID
		seatFound = seatFound || participant.SeatIndex == start.StartingSeat
	}
	return hostFound && seatFound
}

// Snapshot is opaque to the platform runtime except for its migration and optimistic state versions.
type Snapshot struct {
	SnapshotVersion uint32
	StateVersion    uint64
	State           Message
}

// Valid rejects snapshots that cannot be versioned, replayed, or decoded by the registered module.
func (snapshot Snapshot) Valid() bool {
	return snapshot.SnapshotVersion > 0 && snapshot.StateVersion > 0 && snapshot.State.Valid()
}

// CreateRequest initializes a session from frozen participants and a versioned game configuration payload.
type CreateRequest struct {
	Context      DeterministicContext
	StartContext SessionStartContext
	Participants []Participant
	Config       Message
}

// Validate checks deterministic inputs, configuration, manifest bounds, and unique frozen users and seats.
func (request CreateRequest) Validate(limits ParticipantLimits) error {
	if !request.Context.Valid() || !request.Config.Valid() || !limits.Valid() ||
		len(request.Participants) < int(limits.Minimum) || len(request.Participants) > int(limits.Maximum) {
		return ErrInvalidContract
	}
	users := make(map[Identifier]struct{}, len(request.Participants))
	seats := make(map[uint32]struct{}, len(request.Participants))
	for _, participant := range request.Participants {
		if !participant.Valid() {
			return ErrInvalidContract
		}
		if _, duplicate := users[participant.UserID]; duplicate {
			return ErrInvalidContract
		}
		if _, duplicate := seats[participant.SeatIndex]; duplicate {
			return ErrInvalidContract
		}
		users[participant.UserID] = struct{}{}
		seats[participant.SeatIndex] = struct{}{}
	}
	if !request.StartContext.Valid(request.Participants) {
		return ErrInvalidContract
	}
	return nil
}

// CommandRequest is the pure input for one authenticated, idempotent player action.
type CommandRequest struct {
	Context              DeterministicContext
	ActorUserID          Identifier
	ActionID             ActionID
	ExpectedStateVersion uint64
	Command              Message
}

// Valid rejects commands that cannot participate in deterministic action-receipt and version checks.
func (request CommandRequest) Valid() bool {
	_, actorErr := ParseIdentifier(string(request.ActorUserID))
	return request.Context.Valid() && actorErr == nil && request.ActionID.Valid() && request.ExpectedStateVersion > 0 && request.Command.Valid()
}

// TimerRequest replays a persisted timer firing against an exact expected state version.
type TimerRequest struct {
	Context              DeterministicContext
	TimerID              Identifier
	ExpectedStateVersion uint64
	Timer                Message
}

// Valid rejects unversioned or process-local timer firings before they reach an engine.
func (request TimerRequest) Valid() bool {
	_, timerErr := ParseIdentifier(string(request.TimerID))
	return request.Context.Valid() && timerErr == nil && request.ExpectedStateVersion > 0 && request.Timer.Valid()
}

// SystemRequest is one runtime-originated command tied to a durable operation and source event.
type SystemRequest struct {
	Context              DeterministicContext
	SystemOperationID    ActionID
	SourceEventID        Identifier
	ExpectedStateVersion uint64
	System               Message
}

// Valid rejects system commands that cannot participate in exact-version replay and durable deduplication.
func (request SystemRequest) Valid() bool {
	_, sourceErr := ParseIdentifier(string(request.SourceEventID))
	return request.Context.Valid() && request.SystemOperationID.Valid() && sourceErr == nil &&
		request.ExpectedStateVersion > 0 && request.System.Valid()
}

// Event is one engine fact; the runtime owns persistent batch sequence numbers and preserves this slice order.
type Event struct {
	Message Message
}

// Valid requires a decodable game-owned event envelope.
func (event Event) Valid() bool {
	return event.Message.Valid()
}

// VersionedEvent associates an ordered engine fact with the state version committed by its atomic batch.
type VersionedEvent struct {
	StateVersion uint64
	Event        Event
}

// Valid rejects events that cannot advance a viewer subscription cursor deterministically.
func (event VersionedEvent) Valid() bool {
	return event.StateVersion > 0 && event.Event.Valid()
}

// TimerIntent asks the runtime to persist or replace a timer; engines never schedule process timers directly.
type TimerIntent struct {
	TimerID Identifier
	DueAt   time.Time
	Message Message
}

// Valid requires a future UTC deadline and a canonical timer ID/message owned by the registered module.
func (timer TimerIntent) Valid(executedAt time.Time) bool {
	_, err := ParseIdentifier(string(timer.TimerID))
	return err == nil && !timer.DueAt.IsZero() && timer.DueAt == timer.DueAt.Round(0).UTC() &&
		!timer.DueAt.Before(executedAt) && timer.Message.Valid()
}

// Transition is the complete deterministic result committed atomically by the future session runtime.
type Transition struct {
	Snapshot Snapshot
	Events   []Event
	// Timers is the complete next-state timer set; omitting a previous timer cancels it atomically.
	Timers   []TimerIntent
	Finished bool
}

// Validate checks state-version monotonicity and bounds every event and timer in one atomic result.
func (transition Transition) Validate(previousStateVersion uint64, executedAt time.Time) error {
	if previousStateVersion == ^uint64(0) || !transition.Snapshot.Valid() || transition.Snapshot.StateVersion != previousStateVersion+1 ||
		executedAt.IsZero() || executedAt != executedAt.Round(0).UTC() || len(transition.Events) == 0 ||
		len(transition.Events) > MaximumTransitionEvents ||
		len(transition.Timers) > MaximumTransitionTimers {
		return ErrInvalidContract
	}
	for _, event := range transition.Events {
		if !event.Valid() {
			return ErrInvalidContract
		}
	}
	timers := make(map[Identifier]struct{}, len(transition.Timers))
	for _, timer := range transition.Timers {
		if !timer.Valid(executedAt) {
			return ErrInvalidContract
		}
		if _, duplicate := timers[timer.TimerID]; duplicate {
			return ErrInvalidContract
		}
		timers[timer.TimerID] = struct{}{}
	}
	return nil
}

// ViewerKind selects the only projection surface a module may produce for one authenticated recipient.
type ViewerKind string

const (
	ViewerPlayer    ViewerKind = "player"
	ViewerSpectator ViewerKind = "spectator"
	ViewerReplay    ViewerKind = "replay"
)

// Viewer identifies the projection recipient without granting access to authoritative state fields.
type Viewer struct {
	Kind      ViewerKind
	UserID    Identifier
	SeatIndex uint32
}

// Valid requires an authenticated canonical viewer identity and one recognized projection surface.
func (viewer Viewer) Valid() bool {
	_, err := ParseIdentifier(string(viewer.UserID))
	kindValid := viewer.Kind == ViewerPlayer || viewer.Kind == ViewerSpectator || viewer.Kind == ViewerReplay
	return err == nil && kindValid && (viewer.Kind == ViewerPlayer || viewer.SeatIndex == 0)
}

// ReplayAccessPolicy is an already-authorized platform scope; the game still enforces its reveal policy.
type ReplayAccessPolicy string

const (
	ReplayAccessParticipant ReplayAccessPolicy = "participant"
	ReplayAccessRoomMember  ReplayAccessPolicy = "room_member"
	ReplayAccessPublic      ReplayAccessPolicy = "public"
)

// Valid rejects a replay projection whose platform authorization scope was not explicitly selected.
func (policy ReplayAccessPolicy) Valid() bool {
	return policy == ReplayAccessParticipant || policy == ReplayAccessRoomMember || policy == ReplayAccessPublic
}

// Projection contains one viewer-safe view and the action identifiers currently offered to that viewer.
type Projection struct {
	View           Message
	AllowedActions []Identifier
}

// Valid enforces one safe view envelope and a bounded canonical action set without duplicates.
func (projection Projection) Valid() bool {
	if !projection.View.Valid() || len(projection.AllowedActions) > MaximumAllowedActions {
		return false
	}
	actions := make(map[Identifier]struct{}, len(projection.AllowedActions))
	for _, action := range projection.AllowedActions {
		if _, err := ParseIdentifier(string(action)); err != nil {
			return false
		}
		if _, duplicate := actions[action]; duplicate {
			return false
		}
		actions[action] = struct{}{}
	}
	return true
}

// EventProjection is a viewer-specific delta; it deliberately cannot carry authoritative Event values.
type EventProjection struct {
	Messages []Message
}

// Valid bounds delta fanout and validates every viewer-safe module-owned envelope.
func (projection EventProjection) Valid() bool {
	if len(projection.Messages) == 0 || len(projection.Messages) > MaximumTransitionEvents {
		return false
	}
	for _, message := range projection.Messages {
		if !message.Valid() {
			return false
		}
	}
	return true
}

// ServerGameModule is synchronous and IO-free; runtime-owned persistence, clocks, randomness, and routing stay outside.
type ServerGameModule interface {
	Manifest() Manifest
	Create(CreateRequest) (Transition, error)
	HandleCommand(Snapshot, CommandRequest) (Transition, error)
	HandleTimer(Snapshot, TimerRequest) (Transition, error)
	Project(Snapshot, Viewer) (Projection, error)
	ProjectReplay([]Event, Viewer, ReplayAccessPolicy) (Projection, error)
	Migrate(Snapshot, uint32, uint32) (Snapshot, error)
}

// SystemGameModule extends a registered module with runtime-originated commands such as participant revocation.
type SystemGameModule interface {
	HandleSystem(Snapshot, SystemRequest) (Transition, error)
}

// EventProjectingGameModule converts raw committed facts into one viewer-safe subscription delta.
type EventProjectingGameModule interface {
	ProjectEvents(Snapshot, []VersionedEvent, Viewer) (EventProjection, error)
}

// RuntimeServerGameModule is the complete contract required by the authoritative session runtime.
type RuntimeServerGameModule interface {
	ServerGameModule
	SystemGameModule
	EventProjectingGameModule
}
