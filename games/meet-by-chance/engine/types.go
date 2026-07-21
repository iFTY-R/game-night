package engine

import "github.com/iFTY-R/game-night/sdk/go/game/dice"

// Phase retains the complete protocol vocabulary; automatic phases are never persisted between transitions.
type Phase uint8

const (
	PhaseRolling Phase = iota + 1
	PhaseRevealing
	PhaseResolvingMatch
	PhaseTargetDecision
	PhaseTargetRolling
	PhaseRoundSettled
	PhaseFinished
)

const (
	FinishHostRequested            = "host_requested"
	FinishInsufficientParticipants = "insufficient_participants"
	FinishPlatformCancelled        = "platform_cancelled"
)

// PlayerState stores a stable seat, public current hand, activity, cumulative penalty, and current-round target history.
type PlayerState struct {
	UserID                      string
	SeatIndex                   uint32
	Active                      bool
	IncludedInCurrentResolution bool
	TargetedThisRound           bool
	PenaltyTicks                dice.Ticks
	Hand                        Hand
}

// MatchResolution records one bounded automatic batch for reconnect-safe current-round presentation.
type MatchResolution struct {
	Batch  uint32
	Groups []MatchGroup
	Capped bool
}

// RoundSettlement retains the final public hands and counters after a completed round.
type RoundSettlement struct {
	Round                uint32
	TargetUserID         string
	Reason               string
	TargetRerollCount    uint32
	TargetStreak         uint32
	MatchResolutionCount uint32
	Players              []PlayerState
}

// State is the authoritative pure engine state. All hands in a live state have already been revealed.
type State struct {
	SchemaVersion uint32
	Phase         Phase
	Round         uint32
	// HostUserID is the trusted room owner captured at creation. Revocation changes activity, not ownership identity.
	HostUserID               string
	Config                   Config
	Players                  []PlayerState
	TargetUserID             string
	TargetRerollCount        uint32
	TargetStreak             uint32
	MatchResolutionCount     uint32
	ActionDeadlineUnixMillis int64
	MatchHistory             []MatchResolution
	LastSettlement           RoundSettlement
	RoundHistory             []RoundSettlement
	FinishReason             string
}

// ActionTimer binds a timeout to the exact target decision it may settle.
type ActionTimer struct {
	Round                uint32
	TargetUserID         string
	TargetRerollCount    uint32
	TargetStreak         uint32
	MatchResolutionCount uint32
	DeadlineUnixMillis   int64
}

// EventKind identifies one ordered public fact used by replay and live deltas.
type EventKind string

const (
	EventRoundStarted       EventKind = "round.started"
	EventDiceRevealed       EventKind = "dice.revealed"
	EventHandClassified     EventKind = "hand.classified"
	EventMatchResolved      EventKind = "match.resolved"
	EventTargetSelected     EventKind = "target.selected"
	EventTargetRerolled     EventKind = "target.rerolled"
	EventPenaltyRecorded    EventKind = "penalty.recorded"
	EventRoundSettled       EventKind = "round.settled"
	EventParticipantRevoked EventKind = "participant.revoked"
	EventSessionFinished    EventKind = "session.finished"
)

// Event is replay-safe public data. Initialization fields occur only on round one.
type Event struct {
	Kind                    EventKind
	Round                   uint32
	Batch                   uint32
	InitialConfig           *Config
	InitialParticipants     []Participant
	UserID                  string
	PreviousUserID          string
	UserIDs                 []string
	WeakestUserID           string
	MatchGroups             []MatchGroup
	Players                 []PlayerState
	Hand                    Hand
	MatchKind               MatchKind
	PenaltyTicks            dice.Ticks
	PenaltyBeforeTicks      dice.Ticks
	PenaltyAfterTicks       dice.Ticks
	TargetRerollCount       uint32
	TargetStreak            uint32
	MatchResolutionCount    uint32
	FirstSelectionThisRound bool
	Capped                  bool
	RoundCancelled          bool
	NextRound               uint32
	ActivePlayerCount       uint32
	Reason                  string
	OperatorUserID          string
	Settlement              RoundSettlement
}

// Clone returns an independent state for failed transitions and persistence boundaries.
func (state State) Clone() State {
	state.Players = clonePlayers(state.Players)
	state.MatchHistory = cloneMatchHistory(state.MatchHistory)
	state.LastSettlement = cloneSettlement(state.LastSettlement)
	state.RoundHistory = cloneSettlements(state.RoundHistory)
	return state
}

// Clone deep-copies all event-owned collections and optional initialization config.
func (event Event) Clone() Event {
	if event.InitialConfig != nil {
		config := *event.InitialConfig
		event.InitialConfig = &config
	}
	event.InitialParticipants = append([]Participant(nil), event.InitialParticipants...)
	event.UserIDs = append([]string(nil), event.UserIDs...)
	event.MatchGroups = cloneGroups(event.MatchGroups)
	event.Players = clonePlayers(event.Players)
	event.Settlement = cloneSettlement(event.Settlement)
	return event
}

func clonePlayers(players []PlayerState) []PlayerState { return append([]PlayerState(nil), players...) }

func cloneSettlement(value RoundSettlement) RoundSettlement {
	value.Players = clonePlayers(value.Players)
	return value
}

func cloneSettlements(values []RoundSettlement) []RoundSettlement {
	clones := make([]RoundSettlement, len(values))
	for index, value := range values {
		clones[index] = cloneSettlement(value)
	}
	return clones
}

func cloneMatchHistory(values []MatchResolution) []MatchResolution {
	clones := make([]MatchResolution, len(values))
	for index, value := range values {
		clones[index] = MatchResolution{Batch: value.Batch, Capped: value.Capped, Groups: cloneGroups(value.Groups)}
	}
	return clones
}

func cloneGroups(groups []MatchGroup) []MatchGroup {
	clones := make([]MatchGroup, len(groups))
	for index, group := range groups {
		clones[index] = MatchGroup{Kind: group.Kind, UserIDs: append([]string(nil), group.UserIDs...), WeakestUserID: group.WeakestUserID}
	}
	return clones
}
