/**
 * Small same-origin Connect JSON client used by the browser shell.
 * Keeping the transport here makes room and identity views independent from
 * generated protobuf runtime details while preserving the server contract.
 */

import { SubscriptionFailure } from "@game-night/game-client";

import {
  actionRequestDigest,
  beginGameStartRequestDigest,
  cancelGameStartRequestDigest,
  deleteGameRulePresetRequestDigest,
  finishRequestDigest,
  saveGameRulePresetRequestDigest,
  selectRoomGameRequestDigest,
  startRequestDigest,
  updateGameConfigRequestDigest,
} from "./operation-digest";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export interface IdentityUser {
  userId: string;
  status: string;
  username: string;
}

export interface IdentityDevice {
  credentialId: string;
  currentDevice: boolean;
  status: string;
}

export interface IdentityResponse {
  user?: IdentityUser;
  currentDevice?: IdentityDevice;
}

export interface RoomMember {
  userId: string;
  username: string;
  role: string;
  requestedRole: string;
  seatIndex: number;
}

export interface GameEnvelopeWire {
  gameId: string;
  version?: { engine: string; protocol: string; client: string };
  schemaVersion: number;
  messageType: string;
  payload: string | Uint8Array | number[];
}

export interface RoomGameConfigDraftWire {
  gameId: string;
  config?: GameEnvelopeWire;
  revision?: string | number | bigint;
  updatedBy?: string;
  updatedAt?: unknown;
}

export interface PendingGameStartWire {
  pendingStartId?: string;
  cancelToken?: string;
  deadline?: unknown;
  gameId?: string;
  configRevision?: string | number | bigint;
  expectedVersion?: { roomVersion?: string | number | bigint; membershipVersion?: string | number | bigint };
  ownershipEpoch?: string | number | bigint;
}

export interface GameRulePresetWire {
  presetId: string;
  gameId: string;
  name: string;
  config?: GameEnvelopeWire;
  presetRevision?: string | number | bigint;
  createdAt?: unknown;
  updatedAt?: unknown;
  lastUsedAt?: unknown;
  compatible?: boolean;
}

export type GameRulePresetWriteMode =
  | "GAME_RULE_PRESET_WRITE_MODE_CREATE"
  | "GAME_RULE_PRESET_WRITE_MODE_OVERWRITE"
  | "GAME_RULE_PRESET_WRITE_MODE_COPY";

export interface RoomSnapshot {
  roomId: string;
  roomCode: string;
  visibility: string;
  status: string;
  hostUserId: string;
  participantCapacity: number;
  participantAdmission: string;
  spectatorAdmission: string;
  members: RoomMember[];
  activeSessionId: string;
  activeGameId: string;
  lastFinishedSessionId: string;
  lastFinishedGameId: string;
  version?: { roomVersion: string; membershipVersion: string };
  selectedGameId?: string;
  gameConfigDrafts?: RoomGameConfigDraftWire[];
  pendingStart?: PendingGameStartWire;
  ownershipEpoch?: string | number | bigint;
}

export interface RoomResponse {
  room?: RoomSnapshot;
  member?: RoomMember;
  created?: boolean;
  changed?: boolean;
  sessionId?: string;
  gameId?: string;
  draft?: RoomGameConfigDraftWire;
  pendingStart?: PendingGameStartWire;
  participants?: Array<{ userId: string; seatIndex: number }>;
  frozenConfig?: GameEnvelopeWire;
  configRevision?: string;
}

export interface GameRulePresetListResponse {
  presets: GameRulePresetWire[];
}

export interface GameRulePresetResponse {
  preset?: GameRulePresetWire;
}

export interface DeleteGameRulePresetResponse {
  presetId: string;
}

export interface HeartbeatRoomResponse {
  observedAt?: string;
}

export interface PageInfoWire {
  nextPageToken?: string;
}

export interface MyRoomCardWire {
  roomId: string;
  roomCode: string;
  visibility: string;
  hostUsername: string;
  status: string;
  isHost: boolean;
  participantCapacity: number;
  participantCount: number;
  spectatorCount: number;
  waitingCount: number;
  participantAdmission: string;
  spectatorAdmission: string;
  activeGameId: string;
  lastFinishedGameId: string;
  viewerRole: string;
  viewerRequestedRole: string;
  updatedAt?: string;
}

