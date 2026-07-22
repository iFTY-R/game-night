package engine

import (
	"math"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

// NewState validates and freezes participants/config before deterministically starting round one.
func NewState(config Config, participants []Participant, startingSeat uint32, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	canonical, err := canonicalParticipants(participants)
	if err != nil {
		return State{}, nil, err
	}
	if err := config.Validate(len(canonical)); err != nil {
		return State{}, nil, err
	}
	if !validUnixMillis(nowUnixMillis) {
		return State{}, nil, ruleError(CodeInvalidState, "deterministic time is required")
	}
	first := ""
	players := make([]PlayerState, len(canonical))
	for index, participant := range canonical {
		players[index] = PlayerState{UserID: participant.UserID, SeatIndex: participant.SeatIndex, Active: true}
		if participant.SeatIndex == startingSeat {
			first = participant.UserID
		}
	}
	if first == "" {
		return State{}, nil, ruleError(CodeInvalidParticipants, "starting seat is not frozen in the session")
	}
	state := State{SchemaVersion: CurrentSchemaVersion, Phase: PhaseRolling, Round: 1, Config: config, Players: players}
	state, event, err := startRound(state, 1, first, nowUnixMillis, seed)
	if err != nil {
		return State{}, nil, err
	}
	event.Players = append([]Participant(nil), canonical...)
	return state, []Event{event}, nil
}

// PlaceBid validates turn ownership and advances to the next active stable seat.
func PlaceBid(state State, actor string, bid Bid, nowUnixMillis int64) (State, []Event, error) {
	nowUnixMillis, err := validateAction(state, actor, nowUnixMillis)
	if err != nil {
		return State{}, nil, err
	}
	if err := ValidateBid(state.Config, state.CurrentBid, bid); err != nil {
		return State{}, nil, err
	}
	next := state.Clone()
	nextActor, err := nextActiveUser(next, actor)
	if err != nil {
		return State{}, nil, err
	}
	next.CurrentBid = cloneBid(&bid)
	next.LastBidderUserID = actor
	next.CurrentActorUserID = nextActor
	actionDeadline, err := deadlineUnixMillis(next.Config, nowUnixMillis)
	if err != nil {
		return State{}, nil, err
	}
	next.ActionDeadlineUnixMillis = actionDeadline
	return next, []Event{{Kind: EventBidPlaced, Round: state.Round, UserID: actor, Bid: cloneBid(&bid)}}, nil
}

// OpenDice reveals the current round, assigns exactly one loser, and immediately starts the next round.
func OpenDice(state State, actor string, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	nowUnixMillis, err := validateAction(state, actor, nowUnixMillis)
	if err != nil {
		return State{}, nil, err
	}
	if state.CurrentBid == nil || state.LastBidderUserID == "" {
		return State{}, nil, ruleError(CodeBidRequired, "open requires a previous bid")
	}
	actual := CountBid(state, *state.CurrentBid)
	loser := state.LastBidderUserID
	if actual >= state.CurrentBid.Quantity {
		loser = actor
	}
	return settleAndRestart(state, loser, SettlementOpened, actor, actual, true, nowUnixMillis, seed)
}

// HandleTimeout settles only the exact persisted timer and never reveals an unchallenged round.
func HandleTimeout(state State, timer ActionTimer, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session no longer accepts timers")
	}
	if !validUnixMillis(nowUnixMillis) {
		return State{}, nil, ruleError(CodeInvalidState, "deterministic time is required")
	}
	if timer.Round != state.Round || timer.UserID != state.CurrentActorUserID || timer.DeadlineUnixMillis == 0 ||
		timer.DeadlineUnixMillis != state.ActionDeadlineUnixMillis {
		return State{}, nil, ruleError(CodeTimerMismatch, "timer does not match the current turn")
	}
	if nowUnixMillis < state.ActionDeadlineUnixMillis {
		return State{}, nil, ruleError(CodeTimerNotDue, "timer fired before its persisted deadline")
	}
	return settleAndRestart(state, state.CurrentActorUserID, SettlementTimeout, "", 0, false, nowUnixMillis, seed)
}

