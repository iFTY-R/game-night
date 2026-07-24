import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { hasOrderedActionPrefix, type ActionInput, type GameEnvelope, type GameProjection, type ProjectionReducer } from "@game-night/game-client";

import { cardOrdinal, isKnownCardId, sortCardIdsByOrdinal } from "./cards";
import {
  SESSION_FINISH_ACTION,
  THREE_ROUNDS_DELTA_MESSAGE,
  THREE_ROUNDS_GAME_ID,
  THREE_ROUNDS_REPLAY_MESSAGE,
  THREE_ROUNDS_SCHEMA_VERSION,
  THREE_ROUNDS_SUBMIT_SELECTION_ACTION,
  THREE_ROUNDS_VERSION,
  THREE_ROUNDS_VIEW_MESSAGE,
} from "./constants";
import {
  selectionLimitForPhase,
} from "./controls";
import {
  CommandSchema,
  ConfigSchema,
  Phase,
  ReplaySchema,
  SubmitSelectionSchema,
  ViewDeltaSchema,
  ViewSchema,
  type Command,
  type Config,
  type Replay,
  type View,
} from "./generated/game/three_rounds/v1/three_rounds_pb";
import type { ThreeRoundsConflictResolution } from "./types";

const unique = <T>(values: readonly T[]): boolean => new Set(values).size === values.length;

const assertEnvelope = (envelope: GameEnvelope, messageType: string): void => {
  if (
    envelope.gameId !== THREE_ROUNDS_GAME_ID || envelope.schemaVersion !== THREE_ROUNDS_SCHEMA_VERSION || envelope.messageType !== messageType ||
    envelope.version.engine !== THREE_ROUNDS_VERSION.engine || envelope.version.protocol !== THREE_ROUNDS_VERSION.protocol || envelope.version.client !== THREE_ROUNDS_VERSION.client
  ) throw new Error("three_rounds_envelope_invalid");
};

/** The room editor and fixture routes share one authoritative duration baseline for all four phases. */
export const defaultThreeRoundsConfig = (): Config => create(ConfigSchema, {
  roundOneTimeoutSeconds: 20,
  roundTwoTimeoutSeconds: 30,
  roundResultSeconds: 8,
  finalResultSeconds: 12,
});

export const encodeThreeRoundsConfig = (config: Config): Uint8Array => toBinary(ConfigSchema, config);
export const decodeThreeRoundsConfig = (payload: Uint8Array): Config => validateConfig(fromBinary(ConfigSchema, payload));

const validateConfig = (config: Config): Config => {
  const values = [
    config.roundOneTimeoutSeconds,
    config.roundTwoTimeoutSeconds,
    config.roundResultSeconds,
    config.finalResultSeconds,
  ];
  if (values.some((value) => !Number.isInteger(value) || value < 5 || value > 180)) throw new Error("three_rounds_config_invalid");
  return config;
};

const validateKnownCards = (cardIds: readonly string[], expectedLength: number, code: string): void => {
  if (cardIds.length !== expectedLength || !unique(cardIds) || cardIds.some((cardId) => !isKnownCardId(cardId))) throw new Error(code);
};

const validatePlayer = (player: View["publicPlayers"][number], phase: Phase): void => {
  if (!player.userId || player.seatIndex > 8) throw new Error("three_rounds_view_invalid");
  const resultPhase = phase === Phase.ROUND_ONE_RESULT || phase === Phase.ROUND_TWO_RESULT || phase === Phase.ROUND_THREE_RESULT || phase === Phase.FINAL_RESULT || phase === Phase.FINISHED;
  const roundOneCount = player.roundOneCards?.cardIds.length ?? 0;
  const roundTwoCount = player.roundTwoCards?.cardIds.length ?? 0;
  const roundThreeCount = player.roundThreeCards?.cardIds.length ?? 0;
  if (roundOneCount > 1 || roundTwoCount > 2 || roundThreeCount > 3) throw new Error("three_rounds_view_invalid");
  if (roundOneCount > 0) validateKnownCards(player.roundOneCards?.cardIds ?? [], roundOneCount, "three_rounds_view_invalid");
  if (roundTwoCount > 0) validateKnownCards(player.roundTwoCards?.cardIds ?? [], roundTwoCount, "three_rounds_view_invalid");
  if (roundThreeCount > 0) validateKnownCards(player.roundThreeCards?.cardIds ?? [], roundThreeCount, "three_rounds_view_invalid");
  if (player.submitted && !player.active && !resultPhase) throw new Error("three_rounds_view_invalid");
  if (player.finalRank > 0 && player.totalPoints === 0 && resultPhase && player.active) throw new Error("three_rounds_view_invalid");
};

