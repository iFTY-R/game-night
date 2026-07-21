package engine

import "sort"

// MatchKind identifies the one priority class resolved for a group in a batch.
type MatchKind string

const (
	MatchExact  MatchKind = "exact"
	MatchHigh   MatchKind = "high"
	MatchLow    MatchKind = "low"
	MatchCapped MatchKind = "capped"
)

// MatchGroup is one disjoint simultaneous reroll group in the current resolution batch.
type MatchGroup struct {
	Kind          MatchKind
	UserIDs       []string
	WeakestUserID string
}

// FindMatchGroups applies exact-match priority, then the highest and lowest coarse tie groups.
func FindMatchGroups(players []PlayerState) []MatchGroup {
	active := activeHandPlayers(players)
	exact := exactGroups(active)
	if len(exact) > 0 {
		return exact
	}
	byTie := make(map[[2]int32][]PlayerState)
	for _, player := range active {
		byTie[player.Hand.TieKey] = append(byTie[player.Hand.TieKey], player)
	}
	keys := make([][2]int32, 0, len(byTie))
	for tieKey, group := range byTie {
		if len(group) > 1 {
			keys = append(keys, tieKey)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left][0] != keys[right][0] {
			return keys[left][0] < keys[right][0]
		}
		return keys[left][1] < keys[right][1]
	})
	lowKey, highKey := keys[0], keys[len(keys)-1]
	if lowKey == highKey {
		users := stableUsers(byTie[lowKey])
		return []MatchGroup{{Kind: MatchLow, UserIDs: users, WeakestUserID: users[0]}}
	}
	highUsers := stableUsers(byTie[highKey])
	lowUsers := stableUsers(byTie[lowKey])
	return []MatchGroup{
		{Kind: MatchHigh, UserIDs: highUsers},
		{Kind: MatchLow, UserIDs: lowUsers, WeakestUserID: lowUsers[0]},
	}
}

func exactGroups(players []PlayerState) []MatchGroup {
	used := make([]bool, len(players))
	groups := make([]MatchGroup, 0)
	for left := range players {
		if used[left] {
			continue
		}
		members := []PlayerState{players[left]}
		for right := left + 1; right < len(players); right++ {
			if !used[right] && ExactMatch(players[left].Hand, players[right].Hand) {
				used[right] = true
				members = append(members, players[right])
			}
		}
		if len(members) > 1 {
			used[left] = true
			groups = append(groups, MatchGroup{Kind: MatchExact, UserIDs: stableUsers(members)})
		}
	}
	return groups
}

func activeHandPlayers(players []PlayerState) []PlayerState {
	active := make([]PlayerState, 0, len(players))
	for _, player := range players {
		if player.Active {
			active = append(active, player)
		}
	}
	return active
}

func stableUsers(players []PlayerState) []string {
	stable := append([]PlayerState(nil), players...)
	sort.Slice(stable, func(left, right int) bool { return stable[left].SeatIndex < stable[right].SeatIndex })
	users := make([]string, len(stable))
	for index, player := range stable {
		users[index] = player.UserID
	}
	return users
}
