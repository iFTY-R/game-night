import { create, fromBinary } from "@bufbuild/protobuf";
import type { ActionInput } from "@game-night/game-client";

import {
  ActionConstraintsSchema,
  CommandSchema,
  ConfigSchema,
  ContinueMode,
  Effect,
  Phase,
  PlayerStateSchema,
  PoolLayerSchema,
  ReplayPlayerSchema,
  ReplaySchema,
  ReplayTurnSchema,
  TurnOutcome,
  TurnSummarySchema,
  ViewSchema,
  type View,
} from "./generated/game/dice789/v1/dice_789_pb";
import type { Dice789TableContext } from "./types";

export type Dice789FixtureState = "active" | "result-eight" | "result-nine" | "double-four" | "pair" | "add" | "target-one" | "target-six" | "continue" | "forced-reroll" | "forced-pass" | "stacked" | "spectator" | "reconnecting" | "replay";

const players = () => [
  create(PlayerStateSchema, { userId: "user-qing", seatIndex: 0, active: true }),
  create(PlayerStateSchema, { userId: "user-man", seatIndex: 1, active: true, penaltyTicks: 3 }),
  create(PlayerStateSchema, { userId: "user-nan", seatIndex: 2, active: true, penaltyTicks: 5 }),
  create(PlayerStateSchema, { userId: "user-self", seatIndex: 3, active: true, penaltyTicks: 2 }),
];

const config = (stacked = false) => create(ConfigSchema, {
  initialPoolTicks: 2,
  layerCapacityTicks: 8,
  addStepTicks: 2,
  maxLayers: stacked ? 3 : 1,
  stackedPool: stacked,
  ordinaryPairsReverse: true,
  doubleOneEnabled: true,
  doubleFourEnabled: true,
  doubleSixEnabled: true,
  continueMode: ContinueMode.OPTIONAL,
  actionTimeoutSeconds: 30,
  dropReportWindowSeconds: 5,
});

const baseView = (now: number, stacked = false): View => create(ViewSchema, {
  phase: Phase.AWAITING_ROLL,
  turn: 12,
  players: players(),
  currentUserId: "user-self",
  direction: 1,
  pool: stacked
    ? [create(PoolLayerSchema, { index: 0, ticks: 8 }), create(PoolLayerSchema, { index: 1, ticks: 5 })]
    : [create(PoolLayerSchema, { index: 0, ticks: 5 })],
  totalPoolTicks: stacked ? 13 : 5,
  actionDeadlineUnixMillis: BigInt(now + 30_000),
  allowedActions: ["turn.roll"],
  config: config(stacked),
  viewerIsHost: true,
});

const rolledView = (now: number, dieOne: number, dieTwo: number, effect: Effect): View => create(ViewSchema, {
  ...baseView(now),
  phase: Phase.RESULT_PENDING,
  sourceUserId: "user-self",
  dieOne,
  dieTwo,
  sum: dieOne + dieTwo,
  effect,
  actionDeadlineUnixMillis: BigInt(now + 5_000),
  allowedActions: ["turn.confirm_landed", "turn.report_dropped"],
});

export const dice789FixtureView = (state: Dice789FixtureState = "active", now = Date.now()): View => {
  if (state === "result-eight") return rolledView(now, 3, 5, Effect.SUM_EIGHT_HALF_POOL);
  if (state === "result-nine") return rolledView(now, 3, 6, Effect.SUM_NINE_DRAIN_POOL);
  if (state === "double-four") return rolledView(now, 4, 4, Effect.DOUBLE_FOUR_HALF_POOL_REROLL);
  if (state === "pair") return rolledView(now, 3, 3, Effect.ORDINARY_PAIR_REVERSE);
  if (state === "add") return create(ViewSchema, {
    ...rolledView(now, 3, 4, Effect.SUM_SEVEN_ADD),
    phase: Phase.AWAITING_ADD,
    pool: [create(PoolLayerSchema, { index: 0, ticks: 3 })],
    totalPoolTicks: 3,
    actionDeadlineUnixMillis: BigInt(now + 30_000),
    allowedActions: ["pot.add"],
    actionConstraints: create(ActionConstraintsSchema, { minimumAddTicks: 2, maximumAddTicks: 5, addStepTicks: 2, allowCapacityRemainder: true }),
  });
  if (state === "target-one" || state === "target-six") return create(ViewSchema, {
    ...rolledView(now, state === "target-one" ? 1 : 6, state === "target-one" ? 1 : 6, state === "target-one" ? Effect.DOUBLE_ONE_TARGET_DRAIN : Effect.DOUBLE_SIX_TARGET_ADD),
    phase: Phase.AWAITING_TARGET,
    allowedActions: ["turn.choose_target"],
    actionDeadlineUnixMillis: BigInt(now + 30_000),
    actionConstraints: create(ActionConstraintsSchema, { targetUserIds: ["user-qing", "user-man", "user-nan"] }),
  });
  if (state === "continue" || state === "forced-reroll" || state === "forced-pass") {
    const continueMode = state === "forced-reroll" ? ContinueMode.FORCED_REROLL : state === "forced-pass" ? ContinueMode.FORCED_PASS : ContinueMode.OPTIONAL;
    return create(ViewSchema, {
    ...rolledView(now, 3, 5, Effect.SUM_EIGHT_HALF_POOL),
    phase: Phase.AWAITING_CONTINUE,
    pool: [create(PoolLayerSchema, { index: 0, ticks: 2 })],
    totalPoolTicks: 2,
    allowedActions: state === "forced-reroll" ? ["turn.reroll"] : state === "forced-pass" ? ["turn.pass"] : ["turn.reroll", "turn.pass"],
    config: create(ConfigSchema, { ...config(false), continueMode }),
    lastSettlement: create(TurnSummarySchema, {
      turn: 12, sourceUserId: "user-self", dieOne: 3, dieTwo: 5, sum: 8, effect: Effect.SUM_EIGHT_HALF_POOL,
      poolBeforeTicks: 5, poolAfterTicks: 2, penaltyUserId: "user-self", penaltyTicks: 3, directionBefore: 1, directionAfter: 1,
      outcome: TurnOutcome.REROLL, resolutionReason: "eight",
      poolBeforeLayers: [create(PoolLayerSchema, { index: 0, ticks: 5 })],
      poolAfterLayers: [create(PoolLayerSchema, { index: 0, ticks: 2 })],
    }),
  });
  }
  const view = baseView(now, state === "stacked");
  return state === "spectator" ? create(ViewSchema, { ...view, allowedActions: [], viewerIsHost: false }) : view;
};

