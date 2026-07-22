import { create, fromBinary, toBinary } from "@bufbuild/protobuf";

import {
  ClientFrameSchema,
  ServerFrameSchema,
  SubscriptionHelloSchema,
  ViewerKind,
  type GameDelta as WireGameDelta,
  type GameEnvelope as WireGameEnvelope,
  type GameProjection as WireGameProjection,
  type ServerFrame,
} from "../../../../contracts/gen/ts/platform/game/v1/game_pb";

import {
  SubscriptionFailure,
  type GameDelta,
  type GameEnvelope,
  type GameProjection,
  type GameUpdate,
  type ReconnectAdapter,
  type SubscriptionCursor,
  type ViewerRole,
} from "@game-night/game-client";

/*
 * The Web shell owns the public transport contract; individual game clients
 * receive only the shared viewer-safe update types below.
 */
// Browser WebSocket readyState values are stable platform constants but are not exposed by injected test sockets.
const SOCKET_CONNECTING = 0;
const SOCKET_OPEN = 1;
// Realtime deployment config caps server frames at 64 KiB; larger frames violate the public transport contract.
const MAX_SERVER_FRAME_BYTES = 64 << 10;
// A bounded browser queue prevents a background tab from accumulating updates without limit.
const MAX_BUFFERED_FRAMES = 64;
// Server-provided drain delays are bounded so a malformed frame cannot suspend recovery indefinitely.
const MAX_DRAIN_DELAY_MS = 30_000;

export interface SubscriptionAuthorization {
  readonly ticket: Uint8Array;
  readonly grant: Uint8Array;
  readonly projection: GameProjection;
}

export type OpenSubscription = (
  cursor: SubscriptionCursor | null,
  signal: AbortSignal,
) => Promise<SubscriptionAuthorization>;

/** Narrow browser socket surface keeps transport tests independent from a network listener. */
export interface WebSocketPort {
  binaryType: BinaryType;
  readonly readyState: number;
  send(data: ArrayBuffer | ArrayBufferView): void;
  close(code?: number, reason?: string): void;
  addEventListener(type: "open" | "error" | "close" | "message", listener: EventListener): void;
  removeEventListener(type: "open" | "error" | "close" | "message", listener: EventListener): void;
}

export type WebSocketFactory = (url: string) => WebSocketPort;

export interface BrowserRealtimeAdapterOptions {
  readonly url: string | (() => string);
  readonly openSubscription: OpenSubscription;
  readonly createSocket?: WebSocketFactory;
  readonly now?: () => number;
}

/** Exchanges a fresh one-time grant on every attempt and exposes only viewer-safe game updates. */
export class BrowserRealtimeAdapter implements ReconnectAdapter {
  readonly #url: string | (() => string);
  readonly #openSubscription: OpenSubscription;
  readonly #createSocket: WebSocketFactory;
  readonly #now: () => number;

  public constructor(options: BrowserRealtimeAdapterOptions) {
    this.#url = options.url;
    this.#openSubscription = options.openSubscription;
    this.#createSocket = options.createSocket ?? ((url) => new WebSocket(url));
    this.#now = options.now ?? (() => Date.now());
  }