export interface PublicRoomCardWire {
  roomId: string;
  hostUsername: string;
  status: string;
  participantCapacity: number;
  participantCount: number;
  spectatorCount: number;
  waitingCount: number;
  participantAdmission: string;
  spectatorAdmission: string;
  activeGameId: string;
  viewerRole: string;
  viewerRequestedRole: string;
  primaryAction: string;
  updatedAt?: string;
}

export interface MyRoomListResponse {
  rooms: MyRoomCardWire[];
  page?: PageInfoWire;
}

export interface PublicRoomListResponse {
  rooms: PublicRoomCardWire[];
  page?: PageInfoWire;
}

export interface GameEnvelopeInput {
  gameId: string;
  version: { engine: string; protocol: string; client: string };
  schemaVersion: number;
  messageType: string;
  payload: Uint8Array;
}

export interface GameProjectionWire {
  sessionId: string;
  stateVersion: string;
  viewerKind: string;
  view?: {
    gameId: string;
    version?: { engine: string; protocol: string; client: string };
    schemaVersion: number;
    messageType: string;
    payload: string;
  };
  allowedActions: string[];
}

export interface GameProjectionResponse {
  projection?: GameProjectionWire;
}

export interface GameSessionSummaryWire {
  sessionId: string;
  roomId: string;
  gameId: string;
  version?: { engine: string; protocol: string; client: string };
  stateVersion: string;
  status: string;
}

export interface ReplayTerminalMetaWire {
  finished?: boolean;
  cancelled?: boolean;
  endedAt?: string;
  cancelReason?: string;
}

export interface GameReplayProjectionResponse extends GameProjectionResponse {
  session?: GameSessionSummaryWire;
  complete?: boolean;
  terminalMeta?: ReplayTerminalMetaWire;
}

export type ReplayAccessPolicy =
  | "REPLAY_ACCESS_POLICY_PARTICIPANT"
  | "REPLAY_ACCESS_POLICY_ROOM_MEMBER"
  | "REPLAY_ACCESS_POLICY_PUBLIC";

export interface ReplayAccessWire {
  sessionId: string;
  roomId: string;
  policy: ReplayAccessPolicy;
  policyVersion: string;
  memberSnapshotCompletedAt?: string;
  updatedAt?: string;
}

export interface ReplayAccessResponse {
  access?: ReplayAccessWire;
}

export interface GameActionResponse extends GameProjectionResponse {
  sessionId?: string;
  stateVersion?: string;
  resultCode?: string;
  replayed?: boolean;
}

export interface GameSubscriptionResponse extends GameProjectionResponse {
  ticket: Uint8Array;
  grant: Uint8Array;
  expiresAt?: string;
}

declare global {
  type ConfigEnvelopeWire = GameEnvelopeWire;
  type RulePresetWire = GameRulePresetWire;
  type PendingStartWire = PendingGameStartWire;
}

const apiBase = String(import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/$/, "");
const userCSRFName = "__Host-gn_csrf";

const requestID = (): string => {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `web-${Date.now()}-${Math.random().toString(16).slice(2)}`;
};

const readCookie = (name: string): string | undefined => {
  if (typeof document === "undefined") {
    return undefined;
  }
  const prefix = `${name}=`;
  const value = document.cookie.split(";").map((item) => item.trim()).find((item) => item.startsWith(prefix));
  if (!value) {
    return undefined;
  }
  try {
    return decodeURIComponent(value.slice(prefix.length));
  } catch {
    return undefined;
  }
};

const base64 = (bytes: Uint8Array): string => {
  let value = "";
  for (const byte of bytes) value += String.fromCharCode(byte);
  return btoa(value);
};

/** Decodes Connect JSON bytes fields before one-time credentials reach the WebSocket transport. */
const base64Bytes = (encoded: string): Uint8Array => {
  const binary = atob(encoded);
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
};

const invalidSubscriptionCredentials = (cause?: unknown): SubscriptionFailure =>
  new SubscriptionFailure(
    "invalid_subscription_credentials",
    "Subscription credentials are invalid",
    false,
    "reconnecting",
    null,
    cause === undefined ? undefined : { cause },
  );