export const dice789FixtureContext = (displayName = "你", state: Dice789FixtureState = "active"): Dice789TableContext => ({
  roomCode: "D789",
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

export const dice789ReplayFixture = () => create(ReplaySchema, {
  schemaVersion: 1,
  config: config(true),
  players: [
    create(ReplayPlayerSchema, { userId: "user-qing", seatIndex: 0 }),
    create(ReplayPlayerSchema, { userId: "user-man", seatIndex: 1 }),
    create(ReplayPlayerSchema, { userId: "user-nan", seatIndex: 2 }),
    create(ReplayPlayerSchema, { userId: "user-self", seatIndex: 3 }),
  ],
  turns: [
    create(ReplayTurnSchema, { settled: true, summary: create(TurnSummarySchema, {
      turn: 9, sourceUserId: "user-qing", dieOne: 3, dieTwo: 4, sum: 7, effect: Effect.SUM_SEVEN_ADD,
      poolBeforeTicks: 5, poolAfterTicks: 7, directionBefore: 1, directionAfter: 1, nextUserId: "user-man", outcome: TurnOutcome.PASS,
      resolutionReason: "seven",
      poolBeforeLayers: [create(PoolLayerSchema, { index: 0, ticks: 5 })],
      poolAfterLayers: [create(PoolLayerSchema, { index: 0, ticks: 7 })],
    }) }),
    create(ReplayTurnSchema, { settled: true, summary: create(TurnSummarySchema, {
      turn: 10, sourceUserId: "user-man", dieOne: 4, dieTwo: 4, sum: 8, effect: Effect.DOUBLE_FOUR_HALF_POOL_REROLL,
      poolBeforeTicks: 7, poolAfterTicks: 3, penaltyUserId: "user-man", penaltyTicks: 4, directionBefore: 1, directionAfter: 1,
      nextUserId: "user-man", outcome: TurnOutcome.REROLL, resolutionReason: "double_four",
      poolBeforeLayers: [create(PoolLayerSchema, { index: 0, ticks: 7 })],
      poolAfterLayers: [create(PoolLayerSchema, { index: 0, ticks: 3 })],
    }) }),
  ],
});

export const applyDice789FixtureAction = (view: View, input: ActionInput, now = Date.now()): View => {
  const decoded = fromBinary(CommandSchema, input.message.payload);
  switch (decoded.command.case) {
  case "roll": return rolledView(now, 3, 5, Effect.SUM_EIGHT_HALF_POOL);
  case "confirmLanded": return dice789FixtureView("continue", now);
  case "addToPool": return dice789FixtureView("continue", now);
  case "chooseTarget": return baseView(now);
  case "reroll": return create(ViewSchema, { ...baseView(now), turn: view.turn + 1 });
  case "pass": return create(ViewSchema, { ...baseView(now), turn: view.turn + 1, currentUserId: "user-qing", allowedActions: [] });
  case "reportDropped": return create(ViewSchema, { ...baseView(now), turn: view.turn + 1, pool: [create(PoolLayerSchema, { index: 0, ticks: 0 })], totalPoolTicks: 0 });
  default: return view;
  }
};

export const finishDice789Fixture = (view: View): View => create(ViewSchema, {
  ...view,
  phase: Phase.FINISHED,
  currentUserId: "",
  sourceUserId: "",
  targetUserId: "",
  dieOne: 0,
  dieTwo: 0,
  sum: 0,
  actionDeadlineUnixMillis: 0n,
  allowedActions: [],
  effect: Effect.UNSPECIFIED,
  finishReason: "host_requested",
});
