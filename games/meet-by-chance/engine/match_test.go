package engine

import (
	"testing"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

func TestExactGroupsSuppressCoarseGroups(t *testing.T) {
	players := matchPlayers(t, [][3]dice.Face{{3, 4, 5}, {5, 3, 4}, {5, 5, 2}, {5, 5, 6}})
	groups := FindMatchGroups(players)
	if len(groups) != 1 || groups[0].Kind != MatchExact || len(groups[0].UserIDs) != 2 {
		t.Fatalf("groups=%+v", groups)
	}
}

func TestHighLowGroupsAreDisjointAndLowUsesStableSeat(t *testing.T) {
	players := matchPlayers(t, [][3]dice.Face{{5, 5, 2}, {5, 5, 6}, {2, 2, 1}, {2, 2, 4}, {3, 4, 6}})
	groups := FindMatchGroups(players)
	if len(groups) != 2 || groups[0].Kind != MatchHigh || groups[1].Kind != MatchLow || groups[1].WeakestUserID != players[2].UserID {
		t.Fatalf("groups=%+v", groups)
	}
}

func TestSingleCoarseGroupResolvesOnlyAsLow(t *testing.T) {
	players := matchPlayers(t, [][3]dice.Face{{5, 5, 2}, {5, 5, 6}, {3, 4, 6}})
	groups := FindMatchGroups(players)
	if len(groups) != 1 || groups[0].Kind != MatchLow || groups[0].WeakestUserID != players[0].UserID {
		t.Fatalf("groups=%+v", groups)
	}
}

func matchPlayers(t *testing.T, raw [][3]dice.Face) []PlayerState {
	t.Helper()
	players := make([]PlayerState, len(raw))
	for index, faces := range raw {
		hand, err := Classify(faces, DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		players[index] = PlayerState{UserID: "user-" + string(rune('a'+index)), SeatIndex: uint32(index), Active: true, Hand: hand}
	}
	hands := make([]Hand, len(players))
	for index := range players {
		hands[index] = players[index].Hand
	}
	hands = Resolve235Context(hands)
	for index := range players {
		players[index].Hand = hands[index]
	}
	return players
}
