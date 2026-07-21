import { create, fromBinary } from "@bufbuild/protobuf";
import type { ActionInput } from "@game-night/game-client";

import {
  BidMode,
  BidSchema,
  CommandSchema,
  ConfigSchema,
  Phase,
  PrivateDiceSchema,
  PublicPlayerSchema,
  ReplayBidSchema,
  ReplayRoundSchema,
  ReplaySchema,
  RoundSettledSchema,
  ViewSchema,
  type View,
} from "./generated/game/liars_dice/v1/liars_dice_pb";
import type { LiarsDiceTableContext } from "./types";

export const liarsDiceFixtureView = (now = Date.now()): View =>
  create(ViewSchema, {
    phase: Phase.BIDDING,
    round: 3,
    players: [
      create(PublicPlayerSchema, { userId: "user-qing", seatIndex: 0, active: true, penaltyTicks: 0, hasPrivateDice: true }),
      create(PublicPlayerSchema, { userId: "user-man", seatIndex: 1, active: true, penaltyTicks: 4, hasPrivateDice: true }),
      create(PublicPlayerSchema, { userId: "user-nan", seatIndex: 2, active: true, penaltyTicks: 2, hasPrivateDice: true }),
      create(PublicPlayerSchema, { userId: "user-self", seatIndex: 3, active: true, penaltyTicks: 0, hasPrivateDice: true }),
    ],
    ownDice: [2, 4, 4, 5, 6],
    currentBid: create(BidSchema, { quantity: 6, face: 4, mode: BidMode.FLYING }),
    hasCurrentBid: true,
    currentActorUserId: "user-self",
    actionDeadlineUnixMillis: BigInt(now + 30_000),
    allowedActions: ["round.bid", "round.open", "session.finish"],
    revealedDice: [],
    config: create(ConfigSchema, {
      dicePerPlayer: 5,
      onesWild: true,
      strictEnabled: true,
      flyingEnabled: true,
      firstBidMinimum: 4,
      penaltyTicks: 4,
      actionTimeoutSeconds: 30,
    }),
  });

export const liarsDiceRevealedFixture = (now = Date.now()): View => {
  const view = liarsDiceFixtureView(now);
  const { currentBid: _currentBid, ...viewWithoutBid } = view;
  const revealedDice = [
    create(PrivateDiceSchema, { userId: "user-qing", faces: [1, 2, 3, 4, 6] }),
    create(PrivateDiceSchema, { userId: "user-man", faces: [2, 4, 4, 5, 6] }),
    create(PrivateDiceSchema, { userId: "user-nan", faces: [1, 3, 3, 5, 5] }),
    create(PrivateDiceSchema, { userId: "user-self", faces: [2, 4, 4, 5, 6] }),
  ];
  return create(ViewSchema, {
    ...viewWithoutBid,
    round: 4,
    ownDice: [6, 2, 2, 3, 5],
    hasCurrentBid: false,
    currentActorUserId: "user-self",
    actionDeadlineUnixMillis: BigInt(now + 30_000),
    revealedDice,
    lastSettlement: create(RoundSettledSchema, {
      loserUserId: "user-self",
      penaltyTicks: 4,
      nextRound: 4,
      actualQuantity: 7,
      reason: "opened",
      openerUserId: "user-self",
      bid: create(BidSchema, { quantity: 6, face: 5, mode: BidMode.FLYING }),
    }),
    allowedActions: ["round.bid", "session.finish"],
  });
};

export const liarsDiceTimeoutFixture = (now = Date.now()): View => {
  const view = liarsDiceFixtureView(now);
  const { currentBid: _currentBid, ...viewWithoutBid } = view;
  return create(ViewSchema, {
    ...viewWithoutBid,
    round: 4,
    ownDice: [6, 2, 2, 3, 5],
    hasCurrentBid: false,
    currentActorUserId: "user-qing",
    actionDeadlineUnixMillis: BigInt(now + 30_000),
    revealedDice: [],
    lastSettlement: create(RoundSettledSchema, {
      loserUserId: "user-qing",
      penaltyTicks: 4,
      nextRound: 4,
      reason: "timeout",
    }),
    allowedActions: ["session.finish"],
  });
};

