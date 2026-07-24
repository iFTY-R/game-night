package engine

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"strings"
)

type CardID string

type Suit uint8

const (
	SuitNone Suit = iota
	SuitDiamond
	SuitClub
	SuitHeart
	SuitSpade
)

type cardKind uint8

const (
	cardNormal cardKind = iota
	cardSmallJoker
	cardBigJoker
)

type Card struct {
	ID      CardID
	Ordinal uint8
	Rank    uint8
	Suit    Suit
	Kind    cardKind
}

const (
	CurrentSchemaVersion uint32 = 1
	RoundHistoryLimit           = 3
	ShuffleDomain               = "game-night/three-rounds/shuffle/v1"
	SmallJokerID         CardID = "SJ"
	BigJokerID           CardID = "BJ"
)

var (
	deckOrdered  = buildDeck()
	cardByID     = buildCardMap(deckOrdered)
	normalDeck   = deckOrdered[:52]
	zeroSeedHash = "ff936adffbb79dadd336fceaf7b6ccadf36b9c0fe43ab94a3867c0504e685b5f"
)

func buildDeck() []Card {
	result := make([]Card, 0, 54)
	ordinal := uint8(0)
	for rank := uint8(2); rank <= 14; rank++ {
		for _, suit := range []Suit{SuitDiamond, SuitClub, SuitHeart, SuitSpade} {
			result = append(result, Card{ID: makeCardID(rank, suit), Ordinal: ordinal, Rank: rank, Suit: suit, Kind: cardNormal})
			ordinal++
		}
	}
	result = append(result,
		Card{ID: SmallJokerID, Ordinal: ordinal, Rank: 15, Suit: SuitNone, Kind: cardSmallJoker},
		Card{ID: BigJokerID, Ordinal: ordinal + 1, Rank: 16, Suit: SuitNone, Kind: cardBigJoker},
	)
	return result
}

func buildCardMap(cards []Card) map[CardID]Card {
	result := make(map[CardID]Card, len(cards))
	for _, card := range cards {
		result[card.ID] = card
	}
	return result
}

func makeCardID(rank uint8, suit Suit) CardID {
	value := ""
	switch rank {
	case 11:
		value = "J"
	case 12:
		value = "Q"
	case 13:
		value = "K"
	case 14:
		value = "A"
	default:
		value = itoa(uint32(rank))
	}
	switch suit {
	case SuitDiamond:
		return CardID(value + "D")
	case SuitClub:
		return CardID(value + "C")
	case SuitHeart:
		return CardID(value + "H")
	case SuitSpade:
		return CardID(value + "S")
	default:
		return ""
	}
}

func ParseCardID(value string) (Card, bool) {
	card, ok := cardByID[CardID(value)]
	if !ok || value != strings.TrimSpace(value) {
		return Card{}, false
	}
	return card, true
}

// OrderedDeck returns the exact frozen 54-card source order from the spec.
func OrderedDeck() []Card {
	return slices.Clone(deckOrdered)
}

// ShuffleDeck applies the spec's SHA-256 counter stream and rejection-sampled Fisher-Yates shuffle.
func ShuffleDeck(seed [32]byte) ([]Card, error) {
	deck := OrderedDeck()
	stream := newShuffleStream(seed)
	for i := len(deck) - 1; i >= 1; i-- {
		n := uint64(i + 1)
		limit := ^uint64(0) - (^uint64(0) % n)
		var x uint64
		for {
			var err error
			x, err = stream.nextU64()
			if err != nil {
				return nil, ruleError(CodeSeedInvalid, "shuffle seed stream failed")
			}
			if x < limit {
				break
			}
		}
		j := int(x % n)
		deck[i], deck[j] = deck[j], deck[i]
	}
	return deck, nil
}

type shuffleStream struct {
	seed    [32]byte
	counter uint64
	buffer  []byte
	offset  int
}

func newShuffleStream(seed [32]byte) *shuffleStream {
	return &shuffleStream{seed: seed}
}

func (stream *shuffleStream) nextU64() (uint64, error) {
	if len(stream.buffer)-stream.offset < 8 {
		block := make([]byte, 0, len(ShuffleDomain)+1+len(stream.seed)+8)
		block = append(block, []byte(ShuffleDomain)...)
		block = append(block, 0x00)
		block = append(block, stream.seed[:]...)
		counter := make([]byte, 8)
		binary.BigEndian.PutUint64(counter, stream.counter)
		block = append(block, counter...)
		sum := sha256.Sum256(block)
		stream.buffer = sum[:]
		stream.offset = 0
		stream.counter++
	}
	value := binary.BigEndian.Uint64(stream.buffer[stream.offset : stream.offset+8])
	stream.offset += 8
	return value, nil
}

func DealHands(participants []Participant, deck []Card) (map[string][]CardID, []Card, error) {
	if len(participants) < MinimumPlayers || len(participants) > MaximumPlayers || len(deck) != len(deckOrdered) {
		return nil, nil, ruleError(CodeInvalidParticipants, "deal inputs are malformed")
	}
	hands := make(map[string][]CardID, len(participants))
	for _, participant := range participants {
		hands[participant.UserID] = make([]CardID, 0, 6)
	}
	index := 0
	for round := 0; round < 6; round++ {
		for _, participant := range participants {
			hands[participant.UserID] = append(hands[participant.UserID], deck[index].ID)
			index++
		}
	}
	return hands, slices.Clone(deck[index:]), nil
}

func rankStrength(card Card) uint8 {
	switch card.Kind {
	case cardBigJoker:
		return 16
	case cardSmallJoker:
		return 15
	default:
		return card.Rank
	}
}

func suitStrength(card Card) uint8 {
	switch card.Suit {
	case SuitSpade:
		return 4
	case SuitHeart:
		return 3
	case SuitClub:
		return 2
	case SuitDiamond:
		return 1
	default:
		return 0
	}
}

func suitAscending(card Card) uint8 {
	switch card.Suit {
	case SuitDiamond:
		return 1
	case SuitClub:
		return 2
	case SuitHeart:
		return 3
	case SuitSpade:
		return 4
	default:
		return 0
	}
}

func halfPoints(card Card) uint8 {
	switch card.Kind {
	case cardBigJoker, cardSmallJoker:
		return 1
	}
	switch card.Rank {
	case 14:
		return 2
	case 11, 12, 13:
		return 1
	default:
		return card.Rank * 2
	}
}

func normalCardOrdinal(card Card) (uint8, bool) {
	if card.Kind != cardNormal {
		return 0, false
	}
	return card.Ordinal, true
}

func sortCardsByOrdinal(cards []Card) {
	slices.SortFunc(cards, func(left, right Card) int {
		return cmp.Compare(left.Ordinal, right.Ordinal)
	})
}

func sortCardIDsByOrdinal(values []CardID) {
	slices.SortFunc(values, func(left, right CardID) int {
		return cmp.Compare(cardByID[left].Ordinal, cardByID[right].Ordinal)
	})
}

func deckDigest(cards []Card) string {
	ids := make([]string, len(cards))
	for index, card := range cards {
		ids[index] = string(card.ID)
	}
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return encodeHex(sum[:])
}

func encodeHex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, current := range value {
		result[index*2] = digits[current>>4]
		result[index*2+1] = digits[current&0x0f]
	}
	return string(result)
}

func itoa(value uint32) string {
	if value == 0 {
		return "0"
	}
	buffer := [10]byte{}
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
