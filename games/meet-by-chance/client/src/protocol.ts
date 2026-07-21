import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { ActionInput, GameEnvelope, GameProjection, ProjectionReducer } from "@game-night/game-client";

import {
  CommandSchema,
  FinishSchema,
  HandClass,
  MatchKind,
  Phase,
  ReplaySchema,
  RerollSchema,
  Special235Outcome,
  StandSchema,
  ViewDeltaSchema,
  ViewSchema,
  type Command,
  type MatchBatch,
  type PublicPlayer,
  type Replay,
  type View,
} from "./generated/game/meet_by_chance/v1/meet_by_chance_pb";
import {
  MEET_BY_CHANCE_DELTA_MESSAGE,
  MEET_BY_CHANCE_GAME_ID,
  MEET_BY_CHANCE_REPLAY_MESSAGE,
  MEET_BY_CHANCE_REROLL_ACTION,
  MEET_BY_CHANCE_SCHEMA_VERSION,
  MEET_BY_CHANCE_STAND_ACTION,
  MEET_BY_CHANCE_VERSION,
  MEET_BY_CHANCE_VIEW_MESSAGE,
  SESSION_FINISH_ACTION,
} from "./constants";

const assertEnvelope = (envelope: GameEnvelope, messageType: string): void => {
  if (
    envelope.gameId !== MEET_BY_CHANCE_GAME_ID || envelope.schemaVersion !== MEET_BY_CHANCE_SCHEMA_VERSION || envelope.messageType !== messageType ||
    envelope.version.engine !== MEET_BY_CHANCE_VERSION.engine || envelope.version.protocol !== MEET_BY_CHANCE_VERSION.protocol || envelope.version.client !== MEET_BY_CHANCE_VERSION.client
  ) throw new Error("meet_by_chance_envelope_invalid");
};

const facesValid = (faces: readonly number[], sorted = false): boolean => faces.length === 3 && faces.every((face, index) => (
  face >= 1 && face <= 6 && (!sorted || index === 0 || (faces[index - 1] ?? 0) <= face)
));

const handFactsValid = (player: Pick<PublicPlayer, "dice" | "normalizedDice" | "handClass" | "special235" | "special235Outcome">): boolean => {
  const classValid = player.handClass >= HandClass.SINGLE && player.handClass <= HandClass.SPECIAL_235;
  const specialValid = player.special235 === (player.handClass === HandClass.SPECIAL_235)
    && (player.special235
      ? player.special235Outcome === Special235Outcome.BEATS_LEOPARDS || player.special235Outcome === Special235Outcome.MINIMUM_SINGLE
      : player.special235Outcome === Special235Outcome.NOT_APPLICABLE);
  return facesValid(player.dice) && facesValid(player.normalizedDice, true) && classValid && specialValid;
};

const validatePublicPlayer = (player: PublicPlayer): void => {
  if (!player.userId || !handFactsValid(player)) {
    throw new Error("meet_by_chance_view_invalid");
  }
};

const validateMatchBatch = (batch: MatchBatch, users: ReadonlySet<string>, expectedRound: number, resolutionLimit?: number): void => {
  const seen = new Set<string>();
  const rerolled = new Set<string>();
  if (batch.round !== expectedRound || batch.batchIndex !== batch.resolutionCount || batch.groups.length === 0 ||
    resolutionLimit !== undefined && batch.resolutionCount > resolutionLimit) throw new Error("meet_by_chance_view_invalid");
  for (const group of batch.groups) {
    if (group.kind < MatchKind.EXACT || group.kind > MatchKind.LOWEST || group.userIds.length < 2 || group.userIds.some((userId) => !users.has(userId) || seen.has(userId))) {
      throw new Error("meet_by_chance_view_invalid");
    }
    group.userIds.forEach((userId) => { seen.add(userId); rerolled.add(userId); });
    if (group.kind === MatchKind.LOWEST ? !group.userIds.includes(group.weakestUserId) : group.weakestUserId !== "" || group.weakExtraPenaltyTicks !== 0) {
      throw new Error("meet_by_chance_view_invalid");
    }
    if (batch.capped && (group.penaltyTicks !== 0 || group.weakExtraPenaltyTicks !== 0)) throw new Error("meet_by_chance_view_invalid");
  }
  if (batch.capped ? batch.rerolledUserIds.length !== 0 : batch.rerolledUserIds.length !== rerolled.size || batch.rerolledUserIds.some((userId) => !rerolled.delete(userId))) {
    throw new Error("meet_by_chance_view_invalid");
  }
};

const validateView = (view: View): View => {
  if (view.config === undefined || view.round < 1 || view.players.length !== 0 || view.publicPlayers.length < 3 || view.publicPlayers.length > 12 ||
    (view.phase !== Phase.TARGET_DECISION && view.phase !== Phase.FINISHED) || view.targetRerollCount > view.targetRerollLimit ||
    view.targetStreak > view.targetRerollCount || view.matchResolutionCount > view.matchResolutionLimit) {
    throw new Error("meet_by_chance_view_invalid");
  }
  const users = new Set<string>();
  const seats = new Set<number>();
  for (const player of view.publicPlayers) {
    validatePublicPlayer(player);
    if (users.has(player.userId) || seats.has(player.seatIndex)) throw new Error("meet_by_chance_view_invalid");
    users.add(player.userId);
    seats.add(player.seatIndex);
  }
  const knownActions = new Set([MEET_BY_CHANCE_REROLL_ACTION, MEET_BY_CHANCE_STAND_ACTION]);
  if (new Set(view.allowedActions).size !== view.allowedActions.length || view.allowedActions.some((action) => !knownActions.has(action))) {
    throw new Error("meet_by_chance_view_invalid");
  }
  const target = view.publicPlayers.find((player) => player.userId === view.targetUserId);
  if (view.phase === Phase.TARGET_DECISION && (target === undefined || !target.active) || view.phase === Phase.FINISHED && (
    view.targetUserId !== "" || view.actionDeadlineUnixMillis !== 0n || view.allowedActions.length !== 0 || !view.finishReason
  )) {
    throw new Error("meet_by_chance_view_invalid");
  }
  if (view.lastMatchBatch !== undefined) {
    validateMatchBatch(view.lastMatchBatch, users, view.round, view.matchResolutionLimit);
  }
  return view;
};