  public async connect(cursor: SubscriptionCursor | null, signal: AbortSignal): Promise<AsyncIterable<GameUpdate>> {
    if (signal.aborted) {
      throw abortFailure();
    }
    const authorization = await this.#openSubscription(cursor, signal);
    if (signal.aborted) {
      throw abortFailure();
    }
    if (authorization.ticket.length === 0 || authorization.grant.length === 0) {
      throw new SubscriptionFailure("subscription_grant_invalid", "Subscription authorization is incomplete", false);
    }
    const socket = this.#createSocket(resolveWebSocketURL(this.#url));
    socket.binaryType = "arraybuffer";
    const queue = new FrameQueue();
    let opened = false;
    let cleaned = false;
    let resolveOpen: (() => void) | undefined;
    let rejectOpen: ((reason: unknown) => void) | undefined;

    const openPromise = new Promise<void>((resolve, reject) => {
      resolveOpen = resolve;
      rejectOpen = reject;
    });
    const onOpen: EventListener = () => {
      try {
        const hello = create(ClientFrameSchema, {
          body: {
            case: "hello",
            value: create(SubscriptionHelloSchema, { ticket: authorization.ticket, grant: authorization.grant }),
          },
        });
        socket.send(toBinary(ClientFrameSchema, hello));
        opened = true;
        resolveOpen?.();
      } catch (error) {
        const failure = connectionFailure("subscription_hello_failed", error);
        rejectOpen?.(failure);
        queue.fail(failure);
        socket.close(1002, "subscription hello failed");
      }
    };
    const onMessage: EventListener = (event) => {
      try {
        if (!queue.push(decodeServerFrame((event as MessageEvent<unknown>).data))) {
          socket.close(1008, "client frame queue full");
        }
      } catch (error) {
        const failure = error instanceof SubscriptionFailure
          ? error
          : new SubscriptionFailure("invalid_server_frame", "Realtime server frame is invalid", true, "reconnecting", null, { cause: error });
        queue.fail(failure);
        socket.close(1002, "invalid server frame");
      }
    };
    const onError: EventListener = () => {
      const failure = new SubscriptionFailure("connection_lost", "Realtime WebSocket failed");
      if (!opened) rejectOpen?.(failure);
      queue.fail(failure);
    };
    const onClose: EventListener = (event) => {
      const failure = signal.aborted ? null : closeFailure((event as CloseEvent).code);
      if (!opened && failure !== null) rejectOpen?.(failure);
      if (failure === null) queue.finish();
      else queue.fail(failure);
    };
    const onAbort = (): void => {
      queue.finish();
      if (socket.readyState === SOCKET_CONNECTING || socket.readyState === SOCKET_OPEN) {
        socket.close(1000, "subscription cancelled");
      }
      if (!opened) rejectOpen?.(abortFailure());
    };
    const cleanup = (): void => {
      if (cleaned) return;
      cleaned = true;
      socket.removeEventListener("open", onOpen);
      socket.removeEventListener("message", onMessage);
      socket.removeEventListener("error", onError);
      socket.removeEventListener("close", onClose);
      signal.removeEventListener("abort", onAbort);
    };

    socket.addEventListener("open", onOpen);
    socket.addEventListener("message", onMessage);
    socket.addEventListener("error", onError);
    socket.addEventListener("close", onClose);
    signal.addEventListener("abort", onAbort, { once: true });
    // Cancellation may win after socket creation but before listener registration.
    if (signal.aborted) onAbort();

    try {
      await openPromise;
    } catch (error) {
      if (socket.readyState === SOCKET_CONNECTING || socket.readyState === SOCKET_OPEN) {
        socket.close(1000, "subscription connection failed");
      }
      cleanup();
      throw error;
    }

    const initialProjection = authorization.projection;
    const now = this.#now;
    return {
      async *[Symbol.asyncIterator](): AsyncIterator<GameUpdate> {
        try {
          yield initialProjection;
          while (!signal.aborted) {
            const frame = await queue.shift();
            if (frame === null) return;
            const update = updateFromServerFrame(frame, now());
            if (update !== null) yield update;
          }
        } finally {
          cleanup();
          if (socket.readyState === SOCKET_CONNECTING || socket.readyState === SOCKET_OPEN) {
            socket.close(1000, "subscription iterator closed");
          }
        }
      },
    };
  }
}

class FrameQueue {
  readonly #frames: ServerFrame[] = [];
  readonly #waiters: Array<{ resolve: (value: ServerFrame | null) => void; reject: (reason: unknown) => void }> = [];
  #terminal: { readonly error: SubscriptionFailure | null } | null = null;

