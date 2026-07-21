import { create, fromBinary } from "@bufbuild/protobuf";
import type { ActionInput } from "@game-night/game-client";

import {
  CommandSchema,
  ConfigSchema,
  DiceRevealedSchema,
  EventSchema,
  HandClass,
  HandClassifiedSchema,
  MatchBatchSchema,
  MatchGroupSchema,
  MatchKind,
  MatchResolvedSchema,
  PenaltyRecordedSchema,
  Phase,
  PublicDiceSchema,
  PublicPlayerSchema,
  ReplayEntrySchema,
  ReplayPlayerSchema,
  ReplayRoundSchema,
  ReplaySchema,
  ResolutionCause,
  RoundOutcome,
  RoundSettledSchema,
  RoundStartedSchema,
  RoundSummarySchema,
  Special235EvaluatedSchema,
  Special235Outcome,
  TargetRerolledSchema,
  TargetSelectedSchema,
  ViewSchema,
  type Config,
  type Event,
  type PublicPlayer,
  type Replay,
  type View,
} from "./generated/game/meet_by_chance/v1/meet_by_chance_pb";
import type { MeetByChanceTableContext } from "./types";

export type MeetByChanceFixtureState = "active" | "special-235-leopards" | "special-235-minimum" | "match-exact" | "match-high-low" | "match-capped" | "target-transferred" | "reroll-limit" | "timeout" | "spectator" | "reconnecting" | "finished" | "replay";

const config = () => create(ConfigSchema, {
  straight123: true,
  straight234: true,
  straight345: true,
  straight456: true,
  special235Enabled: true,
  onesWild: true,
  targetPenaltyTicks: 2,
  rerollPenaltyTicks: 2,
  matchPenaltyTicks: 4,
  weakExtraPenaltyTicks: 2,
  targetRerollLimit: 2,
  matchResolutionLimit: 3,
  actionTimeoutSeconds: 30,
});

const player = (
  userId: string,
  seatIndex: number,
  dice: readonly number[],
  normalizedDice: readonly number[],
  handClass: HandClass,
  options: { penaltyTicks?: number; special235Outcome?: Special235Outcome; targeted?: boolean; active?: boolean } = {},
): PublicPlayer => create(PublicPlayerSchema, {
  userId,
  seatIndex,
  active: options.active ?? true,
  penaltyTicks: options.penaltyTicks ?? 0,
  dice: [...dice],
  normalizedDice: [...normalizedDice],
  handClass,
  special235: handClass === HandClass.SPECIAL_235,
  special235Outcome: options.special235Outcome ?? Special235Outcome.NOT_APPLICABLE,
  targetedThisRound: options.targeted ?? false,
});

const activePlayers = (): PublicPlayer[] => [
  player("user-qing", 0, [1, 1, 2], [2, 2, 2], HandClass.LEOPARD, { penaltyTicks: 2 }),
  player("user-man", 1, [3, 4, 5], [3, 4, 5], HandClass.STRAIGHT, { penaltyTicks: 4 }),
  player("user-nan", 2, [2, 2, 5], [2, 2, 5], HandClass.PAIR, { penaltyTicks: 3 }),
  player("user-self", 3, [1, 4, 6], [1, 4, 6], HandClass.SINGLE, { penaltyTicks: 2, targeted: true }),
];

const baseView = (now: number): View => create(ViewSchema, {
  phase: Phase.TARGET_DECISION,
  round: 12,
  targetUserId: "user-self",
  targetRerollCount: 0,
  targetRerollLimit: 2,
  targetStreak: 0,
  matchResolutionCount: 0,
  matchResolutionLimit: 3,
  actionDeadlineUnixMillis: BigInt(now + 30_000),
  allowedActions: ["round.reroll", "round.stand"],
  publicPlayers: activePlayers(),
  config: config(),
  viewerIsHost: true,
});

const settlement = (
  round: number,
  targetUserId: string,
  outcome: RoundOutcome,
  cause: ResolutionCause,
  finalPlayers: readonly PublicPlayer[],
  options: { rerolls?: number; matches?: number; targetHistory?: readonly string[]; reason?: string; streak?: number } = {},
) => create(RoundSummarySchema, {
  round,
  targetUserId,
  outcome,
  cause,
  targetRerollCount: options.rerolls ?? 0,
  matchResolutionCount: options.matches ?? 0,
  finalPlayers: [...finalPlayers],
  targetHistoryUserIds: [...(options.targetHistory ?? [targetUserId])],
  reason: options.reason ?? (outcome === RoundOutcome.STOOD ? "stand" : "target_exceeded_all"),
  settled: true,
  targetStreak: options.streak ?? 0,
});

