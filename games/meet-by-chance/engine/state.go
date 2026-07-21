package engine

import (
	"math"
	"sort"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

// NewState freezes the trusted host and participants before automatically resolving round one to target_decision.
func NewState(config Config, participants []Participant, hostUserID string, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	canonical, err := canonicalParticipants(participants)
	if err != nil {
		return State{}, nil, err
	}
	if err := config.Validate(len(canonical)); err != nil {
		return State{}, nil, err
	}
	if !validUnixMillis(nowUnixMillis) {
		return State{}, nil, ruleError(CodeInvalidState, "deterministic time is required")
	}
	players := make([]PlayerState, len(canonical))
	hostFound := false
	for index, participant := range canonical {
		players[index] = PlayerState{UserID: participant.UserID, SeatIndex: participant.SeatIndex, Active: true}
		hostFound = hostFound || participant.UserID == hostUserID
	}
	if !hostFound {
		return State{}, nil, ruleError(CodeInvalidParticipants, "host must be a frozen participant")
	}
	state := State{
		SchemaVersion: CurrentSchemaVersion, Phase: PhaseRolling, Round: 1,
		HostUserID: hostUserID, Config: config, Players: players,
	}
	roller := deterministicRoller{seed: seed}
	next, events, err := startRound(state, &roller, nowUnixMillis, true, canonical)
	if err != nil {
		return State{}, nil, err
	}
	return next, events, nil
}

// CurrentTimer returns the complete stale-resistant target decision token.
func CurrentTimer(state State) *ActionTimer {
	if state.Phase != PhaseTargetDecision || state.ActionDeadlineUnixMillis == 0 {
		return nil
	}
	return &ActionTimer{
		Round: state.Round, TargetUserID: state.TargetUserID,
		TargetRerollCount: state.TargetRerollCount, TargetStreak: state.TargetStreak,
		MatchResolutionCount: state.MatchResolutionCount, DeadlineUnixMillis: state.ActionDeadlineUnixMillis,
	}
}

// Validate rejects corrupted restored state before commands, timers, systems, or projections use it.
func (state State) Validate() error {
	if state.SchemaVersion != CurrentSchemaVersion || state.Round == 0 || len(state.Players) < MinimumPlayers || len(state.Players) > MaximumPlayers ||
		(state.Phase != PhaseTargetDecision && state.Phase != PhaseFinished) || state.Config.Validate(len(state.Players)) != nil {
		return ruleError(CodeInvalidState, "state header is invalid")
	}
	participants := make([]Participant, len(state.Players))
	hostFound := false
	for index, player := range state.Players {
		participants[index] = Participant{UserID: player.UserID, SeatIndex: player.SeatIndex}
		hostFound = hostFound || player.UserID == state.HostUserID
		if index > 0 && state.Players[index-1].SeatIndex >= player.SeatIndex {
			return ruleError(CodeInvalidState, "players are not in canonical seat order")
		}
		if player.Active && !player.IncludedInCurrentResolution {
			return ruleError(CodeInvalidState, "active player is missing from the current resolution")
		}
		if err := validateHandBasic(player.Hand); err != nil {
			return err
		}
	}
	if !hostFound || ValidateParticipants(participants) != nil || state.TargetRerollCount > state.Config.TargetRerollLimit ||
		state.TargetStreak > state.TargetRerollCount || state.MatchResolutionCount > state.Config.MatchResolutionLimit {
		return ruleError(CodeInvalidState, "frozen participants, host, or counters are invalid")
	}
	if err := validateCurrentHands(state); err != nil {
		return err
	}
	if err := validateMatchHistory(state); err != nil {
		return err
	}
	if err := validateRoundHistory(state); err != nil {
		return err
	}
	if state.Phase == PhaseFinished {
		if !validFinishReason(state.FinishReason) || state.TargetUserID != "" || state.ActionDeadlineUnixMillis != 0 {
			return ruleError(CodeInvalidState, "finished state retains a target decision")
		}
		return nil
	}
	targetIndex := playerIndex(state.Players, state.TargetUserID)
	if state.FinishReason != "" || activeCount(state.Players) < MinimumPlayers || targetIndex < 0 ||
		!state.Players[targetIndex].Active || !state.Players[targetIndex].TargetedThisRound ||
		(state.Config.ActionTimeoutSeconds == 0 && state.ActionDeadlineUnixMillis != 0) ||
		(state.Config.ActionTimeoutSeconds != 0 && state.ActionDeadlineUnixMillis <= 0) {
		return ruleError(CodeInvalidState, "target decision is malformed")
	}
	return nil
}

func validateCurrentHands(state State) error {
	resolutionIndexes := make([]int, 0, len(state.Players))
	hands := make([]Hand, 0, len(state.Players))
	for index, player := range state.Players {
		if !player.IncludedInCurrentResolution {
			continue
		}
		classified, err := Classify(player.Hand.Raw, state.Config)
		if err != nil {
			return ruleError(CodeInvalidState, "active hand cannot be classified")
		}
		resolutionIndexes = append(resolutionIndexes, index)
		hands = append(hands, classified)
	}
	resolved := Resolve235Context(hands)
	for index, playerIndex := range resolutionIndexes {
		if state.Players[playerIndex].Hand != resolved[index] {
			return ruleError(CodeInvalidState, "active hand classification is stale")
		}
	}
	return nil
}

func validateHandBasic(hand Hand) error {
	for _, face := range hand.Raw {
		if !face.Valid() {
			return ruleError(CodeInvalidState, "player hand has invalid raw dice")
		}
	}
	for index, face := range hand.Normalized {
		if !face.Valid() || index > 0 && hand.Normalized[index-1] > face {
			return ruleError(CodeInvalidState, "player hand normalization is invalid")
		}
	}
	if hand.Class < HandSingle || hand.Class > HandSpecial235 || hand.FullKey.Length == 0 || hand.FullKey.Length > 5 {
		return ruleError(CodeInvalidState, "player hand rank is invalid")
	}
	return nil
}

func validateMatchHistory(state State) error {
	if len(state.MatchHistory) > int(state.Config.MatchResolutionLimit)+1 {
		return ruleError(CodeInvalidState, "match history exceeds its bounded resolution count")
	}
	lastBatch := uint32(0)
	for _, resolution := range state.MatchHistory {
		if resolution.Batch < lastBatch || resolution.Batch > state.MatchResolutionCount || resolution.Capped && len(resolution.Groups) == 0 {
			return ruleError(CodeInvalidState, "match history batch is invalid")
		}
		lastBatch = resolution.Batch
		seen := make(map[string]struct{})
		for _, group := range resolution.Groups {
			if group.Kind != MatchExact && group.Kind != MatchHigh && group.Kind != MatchLow && group.Kind != MatchCapped || len(group.UserIDs) < 2 {
				return ruleError(CodeInvalidState, "match history group is invalid")
			}
			for _, userID := range group.UserIDs {
				if playerIndex(state.Players, userID) < 0 {
					return ruleError(CodeInvalidState, "match history references an unknown player")
				}
				if _, duplicate := seen[userID]; duplicate {
					return ruleError(CodeInvalidState, "match history groups overlap")
				}
				seen[userID] = struct{}{}
			}
		}
	}
	return nil
}

func validateSettlement(state State, settlement RoundSettlement) error {
	if settlement.Round == 0 {
		if settlement.TargetUserID != "" || settlement.Reason != "" || len(settlement.Players) != 0 || settlement.TargetRerollCount != 0 ||
			settlement.TargetStreak != 0 || settlement.MatchResolutionCount != 0 {
			return ruleError(CodeInvalidState, "empty settlement retains data")
		}
		return nil
	}
	if settlement.Round >= state.Round || settlement.TargetUserID == "" || settlement.Reason == "" || len(settlement.Players) != len(state.Players) ||
		settlement.TargetRerollCount > state.Config.TargetRerollLimit || settlement.TargetStreak > settlement.TargetRerollCount ||
		settlement.MatchResolutionCount > state.Config.MatchResolutionLimit {
		return ruleError(CodeInvalidState, "round settlement is malformed")
	}
	targetFound := false
	for index, player := range settlement.Players {
		if player.UserID != state.Players[index].UserID || player.SeatIndex != state.Players[index].SeatIndex {
			return ruleError(CodeInvalidState, "round settlement participants changed")
		}
		if err := validateHandBasic(player.Hand); err != nil {
			return err
		}
		targetFound = targetFound || player.UserID == settlement.TargetUserID && player.Active && player.TargetedThisRound
	}
	if !targetFound {
		return ruleError(CodeInvalidState, "round settlement target is invalid")
	}
	if err := validateCurrentHands(State{Config: state.Config, Players: settlement.Players}); err != nil {
		return ruleError(CodeInvalidState, "round settlement hands are stale")
	}
	return nil
}

func validateRoundHistory(state State) error {
	if len(state.RoundHistory) > RoundHistoryLimit {
		return ruleError(CodeInvalidState, "round history exceeds its retention limit")
	}
	if err := validateSettlement(state, state.LastSettlement); err != nil {
		return err
	}
	if len(state.RoundHistory) == 0 {
		if state.LastSettlement.Round != 0 {
			return ruleError(CodeInvalidState, "last settlement is missing from round history")
		}
		return nil
	}
	previousRound := uint32(0)
	for _, settlement := range state.RoundHistory {
		if settlement.Round <= previousRound {
			return ruleError(CodeInvalidState, "round history is not strictly ordered")
		}
		if err := validateSettlement(state, settlement); err != nil {
			return err
		}
		previousRound = settlement.Round
	}
	if !settlementsEqual(state.LastSettlement, state.RoundHistory[len(state.RoundHistory)-1]) {
		return ruleError(CodeInvalidState, "last settlement differs from round history")
	}
	return nil
}

func settlementsEqual(left, right RoundSettlement) bool {
	if left.Round != right.Round || left.TargetUserID != right.TargetUserID || left.Reason != right.Reason ||
		left.TargetRerollCount != right.TargetRerollCount || left.TargetStreak != right.TargetStreak ||
		left.MatchResolutionCount != right.MatchResolutionCount || len(left.Players) != len(right.Players) {
		return false
	}
	for index := range left.Players {
		if left.Players[index] != right.Players[index] {
			return false
		}
	}
	return true
}

func startRound(state State, roller *deterministicRoller, now int64, initial bool, participants []Participant) (State, []Event, error) {
	next := state.Clone()
	next.Phase = PhaseRolling
	next.TargetUserID = ""
	next.TargetRerollCount = 0
	next.TargetStreak = 0
	next.MatchResolutionCount = 0
	next.ActionDeadlineUnixMillis = 0
	next.MatchHistory = nil
	for index := range next.Players {
		next.Players[index].TargetedThisRound = false
		next.Players[index].IncludedInCurrentResolution = next.Players[index].Active
	}
	started := Event{Kind: EventRoundStarted, Round: next.Round, Reason: "round_started"}
	if initial {
		config := next.Config
		started.InitialConfig = &config
		started.InitialParticipants = append([]Participant(nil), participants...)
	}
	users := activeUsers(next.Players)
	rolled, revealEvents, err := rollUsers(next, roller, users, 0, "round_started")
	if err != nil {
		return State{}, nil, err
	}
	resolved, matchEvents, err := resolveMatches(rolled, roller)
	if err != nil {
		return State{}, nil, err
	}
	selected, targetEvents, err := selectTarget(resolved, "", now, "initial_target")
	if err != nil {
		return State{}, nil, err
	}
	return selected, append(append([]Event{started}, revealEvents...), append(matchEvents, targetEvents...)...), nil
}

func resolveMatches(state State, roller *deterministicRoller) (State, []Event, error) {
	next := state.Clone()
	events := make([]Event, 0)
	for {
		groups := FindMatchGroups(next.Players)
		if len(groups) == 0 {
			return next, events, nil
		}
		if next.MatchResolutionCount >= next.Config.MatchResolutionLimit {
			resolution := MatchResolution{Batch: next.MatchResolutionCount, Groups: cloneGroups(groups), Capped: true}
			next.MatchHistory = append(next.MatchHistory, resolution)
			events = append(events, Event{Kind: EventMatchResolved, Round: next.Round, Batch: next.MatchResolutionCount,
				MatchGroups: cloneGroups(groups), Capped: true, MatchResolutionCount: next.MatchResolutionCount,
				Reason: "match_resolution_capped"})
			return next, events, nil
		}
		next.MatchResolutionCount++
		batch := next.MatchResolutionCount
		next.MatchHistory = append(next.MatchHistory, MatchResolution{Batch: batch, Groups: cloneGroups(groups)})
		rerollSet := make(map[string]struct{})
		events = append(events, Event{Kind: EventMatchResolved, Round: next.Round, Batch: batch,
			MatchGroups: cloneGroups(groups), PenaltyTicks: next.Config.MatchPenaltyTicks,
			MatchResolutionCount: batch, Reason: "match_resolved"})
		for _, group := range groups {
			for _, userID := range group.UserIDs {
				var penalty Event
				var err error
				next, penalty, err = addPenalty(next, userID, next.Config.MatchPenaltyTicks, "match_"+string(group.Kind), batch)
				if err != nil {
					return State{}, nil, err
				}
				events = append(events, penalty)
				rerollSet[userID] = struct{}{}
			}
			if group.Kind == MatchLow && group.WeakestUserID != "" {
				var penalty Event
				var err error
				next, penalty, err = addPenalty(next, group.WeakestUserID, next.Config.WeakExtraPenaltyTicks, "match_low_extra", batch)
				if err != nil {
					return State{}, nil, err
				}
				events = append(events, penalty)
			}
		}
		users := stableUsersFromSet(next.Players, rerollSet)
		rolled, revealEvents, err := rollUsers(next, roller, users, batch, "match_reroll")
		if err != nil {
			return State{}, nil, err
		}
		next = rolled
		events = append(events, revealEvents...)
	}
}

func selectTarget(state State, previous string, now int64, reason string) (State, []Event, error) {
	target, err := lowestActiveUser(state)
	if err != nil {
		return State{}, nil, err
	}
	next := state.Clone()
	next.Phase = PhaseTargetDecision
	next.TargetUserID = target
	targetIndex := playerIndex(next.Players, target)
	firstSelection := !next.Players[targetIndex].TargetedThisRound
	next.Players[targetIndex].TargetedThisRound = true
	if previous != target {
		next.TargetStreak = 0
	}
	next.ActionDeadlineUnixMillis, err = deadlineUnixMillis(now, next.Config.ActionTimeoutSeconds)
	if err != nil {
		return State{}, nil, err
	}
	penaltyTicks := dice.Ticks(0)
	if firstSelection {
		penaltyTicks = next.Config.TargetPenaltyTicks
	}
	events := []Event{{Kind: EventTargetSelected, Round: next.Round, UserID: target, PreviousUserID: previous,
		TargetRerollCount: next.TargetRerollCount, TargetStreak: next.TargetStreak,
		MatchResolutionCount: next.MatchResolutionCount, PenaltyTicks: penaltyTicks,
		FirstSelectionThisRound: firstSelection, Reason: reason}}
	if firstSelection {
		var penalty Event
		next, penalty, err = addPenalty(next, target, next.Config.TargetPenaltyTicks, "target_selected", 0)
		if err != nil {
			return State{}, nil, err
		}
		events = append(events, penalty)
	}
	return next, events, nil
}

func rollUsers(state State, roller *deterministicRoller, users []string, batch uint32, reason string) (State, []Event, error) {
	if len(users) == 0 {
		return State{}, nil, ruleError(CodeInvalidState, "roll set is empty")
	}
	next := state.Clone()
	for index := range next.Players {
		next.Players[index].IncludedInCurrentResolution = next.Players[index].Active
	}
	faces, err := roller.roll(uint32(len(users)) * DicePerPlayer)
	if err != nil {
		return State{}, nil, err
	}
	for userIndex, userID := range users {
		index := playerIndex(next.Players, userID)
		if index < 0 || !next.Players[index].Active {
			return State{}, nil, ruleError(CodeParticipantInactive, "roll set contains an inactive player")
		}
		offset := userIndex * int(DicePerPlayer)
		raw := [3]dice.Face{faces[offset], faces[offset+1], faces[offset+2]}
		hand, classifyErr := Classify(raw, next.Config)
		if classifyErr != nil {
			return State{}, nil, classifyErr
		}
		next.Players[index].Hand = hand
	}
	next = resolvePlayer235(next)
	revealed := make([]PlayerState, len(users))
	for index, userID := range users {
		revealed[index] = next.Players[playerIndex(next.Players, userID)]
	}
	events := []Event{{Kind: EventDiceRevealed, Round: next.Round, Batch: batch, Players: clonePlayers(revealed), Reason: reason}}
	for _, player := range next.Players {
		if player.IncludedInCurrentResolution {
			events = append(events, Event{Kind: EventHandClassified, Round: next.Round, Batch: batch, UserID: player.UserID, Hand: player.Hand, Reason: reason})
		}
	}
	return next, events, nil
}

func resolvePlayer235(state State) State {
	indexes := make([]int, 0, len(state.Players))
	hands := make([]Hand, 0, len(state.Players))
	for index, player := range state.Players {
		if player.Active {
			indexes = append(indexes, index)
			hands = append(hands, player.Hand)
		}
	}
	resolved := Resolve235Context(hands)
	for index, playerIndex := range indexes {
		state.Players[playerIndex].Hand = resolved[index]
	}
	return state
}

func addPenalty(state State, userID string, amount dice.Ticks, reason string, batch uint32) (State, Event, error) {
	next := state.Clone()
	index := playerIndex(next.Players, userID)
	if index < 0 || !next.Players[index].Active {
		return State{}, Event{}, ruleError(CodeParticipantInactive, "penalty target is inactive")
	}
	before := next.Players[index].PenaltyTicks
	after, err := before.Add(amount)
	if err != nil {
		return State{}, Event{}, ruleError(CodePenaltyOverflow, "player penalty exceeds uint32")
	}
	next.Players[index].PenaltyTicks = after
	return next, Event{Kind: EventPenaltyRecorded, Round: next.Round, Batch: batch, UserID: userID,
		PenaltyTicks: amount, PenaltyBeforeTicks: before, PenaltyAfterTicks: after, Reason: reason}, nil
}

func lowestActiveUser(state State) (string, error) {
	lowest := -1
	for index, player := range state.Players {
		if !player.Active {
			continue
		}
		if lowest < 0 || CompareHand(player.Hand, state.Players[lowest].Hand) < 0 {
			lowest = index
		}
	}
	if lowest < 0 {
		return "", ruleError(CodeInvalidState, "no active target exists")
	}
	return state.Players[lowest].UserID, nil
}

func targetStrictlyStrongest(state State, target string) bool {
	index := playerIndex(state.Players, target)
	if index < 0 || !state.Players[index].Active {
		return false
	}
	for other, player := range state.Players {
		if other == index || !player.Active {
			continue
		}
		if CompareHand(state.Players[index].Hand, player.Hand) <= 0 {
			return false
		}
	}
	return true
}

type deterministicRoller struct {
	seed   [32]byte
	stream uint64
}

func (roller *deterministicRoller) roll(count uint32) ([]dice.Face, error) {
	roller.stream++
	faces, err := dice.Roll(roller.seed, roller.stream, count)
	if err != nil {
		return nil, ruleError(CodeSeedInvalid, "deterministic seed is invalid")
	}
	return faces, nil
}

func deadlineUnixMillis(now int64, seconds uint32) (int64, error) {
	if !validUnixMillis(now) {
		return 0, ruleError(CodeInvalidState, "deterministic time is required")
	}
	if seconds == 0 {
		return 0, nil
	}
	delta := int64(seconds) * 1000
	if now > math.MaxInt64-delta {
		return 0, ruleError(CodeInvalidState, "deadline overflows unix milliseconds")
	}
	return now + delta, nil
}

func validUnixMillis(value int64) bool { return value > 0 }

func activeCount(players []PlayerState) int {
	count := 0
	for _, player := range players {
		if player.Active {
			count++
		}
	}
	return count
}

func activePlayer(state State, userID string) bool {
	index := playerIndex(state.Players, userID)
	return index >= 0 && state.Players[index].Active
}

func playerIndex(players []PlayerState, userID string) int {
	for index := range players {
		if players[index].UserID == userID {
			return index
		}
	}
	return -1
}

func activeUsers(players []PlayerState) []string {
	users := make([]string, 0, len(players))
	for _, player := range players {
		if player.Active {
			users = append(users, player.UserID)
		}
	}
	return users
}

func stableUsersFromSet(players []PlayerState, selected map[string]struct{}) []string {
	users := make([]string, 0, len(selected))
	for _, player := range players {
		if _, ok := selected[player.UserID]; ok {
			users = append(users, player.UserID)
		}
	}
	return users
}

func validFinishReason(value string) bool {
	return value == FinishHostRequested || value == FinishInsufficientParticipants || value == FinishPlatformCancelled
}

func sortPlayersBySeat(players []PlayerState) {
	sort.Slice(players, func(left, right int) bool { return players[left].SeatIndex < players[right].SeatIndex })
}