/** Normalize protobuf JSON/base64 payloads before every rule write. */
const serializeGameEnvelope = (command: GameEnvelopeInput | GameEnvelopeWire): GameEnvelopeWire => {
  const normalized = configDigestInput(command.gameId, command);
  return {
    gameId: normalized.gameId,
    version: normalized.version,
    schemaVersion: normalized.schemaVersion,
    messageType: normalized.messageType,
    payload: base64(normalized.payload),
  };
};

const configDigestInput = (gameId: string, config: GameEnvelopeInput | GameEnvelopeWire): GameEnvelopeInput => ({
  gameId,
  version: config.version ?? { engine: "", protocol: "", client: "" },
  schemaVersion: config.schemaVersion,
  messageType: config.messageType,
  payload: config.payload instanceof Uint8Array
    ? new Uint8Array(config.payload)
    : Array.isArray(config.payload)
      ? Uint8Array.from(config.payload)
      : config.payload.length > 0
        ? base64Bytes(config.payload)
        : new Uint8Array(),
});

const currentRuleVersion = (room: RoomSnapshot): { roomVersion: string; membershipVersion: string; ownershipEpoch: string } => ({
  roomVersion: String(room.version?.roomVersion ?? ""),
  membershipVersion: String(room.version?.membershipVersion ?? ""),
  ownershipEpoch: String(room.ownershipEpoch ?? 0),
});

const draftForGame = (room: RoomSnapshot, gameId: string): RoomGameConfigDraftWire | undefined =>
  room.gameConfigDrafts?.find((draft) => draft.gameId === gameId);

const errorMessage = (body: unknown, status: number): { code: string; message: string } => {
  if (typeof body === "object" && body !== null) {
    const candidate = body as { code?: unknown; message?: unknown; error?: { code?: unknown; message?: unknown } };
    const nested = candidate.error;
    const code = typeof candidate.code === "string" ? candidate.code : typeof nested?.code === "string" ? nested.code : "http_error";
    const message = typeof candidate.message === "string" ? candidate.message : typeof nested?.message === "string" ? nested.message : `请求失败 (${status})`;
    return { code, message };
  }
  return { code: "http_error", message: `请求失败 (${status})` };
};

const replayTerminalStatus = {
  GAME_SESSION_STATUS_FINISHED: { finished: true, cancelled: false },
  GAME_SESSION_STATUS_CANCELLED: { finished: false, cancelled: true },
} as const;

type ReplayTerminalStatus = keyof typeof replayTerminalStatus;

const replayResponseError = (code: string): Error => new Error(code);

const isTimestampString = (value: string): boolean => value.length > 0 && !Number.isNaN(Date.parse(value));

/** Replay responses are immutable terminal snapshots, so terminal runtime facts must agree with the session summary. */
const validateReplayProjectionResponse = (response: GameReplayProjectionResponse): GameReplayProjectionResponse => {
  const terminalMeta = response.terminalMeta;
  if (terminalMeta === undefined) {
    throw replayResponseError("replay_terminal_meta_missing");
  }
  if (typeof terminalMeta.finished !== "boolean" || typeof terminalMeta.cancelled !== "boolean") {
    throw replayResponseError("replay_terminal_meta_invalid");
  }
  if (terminalMeta.finished === terminalMeta.cancelled) {
    throw replayResponseError("replay_terminal_meta_inconsistent");
  }
  if (typeof terminalMeta.endedAt !== "string" || !isTimestampString(terminalMeta.endedAt)) {
    throw replayResponseError("replay_terminal_ended_at_invalid");
  }
  if (terminalMeta.finished) {
    if (terminalMeta.cancelReason !== undefined && terminalMeta.cancelReason !== "") {
      throw replayResponseError("replay_terminal_cancel_reason_invalid");
    }
  } else if (typeof terminalMeta.cancelReason !== "string" || terminalMeta.cancelReason.length === 0) {
    throw replayResponseError("replay_terminal_cancel_reason_invalid");
  }
  const status = response.session?.status;
  if (status === undefined) {
    return response;
  }
  if (!(status in replayTerminalStatus)) {
    throw replayResponseError("replay_terminal_status_invalid");
  }
  const expected = replayTerminalStatus[status as ReplayTerminalStatus];
  if (terminalMeta.finished !== expected.finished || terminalMeta.cancelled !== expected.cancelled) {
    throw replayResponseError("replay_terminal_status_inconsistent");
  }
  return response;
};