// RevokeParticipant removes a frozen player from future calculations without reusing their seat.
func RevokeParticipant(state State, userID string, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session no longer accepts participant changes")
	}
	if !validUnixMillis(nowUnixMillis) {
		return State{}, nil, ruleError(CodeInvalidState, "deterministic time is required")
	}
	next := state.Clone()
	index := playerIndex(next.Players, userID)
	if index < 0 || !next.Players[index].Active {
		return State{}, nil, ruleError(CodeParticipantInactive, "participant is absent or already inactive")
	}
	wasCurrent := next.CurrentActorUserID == userID
	next.Players[index].Active = false
	next.PrivateDice = removeRoll(next.PrivateDice, userID)
	events := []Event{{Kind: EventParticipantRevoked, Round: state.Round, UserID: userID}}
	if activeCount(next.Players) < MinimumPlayers {
		finished, finishEvent := finishState(next, FinishInsufficientParticipants)
		return finished, append(events, finishEvent), nil
	}
	if !wasCurrent {
		return next, events, nil
	}
	if next.Round == math.MaxUint32 {
		return State{}, nil, ruleError(CodeRoundOverflow, "round counter cannot advance")
	}
	first, err := nextActiveUser(next, userID)
	if err != nil {
		return State{}, nil, err
	}
	next.LastSettlement = Settlement{}
	next, started, err := startRound(next, next.Round+1, first, nowUnixMillis, seed)
	if err != nil {
		return State{}, nil, err
	}
	return next, append(events, started), nil
}

// Finish terminates the session without revealing current private dice.
func Finish(state State, reason string) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session is already finished")
	}
	if reason == "" {
		reason = FinishHostRequested
	}
	finished, event := finishState(state.Clone(), reason)
	return finished, []Event{event}, nil
}

// CurrentTimer returns the complete persisted timer intent payload for the current actor.
func CurrentTimer(state State) *ActionTimer {
	if state.Phase != PhaseBidding || state.ActionDeadlineUnixMillis == 0 {
		return nil
	}
	return &ActionTimer{Round: state.Round, UserID: state.CurrentActorUserID, DeadlineUnixMillis: state.ActionDeadlineUnixMillis}
}

