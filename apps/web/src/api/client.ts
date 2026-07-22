/**
 * Small same-origin Connect JSON client used by the browser shell.
 * Keeping the transport here makes room and identity views independent from
 * generated protobuf runtime details while preserving the server contract.
 */

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

export interface GameProjectionResponse {
  projection?: {
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
  };
}

export interface GameActionResponse extends GameProjectionResponse {
  sessionId?: string;
  stateVersion?: string;
  resultCode?: string;
  replayed?: boolean;
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

const digest = async (...parts: string[]): Promise<string> => {
  const input = new TextEncoder().encode(parts.join("\u0000"));
  const hashed = await crypto.subtle.digest("SHA-256", input);
  return base64(new Uint8Array(hashed));
};

/** Includes the complete opaque command identity in mutation idempotency bindings. */
const envelopeDigestParts = (command: GameEnvelopeInput): string[] => [
  command.gameId,
  command.version.engine,
  command.version.protocol,
  command.version.client,
  String(command.schemaVersion),
  command.messageType,
  base64(command.payload),
];

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

async function call<T>(service: string, method: string, body: Record<string, unknown>, write = false, extraHeaders?: Record<string, string>): Promise<T> {
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
  async startGame(room: RoomSnapshot, gameId = "liars-dice"): Promise<RoomResponse> {
    const operationId = requestID();
    const config = { gameId, schemaVersion: 1, messageType: "session.config", payload: "" };
    return call("platform.room.v1.RoomService", "StartGame", {
      roomId: room.roomId,
      gameId,
      config,
      expectedVersion: room.version,
      operationId,
      requestDigest: await digest(
        room.roomId,
        gameId,
        String(room.version?.roomVersion ?? ""),
        String(room.version?.membershipVersion ?? ""),
        operationId,
        config.messageType,
        config.payload,
      ),
    }, true);
  },
  async finishGame(room: RoomSnapshot, sessionId: string, expectedStateVersion: number, command: GameEnvelopeInput): Promise<RoomResponse> {
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
      requestDigest: await digest(
        room.roomId,
        sessionId,
        String(room.version?.roomVersion ?? ""),
        String(room.version?.membershipVersion ?? ""),
        operationId,
        sourceEventId,
        String(expectedStateVersion),
        ...envelopeDigestParts(command),
      ),
    }, true);
  },
  approveMember(room: RoomSnapshot, userId: string): Promise<RoomResponse> {
    return call("platform.room.v1.RoomService", "ApproveMember", { roomId: room.roomId, userId, expectedVersion: room.version }, true);
  },
};

export const gameClient = {
  getProjection(roomId: string, sessionId: string, viewerKind = "VIEWER_KIND_PLAYER"): Promise<GameProjectionResponse> {
    return call("platform.game.v1.GameService", "GetProjection", { roomId, sessionId, viewerKind });
  },
  async action(roomId: string, sessionId: string, expectedStateVersion: number, actionId: string, command: GameEnvelopeInput): Promise<GameActionResponse> {
    return call("platform.game.v1.GameService", "GameAction", {
      roomId,
      sessionId,
      actionId,
      expectedStateVersion: String(expectedStateVersion),
      command: { ...command, payload: base64(command.payload) },
      requestDigest: await digest(roomId, sessionId, actionId, String(expectedStateVersion), ...envelopeDigestParts(command)),
    }, true);
  },
};

export const isDevelopmentFallbackAllowed = (): boolean => import.meta.env.DEV;
