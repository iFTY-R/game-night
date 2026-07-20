package module

import (
	"strings"

	"github.com/iFTY-R/game-night/games/dice-789/engine"
	dice789v1 "github.com/iFTY-R/game-night/games/dice-789/gen/go/game/dice789/v1"
	"github.com/iFTY-R/game-night/games/dice-789/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
	"google.golang.org/protobuf/proto"
)

// EncodeConfig serializes a frozen rules configuration using the module envelope.
func EncodeConfig(config engine.Config) (game.Message, error) {
	if err := config.Validate(engine.MaximumPlayers); err != nil {
		return game.Message{}, err
	}
	return encodeConfigMessage(config)
}

// EncodeConfigForPlayers validates the configuration against the actual room size.
func EncodeConfigForPlayers(config engine.Config, playerCount int) (game.Message, error) {
	if err := config.Validate(playerCount); err != nil {
		return game.Message{}, err
	}
	return encodeConfigMessage(config)
}

func encodeConfigMessage(config engine.Config) (game.Message, error) {
	payload, err := marshalDeterministic(configToProto(config))
	if err != nil {
		return game.Message{}, malformed("config encoding failed")
	}
	return game.Message{MessageType: ConfigMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// DecodeConfig accepts an empty payload as the immutable default ruleset.
func DecodeConfig(message game.Message, playerCount int) (engine.Config, error) {
	if message.MessageType != ConfigMessageType || message.SchemaVersion != ProtocolSchemaVersion {
		return engine.Config{}, malformed("config envelope is not owned by dice-789")
	}
	if len(message.Payload) == 0 {
		config := engine.DefaultConfig(false)
		if err := config.Validate(playerCount); err != nil {
			return engine.Config{}, err
		}
		return config, nil
	}
	var value dice789v1.Config
	if err := unmarshalStrict(message.Payload, &value); err != nil {
		return engine.Config{}, err
	}
	config, err := configFromProto(&value)
	if err != nil {
		return engine.Config{}, err
	}
	if err := config.Validate(playerCount); err != nil {
		return engine.Config{}, err
	}
	return config, nil
}

func configToProto(config engine.Config) *dice789v1.Config {
	return &dice789v1.Config{
		InitialPoolTicks:        uint32(config.InitialPoolTicks),
		LayerCapacityTicks:      uint32(config.LayerCapacityTicks),
		AddStepTicks:            uint32(config.AddStepTicks),
		MaxLayers:               config.MaxLayers,
		StackedPool:             config.StackedPool,
		OrdinaryPairsReverse:    config.OrdinaryPairsReverse,
		DoubleOneEnabled:        config.DoubleOneEnabled,
		DoubleFourEnabled:       config.DoubleFourEnabled,
		DoubleSixEnabled:        config.DoubleSixEnabled,
		ContinueMode:            continueModeToProto(config.ContinueMode),
		LastDigitMatch:          config.LastDigitMatch,
		ActionTimeoutSeconds:    config.ActionTimeoutSeconds,
		DropReportWindowSeconds: config.DropReportWindowSeconds,
	}
}

func configFromProto(value *dice789v1.Config) (engine.Config, error) {
	if value == nil {
		return engine.Config{}, malformed("config message is missing")
	}
	mode, ok := continueModeFromProto(value.GetContinueMode())
	if !ok {
		return engine.Config{}, &engine.RuleError{Code: engine.CodeContinueModeInvalid, Detail: "continue mode is unknown"}
	}
	return engine.Config{
		InitialPoolTicks:        dice.Ticks(value.GetInitialPoolTicks()),
		LayerCapacityTicks:      dice.Ticks(value.GetLayerCapacityTicks()),
		AddStepTicks:            dice.Ticks(value.GetAddStepTicks()),
		MaxLayers:               value.GetMaxLayers(),
		StackedPool:             value.GetStackedPool(),
		OrdinaryPairsReverse:    value.GetOrdinaryPairsReverse(),
		DoubleOneEnabled:        value.GetDoubleOneEnabled(),
		DoubleFourEnabled:       value.GetDoubleFourEnabled(),
		DoubleSixEnabled:        value.GetDoubleSixEnabled(),
		ContinueMode:            mode,
		LastDigitMatch:          value.GetLastDigitMatch(),
		ActionTimeoutSeconds:    value.GetActionTimeoutSeconds(),
		DropReportWindowSeconds: value.GetDropReportWindowSeconds(),
	}, nil
}

// EncodeState validates and emits a canonical authoritative snapshot.
func EncodeState(state engine.State) (game.Message, error) {
	state = normalizeState(state)
	if err := state.Validate(); err != nil {
		return game.Message{}, err
	}
	payload, err := marshalDeterministic(stateToProto(state))
	if err != nil {
		return game.Message{}, malformed("state encoding failed")
	}
	return game.Message{MessageType: StateMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// DecodeState rejects non-canonical snapshots before the engine or projection reads them.
func DecodeState(message game.Message) (engine.State, error) {
	if message.MessageType != StateMessageType || message.SchemaVersion != ProtocolSchemaVersion {
		return engine.State{}, malformed("state envelope is not owned by dice-789")
	}
	var value dice789v1.State
	if err := unmarshalStrict(message.Payload, &value); err != nil {
		return engine.State{}, err
	}
	state, err := stateFromProto(&value)
	if err != nil {
		return engine.State{}, err
	}
	if err := state.Validate(); err != nil {
		return engine.State{}, err
	}
	canonical := stateToProto(state)
	if !proto.Equal(&value, canonical) {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state contains non-canonical derived fields"}
	}
	return state, nil
}

func stateToProto(state engine.State) *dice789v1.State {
	players := make([]*dice789v1.PlayerState, len(state.Players))
	for index, player := range state.Players {
		players[index] = &dice789v1.PlayerState{
			UserId: player.UserID, SeatIndex: player.SeatIndex, Active: player.Active,
			PenaltyTicks: uint32(player.PenaltyTicks),
		}
	}
	value := &dice789v1.State{
		SchemaVersion:            state.SchemaVersion,
		Phase:                    phaseToProto(state.Phase),
		Turn:                     state.Turn,
		Players:                  players,
		CurrentUserId:            state.CurrentUserID,
		SourceUserId:             state.SourceUserID,
		TargetUserId:             state.TargetUserID,
		Direction:                uint32(state.Direction),
		Pool:                     poolToProto(state.Pool),
		TotalPoolTicks:           uint32(state.TotalPoolTicks),
		DieOne:                   state.DieOne,
		DieTwo:                   state.DieTwo,
		Sum:                      state.Sum,
		ActionDeadlineUnixMillis: state.ActionDeadlineUnixMillis,
		Config:                   configToProto(state.Config),
		HostUserId:               state.HostUserID,
		Effect:                   resultToProto(state.PendingResult, state),
		AllowedActions:           ruleActions(state),
		FinishReason:             state.FinishReason,
	}
	if constraints := constraintsForState(state); constraints != nil {
		value.ActionConstraints = constraints
	}
	if state.LastSettlement.Turn != 0 {
		summary := settlementToProto(state.LastSettlement, state)
		value.LastSettlement = summary
		// The current engine retains one reconnect-safe summary. The protocol allows
		// a bounded history, so this remains forward-compatible with a larger engine buffer.
		value.TurnHistory = []*dice789v1.TurnSummary{summary}
	}
	return value
}

func stateFromProto(value *dice789v1.State) (engine.State, error) {
	if value == nil || value.GetSchemaVersion() != engine.CurrentSchemaVersion || value.GetTurn() == 0 || value.GetConfig() == nil {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state header is malformed"}
	}
	config, err := configFromProto(value.GetConfig())
	if err != nil {
		return engine.State{}, err
	}
	players := make([]engine.PlayerState, len(value.GetPlayers()))
	for index, player := range value.GetPlayers() {
		if player == nil || player.GetUserId() == "" {
			return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state contains a malformed player"}
		}
		players[index] = engine.PlayerState{UserID: player.GetUserId(), SeatIndex: player.GetSeatIndex(), Active: player.GetActive(), PenaltyTicks: dice.Ticks(player.GetPenaltyTicks())}
	}
	pool, err := poolFromProto(value.GetPool())
	if err != nil {
		return engine.State{}, err
	}
	phase, ok := phaseFromProto(value.GetPhase())
	if !ok {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state phase is unknown"}
	}
	direction := engine.Direction(value.GetDirection())
	if !direction.Valid() {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state direction is unknown"}
	}
	pending, ok := resultFromProto(value.GetEffect(), phase)
	if !ok {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state effect is unknown for phase"}
	}
	state := engine.State{
		SchemaVersion:            value.GetSchemaVersion(),
		Phase:                    phase,
		Turn:                     value.GetTurn(),
		HostUserID:               value.GetHostUserId(),
		Players:                  players,
		CurrentUserID:            value.GetCurrentUserId(),
		SourceUserID:             value.GetSourceUserId(),
		TargetUserID:             value.GetTargetUserId(),
		Direction:                direction,
		Pool:                     pool,
		TotalPoolTicks:           dice.Ticks(value.GetTotalPoolTicks()),
		DieOne:                   value.GetDieOne(),
		DieTwo:                   value.GetDieTwo(),
		Sum:                      value.GetSum(),
		ActionDeadlineUnixMillis: value.GetActionDeadlineUnixMillis(),
		Config:                   config,
		PendingResult:            pending,
		FinishReason:             value.GetFinishReason(),
		LastSettlement:           engine.TurnSettlement{Result: engine.ResultNone},
	}
	if value.GetLastSettlement() != nil {
		settlement, err := settlementFromProto(value.GetLastSettlement())
		if err != nil {
			return engine.State{}, err
		}
		state.LastSettlement = settlement
	}
	state.PendingPoolBeforeTicks, state.PendingDirectionBefore = pendingStart(state)
	if len(value.GetTurnHistory()) > 1 {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "turn history exceeds current engine buffer"}
	}
	return state, nil
}

// normalizeState bridges the Go zero value to the engine's explicit empty-result sentinel.
func normalizeState(state engine.State) engine.State {
	if state.LastSettlement.Turn == 0 && state.LastSettlement.Result == "" {
		state.LastSettlement.Result = engine.ResultNone
	}
	return state
}

func pendingStart(state engine.State) (dice.Ticks, engine.Direction) {
	if state.Phase == engine.PhaseAwaitingRoll || state.Phase == engine.PhaseFinished {
		return 0, 0
	}
	if state.Phase == engine.PhaseAwaitingContinue && state.LastSettlement.Turn == state.Turn {
		return state.LastSettlement.PoolBeforeTicks, state.LastSettlement.DirectionBefore
	}
	return state.TotalPoolTicks, state.Direction
}

func poolToProto(values []engine.PoolLayer) []*dice789v1.PoolLayer {
	result := make([]*dice789v1.PoolLayer, len(values))
	for index, value := range values {
		result[index] = &dice789v1.PoolLayer{Ticks: uint32(value.Ticks), Index: uint32(index)}
	}
	return result
}

func poolFromProto(values []*dice789v1.PoolLayer) ([]engine.PoolLayer, error) {
	if len(values) == 0 {
		return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "pool is empty"}
	}
	result := make([]engine.PoolLayer, len(values))
	for index, value := range values {
		if value == nil || value.GetIndex() != uint32(index) {
			return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "pool layer indexes are not contiguous"}
		}
		result[index] = engine.PoolLayer{Ticks: dice.Ticks(value.GetTicks())}
	}
	return result, nil
}

func settlementToProto(value engine.TurnSettlement, state engine.State) *dice789v1.TurnSummary {
	if value.Turn == 0 {
		return nil
	}
	effect := resultToProto(value.Result, state)
	if value.Result == engine.ResultOrdinaryPair {
		if value.DirectionBefore == value.DirectionAfter {
			effect = dice789v1.Effect_EFFECT_ORDINARY_PAIR_REROLL
		} else {
			effect = dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE
		}
	}
	summary := &dice789v1.TurnSummary{
		Turn:               value.Turn,
		SourceUserId:       value.SourceUserID,
		DieOne:             value.DieOne,
		DieTwo:             value.DieTwo,
		Sum:                value.Sum,
		Effect:             effect,
		TargetUserId:       value.TargetUserID,
		PoolBeforeTicks:    uint32(value.PoolBeforeTicks),
		PoolAfterTicks:     uint32(value.PoolAfterTicks),
		PenaltyUserId:      value.PenaltyUserID,
		PenaltyTicks:       uint32(value.PenaltyTicks),
		DirectionBefore:    uint32(value.DirectionBefore),
		DirectionAfter:     uint32(value.DirectionAfter),
		NextUserId:         value.NextUserID,
		Outcome:            outcomeToProto(value),
		Cause:              causeToProto(value.Reason),
		DroppedReported:    value.Result == engine.ResultDropped,
		DropOperatorUserId: chooseDropOperator(value, state),
		DropReason:         dropReason(value),
		ResolutionReason:   value.Reason,
		PoolBeforeLayers:   poolToProto(value.PoolBefore),
		PoolAfterLayers:    poolToProto(value.PoolAfter),
		AuditRef:           value.AuditRef,
	}
	if state.Phase == engine.PhaseFinished && value.Turn == state.Turn && value.NextUserID == "" {
		summary.Outcome = dice789v1.TurnOutcome_TURN_OUTCOME_SESSION_FINISHED
		summary.Cause = finishCause(state.FinishReason)
	}
	return summary
}

func settlementFromProto(value *dice789v1.TurnSummary) (engine.TurnSettlement, error) {
	if value == nil || value.GetTurn() == 0 || value.GetSourceUserId() == "" || value.GetResolutionReason() == "" {
		return engine.TurnSettlement{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "settlement summary is malformed"}
	}
	result, ok := resultFromProto(value.GetEffect(), engine.PhaseAwaitingRoll)
	if !ok {
		return engine.TurnSettlement{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "settlement effect is unknown"}
	}
	if value.GetEffect() == dice789v1.Effect_EFFECT_ORDINARY_PAIR_REROLL || value.GetEffect() == dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE {
		result = engine.ResultOrdinaryPair
	}
	if value.GetResolutionReason() == "roll_timeout" {
		result = engine.ResultRollTimeout
	}
	if value.GetResolutionReason() == "cancelled" {
		result = engine.ResultCancelled
	}
	before, err := poolFromProto(value.GetPoolBeforeLayers())
	if err != nil {
		return engine.TurnSettlement{}, err
	}
	after, err := poolFromProto(value.GetPoolAfterLayers())
	if err != nil {
		return engine.TurnSettlement{}, err
	}
	effectTicks := dice.Ticks(0)
	beforeTicks := dice.Ticks(value.GetPoolBeforeTicks())
	afterTicks := dice.Ticks(value.GetPoolAfterTicks())
	if beforeTicks >= afterTicks {
		effectTicks = beforeTicks - afterTicks
	} else {
		effectTicks = afterTicks - beforeTicks
	}
	return engine.TurnSettlement{
		Turn: value.GetTurn(), SourceUserID: value.GetSourceUserId(), DieOne: value.GetDieOne(), DieTwo: value.GetDieTwo(), Sum: value.GetSum(), Result: result,
		TargetUserID: value.GetTargetUserId(), PoolBeforeTicks: beforeTicks, PoolAfterTicks: afterTicks,
		PoolBefore: before, PoolAfter: after, EffectTicks: effectTicks,
		PenaltyUserID: value.GetPenaltyUserId(), PenaltyTicks: dice.Ticks(value.GetPenaltyTicks()), DirectionBefore: engine.Direction(value.GetDirectionBefore()), DirectionAfter: engine.Direction(value.GetDirectionAfter()),
		NextUserID: value.GetNextUserId(), Reason: value.GetResolutionReason(), AuditRef: value.GetAuditRef(), DropReason: value.GetDropReason(),
	}, nil
}

func chooseDropOperator(value engine.TurnSettlement, state engine.State) string {
	if value.Result == engine.ResultDropped {
		return state.HostUserID
	}
	return ""
}

func dropReason(value engine.TurnSettlement) string {
	if value.Result == engine.ResultDropped {
		return value.DropReason
	}
	return ""
}

func constraintsForState(state engine.State) *dice789v1.ActionConstraints {
	switch state.Phase {
	case engine.PhaseAwaitingAdd:
		capacity := uint64(state.Config.LayerCapacityTicks) * uint64(state.Config.MaxLayers)
		remaining := uint32(capacity) - uint32(state.TotalPoolTicks)
		minimum := uint32(0)
		if remaining > 0 {
			minimum = uint32(state.Config.AddStepTicks)
			if remaining < minimum {
				minimum = remaining
			}
		}
		return &dice789v1.ActionConstraints{
			MinimumAddTicks: minimum, MaximumAddTicks: remaining,
			AddStepTicks: uint32(state.Config.AddStepTicks), AllowCapacityRemainder: remaining > 0 && remaining%uint32(state.Config.AddStepTicks) != 0,
		}
	case engine.PhaseAwaitingTarget:
		ids := make([]string, 0, len(state.Players))
		for _, player := range state.Players {
			if player.Active && player.UserID != state.SourceUserID {
				ids = append(ids, player.UserID)
			}
		}
		return &dice789v1.ActionConstraints{TargetUserIds: ids}
	default:
		return nil
	}
}

func ruleActions(state engine.State) []string {
	switch state.Phase {
	case engine.PhaseAwaitingRoll:
		return []string{string(projection.ActionRoll)}
	case engine.PhaseResultPending:
		return []string{string(projection.ActionConfirmLanded), string(projection.ActionReportDropped)}
	case engine.PhaseAwaitingAdd:
		return []string{string(projection.ActionAdd)}
	case engine.PhaseAwaitingTarget:
		return []string{string(projection.ActionChooseTarget)}
	case engine.PhaseAwaitingContinue:
		switch state.Config.ContinueMode {
		case engine.ContinueForcedReroll:
			return []string{string(projection.ActionReroll)}
		case engine.ContinueForcedPass:
			return []string{string(projection.ActionPass)}
		default:
			return []string{string(projection.ActionReroll), string(projection.ActionPass)}
		}
	default:
		return nil
	}
}

func actionTimerToProto(value *engine.ActionTimer) *dice789v1.ActionTimer {
	if value == nil {
		return nil
	}
	return &dice789v1.ActionTimer{
		Turn: value.Turn, Phase: phaseToProto(value.Phase), CurrentUserId: value.CurrentUserID,
		SourceUserId: value.SourceUserID, TargetUserId: value.TargetUserID,
		Effect: resultToProto(value.PendingResult, engine.State{}), DeadlineUnixMillis: value.DeadlineUnixMillis,
	}
}

func actionTimerFromProto(value *dice789v1.ActionTimer) (engine.ActionTimer, error) {
	if value == nil || value.GetTurn() == 0 || value.GetCurrentUserId() == "" || value.GetDeadlineUnixMillis() == 0 {
		return engine.ActionTimer{}, &engine.RuleError{Code: engine.CodeTimerMismatch, Detail: "timer fields are incomplete"}
	}
	phase, ok := phaseFromProto(value.GetPhase())
	if !ok {
		return engine.ActionTimer{}, &engine.RuleError{Code: engine.CodeTimerMismatch, Detail: "timer phase is unknown"}
	}
	pending, ok := resultFromProto(value.GetEffect(), phase)
	if !ok {
		return engine.ActionTimer{}, &engine.RuleError{Code: engine.CodeTimerMismatch, Detail: "timer effect is invalid for phase"}
	}
	return engine.ActionTimer{Turn: value.GetTurn(), Phase: phase, CurrentUserID: value.GetCurrentUserId(), SourceUserID: value.GetSourceUserId(), TargetUserID: value.GetTargetUserId(), PendingResult: pending, DeadlineUnixMillis: value.GetDeadlineUnixMillis()}, nil
}

func phaseToProto(value engine.Phase) dice789v1.Phase {
	switch value {
	case engine.PhaseAwaitingRoll:
		return dice789v1.Phase_PHASE_AWAITING_ROLL
	case engine.PhaseResultPending:
		return dice789v1.Phase_PHASE_RESULT_PENDING
	case engine.PhaseAwaitingAdd:
		return dice789v1.Phase_PHASE_AWAITING_ADD
	case engine.PhaseAwaitingTarget:
		return dice789v1.Phase_PHASE_AWAITING_TARGET
	case engine.PhaseAwaitingContinue:
		return dice789v1.Phase_PHASE_AWAITING_CONTINUE
	case engine.PhaseFinished:
		return dice789v1.Phase_PHASE_FINISHED
	default:
		return dice789v1.Phase_PHASE_UNSPECIFIED
	}
}

func phaseFromProto(value dice789v1.Phase) (engine.Phase, bool) {
	switch value {
	case dice789v1.Phase_PHASE_AWAITING_ROLL:
		return engine.PhaseAwaitingRoll, true
	case dice789v1.Phase_PHASE_RESULT_PENDING:
		return engine.PhaseResultPending, true
	case dice789v1.Phase_PHASE_AWAITING_ADD:
		return engine.PhaseAwaitingAdd, true
	case dice789v1.Phase_PHASE_AWAITING_TARGET:
		return engine.PhaseAwaitingTarget, true
	case dice789v1.Phase_PHASE_AWAITING_CONTINUE:
		return engine.PhaseAwaitingContinue, true
	case dice789v1.Phase_PHASE_FINISHED:
		return engine.PhaseFinished, true
	default:
		return 0, false
	}
}

func continueModeToProto(value engine.ContinueMode) dice789v1.ContinueMode {
	switch value {
	case engine.ContinueOptional:
		return dice789v1.ContinueMode_CONTINUE_MODE_OPTIONAL
	case engine.ContinueForcedReroll:
		return dice789v1.ContinueMode_CONTINUE_MODE_FORCED_REROLL
	case engine.ContinueForcedPass:
		return dice789v1.ContinueMode_CONTINUE_MODE_FORCED_PASS
	default:
		return dice789v1.ContinueMode_CONTINUE_MODE_UNSPECIFIED
	}
}

func continueModeFromProto(value dice789v1.ContinueMode) (engine.ContinueMode, bool) {
	switch value {
	case dice789v1.ContinueMode_CONTINUE_MODE_OPTIONAL:
		return engine.ContinueOptional, true
	case dice789v1.ContinueMode_CONTINUE_MODE_FORCED_REROLL:
		return engine.ContinueForcedReroll, true
	case dice789v1.ContinueMode_CONTINUE_MODE_FORCED_PASS:
		return engine.ContinueForcedPass, true
	default:
		return 0, false
	}
}

func resultToProto(value engine.ResultKind, state engine.State) dice789v1.Effect {
	switch value {
	case engine.ResultSeven:
		return dice789v1.Effect_EFFECT_SUM_SEVEN_ADD
	case engine.ResultEight:
		return dice789v1.Effect_EFFECT_SUM_EIGHT_HALF_POOL
	case engine.ResultNine:
		return dice789v1.Effect_EFFECT_SUM_NINE_DRAIN_POOL
	case engine.ResultDoubleOne:
		return dice789v1.Effect_EFFECT_DOUBLE_ONE_TARGET_DRAIN
	case engine.ResultDoubleFour:
		return dice789v1.Effect_EFFECT_DOUBLE_FOUR_HALF_POOL_REROLL
	case engine.ResultDoubleSix:
		return dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD
	case engine.ResultOrdinaryPair:
		active := 0
		for _, player := range state.Players {
			if player.Active {
				active++
			}
		}
		if active >= 3 && state.Config.OrdinaryPairsReverse {
			return dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE
		}
		return dice789v1.Effect_EFFECT_ORDINARY_PAIR_REROLL

	case engine.ResultDropped:
		return dice789v1.Effect_EFFECT_DROPPED_DRAIN_POOL
	case engine.ResultOther, engine.ResultRollTimeout, engine.ResultCancelled:
		return dice789v1.Effect_EFFECT_PASS
	default:
		return dice789v1.Effect_EFFECT_UNSPECIFIED
	}
}

func resultFromProto(value dice789v1.Effect, phase engine.Phase) (engine.ResultKind, bool) {
	switch value {
	case dice789v1.Effect_EFFECT_UNSPECIFIED:
		return engine.ResultNone, phase == engine.PhaseAwaitingRoll || phase == engine.PhaseFinished
	case dice789v1.Effect_EFFECT_PASS:
		return engine.ResultOther, true
	case dice789v1.Effect_EFFECT_SUM_SEVEN_ADD:
		return engine.ResultSeven, true
	case dice789v1.Effect_EFFECT_SUM_EIGHT_HALF_POOL:
		return engine.ResultEight, true
	case dice789v1.Effect_EFFECT_SUM_NINE_DRAIN_POOL:
		return engine.ResultNine, true
	case dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE, dice789v1.Effect_EFFECT_ORDINARY_PAIR_REROLL:
		return engine.ResultOrdinaryPair, true
	case dice789v1.Effect_EFFECT_DOUBLE_ONE_TARGET_DRAIN:
		return engine.ResultDoubleOne, true
	case dice789v1.Effect_EFFECT_DOUBLE_FOUR_HALF_POOL_REROLL:
		return engine.ResultDoubleFour, true
	case dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD:
		return engine.ResultDoubleSix, true
	case dice789v1.Effect_EFFECT_DROPPED_DRAIN_POOL:
		return engine.ResultDropped, true
	default:
		return engine.ResultNone, false
	}
}

func outcomeToProto(value engine.TurnSettlement) dice789v1.TurnOutcome {
	if strings.Contains(value.Reason, "reroll") || value.Result == engine.ResultDoubleFour ||
		value.Result == engine.ResultOrdinaryPair && value.NextUserID == value.SourceUserID {
		return dice789v1.TurnOutcome_TURN_OUTCOME_REROLL
	}
	if strings.Contains(value.Reason, "revoked") || value.Result == engine.ResultCancelled {
		return dice789v1.TurnOutcome_TURN_OUTCOME_SOURCE_REVOKED
	}
	if value.Result == engine.ResultDoubleOne {
		return dice789v1.TurnOutcome_TURN_OUTCOME_TARGET_TAKES_TURN
	}
	return dice789v1.TurnOutcome_TURN_OUTCOME_PASS
}

func causeToProto(reason string) dice789v1.ResolutionCause {
	switch {
	case strings.Contains(reason, "timeout"):
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT
	case reason == "confirmed":
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_CONFIRMED
	case reason == "dropped":
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_DROP_REPORTED
	case strings.Contains(reason, "revoked") || reason == "cancelled":
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_PARTICIPANT_REVOKED
	default:
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_ACTION
	}
}