// Validate detects malformed decoded snapshots before any rule transition or projection runs.
func (state State) Validate() error {
	if state.SchemaVersion != CurrentSchemaVersion || state.Round == 0 ||
		(state.Phase != PhaseBidding && state.Phase != PhaseFinished) || len(state.Players) < MinimumPlayers || len(state.Players) > MaximumPlayers {
		return ruleError(CodeInvalidState, "state header is invalid")
	}
	participants := make([]Participant, len(state.Players))
	for index, player := range state.Players {
		participants[index] = Participant{UserID: player.UserID, SeatIndex: player.SeatIndex}
		if index > 0 && state.Players[index-1].SeatIndex >= player.SeatIndex {
			return ruleError(CodeInvalidState, "players are not in canonical seat order")
		}
	}
	if err := ValidateParticipants(participants); err != nil || state.Config.Validate(len(state.Players)) != nil {
		return ruleError(CodeInvalidState, "frozen participants or configuration is invalid")
	}
	if err := validateSettlement(state); err != nil {
		return err
	}
	if playerIndex(state.Players, state.FirstActorUserID) < 0 {
		return ruleError(CodeInvalidState, "first actor is not frozen")
	}
	if state.CurrentBid == nil && state.LastBidderUserID != "" || state.CurrentBid != nil && state.LastBidderUserID == "" {
		return ruleError(CodeInvalidState, "current bid and bidder disagree")
	}
	if state.LastBidderUserID != "" && playerIndex(state.Players, state.LastBidderUserID) < 0 {
		return ruleError(CodeInvalidState, "last bidder is not frozen")
	}
	if state.CurrentBid != nil && (state.CurrentBid.Quantity == 0 || state.CurrentBid.Face < 1 || state.CurrentBid.Face > 6 ||
		(state.CurrentBid.Mode != BidModeFlying && state.CurrentBid.Mode != BidModeStrict) ||
		state.CurrentBid.Mode == BidModeStrict && !state.Config.StrictEnabled ||
		state.CurrentBid.Face == 1 && state.CurrentBid.Mode != BidModeStrict) {
		return ruleError(CodeInvalidState, "current bid is malformed")
	}
	if state.Phase == PhaseFinished {
		if state.FinishReason == "" || state.CurrentActorUserID != "" || state.ActionDeadlineUnixMillis != 0 || len(state.PrivateDice) != 0 {
			return ruleError(CodeInvalidState, "finished state retains live turn data")
		}
		return nil
	}
	if state.FinishReason != "" {
		return ruleError(CodeInvalidState, "bidding state carries a finish reason")
	}
	if activeCount(state.Players) < MinimumPlayers {
		return ruleError(CodeInvalidState, "bidding state has insufficient active players")
	}
	actor := playerIndex(state.Players, state.CurrentActorUserID)
	if actor < 0 || !state.Players[actor].Active {
		return ruleError(CodeInvalidState, "current actor is not active")
	}
	if state.Config.ActionTimeoutSeconds == 0 && state.ActionDeadlineUnixMillis != 0 ||
		state.Config.ActionTimeoutSeconds != 0 && state.ActionDeadlineUnixMillis <= 0 {
		return ruleError(CodeInvalidState, "deadline does not match timer configuration")
	}
	active := activeCount(state.Players)
	if len(state.PrivateDice) != active {
		return ruleError(CodeInvalidState, "private rolls do not match active players")
	}
	seen := make(map[string]struct{}, len(state.PrivateDice))
	for _, roll := range state.PrivateDice {
		player := playerIndex(state.Players, roll.UserID)
		if player < 0 || !state.Players[player].Active || len(roll.Faces) != int(state.Config.DicePerPlayer) {
			return ruleError(CodeInvalidState, "private roll ownership or size is invalid")
		}
		if _, duplicate := seen[roll.UserID]; duplicate {
			return ruleError(CodeInvalidState, "private roll is duplicated")
		}
		seen[roll.UserID] = struct{}{}
		for _, face := range roll.Faces {
			if !face.Valid() {
				return ruleError(CodeInvalidState, "private roll contains an invalid face")
			}
		}
	}
	return nil
}

func validateSettlement(state State) error {
	settlement := state.LastSettlement
	if settlement.Round == 0 {
		if settlement.LoserUserID != "" || settlement.PenaltyTicks != 0 || settlement.ActualQuantity != 0 ||
			settlement.Reason != "" || settlement.OpenerUserID != "" || settlement.Bid != nil || len(settlement.RevealedDice) != 0 {
			return ruleError(CodeInvalidState, "empty settlement retains result data")
		}
		return nil
	}
	if settlement.Round+1 != state.Round || settlement.LoserUserID == "" ||
		settlement.PenaltyTicks != state.Config.PenaltyTicks ||
		(settlement.Reason != SettlementOpened && settlement.Reason != SettlementTimeout) ||
		settlement.Reason == SettlementOpened && (settlement.Bid == nil || settlement.OpenerUserID == "" || len(settlement.RevealedDice) == 0) ||
		settlement.Reason == SettlementTimeout && (settlement.OpenerUserID != "" || settlement.ActualQuantity != 0 || len(settlement.RevealedDice) != 0) {
		return ruleError(CodeInvalidState, "last settlement is malformed")
	}
	if playerIndex(state.Players, settlement.LoserUserID) < 0 {
		return ruleError(CodeInvalidState, "last settlement loser is not frozen")
	}
	if settlement.OpenerUserID != "" && playerIndex(state.Players, settlement.OpenerUserID) < 0 {
		return ruleError(CodeInvalidState, "last settlement opener is not frozen")
	}
	if settlement.Bid != nil && (settlement.Bid.Quantity == 0 || settlement.Bid.Face < 1 || settlement.Bid.Face > 6 ||
		(settlement.Bid.Mode != BidModeFlying && settlement.Bid.Mode != BidModeStrict) ||
		settlement.Bid.Mode == BidModeStrict && !state.Config.StrictEnabled ||
		settlement.Bid.Face == 1 && settlement.Bid.Mode != BidModeStrict) {
		return ruleError(CodeInvalidState, "last settlement bid is malformed")
	}
	seen := make(map[string]struct{}, len(settlement.RevealedDice))
	for _, roll := range settlement.RevealedDice {
		if playerIndex(state.Players, roll.UserID) < 0 || len(roll.Faces) != int(state.Config.DicePerPlayer) {
			return ruleError(CodeInvalidState, "revealed roll ownership or size is invalid")
		}
		if _, duplicate := seen[roll.UserID]; duplicate {
			return ruleError(CodeInvalidState, "revealed roll is duplicated")
		}
		seen[roll.UserID] = struct{}{}
		for _, face := range roll.Faces {
			if !face.Valid() {
				return ruleError(CodeInvalidState, "revealed roll contains an invalid face")
			}
		}
	}
	return nil
}

