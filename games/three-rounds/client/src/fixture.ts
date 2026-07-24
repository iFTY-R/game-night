import { create, fromBinary } from "@bufbuild/protobuf";
import type { ActionInput } from "@game-night/game-client";

import { sortCardIdsByOrdinal } from "./cards";
import { defaultThreeRoundsConfig } from "./protocol";
import type { ThreeRoundsFixtureState, ThreeRoundsReplay, ThreeRoundsTableContext, ThreeRoundsView } from "./types";
import {
  CommandSchema,
  EventSchema,
  FinalStandingSchema,
  FinalSummarySchema,
  FinishReason,
  Phase,
  PlayerRevealSchema,
  PublicPlayerSchema,
  ReplayEntrySchema,
  ReplayPlayerSchema,
  ReplaySchema,
  RoundCardsSchema,
  RoundOneEvaluationSchema,
  RoundSummarySchema,
  RoundThreeEvaluationSchema,
  RoundTwoEvaluationSchema,
  SelfViewSchema,
  PendingSelectionSchema,
  SelectionSubmittedSchema,
  RoundSettledSchema,
  SessionFinishedSchema,
  SessionStartedSchema,
  ViewSchema,
  type Event,
} from "./generated/game/three_rounds/v1/three_rounds_pb";

const hostUserId = "user-self";
const fixtureHands = {
  "user-qing": ["2D", "7C", "JH", "QH", "KH", "AH"],
  "user-man": ["3S", "5C", "6D", "9C", "10H", "BJ"],
  "user-nan": ["4H", "8S", "10D", "JD", "QC", "KS"],
  "user-self": ["2S", "8D", "9H", "10S", "SJ", "AS"],
} as const;

const basePlayers = () => [
  create(PublicPlayerSchema, { userId: "user-qing", seatIndex: 0, active: true }),
  create(PublicPlayerSchema, { userId: "user-man", seatIndex: 1, active: true }),
  create(PublicPlayerSchema, { userId: "user-nan", seatIndex: 2, active: true }),
  create(PublicPlayerSchema, { userId: "user-self", seatIndex: 3, active: true }),
];

const finalSummary = () => create(FinalSummarySchema, {
  standings: [
    create(FinalStandingSchema, { userId: "user-qing", seatIndex: 0, active: true, totalPoints: 2, wonRoundOne: true, rank: 1, winner: true }),
    create(FinalStandingSchema, { userId: "user-man", seatIndex: 1, active: true, totalPoints: 2, wonRoundTwo: true, rank: 1, winner: true }),
    create(FinalStandingSchema, { userId: "user-self", seatIndex: 3, active: true, totalPoints: 1, wonRoundThree: true, rank: 3 }),
    create(FinalStandingSchema, { userId: "user-nan", seatIndex: 2, active: true, totalPoints: 0, rank: 4 }),
  ],
  winnerUserIds: ["user-qing", "user-man"],
});

const roundHistory = () => [
  create(RoundSummarySchema, {
    round: 1,
    winnerUserIds: ["user-qing"],
    reveals: [
      create(PlayerRevealSchema, { userId: "user-qing", seatIndex: 0, active: true, cardIds: ["AH"], awardedPoints: 1, roundOne: create(RoundOneEvaluationSchema, { cardId: "AH", rankStrength: 14, suitStrength: 3 }) }),
      create(PlayerRevealSchema, { userId: "user-man", seatIndex: 1, active: true, cardIds: ["BJ"], awardedPoints: 0, roundOne: create(RoundOneEvaluationSchema, { cardId: "BJ", rankStrength: 16, suitStrength: 0 }) }),
      create(PlayerRevealSchema, { userId: "user-nan", seatIndex: 2, active: true, cardIds: ["KS"], awardedPoints: 0, roundOne: create(RoundOneEvaluationSchema, { cardId: "KS", rankStrength: 13, suitStrength: 4 }) }),
      create(PlayerRevealSchema, { userId: "user-self", seatIndex: 3, active: true, cardIds: ["AS"], awardedPoints: 0, roundOne: create(RoundOneEvaluationSchema, { cardId: "AS", rankStrength: 14, suitStrength: 4 }) }),
    ],
  }),
  create(RoundSummarySchema, {
    round: 2,
    winnerUserIds: ["user-man"],
    allBusted: false,
    reveals: [
      create(PlayerRevealSchema, { userId: "user-qing", seatIndex: 0, active: true, cardIds: ["KH", "QH"], awardedPoints: 0, roundTwo: create(RoundTwoEvaluationSchema, { totalHalfPoints: 2, rankStrengthsDesc: [13, 12] }) }),
      create(PlayerRevealSchema, { userId: "user-man", seatIndex: 1, active: true, cardIds: ["10H", "BJ"], awardedPoints: 1, roundTwo: create(RoundTwoEvaluationSchema, { totalHalfPoints: 11, rankStrengthsDesc: [10, 16] }) }),
      create(PlayerRevealSchema, { userId: "user-nan", seatIndex: 2, active: true, cardIds: ["10D", "KS"], awardedPoints: 0, roundTwo: create(RoundTwoEvaluationSchema, { totalHalfPoints: 11, busted: true, rankStrengthsDesc: [13, 10] }) }),
      create(PlayerRevealSchema, { userId: "user-self", seatIndex: 3, active: true, cardIds: ["SJ", "AS"], awardedPoints: 0, roundTwo: create(RoundTwoEvaluationSchema, { totalHalfPoints: 3, rankStrengthsDesc: [14, 15] }) }),
    ],
  }),
  create(RoundSummarySchema, {
    round: 3,
    winnerUserIds: ["user-self"],
    reveals: [
      create(PlayerRevealSchema, { userId: "user-qing", seatIndex: 0, active: true, cardIds: ["2D", "7C", "JH"], awardedPoints: 0, roundThree: create(RoundThreeEvaluationSchema, { compareValues: [11, 7, 2] }) }),
      create(PlayerRevealSchema, { userId: "user-man", seatIndex: 1, active: true, cardIds: ["3S", "5C", "6D"], awardedPoints: 0, roundThree: create(RoundThreeEvaluationSchema, { handClass: 3, compareValues: [6] }) }),
      create(PlayerRevealSchema, { userId: "user-nan", seatIndex: 2, active: true, cardIds: ["4H", "8S", "QC"], awardedPoints: 0, roundThree: create(RoundThreeEvaluationSchema, { compareValues: [12, 8, 4] }) }),
      create(PlayerRevealSchema, { userId: "user-self", seatIndex: 3, active: true, cardIds: ["2S", "8D", "9H"], awardedPoints: 1, roundThree: create(RoundThreeEvaluationSchema, { handClass: 3, compareValues: [9] }) }),
    ],
  }),
];

