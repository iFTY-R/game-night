package projection

import (
	"github.com/iFTY-R/game-night/games/liars-dice/engine"
	liarsdicev1 "github.com/iFTY-R/game-night/games/liars-dice/gen/go/game/liars_dice/v1"
)

// BuildReplay reduces ordered module events into settled rounds and never forwards raw event payloads.
func BuildReplay(events []engine.Event) (*liarsdicev1.Replay, error) {
	if len(events) == 0 {
		return nil, projectionError("replay event stream is empty")
	}
	replay := &liarsdicev1.Replay{}
	var current *liarsdicev1.ReplayRound
	var lastRound uint32
	started := false
	finished := false
	for _, event := range events {
		if finished {
			return nil, projectionError("event appears after session finish")
		}
		if !started && event.Kind != engine.EventRoundStarted {
			return nil, projectionError("replay does not begin with a round")
		}
		switch event.Kind {
		case engine.EventRoundStarted:
			if event.Round == 0 || event.Round <= lastRound || event.FirstActor == "" || current != nil && current.DiceRevealed {
				return nil, projectionError("round start does not advance a valid replay lifecycle")
			}
			// A current-actor revocation cancels an un-revealed accumulator.
			current = &liarsdicev1.ReplayRound{Round: event.Round, FirstActorUserId: event.FirstActor}
			lastRound = event.Round
			started = true
		case engine.EventBidPlaced:
			if current == nil || current.Round != event.Round || event.UserID == "" || event.Bid == nil || current.DiceRevealed {
				return nil, projectionError("bid event is outside a replay round")
			}
			current.Bids = append(current.Bids, &liarsdicev1.ReplayBid{UserId: event.UserID, Bid: bidToProto(event.Bid)})
		case engine.EventDiceRevealed:
			if current == nil || current.Round != event.Round || current.DiceRevealed || len(event.Dice) == 0 {
				return nil, projectionError("reveal event is outside a replay round")
			}
			current.Dice = rollsToProto(event.Dice)
			current.DiceRevealed = true
		case engine.EventRoundSettled:
			if current == nil || current.Round != event.Round {
				return nil, projectionError("settlement event is outside a replay round")
			}
			if event.Reason != string(engine.SettlementOpened) && event.Reason != string(engine.SettlementTimeout) {
				return nil, projectionError("settlement reason is unknown")
			}
			if event.Reason == string(engine.SettlementOpened) && !current.DiceRevealed ||
				event.Reason == string(engine.SettlementTimeout) && current.DiceRevealed {
				return nil, projectionError("settlement reveal policy is inconsistent")
			}
			current.LoserUserId = event.LoserUserID
			current.PenaltyTicks = uint32(event.PenaltyTicks)
			current.ActualQuantity = event.ActualQuantity
			current.Reason = event.Reason
			current.OpenerUserId = event.OpenerUserID
			current.Bid = bidToProto(event.Bid)
			replay.Rounds = append(replay.Rounds, current)
			current = nil
		case engine.EventParticipantRevoked:
			if event.UserID == "" {
				return nil, projectionError("revocation user is missing")
			}
			if !contains(replay.RevokedUserIds, event.UserID) {
				replay.RevokedUserIds = append(replay.RevokedUserIds, event.UserID)
			}
		case engine.EventSessionFinished:
			if event.Reason == "" {
				return nil, projectionError("finish reason is missing")
			}
			replay.FinishReason = event.Reason
			if current != nil && current.DiceRevealed {
				return nil, projectionError("session finished after an un-settled reveal")
			}
			current = nil
			finished = true
		default:
			return nil, projectionError("unknown event cannot enter replay")
		}
	}
	return replay, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