  public push(frame: ServerFrame): boolean {
    if (this.#terminal !== null) return false;
    const waiter = this.#waiters.shift();
    if (waiter !== undefined) {
      waiter.resolve(frame);
      return true;
    }
    if (this.#frames.length >= MAX_BUFFERED_FRAMES) {
      this.#frames.length = 0;
      this.fail(new SubscriptionFailure("client_too_slow", "Realtime client frame queue is full"));
      return false;
    }
    this.#frames.push(frame);
    return true;
  }

  public finish(): void {
    this.#end(null);
  }

  public fail(error: SubscriptionFailure): void {
    this.#end(error);
  }

  public shift(): Promise<ServerFrame | null> {
    const frame = this.#frames.shift();
    if (frame !== undefined) return Promise.resolve(frame);
    if (this.#terminal?.error !== undefined) {
      return this.#terminal.error === null ? Promise.resolve(null) : Promise.reject(this.#terminal.error);
    }
    return new Promise<ServerFrame | null>((resolve, reject) => this.#waiters.push({ resolve, reject }));
  }

  #end(error: SubscriptionFailure | null): void {
    if (this.#terminal !== null) return;
    this.#terminal = { error };
    for (const waiter of this.#waiters.splice(0)) {
      if (error === null) waiter.resolve(null);
      else waiter.reject(error);
    }
  }
}

const resolveWebSocketURL = (configured: string | (() => string)): string => {
  const value = typeof configured === "function" ? configured() : configured;
  let url: URL;
  try {
    url = new URL(value);
  } catch (error) {
    throw new SubscriptionFailure("realtime_url_invalid", "Realtime WebSocket URL is invalid", false, "reconnecting", null, { cause: error });
  }
  if (url.protocol !== "ws:" && url.protocol !== "wss:") {
    throw new SubscriptionFailure("realtime_url_invalid", "Realtime WebSocket URL must use ws or wss", false);
  }
  return url.toString();
};

const decodeServerFrame = (data: unknown): ServerFrame => {
  const bytes = messageBytes(data);
  if (bytes.length === 0 || bytes.length > MAX_SERVER_FRAME_BYTES) {
    throw new SubscriptionFailure("invalid_server_frame", "Realtime server frame size is invalid");
  }
  const frame = fromBinary(ServerFrameSchema, bytes, { readUnknownFields: false });
  const canonical = toBinary(ServerFrameSchema, frame);
  if (frame.body.case === undefined || !equalBytes(bytes, canonical)) {
    throw new SubscriptionFailure("invalid_server_frame", "Realtime server frame is not canonical");
  }
  return frame;
};

const messageBytes = (data: unknown): Uint8Array => {
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (ArrayBuffer.isView(data)) return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  throw new SubscriptionFailure("invalid_server_frame", "Realtime server sent a non-binary frame");
};

const updateFromServerFrame = (frame: ServerFrame, nowMs: number): GameUpdate | null => {
  switch (frame.body.case) {
    case "projection":
      return projectionFromWire(frame.body.value);
    case "delta":
      return deltaFromWire(frame.body.value);
    case "draining":
      throw new SubscriptionFailure(
        frame.body.value.reason || "service_draining",
        "Realtime service is draining",
        true,
        "draining",
        drainDelay(frame.body.value.reconnectAfter?.seconds, frame.body.value.reconnectAfter?.nanos, nowMs),
      );
    case "error": {
      const code = frame.body.value.code || "subscription_rejected";
      throw new SubscriptionFailure(code, "Realtime subscription was rejected", code !== "subscription_unauthorized");
    }
    case "pong":
    case "receipt":
      return null;
    default:
      throw new SubscriptionFailure("invalid_server_frame", "Realtime server frame body is unsupported");
  }
};

const projectionFromWire = (projection: WireGameProjection): GameProjection => ({
  kind: "projection",
  sessionId: projection.sessionId,
  stateVersion: safeVersion(projection.stateVersion, "projection_state_version"),
  viewerRole: viewerRole(projection.viewerKind),
  view: envelopeFromWire(projection.view),
  allowedActions: [...projection.allowedActions],
});

const deltaFromWire = (delta: WireGameDelta): GameDelta => ({
  kind: "delta",
  sessionId: delta.sessionId,
  fromStateVersion: safeVersion(delta.fromStateVersion, "delta_from_state_version"),
  toStateVersion: safeVersion(delta.toStateVersion, "delta_to_state_version"),
  viewerRole: viewerRole(delta.viewerKind),
  messages: delta.messages.map((message) => envelopeFromWire(message)),
});

const envelopeFromWire = (envelope: WireGameEnvelope | undefined): GameEnvelope => {
  if (envelope?.version === undefined) {
    throw new SubscriptionFailure("invalid_server_frame", "Realtime game envelope is incomplete");
  }
  return {
    gameId: envelope.gameId,
    version: { ...envelope.version },
    schemaVersion: envelope.schemaVersion,
    messageType: envelope.messageType,
    payload: new Uint8Array(envelope.payload),
  };
};

const viewerRole = (kind: ViewerKind): ViewerRole => {
  if (kind === ViewerKind.PLAYER) return "player";
  if (kind === ViewerKind.SPECTATOR) return "spectator";
  if (kind === ViewerKind.REPLAY) return "replay";
  throw new SubscriptionFailure("invalid_server_frame", "Realtime viewer role is invalid");
};

const safeVersion = (value: bigint, field: string): number => {
  if (value <= 0n || value > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new SubscriptionFailure("state_version_unsupported", `${field} is outside the browser safe integer range`, false);
  }
  return Number(value);
};

const drainDelay = (seconds: bigint | undefined, nanos: number | undefined, nowMs: number): number | null => {
  if (seconds === undefined || nanos === undefined) return null;
  const targetMs = seconds * 1_000n + BigInt(Math.max(0, nanos)) / 1_000_000n;
  const delay = targetMs - BigInt(Math.max(0, Math.trunc(nowMs)));
  if (delay <= 0n) return 0;
  return Number(delay > BigInt(MAX_DRAIN_DELAY_MS) ? BigInt(MAX_DRAIN_DELAY_MS) : delay);
};

const closeFailure = (code: number): SubscriptionFailure => {
  if (code === 1012) {
    return new SubscriptionFailure("service_restart", "Realtime service is restarting", true, "draining", 1_000);
  }
  if (code === 1008) {
    return new SubscriptionFailure("subscription_rejected", "Realtime subscription was rejected");
  }
  return new SubscriptionFailure("connection_lost", "Realtime WebSocket closed unexpectedly");
};

const connectionFailure = (code: string, cause: unknown): SubscriptionFailure =>
  new SubscriptionFailure(code, "Realtime WebSocket connection failed", true, "reconnecting", null, { cause });

const abortFailure = (): SubscriptionFailure =>
  new SubscriptionFailure("subscription_cancelled", "Realtime subscription was cancelled", false);

const equalBytes = (left: Uint8Array, right: Uint8Array): boolean =>
  left.length === right.length && left.every((value, index) => value === right[index]);