export const meetByChanceFixtureView = (state: MeetByChanceFixtureState = "active", now = Date.now()): View => {
  if (state === "special-235-leopards") {
    return create(ViewSchema, {
      ...baseView(now),
      targetUserId: "user-nan",
      allowedActions: [],
      publicPlayers: [
        player("user-qing", 0, [2, 3, 5], [2, 3, 5], HandClass.SPECIAL_235, { special235Outcome: Special235Outcome.BEATS_LEOPARDS }),
        player("user-man", 1, [6, 6, 6], [6, 6, 6], HandClass.LEOPARD),
        player("user-nan", 2, [4, 4, 4], [4, 4, 4], HandClass.LEOPARD, { penaltyTicks: 2, targeted: true }),
        player("user-self", 3, [5, 5, 5], [5, 5, 5], HandClass.LEOPARD),
      ],
    });
  }
  if (state === "special-235-minimum") {
    return create(ViewSchema, {
      ...baseView(now),
      publicPlayers: [
        player("user-qing", 0, [6, 6, 6], [6, 6, 6], HandClass.LEOPARD),
        player("user-man", 1, [4, 4, 2], [2, 4, 4], HandClass.PAIR),
        player("user-nan", 2, [3, 4, 5], [3, 4, 5], HandClass.STRAIGHT),
        player("user-self", 3, [2, 3, 5], [2, 3, 5], HandClass.SPECIAL_235, { penaltyTicks: 2, targeted: true, special235Outcome: Special235Outcome.MINIMUM_SINGLE }),
      ],
    });
  }
  if (state === "match-exact") {
    const view = baseView(now);
    return create(ViewSchema, {
      ...view,
      matchResolutionCount: 1,
      publicPlayers: view.publicPlayers.map((value) => create(PublicPlayerSchema, { ...value, penaltyTicks: value.penaltyTicks + (value.userId === "user-qing" || value.userId === "user-man" ? 4 : 0) })),
      lastMatchBatch: create(MatchBatchSchema, {
        round: 12, batchIndex: 1, resolutionCount: 1,
        groups: [create(MatchGroupSchema, { kind: MatchKind.EXACT, userIds: ["user-qing", "user-man"], penaltyTicks: 4 })],
        rerolledUserIds: ["user-qing", "user-man"],
      }),
    });
  }
  if (state === "match-high-low") {
    const view = baseView(now);
    return create(ViewSchema, {
      ...view,
      matchResolutionCount: 2,
      publicPlayers: view.publicPlayers.map((value) => create(PublicPlayerSchema, {
        ...value,
        penaltyTicks: value.penaltyTicks + (value.userId === "user-nan" ? 6 : 4),
      })),
      lastMatchBatch: create(MatchBatchSchema, {
        round: 12, batchIndex: 2, resolutionCount: 2,
        groups: [
          create(MatchGroupSchema, { kind: MatchKind.HIGHEST, userIds: ["user-qing", "user-man"], penaltyTicks: 4 }),
          create(MatchGroupSchema, { kind: MatchKind.LOWEST, userIds: ["user-nan", "user-self"], penaltyTicks: 4, weakestUserId: "user-nan", weakExtraPenaltyTicks: 2 }),
        ],
        rerolledUserIds: ["user-qing", "user-man", "user-nan", "user-self"],
      }),
    });
  }
  if (state === "match-capped") {
    return create(ViewSchema, {
      ...baseView(now),
      matchResolutionCount: 3,
      lastMatchBatch: create(MatchBatchSchema, {
        round: 12, batchIndex: 3, resolutionCount: 3, capped: true,
        groups: [create(MatchGroupSchema, { kind: MatchKind.EXACT, userIds: ["user-qing", "user-man"] })],
      }),
    });
  }
  if (state === "target-transferred") {
    const players = activePlayers().map((value) => create(PublicPlayerSchema, {
      ...value,
      targetedThisRound: value.userId === "user-qing" || value.userId === "user-self",
      penaltyTicks: value.penaltyTicks + (value.userId === "user-qing" || value.userId === "user-self" ? 2 : 0),
    }));
    return create(ViewSchema, { ...baseView(now), publicPlayers: players, targetRerollCount: 1, targetStreak: 0 });
  }
  if (state === "reroll-limit") return create(ViewSchema, { ...baseView(now), targetRerollCount: 2, targetStreak: 1, allowedActions: ["round.stand"] });
  if (state === "timeout") {
    const view = baseView(now);
    const previous = settlement(12, "user-self", RoundOutcome.STOOD, ResolutionCause.TIMEOUT_STAND, view.publicPlayers, { reason: "timeout", targetHistory: ["user-self"] });
    return create(ViewSchema, {
      ...view, round: 13, targetUserId: "user-qing", allowedActions: [],
      publicPlayers: view.publicPlayers.map((value) => create(PublicPlayerSchema, { ...value, targetedThisRound: value.userId === "user-qing" })),
      lastSettlement: previous, recentRounds: [previous],
    });
  }
  if (state === "spectator") return create(ViewSchema, { ...baseView(now), allowedActions: [], viewerIsHost: false });
  if (state === "finished") return create(ViewSchema, {
    ...baseView(now), phase: Phase.FINISHED, targetUserId: "", allowedActions: [], actionDeadlineUnixMillis: 0n, finishReason: "host_requested",
  });
  return baseView(now);
};