async function call<T>(
  service: string,
  method: string,
  body: Record<string, unknown>,
  write = false,
  extraHeaders?: Record<string, string>,
  signal?: AbortSignal,
): Promise<T> {
  const headers: Record<string, string> = {
    Accept: "application/json",
    "Content-Type": "application/json",
    "Connect-Protocol-Version": "1",
    "X-Request-ID": requestID(),
    ...extraHeaders,
  };
  if (write) {
    const csrf = readCookie(userCSRFName);
    if (csrf) {
      headers["X-CSRF-Token"] = csrf;
    }
  }
  const response = await fetch(`${apiBase}/${service}/${method}`, {
    method: "POST",
    credentials: "include",
    headers,
    body: JSON.stringify(body),
    ...(signal === undefined ? {} : { signal }),
  });
  const text = await response.text();
  let payload: unknown = undefined;
  if (text.length > 0) {
    try {
      payload = JSON.parse(text) as unknown;
    } catch {
      payload = undefined;
    }
  }
  if (!response.ok) {
    const error = errorMessage(payload, response.status);
    throw new ApiError(response.status, error.code, error.message);
  }
  return (payload ?? {}) as T;
}

/** Restores proto3 scalar defaults omitted by Connect JSON before room state reaches layout code. */
const normalizeRoomResponse = (response: RoomResponse): RoomResponse => {
  const normalizeMember = (member: RoomMember): RoomMember => ({
    ...member,
    seatIndex: member.seatIndex ?? 0,
  });
  const normalizeRoom = (room: RoomSnapshot): RoomSnapshot => ({
    ...room,
    members: (room.members ?? []).map(normalizeMember),
  });
  return {
    ...response,
    ...(response.room === undefined ? {} : { room: normalizeRoom(response.room) }),
    ...(response.member === undefined ? {} : { member: normalizeMember(response.member) }),
    ...(response.participants === undefined
      ? {}
      : { participants: response.participants.map((participant) => ({ ...participant, seatIndex: participant.seatIndex ?? 0 })) }),
  };
};

/** Applies the RoomService wire normalization consistently to current and future room snapshot commands. */
const callRoom = async (method: string, body: Record<string, unknown>, write = false): Promise<RoomResponse> =>
  normalizeRoomResponse(await call<RoomResponse>("platform.room.v1.RoomService", method, body, write));

export const identityClient = {
  beginBootstrap(requestFlowId: string): Promise<{ challenge?: { challengeProof: string } }> {
    return call("platform.identity.v1.IdentityService", "BeginIdentityBootstrap", { requestFlowId });
  },
  bootstrap(challengeProof: string, operationId: string, requestFlowId: string): Promise<IdentityResponse> {
    return call("platform.identity.v1.IdentityService", "BootstrapIdentity", { challengeProof, operationId, deviceLabel: "Game Night 浏览器" }, true, { "X-Request-Flow-ID": requestFlowId });
  },
  completeOnboarding(username: string): Promise<IdentityResponse> {
    return call("platform.identity.v1.IdentityService", "CompleteOnboarding", { username, operationId: requestID() }, true);
  },
  current(): Promise<IdentityResponse> {
    return call("platform.identity.v1.IdentityService", "GetCurrentIdentity", {});
  },
};

