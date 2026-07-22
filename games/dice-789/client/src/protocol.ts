import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { hasOrderedActionPrefix, type ActionInput, type GameEnvelope, type GameProjection, type ProjectionReducer } from "@game-night/game-client";

import {
  AddToPoolSchema,
  ChooseTargetSchema,
  CommandSchema,
  ConfirmLandedSchema,
  FinishSchema,
  PassSchema,
  ReplaySchema,
  ReportDroppedSchema,
  RerollSchema,
  RollSchema,
  ViewDeltaSchema,
  ViewSchema,
  type Command,
  type Replay,
  type View,
} from "./generated/game/dice789/v1/dice_789_pb";
import {
  DICE_789_ADD_ACTION,
  DICE_789_CONFIRM_ACTION,
  DICE_789_DELTA_MESSAGE,
  DICE_789_DROPPED_ACTION,
  DICE_789_GAME_ID,
  DICE_789_PASS_ACTION,
  DICE_789_REPLAY_MESSAGE,
  DICE_789_REROLL_ACTION,
  DICE_789_ROLL_ACTION,
  DICE_789_SCHEMA_VERSION,
  DICE_789_TARGET_ACTION,
  DICE_789_VERSION,
  DICE_789_VIEW_MESSAGE,
  SESSION_FINISH_ACTION,
} from "./constants";

const assertEnvelope = (envelope: GameEnvelope, messageType: string): void => {
  if (
    envelope.gameId !== DICE_789_GAME_ID || envelope.schemaVersion !== DICE_789_SCHEMA_VERSION || envelope.messageType !== messageType ||
    envelope.version.engine !== DICE_789_VERSION.engine || envelope.version.protocol !== DICE_789_VERSION.protocol || envelope.version.client !== DICE_789_VERSION.client
  ) throw new Error("dice_789_envelope_invalid");
};

const validateView = (view: View): View => {
  if (view.config === undefined || view.turn < 1 || view.players.length < 2 || view.players.length > 12 || (view.direction !== 1 && view.direction !== 2)) {
    throw new Error("dice_789_view_invalid");
  }
  const users = new Set<string>();
  const seats = new Set<number>();
  for (const player of view.players) {
    if (!player.userId || users.has(player.userId) || seats.has(player.seatIndex)) throw new Error("dice_789_view_invalid");
    users.add(player.userId);
    seats.add(player.seatIndex);
  }
  if (view.pool.some((layer, index) => layer.index !== index) || view.pool.reduce((sum, layer) => sum + layer.ticks, 0) !== view.totalPoolTicks) {
    throw new Error("dice_789_view_invalid");
  }
  const hasDice = view.dieOne !== 0 || view.dieTwo !== 0 || view.sum !== 0;
  if (hasDice && (view.dieOne < 1 || view.dieOne > 6 || view.dieTwo < 1 || view.dieTwo > 6 || view.sum !== view.dieOne + view.dieTwo)) {
    throw new Error("dice_789_view_invalid");
  }
  return view;
};

export const dice789Reducer: ProjectionReducer<View> = {
  fromProjection(projection): View {
    assertEnvelope(projection.view, DICE_789_VIEW_MESSAGE);
    const view = validateView(fromBinary(ViewSchema, projection.view.payload));
    if (!hasOrderedActionPrefix(view.allowedActions, projection.allowedActions)) throw new Error("dice_789_actions_mismatch");
    return view;
  },
  moduleActions: (view) => view.allowedActions,
  applyDelta(_current, delta) {
    if (delta.messages.length !== 1) throw new Error("dice_789_delta_invalid");
    const envelope = delta.messages[0];
    if (envelope === undefined) throw new Error("dice_789_delta_invalid");
    assertEnvelope(envelope, DICE_789_DELTA_MESSAGE);
    const decoded = fromBinary(ViewDeltaSchema, envelope.payload);
    if (decoded.view === undefined) throw new Error("dice_789_delta_invalid");
    const view = validateView(decoded.view);
    return { view, allowedActions: view.allowedActions };
  },
};

export const decodeDice789Replay = (projection: GameProjection): Replay => {
  if (projection.viewerRole !== "replay" || projection.allowedActions.length !== 0) throw new Error("dice_789_replay_projection_invalid");
  assertEnvelope(projection.view, DICE_789_REPLAY_MESSAGE);
  const replay = fromBinary(ReplaySchema, projection.view.payload);
  if (replay.schemaVersion !== 1 || replay.config === undefined || replay.players.length < 2) throw new Error("dice_789_replay_invalid");
  return replay;
};

const commandEnvelope = (messageType: string, command: Command): ActionInput => ({
  action: messageType,
  message: {
    gameId: DICE_789_GAME_ID,
    version: DICE_789_VERSION,
    schemaVersion: DICE_789_SCHEMA_VERSION,
    messageType,
    payload: toBinary(CommandSchema, command),
  },
});

const command = (action: string, value: Command["command"]): ActionInput => commandEnvelope(action, create(CommandSchema, { command: value }));

export const createRollAction = (): ActionInput => command(DICE_789_ROLL_ACTION, { case: "roll", value: create(RollSchema) });
export const createConfirmAction = (): ActionInput => command(DICE_789_CONFIRM_ACTION, { case: "confirmLanded", value: create(ConfirmLandedSchema) });
export const createAddAction = (ticks: number): ActionInput => command(DICE_789_ADD_ACTION, { case: "addToPool", value: create(AddToPoolSchema, { ticks }) });
export const createTargetAction = (userId: string): ActionInput => command(DICE_789_TARGET_ACTION, { case: "chooseTarget", value: create(ChooseTargetSchema, { userId }) });
export const createRerollAction = (): ActionInput => command(DICE_789_REROLL_ACTION, { case: "reroll", value: create(RerollSchema) });
export const createPassAction = (): ActionInput => command(DICE_789_PASS_ACTION, { case: "pass", value: create(PassSchema) });
export const createDroppedAction = (reason: string): ActionInput => command(DICE_789_DROPPED_ACTION, { case: "reportDropped", value: create(ReportDroppedSchema, { reason }) });
export const createFinishAction = (operatorUserId: string): ActionInput => command(SESSION_FINISH_ACTION, { case: "finish", value: create(FinishSchema, { operatorUserId }) });