const knownModuleActions = new Set([THREE_ROUNDS_SUBMIT_SELECTION_ACTION]);

const validateView = (view: View): View => {
  if (view.config === undefined || view.publicPlayers.length < 2 || view.publicPlayers.length > 9 || view.currentRound > 3) {
    throw new Error("three_rounds_view_invalid");
  }
  validateConfig(view.config);
  const users = new Set<string>();
  const seats = new Set<number>();
  for (const player of view.publicPlayers) {
    validatePlayer(player, view.phase);
    if (users.has(player.userId) || seats.has(player.seatIndex)) throw new Error("three_rounds_view_invalid");
    users.add(player.userId);
    seats.add(player.seatIndex);
  }
  if (!unique(view.allowedActions) || view.allowedActions.some((action) => !knownModuleActions.has(action))) throw new Error("three_rounds_view_invalid");
  const selectionLimit = selectionLimitForPhase(view.phase);
  if (selectionLimit === 0 && view.allowedActions.length !== 0) throw new Error("three_rounds_view_invalid");
  if (view.self?.pendingSelection !== undefined) {
    const pending = view.self.pendingSelection;
    const expectedRound = pending.round;
    if (pending.round < 1 || pending.round > 2 || expectedRound !== view.currentRound) throw new Error("three_rounds_view_invalid");
    validateKnownCards(pending.cardIds, pending.round === 1 ? 1 : 2, "three_rounds_view_invalid");
  }
  if (view.self !== undefined) {
    if (!unique(view.self.remainingHand) || view.self.remainingHand.some((cardId) => !isKnownCardId(cardId))) throw new Error("three_rounds_view_invalid");
  }
  if (view.phase === Phase.ROUND_ONE_SELECTING && view.currentRound !== 1) throw new Error("three_rounds_view_invalid");
  if (view.phase === Phase.ROUND_TWO_SELECTING && view.currentRound !== 2) throw new Error("three_rounds_view_invalid");
  if ((view.phase === Phase.FINAL_RESULT || view.phase === Phase.FINISHED) && view.finalSummary === undefined) throw new Error("three_rounds_view_invalid");
  return view;
};

export const threeRoundsReducer: ProjectionReducer<View> = {
  fromProjection(projection): View {
    assertEnvelope(projection.view, THREE_ROUNDS_VIEW_MESSAGE);
    const view = validateView(fromBinary(ViewSchema, projection.view.payload));
    if (!hasOrderedActionPrefix(view.allowedActions, projection.allowedActions)) throw new Error("three_rounds_actions_mismatch");
    return view;
  },
  moduleActions: (view) => view.allowedActions,
  applyDelta(_current, delta) {
    if (delta.messages.length !== 1) throw new Error("three_rounds_delta_invalid");
    const envelope = delta.messages[0];
    if (envelope === undefined) throw new Error("three_rounds_delta_invalid");
    assertEnvelope(envelope, THREE_ROUNDS_DELTA_MESSAGE);
    const decoded = fromBinary(ViewDeltaSchema, envelope.payload);
    if (decoded.view === undefined) throw new Error("three_rounds_delta_invalid");
    const view = validateView(decoded.view);
    return { view, allowedActions: view.allowedActions };
  },
};