func settleAndRestart(
	state State,
	loser string,
	reason SettlementReason,
	opener string,
	actual uint32,
	reveal bool,
	nowUnixMillis int64,
	seed [32]byte,
) (State, []Event, error) {
	next := state.Clone()
	index := playerIndex(next.Players, loser)
	if index < 0 {
		return State{}, nil, ruleError(CodeInvalidState, "settlement loser is not frozen")
	}
	penalty, err := next.Players[index].PenaltyTicks.Add(next.Config.PenaltyTicks)
	if err != nil {
		return State{}, nil, ruleError(CodePenaltyOverflow, "penalty ticks overflow")
	}
	next.Players[index].PenaltyTicks = penalty
	revealed := []PrivateRoll(nil)
	if reveal {
		revealed = cloneRolls(state.PrivateDice)
	}
	next.LastSettlement = Settlement{
		Round: state.Round, LoserUserID: loser, PenaltyTicks: next.Config.PenaltyTicks,
		ActualQuantity: actual, Reason: reason, OpenerUserID: opener,
		Bid: cloneBid(state.CurrentBid), RevealedDice: revealed,
	}
	if state.Round == math.MaxUint32 {
		return State{}, nil, ruleError(CodeRoundOverflow, "round counter cannot advance")
	}
	first := loser
	if index < 0 || !next.Players[index].Active {
		first, err = nextActiveUser(next, loser)
		if err != nil {
			return State{}, nil, err
		}
	}
	events := make([]Event, 0, 3)
	if reveal {
		events = append(events, Event{Kind: EventDiceRevealed, Round: state.Round, Dice: cloneRolls(revealed)})
	}
	events = append(events, Event{
		Kind: EventRoundSettled, Round: state.Round, LoserUserID: loser,
		PenaltyTicks: next.Config.PenaltyTicks, NextRound: state.Round + 1,
		ActualQuantity: actual, Reason: string(reason), OpenerUserID: opener, Bid: cloneBid(state.CurrentBid),
	})
	next, started, err := startRound(next, state.Round+1, first, nowUnixMillis, seed)
	if err != nil {
		return State{}, nil, err
	}
	return next, append(events, started), nil
}

