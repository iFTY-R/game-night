/**
 * Small same-origin Connect JSON client used by the browser shell.
 * Keeping the transport here makes room and identity views independent from
 * generated protobuf runtime details while preserving the server contract.
 */

import { SubscriptionFailure } from "@game-night/game-client";

import { actionRequestDigest, finishRequestDigest, startRequestDigest } from "./operation-digest";

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
  role: string;
  requestedRole: string;
  seatIndex: number;
}

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
}

export interface RoomResponse {
  room?: RoomSnapshot;
  member?: RoomMember;
  created?: boolean;
  changed?: boolean;
  sessionId?: string;
  gameId?: string;
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

export interface GameReplayProjectionResponse extends GameProjectionResponse {
  session?: GameSessionSummaryWire;
  complete?: boolean;
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
    return call("platform.room.v1.RoomService", "GetRoom", { roomId: roomId ?? "", roomCode: roomCode ?? "" });
  },
  createRoom(): Promise<RoomResponse> {
    return call("platform.room.v1.RoomService", "CreateRoom", {
      visibility: "ROOM_VISIBILITY_PRIVATE",
      participantCapacity: 8,
      participantAdmission: "ADMISSION_MODE_OPEN",
      spectatorAdmission: "ADMISSION_MODE_OPEN",
    }, true);
  },
  joinRoom(roomCode: string, intent: "JOIN_INTENT_PARTICIPANT" | "JOIN_INTENT_SPECTATOR" = "JOIN_INTENT_PARTICIPANT", version?: RoomSnapshot["version"]): Promise<RoomResponse> {
    return call("platform.room.v1.RoomService", "JoinRoom", {
      roomCode,
      intent,
      expectedVersion: version ?? undefined,
    }, true);
  },
  setAdmission(room: RoomSnapshot, participantAdmission: string, spectatorAdmission: string): Promise<RoomResponse> {
    return call("platform.room.v1.RoomService", "SetAdmission", {
      roomId: room.roomId,
      participantAdmission,
      spectatorAdmission,
      expectedVersion: room.version,
    }, true);
  },
  async startGame(room: RoomSnapshot, actorUserId: string, gameId = "liars-dice"): Promise<RoomResponse> {
    const operationId = requestID();
    const configInput = { messageType: "session.config", schemaVersion: 1, payload: new Uint8Array() };
    const config = { gameId, schemaVersion: configInput.schemaVersion, messageType: configInput.messageType, payload: "" };
    return call("platform.room.v1.RoomService", "StartGame", {
      roomId: room.roomId,
      gameId,
      config,
      expectedVersion: room.version,
      operationId,
      requestDigest: await startRequestDigest({
        actorUserId,
        roomId: room.roomId,
        operationId,
        gameId,
        roomVersion: String(room.version?.roomVersion ?? ""),
        membershipVersion: String(room.version?.membershipVersion ?? ""),
        config: configInput,
      }),
    }, true);
  },
  async finishGame(room: RoomSnapshot, actorUserId: string, sessionId: string, expectedStateVersion: number, command: GameEnvelopeInput): Promise<RoomResponse> {
    const operationId = requestID();
    const sourceEventId = requestID();
    return call("platform.room.v1.RoomService", "FinishGame", {
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
    return call("platform.room.v1.RoomService", "ApproveMember", { roomId: room.roomId, userId, expectedVersion: room.version }, true);
  },
  /** Removes one non-host member under the room's exact membership version. */
  removeMember(room: RoomSnapshot, userId: string): Promise<RoomResponse> {
    return call("platform.room.v1.RoomService", "RemoveMember", { roomId: room.roomId, userId, expectedVersion: room.version }, true);
  },
  /** Permanently closes an idle room; active sessions require the separate cancellation boundary. */
  closeRoom(room: RoomSnapshot): Promise<RoomResponse> {
    return call("platform.room.v1.RoomService", "CloseRoom", { roomId: room.roomId, expectedVersion: room.version }, true);
  },
};

export const gameClient = {
  getProjection(roomId: string, sessionId: string, viewerKind = "VIEWER_KIND_PLAYER", signal?: AbortSignal): Promise<GameProjectionResponse> {
    return call("platform.game.v1.GameService", "GetProjection", { roomId, sessionId, viewerKind }, false, undefined, signal);
  },
  /** Loads one immutable, authorized replay projection without opening a realtime subscription. */
  getReplayProjection(roomId: string, sessionId: string, throughStateVersion = 0, signal?: AbortSignal): Promise<GameReplayProjectionResponse> {
    return call("platform.game.v1.GameService", "GetReplayProjection", {
      roomId,
      sessionId,
      viewerKind: "VIEWER_KIND_REPLAY",
      throughStateVersion: String(throughStateVersion),
    }, false, undefined, signal);
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

export const isDevelopmentFallbackAllowed = (): boolean => import.meta.env.DEV;
