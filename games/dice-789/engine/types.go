package engine

import "github.com/iFTY-R/game-night/sdk/go/game/dice"

// Phase mirrors the protocol vocabulary. turn_settled is an event boundary and is not persisted.
type Phase uint8

const (
	PhaseAwaitingRoll Phase = iota + 1
	PhaseResultPending
	PhaseAwaitingAdd
	PhaseAwaitingTarget
	PhaseAwaitingContinue
	PhaseTurnSettled
	PhaseFinished
)

// Direction controls stable-seat traversal.
type Direction uint32

const (
	DirectionClockwise Direction = iota + 1
	DirectionCounterClockwise
)

// ResultKind is the single highest-priority rule selected for the current roll.
type ResultKind string

const (
	ResultNone         ResultKind = "none"
	ResultSeven        ResultKind = "seven"
	ResultEight        ResultKind = "eight"
	ResultNine         ResultKind = "nine"
	ResultDoubleOne    ResultKind = "double_one"
	ResultDoubleFour   ResultKind = "double_four"
	ResultDoubleSix    ResultKind = "double_six"
	ResultOrdinaryPair ResultKind = "ordinary_pair"
	ResultOther        ResultKind = "other"
	ResultDropped      ResultKind = "dropped"
	ResultRollTimeout  ResultKind = "roll_timeout"
	ResultCancelled    ResultKind = "cancelled"
)

const (
	// Finish reasons distinguish an authorized host decision, platform cancellation,
	// and the engine's automatic insufficient-player terminal condition.
	FinishHostRequested            = "host_requested"
	FinishInsufficientParticipants = "insufficient_participants"
	FinishPlatformCancelled        = "platform_cancelled"
)

// Participant is the frozen room identity and stable seat used by the engine.
type Participant struct {
	UserID    string
	SeatIndex uint32
}

// PlayerState stores active status and cumulative abstract penalty.
type PlayerState struct {
	UserID       string
	SeatIndex    uint32
	Active       bool
	PenaltyTicks dice.Ticks
}

// PoolLayer is one visual and capacity-bounded public pool layer.
type PoolLayer struct{ Ticks dice.Ticks }

// TurnSettlement is retained in State for reconnect projections and replay-safe summaries.
type TurnSettlement struct {
	Turn            uint32
	SourceUserID    string
	DieOne          uint32
	DieTwo          uint32
	Sum             uint32
	Result          ResultKind
	TargetUserID    string
	PoolBeforeTicks dice.Ticks
	PoolAfterTicks  dice.Ticks
	PoolBefore      []PoolLayer
	PoolAfter       []PoolLayer
	EffectTicks     dice.Ticks
	PenaltyUserID   string
	PenaltyTicks    dice.Ticks
	DirectionBefore Direction
	DirectionAfter  Direction
	NextUserID      string
	Reason          string
	AuditRef        string
	DropReason      string
}

// State is authoritative engine state and intentionally contains no protobuf or runtime types.
type State struct {
	SchemaVersion            uint32
	Phase                    Phase
	Turn                     uint32
	HostUserID               string
	Players                  []PlayerState
	CurrentUserID            string
	SourceUserID             string
	TargetUserID             string
	Direction                Direction
	Pool                     []PoolLayer
	TotalPoolTicks           dice.Ticks
	DieOne                   uint32
	DieTwo                   uint32
	Sum                      uint32
	ActionDeadlineUnixMillis int64
	Config                   Config
	PendingResult            ResultKind
	PendingPoolBeforeTicks   dice.Ticks
	PendingDirectionBefore   Direction
	LastSettlement           TurnSettlement
	FinishReason             string
}

// ActionTimer binds a persisted timeout to the complete pending-effect identity.
type ActionTimer struct {
	Turn               uint32
	Phase              Phase
	CurrentUserID      string
	SourceUserID       string
	TargetUserID       string
	PendingResult      ResultKind
	DeadlineUnixMillis int64
}

// EventKind identifies an ordered fact sufficient for module replay.
type EventKind string

const (
	EventTurnStarted        EventKind = "turn.started"
	EventDiceRolled         EventKind = "dice.rolled"
	EventEffectSelected     EventKind = "effect.selected"
	EventTargetSelected     EventKind = "target.selected"
	EventPoolChanged        EventKind = "pot.changed"
	EventPenaltyRecorded    EventKind = "penalty.recorded"
	EventDirectionChanged   EventKind = "direction.changed"
	EventTurnSettled        EventKind = "turn.settled"
	EventTurnDropped        EventKind = "turn.dropped_reported"
	EventParticipantRevoked EventKind = "participant.revoked"
	EventSessionFinished    EventKind = "session.finished"
)

// Event is a replay-safe fact. No hidden or runtime-owned data is included.
type Event struct {
	Kind EventKind
	Turn uint32
	// InitialConfig and InitialParticipants are populated only on turn one so
	// replay can reconstruct the public frozen session without a snapshot.
	InitialConfig         *Config
	InitialParticipants   []Participant
	InitialPool           []PoolLayer
	InitialTotalPoolTicks dice.Ticks
	UserID                string
	SourceUserID          string
	TargetUserID          string
	DieOne                uint32
	DieTwo                uint32
	Sum                   uint32
	Result                ResultKind
	// PhaseBefore/After preserve the public transition boundary for effects and revocations.
	PhaseBefore     Phase
	PhaseAfter      Phase
	Direction       Direction
	DirectionBefore Direction
	PoolBeforeTicks dice.Ticks
	PoolAfterTicks  dice.Ticks
	PoolBefore      []PoolLayer
	PoolAfter       []PoolLayer
	EffectTicks     dice.Ticks
	PenaltyTicks    dice.Ticks
	// Penalty totals make the event independently auditable without the final snapshot.
	PenaltyBeforeTicks dice.Ticks
	PenaltyAfterTicks  dice.Ticks
	NextUserID         string
	Reason             string
	AuditRef           string
	// The remaining fields retain trusted finish and participant-revocation context.
	OperatorUserID          string
	PendingEffectCancelled  bool
	TargetSelectionReopened bool
}

func cloneSettlement(value TurnSettlement) TurnSettlement {
	value.PoolBefore = append([]PoolLayer(nil), value.PoolBefore...)
	value.PoolAfter = append([]PoolLayer(nil), value.PoolAfter...)
	return value
}

// Clone returns an independent state for failed transitions and runtime persistence.
func (state State) Clone() State {
	state.Players = append([]PlayerState(nil), state.Players...)
	state.Pool = append([]PoolLayer(nil), state.Pool...)
	state.LastSettlement = cloneSettlement(state.LastSettlement)
	return state
}

// Clone copies event values with no retained caller-owned slices (events are scalar by design).
func (event Event) Clone() Event {
	event.PoolBefore = append([]PoolLayer(nil), event.PoolBefore...)
	event.PoolAfter = append([]PoolLayer(nil), event.PoolAfter...)
	if event.InitialConfig != nil {
		config := *event.InitialConfig
		event.InitialConfig = &config
	}
	event.InitialParticipants = append([]Participant(nil), event.InitialParticipants...)
	event.InitialPool = append([]PoolLayer(nil), event.InitialPool...)
	return event
}
