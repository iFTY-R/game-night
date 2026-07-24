package projection

import (
	"cmp"
	"slices"

	"github.com/iFTY-R/game-night/games/three-rounds/engine"
	threeroundsv1 "github.com/iFTY-R/game-night/games/three-rounds/gen/go/game/three_rounds/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

const (
	EventSessionStartedMessage         game.Identifier = "session.started"
	EventCardsDealtMessage             game.Identifier = "cards.dealt"
	EventPhaseStartedMessage           game.Identifier = "phase.started"
	EventSelectionSubmittedMessage     game.Identifier = "selection.submitted"
	EventSelectionAutoSubmittedMessage game.Identifier = "selection.auto_submitted"
	EventRoundRevealedMessage          game.Identifier = "round.revealed"
	EventRoundSettledMessage           game.Identifier = "round.settled"
	EventWildcardResolvedMessage       game.Identifier = "wildcard.resolved"
	EventParticipantRevokedMessage     game.Identifier = "participant.revoked"
	EventSessionFinishedMessage        game.Identifier = "session.finished"
)

// ValidateEvent rejects malformed public/private event envelopes before live projections wrap a current view.
func ValidateEvent(message game.Message) error {
	if !message.Valid() || message.SchemaVersion != engine.CurrentSchemaVersion {
		return projectionError("event envelope is invalid")
	}
	_, _, err := decodeEventMessage(message)
	return err
}

// BuildReplay converts committed game events into one replay artifact without leaking hidden cards on abnormal endings.
func BuildReplay(events []game.Event, viewer game.Viewer, policy game.ReplayAccessPolicy) (*threeroundsv1.Replay, error) {
	if len(events) == 0 || !viewer.Valid() || viewer.Kind != game.ViewerReplay || !policy.Valid() {
		return nil, projectionError("replay request is invalid")
	}
	replay := &threeroundsv1.Replay{SchemaVersion: engine.CurrentSchemaVersion}
	var (
		config            *threeroundsv1.Config
		players           []*threeroundsv1.ReplayPlayer
		deals             = map[string][]string{}
		active            = map[string]bool{}
		scoreboard        = map[string]*scoreState{}
		finish            = threeroundsv1.FinishReason_FINISH_REASON_UNSPECIFIED
		finalResultPublic bool
		sequence          uint64
	)
	for _, raw := range events {
		if !raw.Valid() {
			return nil, projectionError("replay event envelope is invalid")
		}
		decoded, eventType, err := decodeEventMessage(raw.Message)
		if err != nil {
			return nil, err
		}
		switch eventType {
		case EventSessionStartedMessage:
			started := decoded.GetSessionStarted()
			config = started.GetConfig()
			players = cloneReplayPlayers(started.GetPlayers())
			for _, player := range players {
				active[player.GetUserId()] = true
				scoreboard[player.GetUserId()] = &scoreState{UserID: player.GetUserId(), SeatIndex: player.GetSeatIndex()}
			}
		case EventCardsDealtMessage:
			for _, deal := range decoded.GetCardsDealt().GetDeals() {
				deals[deal.GetUserId()] = append([]string(nil), deal.GetInitialHand()...)
			}
		case EventParticipantRevokedMessage:
			revoked := decoded.GetParticipantRevoked()
			active[revoked.GetUserId()] = false
		case EventPhaseStartedMessage:
			finalResultPublic = finalResultPublic || decoded.GetPhaseStarted().GetPhase() == threeroundsv1.Phase_PHASE_FINAL_RESULT
		case EventRoundSettledMessage:
			summary := decoded.GetRoundSettled().GetSummary()
			replay.Rounds = append(replay.Rounds, cloneRoundSummary(summary))
			accumulateScores(scoreboard, summary)
		case EventSessionFinishedMessage:
			finish = decoded.GetSessionFinished().GetReason()
		}
		if eventType != EventCardsDealtMessage {
			sequence++
			replay.Entries = append(replay.Entries, &threeroundsv1.ReplayEntry{Sequence: sequence, Event: decoded})
		}
	}
	replay.Config = config
	replay.Players = players
	if finish == threeroundsv1.FinishReason_FINISH_REASON_NORMAL_COMPLETED {
		for _, player := range replay.Players {
			player.InitialHand = append([]string(nil), deals[player.GetUserId()]...)
		}
		replay.FinalSummary = buildReplayFinalSummary(scoreboard, active)
	} else if finalResultPublic {
		replay.FinalSummary = buildReplayFinalSummary(scoreboard, active)
		if replay.FinalSummary != nil {
			replay.FinalSummary.WinnerUserIds = nil
			for _, standing := range replay.FinalSummary.GetStandings() {
				standing.Winner = false
			}
		}
	}
	replay.FinishReason = finish
	return replay, nil
}

type scoreState struct {
	UserID        string
	SeatIndex     uint32
	RoundOne      uint32
	RoundTwo      uint32
	RoundThree    uint32
	Total         uint32
	WonRoundOne   bool
	WonRoundTwo   bool
	WonRoundThree bool
}

func accumulateScores(scoreboard map[string]*scoreState, summary *threeroundsv1.RoundSummary) {
	if summary == nil {
		return
	}
	for _, reveal := range summary.GetReveals() {
		row := scoreboard[reveal.GetUserId()]
		if row == nil {
			row = &scoreState{UserID: reveal.GetUserId(), SeatIndex: reveal.GetSeatIndex()}
			scoreboard[reveal.GetUserId()] = row
		}
		switch summary.GetRound() {
		case 1:
			row.RoundOne += reveal.GetAwardedPoints()
			row.WonRoundOne = reveal.GetAwardedPoints() > 0
		case 2:
			row.RoundTwo += reveal.GetAwardedPoints()
			row.WonRoundTwo = reveal.GetAwardedPoints() > 0
		case 3:
			row.RoundThree += reveal.GetAwardedPoints()
			row.WonRoundThree = reveal.GetAwardedPoints() > 0
		}
		row.Total += reveal.GetAwardedPoints()
	}
}

func buildReplayFinalSummary(scoreboard map[string]*scoreState, active map[string]bool) *threeroundsv1.FinalSummary {
	standings := make([]*threeroundsv1.FinalStanding, 0, len(scoreboard))
	for _, row := range scoreboard {
		if !active[row.UserID] {
			continue
		}
		standings = append(standings, &threeroundsv1.FinalStanding{
			UserId:        row.UserID,
			SeatIndex:     row.SeatIndex,
			Active:        true,
			TotalPoints:   row.Total,
			WonRoundOne:   row.WonRoundOne,
			WonRoundTwo:   row.WonRoundTwo,
			WonRoundThree: row.WonRoundThree,
		})
	}
	slices.SortFunc(standings, func(left, right *threeroundsv1.FinalStanding) int {
		if diff := cmp.Compare(right.GetTotalPoints(), left.GetTotalPoints()); diff != 0 {
			return diff
		}
		if diff := cmp.Compare(boolToUint8(right.GetWonRoundThree()), boolToUint8(left.GetWonRoundThree())); diff != 0 {
			return diff
		}
		if diff := cmp.Compare(boolToUint8(right.GetWonRoundTwo()), boolToUint8(left.GetWonRoundTwo())); diff != 0 {
			return diff
		}
		if diff := cmp.Compare(boolToUint8(right.GetWonRoundOne()), boolToUint8(left.GetWonRoundOne())); diff != 0 {
			return diff
		}
		return cmp.Compare(left.GetSeatIndex(), right.GetSeatIndex())
	})
	if len(standings) == 0 {
		return nil
	}
	winnerIDs := make([]string, 0, len(standings))
	leader := standings[0]
	position := uint32(1)
	lastDistinct := uint32(1)
	for index := range standings {
		if index == 0 {
			standings[index].Rank = 1
		} else {
			position++
			if compareStandingProto(standings[index], standings[index-1]) != 0 {
				lastDistinct = position
			}
			standings[index].Rank = lastDistinct
		}
		if compareStandingProto(standings[index], leader) == 0 {
			standings[index].Winner = true
			winnerIDs = append(winnerIDs, standings[index].GetUserId())
		}
	}
	slices.Sort(winnerIDs)
	return &threeroundsv1.FinalSummary{Standings: standings, WinnerUserIds: winnerIDs}
}

func decodeEventMessage(message game.Message) (*threeroundsv1.Event, game.Identifier, error) {
	var event threeroundsv1.Event
	switch message.MessageType {
	case EventSessionStartedMessage, EventCardsDealtMessage, EventPhaseStartedMessage, EventSelectionSubmittedMessage,
		EventSelectionAutoSubmittedMessage, EventRoundRevealedMessage, EventRoundSettledMessage,
		EventWildcardResolvedMessage, EventParticipantRevokedMessage, EventSessionFinishedMessage:
		if err := unmarshalStrict(message.Payload, &event); err != nil || event.GetEvent() == nil {
			return nil, "", projectionError("event payload is invalid")
		}
		return &event, message.MessageType, nil
	default:
		return nil, "", projectionError("unknown event message type")
	}
}

func cloneReplayPlayers(values []*threeroundsv1.ReplayPlayer) []*threeroundsv1.ReplayPlayer {
	result := make([]*threeroundsv1.ReplayPlayer, len(values))
	for index, value := range values {
		result[index] = &threeroundsv1.ReplayPlayer{
			UserId:      value.GetUserId(),
			SeatIndex:   value.GetSeatIndex(),
			InitialHand: append([]string(nil), value.GetInitialHand()...),
		}
	}
	return result
}

func cloneRoundSummary(value *threeroundsv1.RoundSummary) *threeroundsv1.RoundSummary {
	if value == nil {
		return nil
	}
	payload, _ := marshalDeterministic(value)
	var cloned threeroundsv1.RoundSummary
	_ = unmarshalStrict(payload, &cloned)
	return &cloned
}

func compareStandingProto(left, right *threeroundsv1.FinalStanding) int {
	if diff := cmp.Compare(left.GetTotalPoints(), right.GetTotalPoints()); diff != 0 {
		return diff
	}
	if diff := cmp.Compare(boolToUint8(left.GetWonRoundThree()), boolToUint8(right.GetWonRoundThree())); diff != 0 {
		return diff
	}
	if diff := cmp.Compare(boolToUint8(left.GetWonRoundTwo()), boolToUint8(right.GetWonRoundTwo())); diff != 0 {
		return diff
	}
	return cmp.Compare(boolToUint8(left.GetWonRoundOne()), boolToUint8(right.GetWonRoundOne()))
}

func boolToUint8(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

func projectionError(detail string) error {
	return &engine.RuleError{Code: engine.CodeProjectionUnavailable, Detail: detail}
}
