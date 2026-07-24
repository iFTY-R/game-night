package module

import (
	"github.com/iFTY-R/game-night/games/three-rounds/engine"
	threeroundsv1 "github.com/iFTY-R/game-night/games/three-rounds/gen/go/game/three_rounds/v1"
	"github.com/iFTY-R/game-night/games/three-rounds/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

func encodeEvents(facts []engine.Event) ([]game.Event, error) {
	if len(facts) == 0 {
		return nil, malformed("transition has no engine events")
	}
	events := make([]game.Event, 0, len(facts))
	for _, fact := range facts {
		message, err := encodeEvent(fact)
		if err != nil {
			return nil, err
		}
		events = append(events, game.Event{Message: message})
	}
	return events, nil
}

func encodeEvent(fact engine.Event) (game.Message, error) {
	var (
		messageType game.Identifier
		payloadMsg  proto.Message
	)
	event := &threeroundsv1.Event{}
	switch fact.Kind {
	case engine.EventSessionStarted:
		started := &threeroundsv1.SessionStarted{Config: configToProto(*fact.Config), HostUserId: fact.HostUserID}
		for _, participant := range canonicalParticipants(fact.Participants) {
			started.Players = append(started.Players, &threeroundsv1.ReplayPlayer{UserId: participant.UserID, SeatIndex: participant.SeatIndex})
		}
		event.Event = &threeroundsv1.Event_SessionStarted{SessionStarted: started}
		messageType, payloadMsg = projection.EventSessionStartedMessage, event
	case engine.EventCardsDealt:
		dealt := &threeroundsv1.CardsDealt{}
		for _, deal := range fact.Deals {
			dealt.Deals = append(dealt.Deals, &threeroundsv1.PlayerDeal{UserId: deal.UserID, InitialHand: cardIDsToStrings(deal.InitialHand)})
		}
		event.Event = &threeroundsv1.Event_CardsDealt{CardsDealt: dealt}
		messageType, payloadMsg = projection.EventCardsDealtMessage, event
	case engine.EventPhaseStarted:
		event.Event = &threeroundsv1.Event_PhaseStarted{PhaseStarted: &threeroundsv1.PhaseStarted{
			Phase: phaseToProto(fact.Phase), Round: fact.Round, DeadlineUnixMillis: fact.DeadlineUnixMillis, PhaseGeneration: fact.PhaseGeneration,
		}}
		messageType, payloadMsg = projection.EventPhaseStartedMessage, event
	case engine.EventSelectionSubmitted:
		event.Event = &threeroundsv1.Event_SelectionSubmitted{SelectionSubmitted: &threeroundsv1.SelectionSubmitted{UserId: fact.UserID, Round: fact.Round}}
		messageType, payloadMsg = projection.EventSelectionSubmittedMessage, event
	case engine.EventSelectionAutoSubmitted:
		event.Event = &threeroundsv1.Event_SelectionAutoSubmitted{SelectionAutoSubmitted: &threeroundsv1.SelectionAutoSubmitted{UserId: fact.UserID, Round: fact.Round}}
		messageType, payloadMsg = projection.EventSelectionAutoSubmittedMessage, event
	case engine.EventRoundRevealed:
		event.Event = &threeroundsv1.Event_RoundRevealed{RoundRevealed: &threeroundsv1.RoundRevealed{Round: fact.Summary.Round, Reveals: roundSummaryToProto(fact.Summary).GetReveals()}}
		messageType, payloadMsg = projection.EventRoundRevealedMessage, event
	case engine.EventRoundSettled:
		event.Event = &threeroundsv1.Event_RoundSettled{RoundSettled: &threeroundsv1.RoundSettled{Summary: roundSummaryToProto(fact.Summary)}}
		messageType, payloadMsg = projection.EventRoundSettledMessage, event
	case engine.EventWildcardResolved:
		event.Event = &threeroundsv1.Event_WildcardResolved{WildcardResolved: &threeroundsv1.WildcardResolved{UserId: fact.UserID, Resolution: wildcardResolutionToProto(fact.WildcardResolution)}}
		messageType, payloadMsg = projection.EventWildcardResolvedMessage, event
	case engine.EventParticipantRevoked:
		event.Event = &threeroundsv1.Event_ParticipantRevoked{ParticipantRevoked: &threeroundsv1.ParticipantRevoked{UserId: fact.UserID}}
		messageType, payloadMsg = EventParticipantRevokedMessage, event
	case engine.EventSessionFinished:
		event.Event = &threeroundsv1.Event_SessionFinished{SessionFinished: &threeroundsv1.SessionFinished{
			Reason: finishReasonToProto(fact.FinishReason), OperatorUserId: fact.OperatorUserID,
		}}
		messageType, payloadMsg = projection.EventSessionFinishedMessage, event
	default:
		return game.Message{}, malformed("unknown engine event kind")
	}
	payload, err := marshalDeterministic(payloadMsg)
	if err != nil {
		return game.Message{}, malformed("event encoding failed")
	}
	return game.Message{MessageType: messageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

func decodeEvents(events []game.Event) ([]game.Event, error) {
	for _, event := range events {
		if err := projectionValidateEvent(event.Message); err != nil {
			return nil, err
		}
	}
	return events, nil
}