export const meetByChanceReducer: ProjectionReducer<View> = {
  fromProjection(projection): View {
    assertEnvelope(projection.view, MEET_BY_CHANCE_VIEW_MESSAGE);
    const view = validateView(fromBinary(ViewSchema, projection.view.payload));
    if (view.allowedActions.join("\0") !== projection.allowedActions.join("\0")) throw new Error("meet_by_chance_actions_mismatch");
    return view;
  },
  applyDelta(_current, delta) {
    if (delta.messages.length !== 1) throw new Error("meet_by_chance_delta_invalid");
    const envelope = delta.messages[0];
    if (envelope === undefined) throw new Error("meet_by_chance_delta_invalid");
    assertEnvelope(envelope, MEET_BY_CHANCE_DELTA_MESSAGE);
    const decoded = fromBinary(ViewDeltaSchema, envelope.payload);
    if (decoded.view === undefined) throw new Error("meet_by_chance_delta_invalid");
    const view = validateView(decoded.view);
    return { view, allowedActions: view.allowedActions };
  },
};

export const decodeMeetByChanceReplay = (projection: GameProjection): Replay => {
  if (projection.viewerRole !== "replay" || projection.allowedActions.length !== 0) throw new Error("meet_by_chance_replay_projection_invalid");
  assertEnvelope(projection.view, MEET_BY_CHANCE_REPLAY_MESSAGE);
  const replay = fromBinary(ReplaySchema, projection.view.payload);
  if (replay.schemaVersion !== MEET_BY_CHANCE_SCHEMA_VERSION || replay.config === undefined || replay.players.length < 3 || replay.players.length > 12) {
    throw new Error("meet_by_chance_replay_invalid");
  }
  const replayUsers = new Map<string, number>();
  const replaySeats = new Set<number>();
  for (const player of replay.players) {
    if (!player.userId || replayUsers.has(player.userId) || replaySeats.has(player.seatIndex)) throw new Error("meet_by_chance_replay_invalid");
    replayUsers.set(player.userId, player.seatIndex);
    replaySeats.add(player.seatIndex);
  }
  let expectedSequence = 1n;
  for (const entry of replay.entries) {
    if (entry.sequence !== expectedSequence || entry.event === undefined || entry.event.event.case === undefined) throw new Error("meet_by_chance_replay_invalid");
    const event = entry.event.event;
    if (event.case === "diceRevealed") {
      const rolled = new Set(event.value.rolledUserIds);
      if (event.value.players.length !== 0 || rolled.size !== event.value.rolledUserIds.length || event.value.publicDice.length !== rolled.size ||
        event.value.publicDice.some((value) => !replayUsers.has(value.userId) || !rolled.delete(value.userId) || !facesValid(value.dice))) throw new Error("meet_by_chance_replay_invalid");
    }
    if (event.case === "handClassified" && (event.value.fullKey.length !== 0 || event.value.tieKey.length !== 0 || !replayUsers.has(event.value.userId) || !handFactsValid(event.value))) {
      throw new Error("meet_by_chance_replay_invalid");
    }
    if (event.case === "matchResolved") {
      if (event.value.userIds.length !== 0 || event.value.kind !== "" || event.value.penaltyTicks !== 0 || event.value.batch === undefined) throw new Error("meet_by_chance_replay_invalid");
      validateMatchBatch(event.value.batch, new Set(replayUsers.keys()), event.value.round);
    }
    expectedSequence++;
  }
  let previousRound = 0;
  for (const round of replay.rounds) {
    const summary = round.summary;
    if (summary === undefined || !summary.settled || summary.round <= previousRound || summary.finalPlayers.length !== replay.players.length || !replayUsers.has(summary.targetUserId)) {
      throw new Error("meet_by_chance_replay_invalid");
    }
    const summaryUsers = new Set<string>();
    for (const player of summary.finalPlayers) {
      validatePublicPlayer(player);
      if (summaryUsers.has(player.userId) || replayUsers.get(player.userId) !== player.seatIndex) throw new Error("meet_by_chance_replay_invalid");
      summaryUsers.add(player.userId);
    }
    previousRound = summary.round;
  }
  return replay;
};

const commandEnvelope = (messageType: string, command: Command): ActionInput => ({
  action: messageType,
  message: {
    gameId: MEET_BY_CHANCE_GAME_ID,
    version: MEET_BY_CHANCE_VERSION,
    schemaVersion: MEET_BY_CHANCE_SCHEMA_VERSION,
    messageType,
    payload: toBinary(CommandSchema, command),
  },
});

const command = (action: string, value: Command["command"]): ActionInput => commandEnvelope(action, create(CommandSchema, { command: value }));

export const createRerollAction = (): ActionInput => command(MEET_BY_CHANCE_REROLL_ACTION, { case: "reroll", value: create(RerollSchema) });
export const createStandAction = (): ActionInput => command(MEET_BY_CHANCE_STAND_ACTION, { case: "stand", value: create(StandSchema) });
export const createFinishAction = (operatorUserId: string): ActionInput => command(SESSION_FINISH_ACTION, { case: "finish", value: create(FinishSchema, { operatorUserId }) });
