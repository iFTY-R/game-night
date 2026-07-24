package projection

import (
	"github.com/iFTY-R/game-night/games/three-rounds/engine"
	threeroundsv1 "github.com/iFTY-R/game-night/games/three-rounds/gen/go/game/three_rounds/v1"
)

func configToProto(config engine.Config) *threeroundsv1.Config {
	return &threeroundsv1.Config{
		RoundOneTimeoutSeconds: config.RoundOneTimeoutSeconds,
		RoundTwoTimeoutSeconds: config.RoundTwoTimeoutSeconds,
		RoundResultSeconds:     config.RoundResultSeconds,
		FinalResultSeconds:     config.FinalResultSeconds,
	}
}

func phaseToProto(value engine.Phase) threeroundsv1.Phase {
	switch value {
	case engine.PhaseDealing:
		return threeroundsv1.Phase_PHASE_DEALING
	case engine.PhaseRoundOneSelecting:
		return threeroundsv1.Phase_PHASE_ROUND_ONE_SELECTING
	case engine.PhaseRoundOneResult:
		return threeroundsv1.Phase_PHASE_ROUND_ONE_RESULT
	case engine.PhaseRoundTwoSelecting:
		return threeroundsv1.Phase_PHASE_ROUND_TWO_SELECTING
	case engine.PhaseRoundTwoResult:
		return threeroundsv1.Phase_PHASE_ROUND_TWO_RESULT
	case engine.PhaseRoundThreeResult:
		return threeroundsv1.Phase_PHASE_ROUND_THREE_RESULT
	case engine.PhaseFinalResult:
		return threeroundsv1.Phase_PHASE_FINAL_RESULT
	case engine.PhaseFinished:
		return threeroundsv1.Phase_PHASE_FINISHED
	default:
		return threeroundsv1.Phase_PHASE_UNSPECIFIED
	}
}

func finishReasonToProto(value string) threeroundsv1.FinishReason {
	switch value {
	case engine.FinishNormalCompleted:
		return threeroundsv1.FinishReason_FINISH_REASON_NORMAL_COMPLETED
	case engine.FinishHostRequested:
		return threeroundsv1.FinishReason_FINISH_REASON_HOST_REQUESTED
	case engine.FinishInsufficientParticipants:
		return threeroundsv1.FinishReason_FINISH_REASON_INSUFFICIENT_PARTICIPANTS
	default:
		return threeroundsv1.FinishReason_FINISH_REASON_UNSPECIFIED
	}
}

func pendingSelectionToProto(value engine.PendingSelection) *threeroundsv1.PendingSelection {
	if value.Round == 0 {
		return nil
	}
	return &threeroundsv1.PendingSelection{
		Round:         value.Round,
		CardIds:       cardIDsToStrings(value.CardIDs),
		AutoSubmitted: value.AutoSubmitted,
	}
}

func roundCardsToProto(values []engine.CardID) *threeroundsv1.RoundCards {
	if len(values) == 0 {
		return nil
	}
	return &threeroundsv1.RoundCards{CardIds: cardIDsToStrings(values)}
}

func wildcardResolutionToProto(value engine.WildcardResolution) *threeroundsv1.WildcardResolution {
	if len(value.Substitutions) == 0 && len(value.ResolvedCards) == 0 {
		return nil
	}
	result := &threeroundsv1.WildcardResolution{ResolvedCards: cardIDsToStrings(value.ResolvedCards)}
	for _, substitution := range value.Substitutions {
		result.Substitutions = append(result.Substitutions, &threeroundsv1.WildcardSubstitution{
			WildcardCardId:    string(substitution.WildcardCardID),
			SubstitutedCardId: string(substitution.SubstitutedCardID),
			CardOrdinal:       uint32(substitution.CardOrdinal),
		})
	}
	return result
}

func roundHistoryToProto(values []engine.RoundSummary) []*threeroundsv1.RoundSummary {
	result := make([]*threeroundsv1.RoundSummary, len(values))
	for index, value := range values {
		result[index] = roundSummaryToProto(value)
	}
	return result
}

