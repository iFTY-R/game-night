package module

import (
	"math"

	"github.com/iFTY-R/game-night/games/liars-dice/engine"
	liarsdicev1 "github.com/iFTY-R/game-night/games/liars-dice/gen/go/game/liars_dice/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

// EncodeConfig returns a canonical frozen configuration envelope.
func EncodeConfig(config engine.Config) (game.Message, error) {
	// MaximumPlayers validates every invariant while allowing the full legal
	// first-bid range. Creation applies the exact frozen participant count later.
	if err := config.Validate(engine.MaximumPlayers); err != nil {
		return game.Message{}, err
	}
	payload, err := marshalDeterministic(configToProto(config))
	if err != nil {
		return game.Message{}, malformed("config encoding failed")
	}
	return game.Message{MessageType: ConfigMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// EncodeConfigForPlayers validates the first-bid minimum against the frozen player count.
func EncodeConfigForPlayers(config engine.Config, playerCount int) (game.Message, error) {
	if err := config.Validate(playerCount); err != nil {
		return game.Message{}, err
	}
	payload, err := marshalDeterministic(configToProto(config))
	if err != nil {
		return game.Message{}, malformed("config encoding failed")
	}
	return game.Message{MessageType: ConfigMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// DecodeConfig decodes the opaque room config and applies the complete default
// ruleset when the payload is empty. A non-empty payload must be canonical.
func DecodeConfig(message game.Message, playerCount int) (engine.Config, error) {
	if message.MessageType != ConfigMessageType || message.SchemaVersion != ProtocolSchemaVersion {
		return engine.Config{}, malformed("config envelope is not owned by liars-dice")
	}
	if len(message.Payload) == 0 {
		config := engine.DefaultConfig(uint32(playerCount))
		if err := config.Validate(playerCount); err != nil {
			return engine.Config{}, err
		}
		return config, nil
	}
	var value liarsdicev1.Config
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

// EncodeState returns the canonical authoritative state envelope. It is kept
// exported for deterministic snapshot fixtures and migration tests.
func EncodeState(state engine.State) (game.Message, error) {
	if err := state.Validate(); err != nil {
		return game.Message{}, err
	}
	payload, err := marshalDeterministic(stateToProto(state))
	if err != nil {
		return game.Message{}, malformed("state encoding failed")
	}
	return game.Message{MessageType: StateMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// DecodeState strictly decodes a state envelope before allowing rules or projections to use it.
func DecodeState(message game.Message) (engine.State, error) {
	if message.MessageType != StateMessageType || message.SchemaVersion != ProtocolSchemaVersion {
		return engine.State{}, malformed("state envelope is not owned by liars-dice")
	}
	var value liarsdicev1.State
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
	return state, nil
}

func configToProto(config engine.Config) *liarsdicev1.Config {
	return &liarsdicev1.Config{
		DicePerPlayer:        config.DicePerPlayer,
		OnesWild:             config.OnesWild,
		StrictEnabled:        config.StrictEnabled,
		FlyingEnabled:        config.FlyingEnabled,
		FirstBidMinimum:      config.FirstBidMinimum,
		PenaltyTicks:         uint32(config.PenaltyTicks),
		ActionTimeoutSeconds: config.ActionTimeoutSeconds,
	}
}

func configFromProto(value *liarsdicev1.Config) (engine.Config, error) {
	if value == nil {
		return engine.Config{}, malformed("config message is missing")
	}
	return engine.Config{
		DicePerPlayer: value.GetDicePerPlayer(), OnesWild: value.GetOnesWild(),
		StrictEnabled: value.GetStrictEnabled(), FlyingEnabled: value.GetFlyingEnabled(),
		FirstBidMinimum: value.GetFirstBidMinimum(), PenaltyTicks: dice.Ticks(value.GetPenaltyTicks()),
		ActionTimeoutSeconds: value.GetActionTimeoutSeconds(),
	}, nil
}

func stateToProto(state engine.State) *liarsdicev1.State {
	players := make([]*liarsdicev1.PlayerState, len(state.Players))
	for index, player := range state.Players {
		players[index] = &liarsdicev1.PlayerState{
			UserId: player.UserID, SeatIndex: player.SeatIndex, Active: player.Active,
			PenaltyTicks: uint32(player.PenaltyTicks),
		}
	}
	value := &liarsdicev1.State{
		SchemaVersion: state.SchemaVersion,
		Phase:         phaseToProto(state.Phase), Round: state.Round, Players: players,
		FirstActorUserId: state.FirstActorUserID, CurrentActorUserId: state.CurrentActorUserID,
		LastBidderUserId:         state.LastBidderUserID,
		ActionDeadlineUnixMillis: state.ActionDeadlineUnixMillis,
		PrivateDice:              rollsToProto(state.PrivateDice), Config: configToProto(state.Config),
		LastRevealedDice: rollsToProto(state.LastSettlement.RevealedDice),
		FinishReason:     state.FinishReason,
	}
	if state.CurrentBid != nil {
		value.CurrentBid = bidToProto(state.CurrentBid)
		value.HasCurrentBid = true
	}
	if state.LastSettlement.Round != 0 {
		value.LastSettlement = settlementToProto(state.LastSettlement)
	}
	return value
}

func stateFromProto(value *liarsdicev1.State) (engine.State, error) {
	if value == nil || value.GetSchemaVersion() != engine.CurrentSchemaVersion || value.GetRound() == 0 || value.GetConfig() == nil {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state header is malformed"}
	}
	if value.GetHasCurrentBid() != (value.GetCurrentBid() != nil) {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "bid presence marker disagrees with payload"}
	}
	config, err := configFromProto(value.GetConfig())
	if err != nil {
		return engine.State{}, err
	}
	players := make([]engine.PlayerState, len(value.GetPlayers()))
	for index, player := range value.GetPlayers() {
		if player == nil {
			return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state contains a nil player"}
		}
		players[index] = engine.PlayerState{UserID: player.GetUserId(), SeatIndex: player.GetSeatIndex(), Active: player.GetActive(), PenaltyTicks: dice.Ticks(player.GetPenaltyTicks())}
	}
	privateDice, err := rollsFromProto(value.GetPrivateDice())
	if err != nil {
		return engine.State{}, err
	}
	revealed, err := rollsFromProto(value.GetLastRevealedDice())
	if err != nil {
		return engine.State{}, err
	}
	state := engine.State{
		SchemaVersion: value.GetSchemaVersion(), Phase: phaseFromProto(value.GetPhase()), Round: value.GetRound(),
		Config: config, Players: players, FirstActorUserID: value.GetFirstActorUserId(),
		CurrentActorUserID: value.GetCurrentActorUserId(), LastBidderUserID: value.GetLastBidderUserId(),
		ActionDeadlineUnixMillis: value.GetActionDeadlineUnixMillis(), PrivateDice: privateDice,
		FinishReason: value.GetFinishReason(),
	}
	if value.GetCurrentBid() != nil {
		bid, bidErr := bidFromProto(value.GetCurrentBid())
		if bidErr != nil {
			return engine.State{}, bidErr
		}
		state.CurrentBid = &bid
	}
	if value.GetLastSettlement() != nil {
		settlement, settlementErr := settlementFromProto(value.GetLastSettlement(), revealed)
		if settlementErr != nil {
			return engine.State{}, settlementErr
		}
		state.LastSettlement = settlement
	} else if len(revealed) != 0 {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "revealed dice have no settlement"}
	}
	return state, nil
}

func bidFromProto(value *liarsdicev1.Bid) (engine.Bid, error) {
	if value == nil {
		return engine.Bid{}, malformed("bid message is missing")
	}
	var mode engine.BidMode
	switch value.GetMode() {
	case liarsdicev1.BidMode_BID_MODE_FLYING:
		mode = engine.BidModeFlying
	case liarsdicev1.BidMode_BID_MODE_STRICT:
		mode = engine.BidModeStrict
	default:
		return engine.Bid{}, &engine.RuleError{Code: engine.CodeInvalidBidMode, Detail: "bid mode is unknown"}
	}
	return engine.Bid{Quantity: value.GetQuantity(), Face: value.GetFace(), Mode: mode}, nil
}

func bidToProto(value *engine.Bid) *liarsdicev1.Bid {
	if value == nil {
		return nil
	}
	mode := liarsdicev1.BidMode_BID_MODE_FLYING
	if value.Mode == engine.BidModeStrict {
		mode = liarsdicev1.BidMode_BID_MODE_STRICT
	}
	return &liarsdicev1.Bid{Quantity: value.Quantity, Face: value.Face, Mode: mode}
}

func settlementFromProto(value *liarsdicev1.RoundSettled, revealed []engine.PrivateRoll) (engine.Settlement, error) {
	if value == nil || value.GetNextRound() == 0 || value.GetLoserUserId() == "" || value.GetReason() == "" {
		return engine.Settlement{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "settlement is malformed"}
	}
	var bid *engine.Bid
	if value.GetBid() != nil {
		decoded, err := bidFromProto(value.GetBid())
		if err != nil {
			return engine.Settlement{}, err
		}
		bid = &decoded
	}
	return engine.Settlement{
		Round: value.GetNextRound() - 1, LoserUserID: value.GetLoserUserId(), PenaltyTicks: dice.Ticks(value.GetPenaltyTicks()),
		ActualQuantity: value.GetActualQuantity(), Reason: engine.SettlementReason(value.GetReason()),
		OpenerUserID: value.GetOpenerUserId(), Bid: bid, RevealedDice: revealed,
	}, nil
}

func settlementToProto(value engine.Settlement) *liarsdicev1.RoundSettled {
	if value.Round == 0 || value.LoserUserID == "" {
		return nil
	}
	return &liarsdicev1.RoundSettled{
		LoserUserId: value.LoserUserID, PenaltyTicks: uint32(value.PenaltyTicks), NextRound: value.Round + 1,
		ActualQuantity: value.ActualQuantity, Reason: string(value.Reason), OpenerUserId: value.OpenerUserID, Bid: bidToProto(value.Bid),
	}
}

func rollsFromProto(values []*liarsdicev1.PrivateDice) ([]engine.PrivateRoll, error) {
	if len(values) == 0 {
		return nil, nil
	}
	rolls := make([]engine.PrivateRoll, len(values))
	for index, value := range values {
		if value == nil || value.GetUserId() == "" || len(value.GetFaces()) == 0 {
			return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "private dice entry is malformed"}
		}
		faces := make([]dice.Face, len(value.GetFaces()))
		for faceIndex, face := range value.GetFaces() {
			if face > math.MaxUint8 || !dice.Face(face).Valid() {
				return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "private dice contains an invalid face"}
			}
			faces[faceIndex] = dice.Face(face)
		}
		rolls[index] = engine.PrivateRoll{UserID: value.GetUserId(), Faces: faces}
	}
	return rolls, nil
}

func rollsToProto(values []engine.PrivateRoll) []*liarsdicev1.PrivateDice {
	rolls := make([]*liarsdicev1.PrivateDice, len(values))
	for index, value := range values {
		faces := make([]uint32, len(value.Faces))
		for faceIndex, face := range value.Faces {
			faces[faceIndex] = uint32(face)
		}
		rolls[index] = &liarsdicev1.PrivateDice{UserId: value.UserID, Faces: faces}
	}
	return rolls
}

func phaseFromProto(value liarsdicev1.Phase) engine.Phase {
	switch value {
	case liarsdicev1.Phase_PHASE_BIDDING:
		return engine.PhaseBidding
	case liarsdicev1.Phase_PHASE_FINISHED:
		return engine.PhaseFinished
	default:
		return 0
	}
}

func phaseToProto(value engine.Phase) liarsdicev1.Phase {
	switch value {
	case engine.PhaseBidding:
		return liarsdicev1.Phase_PHASE_BIDDING
	case engine.PhaseFinished:
		return liarsdicev1.Phase_PHASE_FINISHED
	default:
		return liarsdicev1.Phase_PHASE_UNSPECIFIED
	}
}

func actionTimerToProto(value *engine.ActionTimer) *liarsdicev1.ActionTimer {
	if value == nil {
		return nil
	}
	return &liarsdicev1.ActionTimer{Round: value.Round, UserId: value.UserID, DeadlineUnixMillis: value.DeadlineUnixMillis}
}

func actionTimerFromProto(value *liarsdicev1.ActionTimer) (engine.ActionTimer, error) {
	if value == nil || value.GetRound() == 0 || value.GetUserId() == "" || value.GetDeadlineUnixMillis() == 0 {
		return engine.ActionTimer{}, &engine.RuleError{Code: engine.CodeTimerMismatch, Detail: "timer fields are incomplete"}
	}
	return engine.ActionTimer{Round: value.GetRound(), UserID: value.GetUserId(), DeadlineUnixMillis: value.GetDeadlineUnixMillis()}, nil
}
