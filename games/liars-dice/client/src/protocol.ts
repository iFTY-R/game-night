import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { hasOrderedActionPrefix, type ActionInput, type GameEnvelope, type GameProjection, type ProjectionReducer } from "@game-night/game-client";

import {
  BidMode,
  BidSchema,
  CommandSchema,
  FinishSchema,
  OpenDiceSchema,
  PlaceBidSchema,
  ReplaySchema,
  ViewDeltaSchema,
  ViewSchema,
  type View,
  type Replay,
} from "./generated/game/liars_dice/v1/liars_dice_pb";
import {
  LIARS_DICE_BID_ACTION,
  LIARS_DICE_DELTA_MESSAGE,
  LIARS_DICE_GAME_ID,
  LIARS_DICE_OPEN_ACTION,
  LIARS_DICE_REPLAY_MESSAGE,
  LIARS_DICE_SCHEMA_VERSION,
  LIARS_DICE_VERSION,
  LIARS_DICE_VIEW_MESSAGE,
} from "./constants";
import type { BidDraft } from "./types";

const assertEnvelope = (envelope: GameEnvelope, messageType: string): void => {
  if (
    envelope.gameId !== LIARS_DICE_GAME_ID ||
    envelope.schemaVersion !== LIARS_DICE_SCHEMA_VERSION ||
    envelope.messageType !== messageType ||
    envelope.version.engine !== LIARS_DICE_VERSION.engine ||
    envelope.version.protocol !== LIARS_DICE_VERSION.protocol ||
    envelope.version.client !== LIARS_DICE_VERSION.client
  ) {
    throw new Error("liars_dice_envelope_invalid");
  }
};

const validateView = (view: View): View => {
  if (view.config === undefined || view.round < 1 || view.players.length < 2 || view.players.length > 8) {
    throw new Error("liars_dice_view_invalid");
  }
  const users = new Set<string>();
  const seats = new Set<number>();
  for (const player of view.players) {
    if (!player.userId || users.has(player.userId) || seats.has(player.seatIndex)) throw new Error("liars_dice_view_invalid");
    users.add(player.userId);
    seats.add(player.seatIndex);
  }
  if (view.ownDice.some((face) => face < 1 || face > 6) || view.revealedDice.some((roll) => roll.faces.some((face) => face < 1 || face > 6))) {
    throw new Error("liars_dice_view_invalid");
  }
  return view;
};

/**
 * The module-owned actions are embedded in the opaque view, while the platform
 * may append host-only actions such as session.finish to the outer projection.
 * Requiring an exact match would reject that valid platform authorization.
 */
const validateProjectionActions = (viewActions: readonly string[], projectionActions: readonly string[]): void => {
  if (!hasOrderedActionPrefix(viewActions, projectionActions)) {
    throw new Error("liars_dice_actions_mismatch");
  }
};

export const liarsDiceReducer: ProjectionReducer<View> = {
  fromProjection(projection: GameProjection): View {
    assertEnvelope(projection.view, LIARS_DICE_VIEW_MESSAGE);
    const view = validateView(fromBinary(ViewSchema, projection.view.payload));
    if (projection.viewerRole !== "player" && view.ownDice.length > 0) throw new Error("liars_dice_private_dice_leak");
    validateProjectionActions(view.allowedActions, projection.allowedActions);
    return view;
  },
  moduleActions: (view) => view.allowedActions,
  applyDelta(_current, delta) {
    if (delta.messages.length !== 1) throw new Error("liars_dice_delta_invalid");
    const envelope = delta.messages[0];
    if (envelope === undefined) throw new Error("liars_dice_delta_invalid");
    assertEnvelope(envelope, LIARS_DICE_DELTA_MESSAGE);
    const decoded = fromBinary(ViewDeltaSchema, envelope.payload);
    if (decoded.view === undefined) throw new Error("liars_dice_delta_invalid");
    const view = validateView(decoded.view);
    return { view, allowedActions: view.allowedActions };
  },
};