export const roomClient = {
  getRoom(roomId?: string, roomCode?: string): Promise<RoomResponse> {
    return callRoom("GetRoom", { roomId: roomId ?? "", roomCode: roomCode ?? "" });
  },
  heartbeatRoom(roomId: string): Promise<HeartbeatRoomResponse> {
    return call("platform.room.v1.RoomService", "HeartbeatRoom", { roomId }, true);
  },
  listMyRooms(pageToken = "", pageSize = 20): Promise<MyRoomListResponse> {
    return call("platform.room.v1.RoomService", "ListMyRooms", { page: { pageToken, pageSize } });
  },
  listPublicRooms(pageToken = "", pageSize = 20): Promise<PublicRoomListResponse> {
    return call("platform.room.v1.RoomService", "ListPublicRooms", { filter: {}, page: { pageToken, pageSize } });
  },
  createRoom(visibility: "ROOM_VISIBILITY_PRIVATE" | "ROOM_VISIBILITY_PUBLIC" = "ROOM_VISIBILITY_PRIVATE"): Promise<RoomResponse> {
    return callRoom("CreateRoom", {
      visibility,
      participantCapacity: 8,
      participantAdmission: "ADMISSION_MODE_OPEN",
      spectatorAdmission: "ADMISSION_MODE_OPEN",
    }, true);
  },
  joinRoom(roomCode: string, intent: "JOIN_INTENT_PARTICIPANT" | "JOIN_INTENT_SPECTATOR" = "JOIN_INTENT_PARTICIPANT", version?: RoomSnapshot["version"]): Promise<RoomResponse> {
    return callRoom("JoinRoom", {
      roomCode,
      intent,
      expectedVersion: version ?? undefined,
    }, true);
  },
  joinPublicRoom(roomId: string, intent: "JOIN_INTENT_PARTICIPANT" | "JOIN_INTENT_SPECTATOR"): Promise<RoomResponse> {
    return callRoom("JoinRoom", { roomId, intent }, true);
  },
  setAdmission(room: RoomSnapshot, participantAdmission: string, spectatorAdmission: string): Promise<RoomResponse> {
    return callRoom("SetAdmission", {
      roomId: room.roomId,
      participantAdmission,
      spectatorAdmission,
      expectedVersion: room.version,
    }, true);
  },
  async selectRoomGame(room: RoomSnapshot, gameId: string): Promise<RoomResponse> {
    const operationId = requestID();
    const version = currentRuleVersion(room);
    return callRoom("SelectRoomGame", {
      roomId: room.roomId,
      gameId,
      expectedVersion: room.version,
      ownershipEpoch: version.ownershipEpoch,
      operationId,
      requestDigest: await selectRoomGameRequestDigest({ ...version, roomId: room.roomId, operationId, gameId }),
    }, true);
  },
  async updateGameConfig(
    room: RoomSnapshot,
    gameId: string,
    config: GameEnvelopeInput | GameEnvelopeWire | undefined,
    expectedRevision: string | number | bigint = "0",
  ): Promise<RoomResponse> {
    if (!config) {
      throw new Error("invalid game config");
    }
    const operationId = requestID();
    const version = currentRuleVersion(room);
    const normalizedConfig = configDigestInput(gameId, config);
    return callRoom("UpdateGameConfig", {
      roomId: room.roomId,
      gameId,
      config: serializeGameEnvelope(normalizedConfig),
      expectedRevision: String(expectedRevision),
      expectedVersion: room.version,
      ownershipEpoch: version.ownershipEpoch,
      operationId,
      requestDigest: await updateGameConfigRequestDigest({
        ...version,
        roomId: room.roomId,
        operationId,
        gameId,
        expectedRevision,
        config: normalizedConfig,
      }),
    }, true);
  },
  listGameRulePresets(gameId: string): Promise<GameRulePresetListResponse> {
    return call("platform.room.v1.RoomService", "ListGameRulePresets", { gameId });
  },
  async saveGameRulePreset(input: {
    presetId: string | undefined;
    gameId: string;
    name: string;
    config: GameEnvelopeInput | GameEnvelopeWire;
    mode: GameRulePresetWriteMode;
    expectedPresetRevision: string | number | bigint | undefined;
  }): Promise<GameRulePresetResponse> {
    const operationId = requestID();
    const expectedPresetRevision = input.expectedPresetRevision ?? "0";
    const presetId = input.presetId ?? "";
    const normalizedConfig = configDigestInput(input.gameId, input.config);
    return call("platform.room.v1.RoomService", "SaveGameRulePreset", {
      presetId,
      gameId: input.gameId,
      name: input.name,
      config: serializeGameEnvelope(normalizedConfig),
      mode: input.mode,
      expectedPresetRevision: String(expectedPresetRevision),
      operationId,
      requestDigest: await saveGameRulePresetRequestDigest({
        presetId,
        gameId: input.gameId,
        name: input.name,
        mode: input.mode,
        expectedPresetRevision,
        operationId,
        config: normalizedConfig,
      }),
    }, true);
  },
  async deleteGameRulePreset(presetId: string, expectedPresetRevision: string | number | bigint): Promise<DeleteGameRulePresetResponse> {
    const operationId = requestID();
    return call("platform.room.v1.RoomService", "DeleteGameRulePreset", {
      presetId,
      expectedPresetRevision: String(expectedPresetRevision),
      operationId,
      requestDigest: await deleteGameRulePresetRequestDigest({ presetId, expectedPresetRevision, operationId }),
    }, true);
  },
  async beginGameStart(room: RoomSnapshot, gameId: string, configRevision: string | number | bigint = "0"): Promise<RoomResponse> {
    const operationId = requestID();
    const version = currentRuleVersion(room);
    return callRoom("BeginGameStart", {
      roomId: room.roomId,
      gameId,
      configRevision: String(configRevision),
      expectedVersion: room.version,
      ownershipEpoch: version.ownershipEpoch,
      operationId,
      requestDigest: await beginGameStartRequestDigest({
        ...version,
        roomId: room.roomId,
        operationId,
        gameId,
        configRevision,
      }),
    }, true);
  },
  async cancelGameStart(room: RoomSnapshot, pendingStart: PendingGameStartWire): Promise<RoomResponse> {
    const operationId = requestID();
    const version = currentRuleVersion(room);
    const pendingStartId = pendingStart.pendingStartId;
    const cancelToken = pendingStart.cancelToken;
    if (!pendingStartId || !cancelToken) {
      throw new Error("invalid pending start");
    }
    return callRoom("CancelGameStart", {
      roomId: room.roomId,
      pendingStartId,
      cancelToken,
      expectedVersion: room.version,
      ownershipEpoch: version.ownershipEpoch,
      operationId,
      requestDigest: await cancelGameStartRequestDigest({
        ...version,
        roomId: room.roomId,
        operationId,
        pendingStartId,
        cancelToken,
      }),
    }, true);
  },
  async startGame(
    room: RoomSnapshot,
    actorUserId: string,
    gameId = "liars-dice",
    configRevision?: string | number | bigint,
  ): Promise<RoomResponse> {
    const operationId = requestID();
    const pendingStart = room.pendingStart?.gameId === gameId ? room.pendingStart : undefined;
    const draft = pendingStart ? draftForGame(room, gameId) : undefined;
    const startConfigRevision = configRevision ?? (pendingStart && draft?.config ? pendingStart.configRevision ?? "0" : "0");
    const configInput = draft?.config
      ? configDigestInput(gameId, draft.config)
      : { messageType: "session.config", schemaVersion: 1, payload: new Uint8Array() };
    const config = {
      gameId,
      ...(draft?.config?.version ? { version: draft.config.version } : {}),
      schemaVersion: configInput.schemaVersion,
      messageType: configInput.messageType,
      payload: base64(configInput.payload),
    };
    const pendingFields = pendingStart && draft?.config
      ? {
          pendingStartId: pendingStart.pendingStartId,
          cancelToken: pendingStart.cancelToken,
          ownershipEpoch: pendingStart.ownershipEpoch,
        }
      : {};
    return callRoom("StartGame", {
      roomId: room.roomId,
      gameId,
      config,
      configRevision: String(startConfigRevision),
      expectedVersion: room.version,
      operationId,
      ...pendingFields,
      requestDigest: await startRequestDigest({
        actorUserId,
        roomId: room.roomId,
        operationId,
        gameId,
        roomVersion: String(room.version?.roomVersion ?? ""),
        membershipVersion: String(room.version?.membershipVersion ?? ""),
        configRevision: startConfigRevision,
        config: configInput,
      }),
    }, true);
  },
  async finishGame(room: RoomSnapshot, actorUserId: string, sessionId: string, expectedStateVersion: number, command: GameEnvelopeInput): Promise<RoomResponse> {
    const operationId = requestID();
    const sourceEventId = requestID();
    return callRoom("FinishGame", {
      roomId: room.roomId,
      sessionId,
      expectedVersion: room.version,
      operationId,
      sourceEventId,
      expectedStateVersion: String(expectedStateVersion),
      command: { ...command, payload: base64(command.payload) },
      requestDigest: await finishRequestDigest({
        actorUserId,
        sessionId,
        operationId,
        sourceEventId,
        expectedStateVersion,
        command,
      }),
    }, true);
  },
  approveMember(room: RoomSnapshot, userId: string): Promise<RoomResponse> {
    return callRoom("ApproveMember", { roomId: room.roomId, userId, expectedVersion: room.version }, true);
  },
  /** Removes one non-host member under the room's exact membership version. */
  removeMember(room: RoomSnapshot, userId: string): Promise<RoomResponse> {
    return callRoom("RemoveMember", { roomId: room.roomId, userId, expectedVersion: room.version }, true);
  },
  /** Permanently closes an idle room; active sessions require the separate cancellation boundary. */
  closeRoom(room: RoomSnapshot): Promise<RoomResponse> {
    return callRoom("CloseRoom", { roomId: room.roomId, expectedVersion: room.version }, true);
  },
};