const baseView = (now: number): ThreeRoundsView => create(ViewSchema, {
  phase: Phase.ROUND_ONE_SELECTING,
  currentRound: 1,
  phaseDeadlineUnixMillis: BigInt(now + 20_000),
  phaseGeneration: 1,
  config: defaultThreeRoundsConfig(),
  viewerIsHost: true,
  self: {
    remainingHand: [...fixtureHands["user-self"]],
  },
  publicPlayers: basePlayers(),
  allowedActions: ["round.submit_selection"],
  roundHistory: [],
});

export const threeRoundsFixtureView = (state: ThreeRoundsFixtureState = "active", now = Date.now()): ThreeRoundsView => {
  if (state === "pending") {
    return create(ViewSchema, {
      ...baseView(now),
      self: create(SelfViewSchema, {
        remainingHand: [...fixtureHands["user-self"]],
        pendingSelection: create(PendingSelectionSchema, { round: 1, cardIds: ["AS"], autoSubmitted: false }),
      }),
      publicPlayers: basePlayers().map((player) => player.userId === "user-self" ? create(PublicPlayerSchema, { ...player, submitted: true }) : player),
    });
  }
  if (state === "round-two") {
    return create(ViewSchema, {
      ...baseView(now),
      phase: Phase.ROUND_TWO_SELECTING,
      currentRound: 2,
      phaseDeadlineUnixMillis: BigInt(now + 30_000),
      self: create(SelfViewSchema, {
        remainingHand: sortCardIdsByOrdinal(["2S", "8D", "9H", "10S", "SJ"]),
      }),
      publicPlayers: basePlayers().map((player) => create(PublicPlayerSchema, {
        ...player,
        roundOneCards: create(RoundCardsSchema, { cardIds: [player.userId === "user-self" ? "AS" : player.userId === "user-qing" ? "AH" : player.userId === "user-man" ? "BJ" : "KS"] }),
        roundOnePoints: player.userId === "user-qing" ? 1 : 0,
        wonRoundOne: player.userId === "user-qing",
      })),
      roundHistory: [roundHistory()[0]!],
    });
  }
  if (state === "round-three") {
    return create(ViewSchema, {
      ...baseView(now),
      phase: Phase.ROUND_THREE_RESULT,
      currentRound: 3,
      phaseDeadlineUnixMillis: BigInt(now + 8_000),
      allowedActions: [],
      self: create(SelfViewSchema, { remainingHand: ["2S", "8D", "9H"] }),
      publicPlayers: roundHistory()[2]!.reveals.map((reveal) => create(PublicPlayerSchema, {
        userId: reveal.userId,
        seatIndex: reveal.seatIndex,
        active: reveal.active,
        roundOnePoints: reveal.userId === "user-qing" ? 1 : 0,
        roundTwoPoints: reveal.userId === "user-man" ? 1 : 0,
        roundThreePoints: reveal.userId === "user-self" ? 1 : 0,
        totalPoints: reveal.userId === "user-self" || reveal.userId === "user-man" || reveal.userId === "user-qing" ? 1 : 0,
        roundThreeCards: create(RoundCardsSchema, { cardIds: reveal.cardIds }),
        wonRoundThree: reveal.userId === "user-self",
      })),
      roundHistory: roundHistory().slice(0, 3),
    });
  }
  if (state === "final") {
    return create(ViewSchema, {
      ...threeRoundsFixtureView("round-three", now),
      phase: Phase.FINAL_RESULT,
      phaseDeadlineUnixMillis: BigInt(now + 12_000),
      finalSummary: finalSummary(),
      publicPlayers: threeRoundsFixtureView("round-three", now).publicPlayers.map((player) => {
        const standing = finalSummary().standings.find((candidate) => candidate.userId === player.userId);
        return create(PublicPlayerSchema, {
          ...player,
          totalPoints: standing?.totalPoints ?? 0,
          finalRank: standing?.rank ?? 0,
          finalWinner: standing?.winner ?? false,
          wonRoundOne: standing?.wonRoundOne ?? false,
          wonRoundTwo: standing?.wonRoundTwo ?? false,
          wonRoundThree: standing?.wonRoundThree ?? false,
        });
      }),
    });
  }
  if (state === "spectator") return create(ViewSchema, { ...baseView(now), viewerIsHost: false, self: create(SelfViewSchema), allowedActions: [] });
  if (state === "reconnecting") return create(ViewSchema, { ...baseView(now) });
  if (state === "finished") {
    return create(ViewSchema, {
      ...threeRoundsFixtureView("final", now),
      phase: Phase.FINISHED,
      phaseDeadlineUnixMillis: 0n,
      finishReason: FinishReason.HOST_REQUESTED,
    });
  }
  return baseView(now);
};