export const decodeThreeRoundsReplay = (projection: GameProjection): Replay => {
  if (projection.viewerRole !== "replay" || projection.allowedActions.length !== 0) throw new Error("three_rounds_replay_projection_invalid");
  assertEnvelope(projection.view, THREE_ROUNDS_REPLAY_MESSAGE);
  const replay = fromBinary(ReplaySchema, projection.view.payload);
  if (replay.schemaVersion !== THREE_ROUNDS_SCHEMA_VERSION || replay.config === undefined || replay.players.length < 2 || replay.players.length > 9) {
    throw new Error("three_rounds_replay_invalid");
  }
  validateConfig(replay.config);
  const users = new Set<string>();
  const seats = new Set<number>();
  for (const player of replay.players) {
    if (!player.userId || users.has(player.userId) || seats.has(player.seatIndex) || player.initialHand.length !== 6) throw new Error("three_rounds_replay_invalid");
    if (!unique(player.initialHand) || player.initialHand.some((cardId) => !isKnownCardId(cardId))) throw new Error("three_rounds_replay_invalid");
    users.add(player.userId);
    seats.add(player.seatIndex);
  }
  let expectedSequence = 1n;
  for (const entry of replay.entries) {
    if (entry.sequence !== expectedSequence || entry.event?.event.case === undefined) throw new Error("three_rounds_replay_invalid");
    expectedSequence++;
  }
  let previousRound = 0;
  for (const round of replay.rounds) {
    if (round.round <= previousRound || round.round > 3) throw new Error("three_rounds_replay_invalid");
    previousRound = round.round;
  }
  return replay;
};

const canonicalSelection = (round: number, cardIds: readonly string[]): string[] => {
  const expectedLength = round === 1 ? 1 : round === 2 ? 2 : 0;
  if (expectedLength === 0 || cardIds.length !== expectedLength || !unique(cardIds) || cardIds.some((cardId) => !isKnownCardId(cardId))) {
    throw new Error("three_rounds_selection_invalid");
  }
  return sortCardIdsByOrdinal(cardIds);
};

const commandEnvelope = (messageType: string, command: Command): ActionInput => ({
  action: messageType,
  message: {
    gameId: THREE_ROUNDS_GAME_ID,
    version: THREE_ROUNDS_VERSION,
    schemaVersion: THREE_ROUNDS_SCHEMA_VERSION,
    messageType,
    payload: toBinary(CommandSchema, command),
  },
});

const command = (action: string, value: Command["command"]): ActionInput =>
  commandEnvelope(action, create(CommandSchema, { command: value }));

/** SubmitSelection is canonicalized so conflict retries and deterministic replay compare one stable byte sequence. */
export const createSubmitSelectionAction = (round: number, cardIds: readonly string[]): ActionInput =>
  command(THREE_ROUNDS_SUBMIT_SELECTION_ACTION, {
    case: "submitSelection",
    value: create(SubmitSelectionSchema, { round, cardIds: canonicalSelection(round, cardIds) }),
  });

/** session.finish authority belongs to runtime RequestedByUserID; the client sends only the control-plane intent. */
export const createFinishAction = (): ActionInput => ({
  action: SESSION_FINISH_ACTION,
  message: {
    gameId: THREE_ROUNDS_GAME_ID,
    version: THREE_ROUNDS_VERSION,
    schemaVersion: THREE_ROUNDS_SCHEMA_VERSION,
    messageType: SESSION_FINISH_ACTION,
    payload: new Uint8Array(),
  },
});

/**
 * Simultaneous selection conflicts are safe to replay only while the same round and exact cards remain legal.
 * When the server already persisted the same pending selection, the optimistic write is treated as confirmed.
 */
export const resolveSelectionConflict = (
  latestView: View,
  latestAllowedActions: readonly string[],
  attemptedAction: ActionInput,
): ThreeRoundsConflictResolution => {
  if (attemptedAction.action !== THREE_ROUNDS_SUBMIT_SELECTION_ACTION) return "abort";
  const decoded = fromBinary(CommandSchema, attemptedAction.message.payload);
  if (decoded.command.case !== "submitSelection") return "abort";
  const attempted = decoded.command.value;
  const attemptedCards = sortCardIdsByOrdinal(attempted.cardIds);
  const pending = latestView.self?.pendingSelection;
  if (pending !== undefined && pending.round === attempted.round && sortCardIdsByOrdinal(pending.cardIds).join(",") === attemptedCards.join(",")) {
    return "confirmed";
  }
  if (!latestAllowedActions.includes(THREE_ROUNDS_SUBMIT_SELECTION_ACTION)) return "abort";
  if (latestView.currentRound !== attempted.round || selectionLimitForPhase(latestView.phase) !== attemptedCards.length) return "abort";
  const hand = new Set(latestView.self?.remainingHand ?? []);
  return attemptedCards.every((cardId) => hand.has(cardId)) ? "retry" : "abort";
};