export const gameClient = {
  getProjection(roomId: string, sessionId: string, viewerKind = "VIEWER_KIND_PLAYER", signal?: AbortSignal): Promise<GameProjectionResponse> {
    return call("platform.game.v1.GameService", "GetProjection", { roomId, sessionId, viewerKind }, false, undefined, signal);
  },
  /** Loads one immutable, authorized replay projection without opening a realtime subscription. */
  async getReplayProjection(roomId: string, sessionId: string, throughStateVersion = 0, signal?: AbortSignal): Promise<GameReplayProjectionResponse> {
    const response = await call<GameReplayProjectionResponse>("platform.game.v1.GameService", "GetReplayProjection", {
      roomId,
      sessionId,
      viewerKind: "VIEWER_KIND_REPLAY",
      throughStateVersion: String(throughStateVersion),
    }, false, undefined, signal);
    return validateReplayProjectionResponse(response);
  },
  /** Reads the host-controlled resource policy separately from the viewer-safe replay payload. */
  getReplayAccess(roomId: string, sessionId: string, signal?: AbortSignal): Promise<ReplayAccessResponse> {
    return call("platform.game.v1.GameService", "GetReplayAccess", { roomId, sessionId }, false, undefined, signal);
  },
  /** Applies a replay policy through the server's policy-version compare-and-swap boundary. */
  setReplayAccess(roomId: string, sessionId: string, policy: ReplayAccessPolicy, expectedPolicyVersion: string): Promise<ReplayAccessResponse> {
    return call("platform.game.v1.GameService", "SetReplayAccess", {
      roomId,
      sessionId,
      policy,
      expectedPolicyVersion,
    }, true);
  },
  async action(
    roomId: string,
    actorUserId: string,
    sessionId: string,
    expectedStateVersion: number,
    actionId: string,
    command: GameEnvelopeInput,
    signal?: AbortSignal,
  ): Promise<GameActionResponse> {
    return call("platform.game.v1.GameService", "GameAction", {
      roomId,
      sessionId,
      actionId,
      expectedStateVersion: String(expectedStateVersion),
      command: { ...command, payload: base64(command.payload) },
      requestDigest: await actionRequestDigest({ sessionId, actorUserId, actionId, expectedStateVersion, command }),
    }, true, undefined, signal);
  },
  /** Exchanges the device cookie for one short-lived ticket/grant pair bound to the current Origin. */
  async openSubscription(
    roomId: string,
    sessionId: string,
    viewerKind: string,
    lastStateVersion: number,
    signal?: AbortSignal,
  ): Promise<GameSubscriptionResponse> {
    const response = await call<GameProjectionResponse & { ticket?: unknown; grant?: unknown; expiresAt?: string }>(
      "platform.game.v1.GameService",
      "OpenSubscription",
      {
        roomId,
        sessionId,
        viewerKind,
        lastStateVersion: String(lastStateVersion),
        lastEventOrdinal: 0,
      },
      true,
      undefined,
      signal,
    );
    if (typeof response.ticket !== "string" || typeof response.grant !== "string") {
      throw invalidSubscriptionCredentials();
    }
    try {
      return {
        ticket: base64Bytes(response.ticket),
        grant: base64Bytes(response.grant),
        ...(response.projection === undefined ? {} : { projection: response.projection }),
        ...(response.expiresAt === undefined ? {} : { expiresAt: response.expiresAt }),
      };
    } catch (error) {
      if (error instanceof SubscriptionFailure) throw error;
      throw invalidSubscriptionCredentials(error);
    }
  },
};