export const liarsDiceReplayFixture = () =>
  create(ReplaySchema, {
    rounds: [
      create(ReplayRoundSchema, {
        round: 3,
        firstActorUserId: "user-qing",
        bids: [
          create(ReplayBidSchema, { userId: "user-qing", bid: create(BidSchema, { quantity: 4, face: 3, mode: BidMode.FLYING }) }),
          create(ReplayBidSchema, { userId: "user-man", bid: create(BidSchema, { quantity: 4, face: 5, mode: BidMode.FLYING }) }),
          create(ReplayBidSchema, { userId: "user-nan", bid: create(BidSchema, { quantity: 5, face: 5, mode: BidMode.FLYING }) }),
          create(ReplayBidSchema, { userId: "user-self", bid: create(BidSchema, { quantity: 6, face: 5, mode: BidMode.FLYING }) }),
        ],
        dice: [
          create(PrivateDiceSchema, { userId: "user-qing", faces: [1, 2, 3, 4, 6] }),
          create(PrivateDiceSchema, { userId: "user-man", faces: [2, 4, 4, 5, 6] }),
          create(PrivateDiceSchema, { userId: "user-nan", faces: [1, 3, 3, 5, 5] }),
          create(PrivateDiceSchema, { userId: "user-self", faces: [2, 4, 4, 5, 6] }),
        ],
        diceRevealed: true,
        loserUserId: "user-self",
        penaltyTicks: 4,
        actualQuantity: 7,
        reason: "opened",
        openerUserId: "user-self",
        bid: create(BidSchema, { quantity: 6, face: 5, mode: BidMode.FLYING }),
      }),
    ],
  });

export const liarsDiceFixtureContext = (displayName = "你"): LiarsDiceTableContext => ({
  roomCode: "N789",
  selfUserId: "user-self",
  viewerRole: "player",
  connection: "online",
  players: [
    { userId: "user-qing", displayName: "阿青", avatarText: "青", connected: true },
    { userId: "user-man", displayName: "小满", avatarText: "满", connected: true, host: true },
    { userId: "user-nan", displayName: "南风", avatarText: "南", connected: true },
    { userId: "user-self", displayName, avatarText: displayName.slice(0, 1), connected: true },
  ],
});

export const liarsDiceSpectatorFixture = (now = Date.now()): View =>
  create(ViewSchema, { ...liarsDiceFixtureView(now), ownDice: [], allowedActions: [] });

// applyLiarsDiceFixtureAction keeps preview-only state transitions inside the versioned game client.
export const applyLiarsDiceFixtureAction = (view: View, input: ActionInput, selfUserId: string, now = Date.now()): View => {
  const command = fromBinary(CommandSchema, input.message.payload);
  if (command.command.case === "placeBid" && command.command.value.bid !== undefined) {
    const playerIds = view.players.filter((player) => player.active).map((player) => player.userId);
    const currentIndex = playerIds.indexOf(view.currentActorUserId);
    const nextActor = playerIds[(currentIndex + 1) % playerIds.length] ?? view.currentActorUserId;
    return create(ViewSchema, {
      ...view,
      currentBid: command.command.value.bid,
      hasCurrentBid: true,
      currentActorUserId: nextActor,
      actionDeadlineUnixMillis: BigInt(now + 30_000),
      allowedActions: nextActor === selfUserId ? ["round.bid", "round.open", "session.finish"] : ["session.finish"],
    });
  }
  if (command.command.case === "openDice") return liarsDiceRevealedFixture(now);
  return view;
};

export const finishLiarsDiceFixture = (view: View): View =>
  create(ViewSchema, {
    ...view,
    phase: Phase.FINISHED,
    finishReason: "host_requested",
    ownDice: [],
    allowedActions: [],
  });
