package engine

import (
	"math"
	"sort"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

// NewState validates the frozen room context and starts turn one at the trusted starting seat.
func NewState(
	config Config,
	participants []Participant,
	hostUserID string,
	startingSeat uint32,
	nowUnixMillis int64,
	seed [32]byte,
) (State, []Event, error) {
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
	hostFound, first := false, ""
	players := make([]PlayerState, len(canonical))
	for index, participant := range canonical {
		players[index] = PlayerState{UserID: participant.UserID, SeatIndex: participant.SeatIndex, Active: true}
		hostFound = hostFound || participant.UserID == hostUserID
		if participant.SeatIndex == startingSeat {
			first = participant.UserID
		}
	}
	if !hostFound || first == "" {
		return State{}, nil, ruleError(CodeInvalidParticipants, "host and starting seat must be frozen participants")
	}
	pool, err := poolFromTotal(config, config.InitialPoolTicks)
	if err != nil {
		return State{}, nil, err
	}
	state := State{
		SchemaVersion:  CurrentSchemaVersion,
		Phase:          PhaseAwaitingRoll,
		Turn:           1,
		HostUserID:     hostUserID,
		Players:        players,
		Direction:      DirectionClockwise,
		Pool:           pool,
		TotalPoolTicks: config.InitialPoolTicks,
		Config:         config,
		LastSettlement: TurnSettlement{Result: ResultNone},
	}
	state, event, err := startTurn(state, first, nowUnixMillis)
	if err != nil {
		return State{}, nil, err
	}
	// Validate the injected seed at creation so a malformed deterministic context fails before persistence.
	if _, err := dice.Roll(seed, uint64(state.Turn), 2); err != nil {
		return State{}, nil, ruleError(CodeSeedInvalid, "deterministic seed is invalid")
	}
	initialConfig := config
	event.InitialConfig = &initialConfig
	event.InitialParticipants = append([]Participant(nil), canonical...)
	event.InitialPool = append([]PoolLayer(nil), state.Pool...)
	event.InitialTotalPoolTicks = state.TotalPoolTicks
	return state, []Event{event}, nil
}

// CurrentTimer returns the exact timer payload that must replace every previous turn timer.
func CurrentTimer(state State) *ActionTimer {
	if state.Phase == PhaseFinished || state.Phase == PhaseTurnSettled || state.ActionDeadlineUnixMillis == 0 {
		return nil
	}
	return &ActionTimer{Turn: state.Turn, Phase: state.Phase, CurrentUserID: state.CurrentUserID,
		SourceUserID: state.SourceUserID, TargetUserID: state.TargetUserID, PendingResult: state.PendingResult,
		DeadlineUnixMillis: state.ActionDeadlineUnixMillis}
}

// Validate rejects malformed restored snapshots before actions, timers, projections, or migration use them.
func (state State) Validate() error {
	if state.SchemaVersion != CurrentSchemaVersion || state.Turn == 0 ||
		state.Phase < PhaseAwaitingRoll || state.Phase > PhaseFinished || state.Phase == PhaseTurnSettled ||
		len(state.Players) < MinimumPlayers || len(state.Players) > MaximumPlayers || !state.Direction.Valid() {
		return ruleError(CodeInvalidState, "state header is invalid")
	}
	participants := make([]Participant, len(state.Players))
	hostFound := false
	for index, player := range state.Players {
		participants[index] = Participant{UserID: player.UserID, SeatIndex: player.SeatIndex}
		hostFound = hostFound || player.UserID == state.HostUserID
		if index > 0 && state.Players[index-1].SeatIndex >= player.SeatIndex {
			return ruleError(CodeInvalidState, "players are not in stable seat order")
		}
	}
	if !hostFound || ValidateParticipants(participants) != nil || state.Config.Validate(len(state.Players)) != nil {
		return ruleError(CodeInvalidState, "frozen participants, host, or configuration is invalid")
	}
	if err := validatePool(state); err != nil {
		return err
	}
	if err := validateSettlement(state); err != nil {
		return err
	}
	if state.Phase == PhaseFinished {
		if !validFinishReason(state.FinishReason) || state.CurrentUserID != "" || state.SourceUserID != "" || state.TargetUserID != "" ||
			state.ActionDeadlineUnixMillis != 0 || state.DieOne != 0 || state.DieTwo != 0 || state.Sum != 0 ||
			state.PendingResult != ResultNone || state.PendingPoolBeforeTicks != 0 || state.PendingDirectionBefore != 0 {
			return ruleError(CodeInvalidState, "finished state retains live turn data")
		}
		return nil
	}
	if state.FinishReason != "" || activeCount(state.Players) < MinimumPlayers || !activePlayer(state, state.CurrentUserID) {
		return ruleError(CodeInvalidState, "active turn ownership is invalid")
	}
	if err := validatePhase(state); err != nil {
		return err
	}
	return nil
}

func validFinishReason(value string) bool {
	return value == FinishHostRequested || value == FinishInsufficientParticipants || value == FinishPlatformCancelled
}

func validatePhase(state State) error {
	diceValid := state.DieOne >= 1 && state.DieOne <= 6 && state.DieTwo >= 1 && state.DieTwo <= 6 && state.Sum == state.DieOne+state.DieTwo
	switch state.Phase {
	case PhaseAwaitingRoll:
		if state.SourceUserID != "" || state.TargetUserID != "" || state.DieOne != 0 || state.DieTwo != 0 || state.Sum != 0 ||
			state.PendingResult != ResultNone || state.PendingPoolBeforeTicks != 0 || state.PendingDirectionBefore != 0 ||
			!deadlineMatches(state, state.Config.ActionTimeoutSeconds) {
			return ruleError(CodeInvalidState, "awaiting-roll state is malformed")
		}
	case PhaseResultPending:
		if state.SourceUserID != state.CurrentUserID || !activePlayer(state, state.SourceUserID) || state.TargetUserID != "" || !diceValid ||
			!state.PendingResult.validRollResult() || state.PendingPoolBeforeTicks != state.TotalPoolTicks ||
			!state.PendingDirectionBefore.Valid() ||
			!deadlineMatches(state, state.Config.DropReportWindowSeconds) {
			return ruleError(CodeInvalidState, "result-pending state is malformed")
		}
	case PhaseAwaitingAdd:
		validSeven := state.PendingResult == ResultSeven && state.TargetUserID == "" && state.CurrentUserID == state.SourceUserID
		validDoubleSix := state.PendingResult == ResultDoubleSix && state.TargetUserID == state.CurrentUserID && state.TargetUserID != state.SourceUserID
		if !diceValid || !activePlayer(state, state.SourceUserID) || !activePlayer(state, state.CurrentUserID) || !state.PendingDirectionBefore.Valid() ||
			(!validSeven && !validDoubleSix) || !deadlineMatches(state, state.Config.ActionTimeoutSeconds) {
			return ruleError(CodeInvalidState, "awaiting-add state is malformed")
		}
	case PhaseAwaitingTarget:
		if !diceValid || state.CurrentUserID != state.SourceUserID || !activePlayer(state, state.SourceUserID) || state.TargetUserID != "" || !state.PendingDirectionBefore.Valid() ||
			(state.PendingResult != ResultDoubleOne && state.PendingResult != ResultDoubleSix) ||
			!deadlineMatches(state, state.Config.ActionTimeoutSeconds) {
			return ruleError(CodeInvalidState, "awaiting-target state is malformed")
		}
	case PhaseAwaitingContinue:
		if !diceValid || state.CurrentUserID != state.SourceUserID || !activePlayer(state, state.SourceUserID) || state.TargetUserID != "" || !state.PendingDirectionBefore.Valid() ||
			(state.PendingResult != ResultSeven && state.PendingResult != ResultEight && state.PendingResult != ResultNine) ||
			state.PendingPoolBeforeTicks > mustCapacity(state.Config) || state.LastSettlement.Turn != state.Turn ||
			!deadlineMatches(state, state.Config.ActionTimeoutSeconds) {
			return ruleError(CodeInvalidState, "awaiting-continue state is malformed")
		}
	default:
		return ruleError(CodeInvalidState, "phase cannot be persisted")
	}
	return nil
}

func validateSettlement(state State) error {
	settlement := state.LastSettlement
	if settlement.Turn == 0 {
		if settlement.SourceUserID != "" || settlement.DieOne != 0 || settlement.DieTwo != 0 || settlement.Sum != 0 ||
			settlement.Result != ResultNone || settlement.TargetUserID != "" || settlement.PoolBeforeTicks != 0 ||
			settlement.PoolAfterTicks != 0 || len(settlement.PoolBefore) != 0 || len(settlement.PoolAfter) != 0 ||
			settlement.EffectTicks != 0 || settlement.PenaltyUserID != "" ||
			settlement.PenaltyTicks != 0 || settlement.DirectionBefore != 0 || settlement.DirectionAfter != 0 ||
			settlement.NextUserID != "" || settlement.Reason != "" || settlement.AuditRef != "" || settlement.DropReason != "" {
			return ruleError(CodeInvalidState, "empty settlement retains data")
		}
		return nil
	}
	partialAppliedEffect := settlement.Turn == state.Turn && settlement.NextUserID == "" &&
		(settlement.Result == ResultSeven || settlement.Result == ResultEight || settlement.Result == ResultNine) &&
		(state.Phase == PhaseAwaitingContinue || state.Phase == PhaseFinished)
	if settlement.Turn > state.Turn || settlement.Turn == state.Turn && !partialAppliedEffect ||
		playerIndex(state.Players, settlement.SourceUserID) < 0 ||
		settlement.NextUserID == "" && !partialAppliedEffect || settlement.NextUserID != "" && playerIndex(state.Players, settlement.NextUserID) < 0 ||
		!settlement.Result.validSettledResult() ||
		!settlementDiceValid(settlement) || !settlement.DirectionBefore.Valid() || !settlement.DirectionAfter.Valid() ||
		settlement.Reason == "" {
		return ruleError(CodeInvalidState, "last settlement header is malformed")
	}
	capacity, _ := state.Config.totalCapacity()
	if settlement.PoolBeforeTicks > capacity || settlement.PoolAfterTicks > capacity {
		return ruleError(CodeInvalidState, "last settlement pool is outside capacity")
	}
	if poolLayersTotal(settlement.PoolBefore) != settlement.PoolBeforeTicks || poolLayersTotal(settlement.PoolAfter) != settlement.PoolAfterTicks {
		return ruleError(CodeInvalidState, "last settlement layer snapshots disagree with totals")
	}
	drinking := settlement.Result == ResultEight || settlement.Result == ResultNine || settlement.Result == ResultDoubleOne ||
		settlement.Result == ResultDoubleFour || settlement.Result == ResultDropped
	adding := settlement.Result == ResultSeven || settlement.Result == ResultDoubleSix
	if drinking {
		if settlement.PenaltyUserID == "" || settlement.PenaltyTicks != settlement.EffectTicks ||
			settlement.PoolBeforeTicks < settlement.PoolAfterTicks || settlement.PoolBeforeTicks-settlement.PoolAfterTicks != settlement.EffectTicks {
			return ruleError(CodeInvalidState, "drinking settlement violates tick conservation")
		}
	} else if adding {
		if settlement.PenaltyUserID != "" || settlement.PenaltyTicks != 0 || settlement.PoolAfterTicks < settlement.PoolBeforeTicks ||
			settlement.PoolAfterTicks-settlement.PoolBeforeTicks != settlement.EffectTicks {
			return ruleError(CodeInvalidState, "add settlement violates tick conservation")
		}
	} else if settlement.EffectTicks != 0 || settlement.PenaltyTicks != 0 || settlement.PenaltyUserID != "" ||
		settlement.PoolBeforeTicks != settlement.PoolAfterTicks {
		return ruleError(CodeInvalidState, "no-effect settlement changed ticks")
	}
	if settlement.TargetUserID != "" && playerIndex(state.Players, settlement.TargetUserID) < 0 {
		return ruleError(CodeInvalidState, "last settlement target is not frozen")
	}
	if settlement.Result == ResultDropped {
		if settlement.AuditRef == "" || settlement.DropReason == "" {
			return ruleError(CodeInvalidState, "dropped settlement lacks audit detail")
		}
	} else if settlement.AuditRef != "" || settlement.DropReason != "" {
		return ruleError(CodeInvalidState, "non-dropped settlement retains drop audit detail")
	}
	return nil
}

// ClassifyRoll applies the frozen priority matrix without mutating a session.
// Callers use it to verify public dice facts against the room configuration.
func ClassifyRoll(config Config, dieOne, dieTwo uint32) (ResultKind, error) {
	if dieOne < 1 || dieOne > 6 || dieTwo < 1 || dieTwo > 6 {
		return ResultNone, ruleError(CodeInvalidAction, "dice faces must be in 1..6")
	}
	return selectResult(State{Config: config, DieOne: dieOne, DieTwo: dieTwo, Sum: dieOne + dieTwo}), nil
}

func validatePool(state State) error {
	capacity, err := state.Config.totalCapacity()
	if err != nil || len(state.Pool) == 0 || len(state.Pool) > int(state.Config.MaxLayers) || state.TotalPoolTicks > capacity {
		return ruleError(CodeInvalidState, "pool header is invalid")
	}
	if !state.Config.StackedPool && len(state.Pool) != 1 {
		return ruleError(CodeInvalidState, "single-layer pool has extra layers")
	}
	total := dice.Ticks(0)
	for index, layer := range state.Pool {
		if layer.Ticks > state.Config.LayerCapacityTicks || (index < len(state.Pool)-1 && layer.Ticks != state.Config.LayerCapacityTicks) ||
			(len(state.Pool) > 1 && index == len(state.Pool)-1 && layer.Ticks == 0) {
			return ruleError(CodeInvalidState, "pool layer layout is non-canonical")
		}
		total, err = total.Add(layer.Ticks)
		if err != nil {
			return ruleError(CodeInvalidState, "pool layer sum overflows")
		}
	}
	if total != state.TotalPoolTicks {
		return ruleError(CodeInvalidState, "pool layer sum differs from total")
	}
	return nil
}

func (config Config) totalCapacity() (dice.Ticks, error) {
	if uint64(config.LayerCapacityTicks)*uint64(config.MaxLayers) > math.MaxUint32 {
		return 0, ruleError(CodePoolOverflow, "pool capacity overflows uint32")
	}
	return dice.Ticks(uint32(config.LayerCapacityTicks) * config.MaxLayers), nil
}

func poolFromTotal(config Config, total dice.Ticks) ([]PoolLayer, error) {
	capacity, err := config.totalCapacity()
	if err != nil || total > capacity {
		return nil, ruleError(CodePoolOverflow, "pool total exceeds capacity")
	}
	if total == 0 {
		return []PoolLayer{{}}, nil
	}
	layers := make([]PoolLayer, 0, config.MaxLayers)
	remaining := total
	for remaining > 0 {
		value := config.LayerCapacityTicks
		if remaining < value {
			value = remaining
		}
		layers = append(layers, PoolLayer{Ticks: value})
		remaining -= value
	}
	return layers, nil
}

func canonicalParticipants(participants []Participant) ([]Participant, error) {
	if err := ValidateParticipants(participants); err != nil {
		return nil, err
	}
	canonical := append([]Participant(nil), participants...)
	sort.Slice(canonical, func(left, right int) bool { return canonical[left].SeatIndex < canonical[right].SeatIndex })
	return canonical, nil
}

func startTurn(state State, actor string, nowUnixMillis int64) (State, Event, error) {
	if !activePlayer(state, actor) || !validUnixMillis(nowUnixMillis) {
		return State{}, Event{}, ruleError(CodeInvalidState, "next actor or deterministic time is invalid")
	}
	deadline, err := deadlineUnixMillis(nowUnixMillis, state.Config.ActionTimeoutSeconds)
	if err != nil {
		return State{}, Event{}, err
	}
	next := state.Clone()
	next.Phase = PhaseAwaitingRoll
	next.CurrentUserID = actor
	next.SourceUserID = ""
	next.TargetUserID = ""
	next.DieOne, next.DieTwo, next.Sum = 0, 0, 0
	next.PendingResult = ResultNone
	next.PendingPoolBeforeTicks = 0
	next.PendingDirectionBefore = 0
	next.ActionDeadlineUnixMillis = deadline
	return next, Event{Kind: EventTurnStarted, Turn: next.Turn, UserID: actor, NextUserID: actor, Direction: next.Direction}, nil
}

func deadlineUnixMillis(now int64, seconds uint32) (int64, error) {
	if !validUnixMillis(now) {
		return 0, ruleError(CodeInvalidState, "deterministic time is required")
	}
	if seconds == 0 {
		return 0, nil
	}
	delta := int64(seconds) * 1000
	if now > math.MaxInt64-delta {
		return 0, ruleError(CodeInvalidState, "deadline overflows unix milliseconds")
	}
	return now + delta, nil
}

func validUnixMillis(value int64) bool { return value > 0 }

func deadlineMatches(state State, seconds uint32) bool {
	return seconds == 0 && state.ActionDeadlineUnixMillis == 0 || seconds != 0 && state.ActionDeadlineUnixMillis > 0
}

func (direction Direction) Valid() bool {
	return direction == DirectionClockwise || direction == DirectionCounterClockwise
}

func (result ResultKind) validRollResult() bool {
	return result == ResultSeven || result == ResultEight || result == ResultNine || result == ResultDoubleOne ||
		result == ResultDoubleFour || result == ResultDoubleSix || result == ResultOrdinaryPair || result == ResultOther
}

func (result ResultKind) validSettledResult() bool {
	return result.validRollResult() || result == ResultDropped || result == ResultRollTimeout || result == ResultCancelled
}

func settlementDiceValid(settlement TurnSettlement) bool {
	if settlement.Result == ResultRollTimeout || settlement.Result == ResultCancelled && settlement.DieOne == 0 && settlement.DieTwo == 0 {
		return settlement.DieOne == 0 && settlement.DieTwo == 0 && settlement.Sum == 0
	}
	return settlement.DieOne >= 1 && settlement.DieOne <= 6 && settlement.DieTwo >= 1 && settlement.DieTwo <= 6 &&
		settlement.Sum == settlement.DieOne+settlement.DieTwo
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

func playerIndex(players []PlayerState, userID string) int {
	for index := range players {
		if players[index].UserID == userID {
			return index
		}
	}
	return -1
}

func activePlayer(state State, userID string) bool {
	index := playerIndex(state.Players, userID)
	return index >= 0 && state.Players[index].Active
}

func nextActiveUser(state State, afterUserID string) (string, error) {
	start := playerIndex(state.Players, afterUserID)
	if start < 0 || !state.Direction.Valid() {
		return "", ruleError(CodeInvalidState, "turn traversal origin is not frozen")
	}
	step := 1
	if state.Direction == DirectionCounterClockwise {
		step = -1
	}
	for offset := 1; offset <= len(state.Players); offset++ {
		index := (start + step*offset) % len(state.Players)
		if index < 0 {
			index += len(state.Players)
		}
		if state.Players[index].Active {
			return state.Players[index].UserID, nil
		}
	}
	return "", ruleError(CodeInvalidState, "no active next player exists")
}

func reverseDirection(value Direction) Direction {
	if value == DirectionClockwise {
		return DirectionCounterClockwise
	}
	return DirectionClockwise
}

func poolLayersTotal(layers []PoolLayer) dice.Ticks {
	var total dice.Ticks
	for _, layer := range layers {
		total += layer.Ticks
	}
	return total
}
