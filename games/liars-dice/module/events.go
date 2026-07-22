package module

import (
	"github.com/iFTY-R/game-night/games/liars-dice/engine"
	liarsdicev1 "github.com/iFTY-R/game-night/games/liars-dice/gen/go/game/liars_dice/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
	"google.golang.org/protobuf/proto"
)

func encodeEvents(facts []engine.Event) ([]game.Event, error) {
	if len(facts) == 0 {
		return nil, malformed("transition has no engine events")
	}
	events := make([]game.Event, len(facts))
	for index, fact := range facts {
		message, err := encodeEvent(fact)
		if err != nil {
			return nil, err
		}
		events[index] = game.Event{Message: message}
	}
	return events, nil
}

func encodeEvent(fact engine.Event) (game.Message, error) {
	var messageType game.Identifier
	var message proto.Message
	switch fact.Kind {
	case engine.EventRoundStarted:
		if fact.Round == 0 || fact.FirstActor == "" {
			return game.Message{}, malformed("round start event is incomplete")
		}
		messageType, message = EventRoundStartedMessage, &liarsdicev1.RoundStarted{
			Round: fact.Round, FirstActorUserId: fact.FirstActor, Players: replayPlayersToProto(fact.Players),
		}
	case engine.EventBidPlaced:
		if fact.Bid == nil || fact.UserID == "" {
			return game.Message{}, malformed("bid event is incomplete")
		}
		messageType, message = EventBidPlacedMessage, &liarsdicev1.BidPlaced{UserId: fact.UserID, Bid: bidToProto(fact.Bid)}
	case engine.EventDiceRevealed:
		if len(fact.Dice) == 0 {
			return game.Message{}, malformed("reveal event is incomplete")
		}
		messageType, message = EventDiceRevealedMessage, &liarsdicev1.DiceRevealed{Dice: rollsToProto(fact.Dice)}
	case engine.EventRoundSettled:
		if fact.LoserUserID == "" || fact.NextRound == 0 {
			return game.Message{}, malformed("settlement event is incomplete")
		}
		messageType, message = EventRoundSettledMessage, &liarsdicev1.RoundSettled{
			LoserUserId: fact.LoserUserID, PenaltyTicks: uint32(fact.PenaltyTicks), NextRound: fact.NextRound,
			ActualQuantity: fact.ActualQuantity, Reason: fact.Reason, OpenerUserId: fact.OpenerUserID, Bid: bidToProto(fact.Bid),
		}
	case engine.EventParticipantRevoked:
		if fact.UserID == "" {
			return game.Message{}, malformed("revocation event is incomplete")
		}
		messageType, message = EventParticipantRevokedMessage, &liarsdicev1.ParticipantRevoked{UserId: fact.UserID}
	case engine.EventSessionFinished:
		if fact.Reason == "" {
			return game.Message{}, malformed("finish event is incomplete")
		}
		messageType, message = EventSessionFinishedMessage, &liarsdicev1.SessionFinished{Reason: fact.Reason}
	default:
		return game.Message{}, malformed("unknown engine event")
	}
	payload, err := marshalDeterministic(message)
	if err != nil {
		return game.Message{}, malformed("event encoding failed")
	}
	return game.Message{MessageType: messageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// decodeEvents reconstructs round context omitted from compact event payloads.
func decodeEvents(events []game.Event) ([]engine.Event, error) {
	decoded := make([]engine.Event, 0, len(events))
	var currentRound uint32
	for _, event := range events {
		if !event.Valid() || event.Message.SchemaVersion != ProtocolSchemaVersion {
			return nil, malformed("event envelope is invalid")
		}
		fact, round, err := decodeEvent(event.Message, currentRound)
		if err != nil {
			return nil, err
		}
		if round != 0 {
			currentRound = round
		}
		decoded = append(decoded, fact)
	}
	return decoded, nil
}

func validateVersionedEvents(events []game.VersionedEvent) error {
	for _, event := range events {
		if !event.Valid() {
			return malformed("versioned event is invalid")
		}
		if err := validateEventMessage(event.Event.Message); err != nil {
			return err
		}
	}
	return nil
}

// validateEventMessage validates a delta event without assuming the caller's
// cursor begins at a round.started event. Replay decoding performs the stronger
// ordered round-context checks separately.
func validateEventMessage(message game.Message) error {
	if message.SchemaVersion != ProtocolSchemaVersion {
		return malformed("event schema version is unsupported")
	}
	switch message.MessageType {
	case EventRoundStartedMessage:
		var value liarsdicev1.RoundStarted
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetRound() == 0 || value.GetFirstActorUserId() == "" {
			return malformed("round.started event is invalid")
		}
		if _, err := replayPlayersFromProto(value.GetPlayers()); err != nil {
			return err
		}
	case EventBidPlacedMessage:
		var value liarsdicev1.BidPlaced
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetUserId() == "" || value.GetBid() == nil {
			return malformed("bid.placed event is invalid")
		}
		if _, err := bidFromProto(value.GetBid()); err != nil {
			return err
		}
	case EventDiceRevealedMessage:
		var value liarsdicev1.DiceRevealed
		if err := unmarshalStrict(message.Payload, &value); err != nil || len(value.GetDice()) == 0 {
			return malformed("dice.revealed event is invalid")
		}
		if _, err := rollsFromProto(value.GetDice()); err != nil {
			return err
		}
	case EventRoundSettledMessage:
		var value liarsdicev1.RoundSettled
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetNextRound() == 0 || value.GetLoserUserId() == "" {
			return malformed("round.settled event is invalid")
		}
		if value.GetReason() != string(engine.SettlementOpened) && value.GetReason() != string(engine.SettlementTimeout) ||
			value.GetReason() == string(engine.SettlementOpened) && (value.GetBid() == nil || value.GetOpenerUserId() == "") ||
			value.GetReason() == string(engine.SettlementTimeout) && (value.GetOpenerUserId() != "" || value.GetActualQuantity() != 0) {
			return malformed("round.settled event reason is invalid")
		}
		if value.GetBid() != nil {
			if _, err := bidFromProto(value.GetBid()); err != nil {
				return err
			}
		}
	case EventParticipantRevokedMessage:
		var value liarsdicev1.ParticipantRevoked
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetUserId() == "" {
			return malformed("participant.revoked event is invalid")
		}
	case EventSessionFinishedMessage:
		var value liarsdicev1.SessionFinished
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetReason() == "" {
			return malformed("session.finished event is invalid")
		}
	default:
		return malformed("unknown event message type")
	}
	return nil
}

func decodeEvent(message game.Message, currentRound uint32) (engine.Event, uint32, error) {
	if message.SchemaVersion != ProtocolSchemaVersion {
		return engine.Event{}, 0, malformed("event schema version is unsupported")
	}
	switch message.MessageType {
	case EventRoundStartedMessage:
		var value liarsdicev1.RoundStarted
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetRound() == 0 || value.GetFirstActorUserId() == "" {
			return engine.Event{}, 0, malformed("round.started event is invalid")
		}
		players, err := replayPlayersFromProto(value.GetPlayers())
		if err != nil {
			return engine.Event{}, 0, err
		}
		return engine.Event{Kind: engine.EventRoundStarted, Round: value.GetRound(), FirstActor: value.GetFirstActorUserId(), Players: players}, value.GetRound(), nil
	case EventBidPlacedMessage:
		var value liarsdicev1.BidPlaced
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetUserId() == "" || value.GetBid() == nil {
			return engine.Event{}, 0, malformed("bid.placed event is invalid")
		}
		bid, err := bidFromProto(value.GetBid())
		if err != nil {
			return engine.Event{}, 0, err
		}
		if currentRound == 0 {
			return engine.Event{}, 0, malformed("bid.placed event has no round anchor")
		}
		return engine.Event{Kind: engine.EventBidPlaced, Round: currentRound, UserID: value.GetUserId(), Bid: &bid}, currentRound, nil
	case EventDiceRevealedMessage:
		var value liarsdicev1.DiceRevealed
		if err := unmarshalStrict(message.Payload, &value); err != nil || len(value.GetDice()) == 0 {
			return engine.Event{}, 0, malformed("dice.revealed event is invalid")
		}
		dice, err := rollsFromProto(value.GetDice())
		if err != nil || currentRound == 0 {
			if err != nil {
				return engine.Event{}, 0, err
			}
			return engine.Event{}, 0, malformed("dice.revealed event has no round anchor")
		}
		return engine.Event{Kind: engine.EventDiceRevealed, Round: currentRound, Dice: dice}, currentRound, nil
	case EventRoundSettledMessage:
		var value liarsdicev1.RoundSettled
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetNextRound() == 0 || value.GetLoserUserId() == "" {
			return engine.Event{}, 0, malformed("round.settled event is invalid")
		}
		if value.GetReason() != string(engine.SettlementOpened) && value.GetReason() != string(engine.SettlementTimeout) ||
			value.GetReason() == string(engine.SettlementOpened) && (value.GetBid() == nil || value.GetOpenerUserId() == "") ||
			value.GetReason() == string(engine.SettlementTimeout) && (value.GetOpenerUserId() != "" || value.GetActualQuantity() != 0) {
			return engine.Event{}, 0, malformed("round.settled event reason is invalid")
		}
		var bid *engine.Bid
		if value.GetBid() != nil {
			decoded, err := bidFromProto(value.GetBid())
			if err != nil {
				return engine.Event{}, 0, err
			}
			bid = &decoded
		}
		round := value.GetNextRound() - 1
		if currentRound != 0 && round != currentRound {
			return engine.Event{}, 0, malformed("settlement event round does not match the active replay round")
		}
		return engine.Event{Kind: engine.EventRoundSettled, Round: round, LoserUserID: value.GetLoserUserId(), PenaltyTicks: dice.Ticks(value.GetPenaltyTicks()), NextRound: value.GetNextRound(), ActualQuantity: value.GetActualQuantity(), Reason: value.GetReason(), OpenerUserID: value.GetOpenerUserId(), Bid: bid}, round, nil
	case EventParticipantRevokedMessage:
		var value liarsdicev1.ParticipantRevoked
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetUserId() == "" {
			return engine.Event{}, 0, malformed("participant.revoked event is invalid")
		}
		return engine.Event{Kind: engine.EventParticipantRevoked, Round: currentRound, UserID: value.GetUserId()}, currentRound, nil
	case EventSessionFinishedMessage:
		var value liarsdicev1.SessionFinished
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetReason() == "" {
			return engine.Event{}, 0, malformed("session.finished event is invalid")
		}
		return engine.Event{Kind: engine.EventSessionFinished, Round: currentRound, Reason: value.GetReason()}, currentRound, nil
	default:
		return engine.Event{}, 0, malformed("unknown event message type")
	}
}

func replayPlayersToProto(players []engine.Participant) []*liarsdicev1.ReplayPlayer {
	values := make([]*liarsdicev1.ReplayPlayer, len(players))
	for index, player := range players {
		values[index] = &liarsdicev1.ReplayPlayer{UserId: player.UserID, SeatIndex: player.SeatIndex}
	}
	return values
}

// replayPlayersFromProto accepts an empty roster for sessions written before the additive replay field existed.
func replayPlayersFromProto(values []*liarsdicev1.ReplayPlayer) ([]engine.Participant, error) {
	if len(values) == 0 {
		return nil, nil
	}
	players := make([]engine.Participant, len(values))
	for index, value := range values {
		if value == nil {
			return nil, malformed("round.started replay roster is invalid")
		}
		players[index] = engine.Participant{UserID: value.GetUserId(), SeatIndex: value.GetSeatIndex()}
	}
	if err := engine.ValidateParticipants(players); err != nil {
		return nil, malformed("round.started replay roster is invalid")
	}
	return players, nil
}