// Replay decoding is deliberately separate from live deltas because replay artifacts are immutable and read-only.
export const decodeLiarsDiceReplay = (projection: GameProjection): Replay => {
  if (projection.viewerRole !== "replay" || projection.allowedActions.length !== 0) {
    throw new Error("liars_dice_replay_projection_invalid");
  }
  assertEnvelope(projection.view, LIARS_DICE_REPLAY_MESSAGE);
  const replay = fromBinary(ReplaySchema, projection.view.payload);
  const replayUsers = new Set<string>();
  const replaySeats = new Set<number>();
  if (replay.players.length > 0 && (replay.players.length < 2 || replay.players.length > 8)) throw new Error("liars_dice_replay_invalid");
  for (const player of replay.players) {
    if (!player.userId || replayUsers.has(player.userId) || replaySeats.has(player.seatIndex)) throw new Error("liars_dice_replay_invalid");
    replayUsers.add(player.userId);
    replaySeats.add(player.seatIndex);
  }
  let previousRound = 0;
  for (const round of replay.rounds) {
    const reasonValid = round.reason === "opened" || round.reason === "timeout";
    const revealValid = round.reason === "opened" ? round.diceRevealed && round.dice.length > 0 : !round.diceRevealed && round.dice.length === 0;
    if (round.round <= previousRound || !round.firstActorUserId || !round.loserUserId || !reasonValid || !revealValid ||
      replayUsers.size > 0 && (!replayUsers.has(round.firstActorUserId) || !replayUsers.has(round.loserUserId) ||
        round.openerUserId !== "" && !replayUsers.has(round.openerUserId))) {
      throw new Error("liars_dice_replay_invalid");
    }
    for (const entry of round.bids) {
      if (!entry.userId || replayUsers.size > 0 && !replayUsers.has(entry.userId) || entry.bid === undefined ||
        entry.bid.quantity < 1 || entry.bid.face < 1 || entry.bid.face > 6) {
        throw new Error("liars_dice_replay_invalid");
      }
    }
    if (round.dice.some((roll) => !roll.userId || replayUsers.size > 0 && !replayUsers.has(roll.userId) || roll.faces.some((face) => face < 1 || face > 6))) {
      throw new Error("liars_dice_replay_invalid");
    }
    previousRound = round.round;
  }
  if (replayUsers.size > 0 && replay.revokedUserIds.some((userId) => !replayUsers.has(userId))) throw new Error("liars_dice_replay_invalid");
  return replay;
};

const commandEnvelope = (messageType: string, payload: Uint8Array): GameEnvelope => ({
  gameId: LIARS_DICE_GAME_ID,
  version: LIARS_DICE_VERSION,
  schemaVersion: LIARS_DICE_SCHEMA_VERSION,
  messageType,
  payload,
});

export const createBidAction = (draft: BidDraft): ActionInput => {
  const bid = create(BidSchema, { quantity: draft.quantity, face: draft.face, mode: draft.mode });
  const placeBid = create(PlaceBidSchema, { bid });
  const command = create(CommandSchema, { command: { case: "placeBid", value: placeBid } });
  return { action: LIARS_DICE_BID_ACTION, message: commandEnvelope(LIARS_DICE_BID_ACTION, toBinary(CommandSchema, command)) };
};

export const createOpenAction = (): ActionInput => {
  const command = create(CommandSchema, { command: { case: "openDice", value: create(OpenDiceSchema) } });
  return { action: LIARS_DICE_OPEN_ACTION, message: commandEnvelope(LIARS_DICE_OPEN_ACTION, toBinary(CommandSchema, command)) };
};

/** Builds the host-only system command used to finish a live session. */
export const createFinishAction = (): ActionInput => {
  const command = create(CommandSchema, { command: { case: "finish", value: create(FinishSchema) } });
  return { action: "session.finish", message: commandEnvelope("session.finish", toBinary(CommandSchema, command)) };
};

export type { GameProjection };
export { BidMode };