export const threeRoundsFixtureContext = (displayName = "你", state: ThreeRoundsFixtureState = "active"): ThreeRoundsTableContext => ({
  roomCode: "3RND",
  selfUserId: hostUserId,
  viewerRole: state === "spectator" ? "spectator" : state === "replay" ? "replay" : "player",
  connection: state === "reconnecting" ? "reconnecting" : "online",
  players: [
    { userId: "user-qing", displayName: "阿青", avatarText: "青", connected: true, seatIndex: 0 },
    { userId: "user-man", displayName: "小满", avatarText: "满", connected: true, seatIndex: 1 },
    { userId: "user-nan", displayName: "南风", avatarText: "南", connected: true, seatIndex: 2 },
    { userId: "user-self", displayName, avatarText: displayName.slice(0, 1), connected: true, host: true, seatIndex: 3 },
  ],
});

const replayPlayers = () => Object.entries(fixtureHands).map(([userId, initialHand], seatIndex) => create(ReplayPlayerSchema, {
  userId,
  seatIndex,
  initialHand: [...initialHand],
}));

const replayEntry = (sequence: number, event: Event["event"]) =>
  create(ReplayEntrySchema, {
    sequence: BigInt(sequence),
    event: create(EventSchema, { event }),
  });

export const threeRoundsReplayFixture = (): ThreeRoundsReplay => create(ReplaySchema, {
  schemaVersion: 1,
  config: defaultThreeRoundsConfig(),
  players: replayPlayers(),
  finishReason: FinishReason.HOST_REQUESTED,
  finalSummary: finalSummary(),
  rounds: roundHistory(),
  entries: [
    replayEntry(1, { case: "sessionStarted", value: create(SessionStartedSchema, { config: defaultThreeRoundsConfig(), players: replayPlayers(), hostUserId }) }),
    replayEntry(2, { case: "selectionSubmitted", value: create(SelectionSubmittedSchema, { userId: "user-self", round: 1 }) }),
    replayEntry(3, { case: "roundSettled", value: create(RoundSettledSchema, { summary: roundHistory()[0]! }) }),
    replayEntry(4, { case: "selectionSubmitted", value: create(SelectionSubmittedSchema, { userId: "user-self", round: 2 }) }),
    replayEntry(5, { case: "roundSettled", value: create(RoundSettledSchema, { summary: roundHistory()[1]! }) }),
    replayEntry(6, { case: "roundSettled", value: create(RoundSettledSchema, { summary: roundHistory()[2]! }) }),
    replayEntry(7, { case: "sessionFinished", value: create(SessionFinishedSchema, { reason: FinishReason.HOST_REQUESTED, operatorUserId: hostUserId }) }),
  ],
});

/** Fixture mode mirrors the live command path closely enough to exercise selection limits and replay tabs. */
export const applyThreeRoundsFixtureAction = (view: ThreeRoundsView, input: ActionInput, now = Date.now()): ThreeRoundsView => {
  const commandMessage = fromBinary(CommandSchema, input.message.payload);
  if (commandMessage.command.case === "submitSelection") {
    if (commandMessage.command.value.round === 1) return threeRoundsFixtureView("round-two", now);
    if (commandMessage.command.value.round === 2) return threeRoundsFixtureView("round-three", now);
  }
  return view;
};

export const finishThreeRoundsFixture = (view: ThreeRoundsView): ThreeRoundsView => create(ViewSchema, {
  ...view,
  phase: Phase.FINISHED,
  allowedActions: [],
  phaseDeadlineUnixMillis: 0n,
  finishReason: FinishReason.HOST_REQUESTED,
  finalSummary: view.finalSummary ?? finalSummary(),
});
