package engine

import (
	"sort"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

// HandClass is ordered only for ordinary hands; special 235 uses its contextual FullKey.
type HandClass uint8

const (
	HandSingle HandClass = iota + 1
	HandPair
	HandStraight
	HandLeopard
	HandSpecial235
)

// Special235Context records the single full-table interpretation used by all comparisons in one reveal.
type Special235Context uint8

const (
	Special235None Special235Context = iota
	Special235Minimum
	Special235BeatsLeopards
)

// RankKey is a fixed-capacity lexicographic key, avoiding mutable slice aliases in snapshots.
type RankKey struct {
	Values [5]int32
	Length uint8
}

// Hand stores both original public dice and the deterministic normalized wildcard result.
type Hand struct {
	Raw            [3]dice.Face
	Normalized     [3]dice.Face
	Class          HandClass
	FullKey        RankKey
	TieKey         [2]int32
	WildUsed       bool
	Special235     bool
	SpecialContext Special235Context
}

// Classify selects the strongest legal wildcard interpretation before table-wide 235 context is applied.
func Classify(raw [3]dice.Face, config Config) (Hand, error) {
	for _, face := range raw {
		if !face.Valid() {
			return Hand{}, ruleError(CodeInvalidState, "hand contains a non-six-sided face")
		}
	}
	special := config.Special235Enabled && sortedFaces(raw) == [3]dice.Face{2, 3, 5}
	best := classifyOrdinary(raw, config)
	if config.OnesWild {
		positions := make([]int, 0, 3)
		for index, face := range raw {
			if face == 1 {
				positions = append(positions, index)
			}
		}
		candidate := raw
		var enumerate func(int)
		enumerate = func(position int) {
			if position == len(positions) {
				classified := classifyOrdinary(candidate, config)
				classified.WildUsed = candidate != raw
				if compareCandidate(classified, best) > 0 {
					best = classified
				}
				return
			}
			for replacement := dice.Face(1); replacement <= 6; replacement++ {
				candidate[positions[position]] = replacement
				enumerate(position + 1)
			}
			candidate[positions[position]] = 1
		}
		enumerate(0)
	}
	best.Raw = raw
	if special {
		best.Class = HandSpecial235
		best.Normalized = [3]dice.Face{2, 3, 5}
		best.Special235 = true
		best.SpecialContext = Special235Minimum
		best.FullKey = key(0, -1)
		best.TieKey = [2]int32{int32(HandSingle), -1}
		best.WildUsed = false
	}
	return best, nil
}

// Resolve235Context applies one full-table decision and never performs pairwise non-transitive comparison.
func Resolve235Context(hands []Hand) []Hand {
	resolved := append([]Hand(nil), hands...)
	for index := range resolved {
		if !resolved[index].Special235 {
			continue
		}
		allOthersLeopards := true
		for other := range resolved {
			if other == index {
				continue
			}
			if resolved[other].Class != HandLeopard {
				allOthersLeopards = false
				break
			}
		}
		if allOthersLeopards {
			resolved[index].SpecialContext = Special235BeatsLeopards
			resolved[index].FullKey = key(5, 0)
			resolved[index].TieKey = [2]int32{5, 0}
		} else {
			resolved[index].SpecialContext = Special235Minimum
			resolved[index].FullKey = key(0, -1)
			resolved[index].TieKey = [2]int32{int32(HandSingle), -1}
		}
	}
	return resolved
}

// CompareHand returns -1, 0, or 1 using the already-resolved contextual full keys.
func CompareHand(left, right Hand) int { return compareKey(left.FullKey, right.FullKey) }

// ExactMatch implements the stronger equality required before coarse tie groups are considered.
func ExactMatch(left, right Hand) bool {
	return left.Normalized == right.Normalized && left.Class == right.Class && compareKey(left.FullKey, right.FullKey) == 0
}

func classifyOrdinary(raw [3]dice.Face, config Config) Hand {
	normalized := sortedFaces(raw)
	hand := Hand{Raw: raw, Normalized: normalized, Class: HandSingle}
	if normalized[0] == normalized[2] {
		hand.Class = HandLeopard
		hand.FullKey = key(4, int32(normalized[0]))
		hand.TieKey = [2]int32{4, int32(normalized[0])}
		return hand
	}
	if straightEnabled(normalized, config) {
		hand.Class = HandStraight
		hand.FullKey = key(3, int32(normalized[2]))
		hand.TieKey = [2]int32{3, int32(normalized[2])}
		return hand
	}
	if normalized[0] == normalized[1] || normalized[1] == normalized[2] {
		hand.Class = HandPair
		pair, single := normalized[1], normalized[0]
		if normalized[0] == normalized[1] {
			pair, single = normalized[0], normalized[2]
		}
		hand.FullKey = key(2, int32(pair), int32(single))
		hand.TieKey = [2]int32{2, int32(pair)}
		return hand
	}
	high, middle, low := normalized[2], normalized[1], normalized[0]
	hand.FullKey = key(1, int32(high), int32(middle), int32(low), int32(high+middle+low))
	hand.TieKey = [2]int32{1, int32(high)}
	return hand
}

func compareCandidate(left, right Hand) int {
	if comparison := CompareHand(left, right); comparison != 0 {
		return comparison
	}
	for index := 2; index >= 0; index-- {
		if left.Normalized[index] < right.Normalized[index] {
			return -1
		}
		if left.Normalized[index] > right.Normalized[index] {
			return 1
		}
	}
	return 0
}

func compareKey(left, right RankKey) int {
	length := int(left.Length)
	if int(right.Length) < length {
		length = int(right.Length)
	}
	for index := 0; index < length; index++ {
		if left.Values[index] < right.Values[index] {
			return -1
		}
		if left.Values[index] > right.Values[index] {
			return 1
		}
	}
	if left.Length < right.Length {
		return -1
	}
	if left.Length > right.Length {
		return 1
	}
	return 0
}

func key(values ...int32) RankKey {
	result := RankKey{Length: uint8(len(values))}
	copy(result.Values[:], values)
	return result
}

func sortedFaces(raw [3]dice.Face) [3]dice.Face {
	values := []dice.Face{raw[0], raw[1], raw[2]}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return [3]dice.Face{values[0], values[1], values[2]}
}

func straightEnabled(faces [3]dice.Face, config Config) bool {
	switch faces {
	case [3]dice.Face{1, 2, 3}:
		return config.Straight123
	case [3]dice.Face{2, 3, 4}:
		return config.Straight234
	case [3]dice.Face{3, 4, 5}:
		return config.Straight345
	case [3]dice.Face{4, 5, 6}:
		return config.Straight456
	default:
		return false
	}
}