export const meetByChanceFixtureContext = (displayName = "你", state: MeetByChanceFixtureState = "active"): MeetByChanceTableContext => ({
  roomCode: "MEET",
  selfUserId: "user-self",
  viewerRole: state === "spectator" ? "spectator" : state === "replay" ? "replay" : "player",
  connection: state === "reconnecting" ? "reconnecting" : "online",
  players: [
    { userId: "user-qing", displayName: "阿青", avatarText: "青", connected: true, seatIndex: 0 },
    { userId: "user-man", displayName: "小满", avatarText: "满", connected: true, seatIndex: 1 },
    { userId: "user-nan", displayName: "南风", avatarText: "南", connected: true, seatIndex: 2 },
    { userId: "user-self", displayName, avatarText: displayName.slice(0, 1), connected: true, host: true, seatIndex: 3 },
  ],
});

const replayPlayers = () => [
  create(ReplayPlayerSchema, { userId: "user-qing", seatIndex: 0 }),
  create(ReplayPlayerSchema, { userId: "user-man", seatIndex: 1 }),
  create(ReplayPlayerSchema, { userId: "user-nan", seatIndex: 2 }),
  create(ReplayPlayerSchema, { userId: "user-self", seatIndex: 3 }),
];

const replayEntry = (sequence: number, event: Event["event"]) => create(ReplayEntrySchema, {
  sequence: BigInt(sequence),
  event: create(EventSchema, { event }),
});

