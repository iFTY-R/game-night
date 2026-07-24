package engine

import (
	"cmp"
	"slices"
)

// Phase is the authoritative lifecycle frozen by the rules spec.
type Phase uint8

const (
	PhaseDealing Phase = iota + 1
	PhaseRoundOneSelecting
	PhaseRoundOneResult
	PhaseRoundTwoSelecting
	PhaseRoundTwoResult
	PhaseRoundThreeResult
	PhaseFinalResult
	PhaseFinished
)

const (
	FinishNormalCompleted          = "normal_completed"
	FinishHostRequested            = "host_requested"
	FinishInsufficientParticipants = "insufficient_participants"
)

type HandClass uint8

const (
	HandSingle HandClass = iota + 1
	HandPair
	HandStraight
	HandFlush
	HandStraightFlush
	HandTrips
)

// Participant is the room-owned immutable seat snapshot handed to the game module.
type Participant struct {
	UserID    string
	SeatIndex uint32
}

// PendingSelection retains the still-private current-round choice.
type PendingSelection struct {
	Round         uint32
	CardIDs       []CardID
	AutoSubmitted bool
}

// WildcardSubstitution records one deterministic joker-to-normal-card mapping.
type WildcardSubstitution struct {
	WildcardCardID    CardID
	SubstitutedCardID CardID
	CardOrdinal       uint8
}

// WildcardResolution records the canonical third-round joker mapping output.
type WildcardResolution struct {
	Substitutions []WildcardSubstitution
	ResolvedCards []CardID
}

type RoundOneEvaluation struct {
	CardID       CardID
	RankStrength uint8
	SuitStrength uint8
}

type RoundTwoEvaluation struct {
	TotalHalfPoints   uint8
	Busted            bool
	RankStrengthsDesc [2]uint8
}

type RoundThreeEvaluation struct {
	HandClass     HandClass
	CompareValues []uint8
	ResolvedCards []CardID
}

// PlayerReveal is the public round-level reveal payload.
type PlayerReveal struct {
	UserID               string
	SeatIndex            uint32
	Active               bool
	CardIDs              []CardID
	AutoSubmitted        bool
	AwardedPoints        uint8
	RoundOneEvaluation   RoundOneEvaluation
	RoundTwoEvaluation   RoundTwoEvaluation
	RoundThreeEvaluation RoundThreeEvaluation
	WildcardResolution   WildcardResolution
}

// RoundSummary records one settled public round.
type RoundSummary struct {
	Round         uint32
	WinnerUserIDs []string
	Reveals       []PlayerReveal
	AllBusted     bool
}

// FinalStanding keeps the final ranking input stable for replay and public view.
type FinalStanding struct {
	UserID        string
	SeatIndex     uint32
	Active        bool
	TotalPoints   uint8
	WonRoundOne   bool
	WonRoundTwo   bool
	WonRoundThree bool
	Winner        bool
	Rank          uint32
}

// FinalSummary excludes inactive players from winners and standings.
type FinalSummary struct {
	Standings     []FinalStanding
	WinnerUserIDs []string
}

// PlayerState is the complete authoritative per-seat state.
type PlayerState struct {
	UserID                       string
	SeatIndex                    uint32
	Active                       bool
	InitialHand                  []CardID
	RemainingHand                []CardID
	PendingSelection             PendingSelection
	RoundOneCards                []CardID
	RoundTwoCards                []CardID
	RoundThreeCards              []CardID
	RoundThreeWildcardResolution WildcardResolution
	RoundOnePoints               uint8
	RoundTwoPoints               uint8
	RoundThreePoints             uint8
	TotalPoints                  uint8
	WonRoundOne                  bool
	WonRoundTwo                  bool
	WonRoundThree                bool
	FinalWinner                  bool
	FinalRank                    uint32
}

// State is the full deterministic snapshot owned by the game engine.
type State struct {
	SchemaVersion           uint32
	Phase                   Phase
	CurrentRound            uint32
	PhaseDeadlineUnixMillis int64
	PhaseGeneration         uint32
	Config                  Config
	HostUserID              string
	Players                 []PlayerState
	RoundHistory            []RoundSummary
	FinalSummary            FinalSummary
	FinishReason            string
}

// Timer binds a timeout to one exact phase generation.
type Timer struct {
	Phase              Phase
	Round              uint32
	DeadlineUnixMillis int64
	PhaseGeneration    uint32
}

// EventKind keeps the ordered fact stream stable across projections.
type EventKind string

const (
	EventSessionStarted         EventKind = "session.started"
	EventCardsDealt             EventKind = "cards.dealt"
	EventPhaseStarted           EventKind = "phase.started"
	EventSelectionSubmitted     EventKind = "selection.submitted"
	EventSelectionAutoSubmitted EventKind = "selection.auto_submitted"
	EventRoundRevealed          EventKind = "round.revealed"
	EventRoundSettled           EventKind = "round.settled"
	EventWildcardResolved       EventKind = "wildcard.resolved"
	EventParticipantRevoked     EventKind = "participant.revoked"
	EventSessionFinished        EventKind = "session.finished"
)

