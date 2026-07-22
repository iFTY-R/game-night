package engine

import "github.com/iFTY-R/game-night/sdk/go/game/dice"

const (
	// CurrentSchemaVersion is persisted inside every authoritative state.
	CurrentSchemaVersion uint32 = 1
	// MinimumPlayers and MaximumPlayers are the hard session participant bounds.
	MinimumPlayers = 2
	MaximumPlayers = 8
	// MinimumDicePerPlayer and MaximumDicePerPlayer bound the frozen per-player roll size.
	MinimumDicePerPlayer uint32 = 3
	MaximumDicePerPlayer uint32 = 6
)

// Phase identifies whether gameplay accepts bids or has reached a terminal state.
type Phase uint8

const (
	// Transient phases remain defined for protocol vocabulary; completed engine
	// calls persist only PhaseBidding or PhaseFinished.
	PhaseRolling Phase = iota + 1
	PhaseBidding
	PhaseRevealing
	PhaseRoundSettled
	PhaseFinished
)

// BidMode controls whether ones may act as wildcards for a non-one face.
type BidMode uint8

const (
	// BidModeFlying applies configured ones-wild behavior; BidModeStrict counts exact faces only.
	BidModeFlying BidMode = iota + 1
	BidModeStrict
)

// SettlementReason records why one round assigned its penalty.
type SettlementReason string

const (
	SettlementOpened  SettlementReason = "opened"
	SettlementTimeout SettlementReason = "timeout"
)

const (
	// Finish reasons remain stable replay and client-facing machine values.
	FinishHostRequested            = "host_requested"
	FinishInsufficientParticipants = "insufficient_participants"
)

// Config is frozen when the session starts; ticks use the shared four-per-unit scale.
type Config struct {
	DicePerPlayer        uint32
	OnesWild             bool
	StrictEnabled        bool
	FlyingEnabled        bool
	FirstBidMinimum      uint32
	PenaltyTicks         dice.Ticks
	ActionTimeoutSeconds uint32
}

// Bid is one quantity-first claim in either flying or strict mode.
type Bid struct {
	Quantity uint32
	Face     uint32
	Mode     BidMode
}

// Participant freezes a canonical user identity to a stable room seat.
type Participant struct {
	UserID    string
	SeatIndex uint32
}

// PlayerState tracks activity and cumulative abstract penalty ticks.
type PlayerState struct {
	UserID       string
	SeatIndex    uint32
	Active       bool
	PenaltyTicks dice.Ticks
}

// PrivateRoll belongs to exactly one active player until rules reveal it.
type PrivateRoll struct {
	UserID string
	Faces  []dice.Face
}

// Settlement records only the most recently completed round.
type Settlement struct {
	Round          uint32
	LoserUserID    string
	PenaltyTicks   dice.Ticks
	ActualQuantity uint32
	Reason         SettlementReason
	OpenerUserID   string
	Bid            *Bid
	RevealedDice   []PrivateRoll
}

// State is authoritative and must only cross the runtime boundary through an encoded SDK Snapshot.
type State struct {
	SchemaVersion      uint32
	Phase              Phase
	Round              uint32
	Config             Config
	Players            []PlayerState
	FirstActorUserID   string
	CurrentActorUserID string
	LastBidderUserID   string
	CurrentBid         *Bid
	// ActionDeadlineUnixMillis is zero when timers are disabled.
	ActionDeadlineUnixMillis int64
	PrivateDice              []PrivateRoll
	LastSettlement           Settlement
	FinishReason             string
}

// ActionTimer binds one firing to the exact round, actor, and deadline.
type ActionTimer struct {
	Round  uint32
	UserID string
	// DeadlineUnixMillis is the exact persisted deadline token for this turn.
	DeadlineUnixMillis int64
}

// EventKind identifies one ordered persisted fact consumed by replay projection.
type EventKind string

const (
	// Event kinds never carry private dice except EventDiceRevealed.
	EventRoundStarted       EventKind = "round.started"
	EventBidPlaced          EventKind = "bid.placed"
	EventDiceRevealed       EventKind = "dice.revealed"
	EventRoundSettled       EventKind = "round.settled"
	EventParticipantRevoked EventKind = "participant.revoked"
	EventSessionFinished    EventKind = "session.finished"
)

// Event is an engine fact. Dice must be populated only for EventDiceRevealed.
type Event struct {
	Kind       EventKind
	Round      uint32
	UserID     string
	FirstActor string
	// Players is populated only on the first round start so replay can restore every frozen seat.
	Players        []Participant
	Bid            *Bid
	Dice           []PrivateRoll
	LoserUserID    string
	PenaltyTicks   dice.Ticks
	NextRound      uint32
	ActualQuantity uint32
	Reason         string
	OpenerUserID   string
}

func cloneBid(value *Bid) *Bid {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneRolls(values []PrivateRoll) []PrivateRoll {
	clones := make([]PrivateRoll, len(values))
	for index, value := range values {
		clones[index] = PrivateRoll{UserID: value.UserID, Faces: append([]dice.Face(nil), value.Faces...)}
	}
	return clones
}

// Clone prevents failed commands and caller-owned projections from mutating authoritative state.
func (state State) Clone() State {
	state.Players = append([]PlayerState(nil), state.Players...)
	state.CurrentBid = cloneBid(state.CurrentBid)
	state.PrivateDice = cloneRolls(state.PrivateDice)
	state.LastSettlement.Bid = cloneBid(state.LastSettlement.Bid)
	state.LastSettlement.RevealedDice = cloneRolls(state.LastSettlement.RevealedDice)
	return state
}