export const meetByChanceReplayFixture = (): Replay => {
  const firstPlayers = meetByChanceFixtureView("special-235-leopards").publicPlayers.map((value) => create(PublicPlayerSchema, {
    ...value,
    targetedThisRound: value.userId === "user-nan" || value.userId === "user-self",
  }));
  const secondPlayers = meetByChanceFixtureView("match-exact").publicPlayers;
  const firstSummary = settlement(1, "user-nan", RoundOutcome.TARGET_EXCEEDED_ALL, ResolutionCause.PLAYER_REROLL, firstPlayers, { rerolls: 1, targetHistory: ["user-nan", "user-self"], streak: 1 });
  const secondSummary = settlement(2, "user-self", RoundOutcome.STOOD, ResolutionCause.PLAYER_STAND, secondPlayers, { matches: 1, targetHistory: ["user-self"], reason: "stand" });
  const initialConfig = config();
  const initialPlayers = replayPlayers();
  return create(ReplaySchema, {
    schemaVersion: 1,
    config: initialConfig,
    players: initialPlayers,
    rounds: [create(ReplayRoundSchema, { summary: firstSummary }), create(ReplayRoundSchema, { summary: secondSummary })],
    entries: [
      replayEntry(1, { case: "roundStarted", value: create(RoundStartedSchema, { round: 1, cause: ResolutionCause.INITIAL_ROLL, config: initialConfig, players: initialPlayers, hostUserId: "user-self" }) }),
      replayEntry(2, { case: "diceRevealed", value: create(DiceRevealedSchema, { round: 1, cause: ResolutionCause.INITIAL_ROLL, rolledUserIds: firstPlayers.map((value) => value.userId), publicDice: firstPlayers.map((value) => create(PublicDiceSchema, { userId: value.userId, seatIndex: value.seatIndex, dice: value.dice })) }) }),
      replayEntry(3, { case: "handClassified", value: create(HandClassifiedSchema, { round: 1, userId: "user-qing", handClass: HandClass.SPECIAL_235, dice: [2, 3, 5], normalizedDice: [2, 3, 5], special235: true, special235Outcome: Special235Outcome.BEATS_LEOPARDS }) }),
      replayEntry(4, { case: "special235Evaluated", value: create(Special235EvaluatedSchema, { round: 1, specialUserIds: ["user-qing"], allOtherPlayersAreLeopards: true, outcome: Special235Outcome.BEATS_LEOPARDS }) }),
      replayEntry(5, { case: "targetSelected", value: create(TargetSelectedSchema, { round: 1, userId: "user-nan", penaltyTicks: 2, firstSelectionThisRound: true, cause: ResolutionCause.INITIAL_ROLL }) }),
      replayEntry(6, { case: "penaltyRecorded", value: create(PenaltyRecordedSchema, { round: 1, userId: "user-nan", ticks: 2, beforeTotalTicks: 0, afterTotalTicks: 2, reason: "target_selected", cause: ResolutionCause.INITIAL_ROLL }) }),
      replayEntry(7, { case: "targetRerolled", value: create(TargetRerolledSchema, { round: 1, userId: "user-nan", count: 1, targetStreak: 1, penaltyTicks: 2, cause: ResolutionCause.PLAYER_REROLL }) }),
      replayEntry(8, { case: "targetSelected", value: create(TargetSelectedSchema, { round: 1, userId: "user-self", previousUserId: "user-nan", penaltyTicks: 2, firstSelectionThisRound: true, targetRerollCount: 1, cause: ResolutionCause.PLAYER_REROLL }) }),
      replayEntry(9, { case: "roundSettled", value: create(RoundSettledSchema, { summary: firstSummary }) }),
      replayEntry(10, { case: "roundStarted", value: create(RoundStartedSchema, { round: 2, cause: ResolutionCause.PLAYER_REROLL, previousOutcome: RoundOutcome.TARGET_EXCEEDED_ALL }) }),
      replayEntry(11, { case: "matchResolved", value: create(MatchResolvedSchema, { round: 2, cause: ResolutionCause.MATCH_REROLL, batch: create(MatchBatchSchema, { round: 2, batchIndex: 1, resolutionCount: 1, groups: [create(MatchGroupSchema, { kind: MatchKind.EXACT, userIds: ["user-qing", "user-man"], penaltyTicks: 4 })], rerolledUserIds: ["user-qing", "user-man"] }) }) }),
      replayEntry(12, { case: "penaltyRecorded", value: create(PenaltyRecordedSchema, { round: 2, userId: "user-qing", ticks: 4, beforeTotalTicks: 2, afterTotalTicks: 6, reason: "match_exact", cause: ResolutionCause.MATCH_REROLL }) }),
      replayEntry(13, { case: "penaltyRecorded", value: create(PenaltyRecordedSchema, { round: 2, userId: "user-man", ticks: 4, beforeTotalTicks: 4, afterTotalTicks: 8, reason: "match_exact", cause: ResolutionCause.MATCH_REROLL }) }),
      replayEntry(14, { case: "targetSelected", value: create(TargetSelectedSchema, { round: 2, userId: "user-self", penaltyTicks: 2, firstSelectionThisRound: true, matchResolutionCount: 1, cause: ResolutionCause.INITIAL_ROLL }) }),
      replayEntry(15, { case: "roundSettled", value: create(RoundSettledSchema, { summary: secondSummary }) }),
    ],
  });
};

export const applyMeetByChanceFixtureAction = (view: View, input: ActionInput, now = Date.now()): View => {
  const decoded = fromBinary(CommandSchema, input.message.payload);
  if (decoded.command.case === "reroll") {
    const next = meetByChanceFixtureView("target-transferred", now);
    return create(ViewSchema, { ...next, targetStreak: 1, publicPlayers: next.publicPlayers.map((value) => value.userId === "user-self" ? create(PublicPlayerSchema, { ...value, dice: [3, 4, 5], normalizedDice: [3, 4, 5], handClass: HandClass.STRAIGHT }) : value) });
  }
  if (decoded.command.case === "stand") {
    const next = baseView(now);
    const previous = settlement(view.round, view.targetUserId, RoundOutcome.STOOD, ResolutionCause.PLAYER_STAND, view.publicPlayers, { rerolls: view.targetRerollCount, matches: view.matchResolutionCount, reason: "stand" });
    return create(ViewSchema, { ...next, round: view.round + 1, targetUserId: "user-qing", allowedActions: [], lastSettlement: previous, recentRounds: [previous], publicPlayers: next.publicPlayers.map((value) => create(PublicPlayerSchema, { ...value, targetedThisRound: value.userId === "user-qing" })) });
  }
  return view;
};

export const finishMeetByChanceFixture = (view: View): View => create(ViewSchema, {
  ...view, phase: Phase.FINISHED, targetUserId: "", allowedActions: [], actionDeadlineUnixMillis: 0n, finishReason: "host_requested",
});