func startRound(state State, round uint32, first string, nowUnixMillis int64, seed [32]byte) (State, Event, error) {
	if playerIndex(state.Players, first) < 0 || !state.Players[playerIndex(state.Players, first)].Active {
		return State{}, Event{}, ruleError(CodeInvalidState, "round starter is not active")
	}
	next := state.Clone()
	next.SchemaVersion = CurrentSchemaVersion
	next.Phase = PhaseBidding
	next.Round = round
	next.FirstActorUserID = first
	next.CurrentActorUserID = first
	next.LastBidderUserID = ""
	next.CurrentBid = nil
	actionDeadline, err := deadlineUnixMillis(next.Config, nowUnixMillis)
	if err != nil {
		return State{}, Event{}, err
	}
	next.ActionDeadlineUnixMillis = actionDeadline
	next.PrivateDice = make([]PrivateRoll, 0, activeCount(next.Players))
	for _, player := range next.Players {
		if !player.Active {
			continue
		}
		stream := uint64(round)<<32 | uint64(player.SeatIndex)
		faces, err := dice.Roll(seed, stream, next.Config.DicePerPlayer)
		if err != nil {
			return State{}, Event{}, ruleError(CodeInvalidState, "deterministic dice generation failed")
		}
		next.PrivateDice = append(next.PrivateDice, PrivateRoll{UserID: player.UserID, Faces: faces})
	}
	return next, Event{Kind: EventRoundStarted, Round: round, FirstActor: first}, nil
}

func validateAction(state State, actor string, nowUnixMillis int64) (int64, error) {
	if err := state.Validate(); err != nil {
		return 0, err
	}
	if state.Phase == PhaseFinished {
		return 0, ruleError(CodeSessionFinished, "session no longer accepts actions")
	}
	if state.CurrentActorUserID != actor {
		return 0, ruleError(CodeNotCurrentActor, "actor does not own the current turn")
	}
	if !validUnixMillis(nowUnixMillis) {
		return 0, ruleError(CodeInvalidState, "deterministic time is required")
	}
	if state.ActionDeadlineUnixMillis != 0 && nowUnixMillis >= state.ActionDeadlineUnixMillis {
		return 0, ruleError(CodeActionExpired, "turn deadline has elapsed")
	}
	return nowUnixMillis, nil
}

func deadlineUnixMillis(config Config, nowUnixMillis int64) (int64, error) {
	if config.ActionTimeoutSeconds == 0 {
		return 0, nil
	}
	offset := int64(config.ActionTimeoutSeconds) * 1000
	if nowUnixMillis > math.MaxInt64-offset {
		return 0, ruleError(CodeInvalidState, "action deadline overflows unix milliseconds")
	}
	return nowUnixMillis + offset, nil
}

func validUnixMillis(value int64) bool { return value > 0 }

func nextActiveUser(state State, current string) (string, error) {
	currentIndex := playerIndex(state.Players, current)
	if currentIndex < 0 {
		return "", ruleError(CodeInvalidState, "turn anchor is not frozen")
	}
	for offset := 1; offset <= len(state.Players); offset++ {
		candidate := state.Players[(currentIndex+offset)%len(state.Players)]
		if candidate.Active {
			return candidate.UserID, nil
		}
	}
	return "", ruleError(CodeInvalidState, "no active turn successor exists")
}

func playerIndex(players []PlayerState, userID string) int {
	for index, player := range players {
		if player.UserID == userID {
			return index
		}
	}
	return -1
}

func activeCount(players []PlayerState) int {
	count := 0
	for _, player := range players {
		if player.Active {
			count++
		}
	}
	return count
}

func removeRoll(values []PrivateRoll, userID string) []PrivateRoll {
	filtered := make([]PrivateRoll, 0, len(values))
	for _, value := range values {
		if value.UserID != userID {
			filtered = append(filtered, PrivateRoll{UserID: value.UserID, Faces: append([]dice.Face(nil), value.Faces...)})
		}
	}
	return filtered
}

func finishState(state State, reason string) (State, Event) {
	state.Phase = PhaseFinished
	state.FinishReason = reason
	state.CurrentActorUserID = ""
	state.ActionDeadlineUnixMillis = 0
	state.PrivateDice = nil
	return state, Event{Kind: EventSessionFinished, Round: state.Round, Reason: reason}
}