// Event is the engine's stable public/private fact payload.
type Event struct {
	Kind                    EventKind
	Config                  *Config
	Participants            []Participant
	HostUserID              string
	Deals                   []PlayerDeal
	Phase                   Phase
	Round                   uint32
	DeadlineUnixMillis      int64
	PhaseGeneration         uint32
	UserID                  string
	AutoSubmitted           bool
	Summary                 RoundSummary
	WildcardResolution      WildcardResolution
	OperatorUserID          string
	FinishReason            string
	RemovedPendingSelection bool
	ActivePlayerCount       uint32
}

// PlayerDeal is the internal cards.dealt payload retained for replay reconstruction.
type PlayerDeal struct {
	UserID      string
	InitialHand []CardID
}

// Clone protects shared runtime callers from mutating engine-owned slices.
func (state State) Clone() State {
	state.Players = clonePlayers(state.Players)
	state.RoundHistory = cloneRoundHistory(state.RoundHistory)
	state.FinalSummary = cloneFinalSummary(state.FinalSummary)
	return state
}

func (event Event) Clone() Event {
	if event.Config != nil {
		config := *event.Config
		event.Config = &config
	}
	event.Participants = append([]Participant(nil), event.Participants...)
	event.Deals = cloneDeals(event.Deals)
	event.Summary = cloneRoundSummary(event.Summary)
	event.WildcardResolution = cloneWildcardResolution(event.WildcardResolution)
	return event
}

func clonePlayers(players []PlayerState) []PlayerState {
	clones := make([]PlayerState, len(players))
	for index, player := range players {
		clones[index] = clonePlayer(player)
	}
	return clones
}

func clonePlayer(player PlayerState) PlayerState {
	player.InitialHand = append([]CardID(nil), player.InitialHand...)
	player.RemainingHand = append([]CardID(nil), player.RemainingHand...)
	player.PendingSelection = clonePendingSelection(player.PendingSelection)
	player.RoundOneCards = append([]CardID(nil), player.RoundOneCards...)
	player.RoundTwoCards = append([]CardID(nil), player.RoundTwoCards...)
	player.RoundThreeCards = append([]CardID(nil), player.RoundThreeCards...)
	player.RoundThreeWildcardResolution = cloneWildcardResolution(player.RoundThreeWildcardResolution)
	return player
}

func clonePendingSelection(value PendingSelection) PendingSelection {
	value.CardIDs = append([]CardID(nil), value.CardIDs...)
	return value
}

func cloneWildcardResolution(value WildcardResolution) WildcardResolution {
	value.Substitutions = append([]WildcardSubstitution(nil), value.Substitutions...)
	value.ResolvedCards = append([]CardID(nil), value.ResolvedCards...)
	return value
}

func clonePlayerReveal(value PlayerReveal) PlayerReveal {
	value.CardIDs = append([]CardID(nil), value.CardIDs...)
	value.RoundThreeEvaluation.CompareValues = append([]uint8(nil), value.RoundThreeEvaluation.CompareValues...)
	value.RoundThreeEvaluation.ResolvedCards = append([]CardID(nil), value.RoundThreeEvaluation.ResolvedCards...)
	value.WildcardResolution = cloneWildcardResolution(value.WildcardResolution)
	return value
}

func cloneRoundSummary(value RoundSummary) RoundSummary {
	value.WinnerUserIDs = append([]string(nil), value.WinnerUserIDs...)
	clones := make([]PlayerReveal, len(value.Reveals))
	for index, reveal := range value.Reveals {
		clones[index] = clonePlayerReveal(reveal)
	}
	value.Reveals = clones
	return value
}

func cloneRoundHistory(values []RoundSummary) []RoundSummary {
	clones := make([]RoundSummary, len(values))
	for index, value := range values {
		clones[index] = cloneRoundSummary(value)
	}
	return clones
}

func cloneFinalSummary(value FinalSummary) FinalSummary {
	value.Standings = append([]FinalStanding(nil), value.Standings...)
	value.WinnerUserIDs = append([]string(nil), value.WinnerUserIDs...)
	return value
}

func cloneDeals(values []PlayerDeal) []PlayerDeal {
	clones := make([]PlayerDeal, len(values))
	for index, value := range values {
		clones[index] = PlayerDeal{UserID: value.UserID, InitialHand: append([]CardID(nil), value.InitialHand...)}
	}
	return clones
}

func sortParticipants(participants []Participant) {
	slices.SortFunc(participants, func(left, right Participant) int {
		if diff := cmp.Compare(left.SeatIndex, right.SeatIndex); diff != 0 {
			return diff
		}
		return cmp.Compare(left.UserID, right.UserID)
	})
}