func roundSummaryToProto(value engine.RoundSummary) *threeroundsv1.RoundSummary {
	result := &threeroundsv1.RoundSummary{
		Round:         value.Round,
		WinnerUserIds: append([]string(nil), value.WinnerUserIDs...),
		AllBusted:     value.AllBusted,
	}
	for _, reveal := range value.Reveals {
		item := &threeroundsv1.PlayerReveal{
			UserId:             reveal.UserID,
			SeatIndex:          reveal.SeatIndex,
			Active:             reveal.Active,
			CardIds:            cardIDsToStrings(reveal.CardIDs),
			AutoSubmitted:      reveal.AutoSubmitted,
			AwardedPoints:      uint32(reveal.AwardedPoints),
			WildcardResolution: wildcardResolutionToProto(reveal.WildcardResolution),
		}
		if reveal.RoundOneEvaluation.CardID != "" {
			item.RoundOne = &threeroundsv1.RoundOneEvaluation{
				CardId:       string(reveal.RoundOneEvaluation.CardID),
				RankStrength: uint32(reveal.RoundOneEvaluation.RankStrength),
				SuitStrength: uint32(reveal.RoundOneEvaluation.SuitStrength),
			}
		}
		if reveal.RoundTwoEvaluation.TotalHalfPoints != 0 || reveal.RoundTwoEvaluation.Busted {
			item.RoundTwo = &threeroundsv1.RoundTwoEvaluation{
				TotalHalfPoints:   uint32(reveal.RoundTwoEvaluation.TotalHalfPoints),
				Busted:            reveal.RoundTwoEvaluation.Busted,
				RankStrengthsDesc: []uint32{uint32(reveal.RoundTwoEvaluation.RankStrengthsDesc[0]), uint32(reveal.RoundTwoEvaluation.RankStrengthsDesc[1])},
			}
		}
		if reveal.RoundThreeEvaluation.HandClass != 0 {
			item.RoundThree = &threeroundsv1.RoundThreeEvaluation{
				HandClass:     handClassToProto(reveal.RoundThreeEvaluation.HandClass),
				CompareValues: bytesToUint32s(reveal.RoundThreeEvaluation.CompareValues),
				ResolvedCards: cardIDsToStrings(reveal.RoundThreeEvaluation.ResolvedCards),
			}
		}
		result.Reveals = append(result.Reveals, item)
	}
	return result
}

func finalSummaryToProto(value engine.FinalSummary) *threeroundsv1.FinalSummary {
	if len(value.Standings) == 0 && len(value.WinnerUserIDs) == 0 {
		return nil
	}
	result := &threeroundsv1.FinalSummary{WinnerUserIds: append([]string(nil), value.WinnerUserIDs...)}
	for _, standing := range value.Standings {
		result.Standings = append(result.Standings, &threeroundsv1.FinalStanding{
			UserId:        standing.UserID,
			SeatIndex:     standing.SeatIndex,
			Active:        standing.Active,
			TotalPoints:   uint32(standing.TotalPoints),
			WonRoundOne:   standing.WonRoundOne,
			WonRoundTwo:   standing.WonRoundTwo,
			WonRoundThree: standing.WonRoundThree,
			Winner:        standing.Winner,
			Rank:          standing.Rank,
		})
	}
	return result
}

func handClassToProto(value engine.HandClass) threeroundsv1.HandClass {
	switch value {
	case engine.HandSingle:
		return threeroundsv1.HandClass_HAND_CLASS_SINGLE
	case engine.HandPair:
		return threeroundsv1.HandClass_HAND_CLASS_PAIR
	case engine.HandStraight:
		return threeroundsv1.HandClass_HAND_CLASS_STRAIGHT
	case engine.HandFlush:
		return threeroundsv1.HandClass_HAND_CLASS_FLUSH
	case engine.HandStraightFlush:
		return threeroundsv1.HandClass_HAND_CLASS_STRAIGHT_FLUSH
	case engine.HandTrips:
		return threeroundsv1.HandClass_HAND_CLASS_TRIPS
	default:
		return threeroundsv1.HandClass_HAND_CLASS_UNSPECIFIED
	}
}

func cardIDsToStrings(values []engine.CardID) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func bytesToUint32s(values []uint8) []uint32 {
	result := make([]uint32, len(values))
	for index, value := range values {
		result[index] = uint32(value)
	}
	return result
}
