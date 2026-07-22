import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it, vi } from "vitest";

import {
  ClientFrameSchema,
  GameDeltaSchema as WireGameDeltaSchema,
  GameEnvelopeSchema as WireGameEnvelopeSchema,
  ServerFrameSchema,
  SubscriptionDrainingSchema,
  VersionTupleSchema,
  ViewerKind,
} from "../../../contracts/gen/ts/platform/game/v1/game_pb";
import {
  type GameEnvelope,
  type GameProjection,
} from "@game-night/game-client";
import {
  BrowserRealtimeAdapter,
  type SubscriptionAuthorization,
  type WebSocketPort,
} from "../src/api/browser-realtime";

const envelope = (payload: number): GameEnvelope => ({
  gameId: "liars-dice",
  version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
  schemaVersion: 1,
  messageType: "viewer.state",
  payload: new Uint8Array([payload]),
});

const projection = (): GameProjection => ({
  kind: "projection",
  sessionId: "session-1",
  stateVersion: 1,
  viewerRole: "player",
  view: envelope(1),
  allowedActions: ["round.bid"],
});

const authorization = (): SubscriptionAuthorization => ({
  ticket: new Uint8Array([1, 2]),
  grant: new Uint8Array([3, 4]),
  projection: projection(),
});

class FakeWebSocket extends EventTarget implements WebSocketPort {
  public binaryType: BinaryType = "blob";
  public readyState = 0;
  public readonly sent: Uint8Array[] = [];
  public readonly closes: Array<{ code?: number; reason?: string }> = [];

  public send(data: ArrayBuffer | ArrayBufferView): void {
    const bytes = data instanceof ArrayBuffer
      ? new Uint8Array(data)
      : new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    this.sent.push(new Uint8Array(bytes));
  }

  public close(code?: number, reason?: string): void {
    this.readyState = 3;
    this.closes.push(code === undefined ? {} : reason === undefined ? { code } : { code, reason });
  }

  public open(): void {
    this.readyState = 1;
    this.dispatchEvent(new Event("open"));
  }

  public message(bytes: Uint8Array): void {
    const event = new Event("message");
    Object.defineProperty(event, "data", { value: new Uint8Array(bytes).buffer });
    this.dispatchEvent(event);
  }

  public serverClose(code: number): void {
    this.readyState = 3;
    const event = new Event("close");
    Object.defineProperty(event, "code", { value: code });
    this.dispatchEvent(event);
  }
}

const connect = async (socket: FakeWebSocket, controller: AbortController) => {
  const adapter = new BrowserRealtimeAdapter({
    url: "wss://game.example/realtime/game",
    openSubscription: async () => authorization(),
    createSocket: () => socket,
  });
  const connecting = adapter.connect(null, controller.signal);
  await vi.waitFor(() => expect(socket.binaryType).toBe("arraybuffer"));
  socket.open();
  return connecting;
};

describe("BrowserRealtimeAdapter", () => {
  it("sends a binary hello and yields the authorization projection followed by deltas", async () => {
    const controller = new AbortController();
    const socket = new FakeWebSocket();
    const updates = await connect(socket, controller);
    const iterator = updates[Symbol.asyncIterator]();

    await expect(iterator.next()).resolves.toMatchObject({ done: false, value: { kind: "projection", stateVersion: 1 } });
    const hello = fromBinary(ClientFrameSchema, socket.sent[0] ?? new Uint8Array());
    expect(hello.body.case).toBe("hello");
    if (hello.body.case === "hello") {
      expect([...hello.body.value.ticket]).toEqual([1, 2]);
      expect([...hello.body.value.grant]).toEqual([3, 4]);
    }

    const wireEnvelope = create(WireGameEnvelopeSchema, {
      gameId: "liars-dice",
      version: create(VersionTupleSchema, { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" }),
      schemaVersion: 1,
      messageType: "viewer.delta",
      payload: new Uint8Array([2]),
    });
    const frame = create(ServerFrameSchema, {
      body: {
        case: "delta",
        value: create(WireGameDeltaSchema, {
          sessionId: "session-1",
          fromStateVersion: 1n,
          toStateVersion: 2n,
          viewerKind: ViewerKind.PLAYER,
          messages: [wireEnvelope],
        }),
      },
    });
    const next = iterator.next();
    socket.message(toBinary(ServerFrameSchema, frame));

    await expect(next).resolves.toMatchObject({
      done: false,
      value: { kind: "delta", sessionId: "session-1", fromStateVersion: 1, toStateVersion: 2 },
    });
    controller.abort();
    await expect(iterator.next()).resolves.toEqual({ done: true, value: undefined });
    expect(socket.closes).toContainEqual({ code: 1000, reason: "subscription cancelled" });
  });

  it("surfaces draining as a retryable transport phase", async () => {
    const controller = new AbortController();
    const socket = new FakeWebSocket();
    const updates = await connect(socket, controller);
    const iterator = updates[Symbol.asyncIterator]();
    await iterator.next();

    const draining = create(ServerFrameSchema, {
      body: {
        case: "draining",
        value: create(SubscriptionDrainingSchema, { reason: "service_restart" }),
      },
    });
    const next = iterator.next();
    socket.message(toBinary(ServerFrameSchema, draining));

    await expect(next).rejects.toMatchObject({
      code: "service_restart",
      retryable: true,
      phase: "draining",
    });
    expect(socket.closes).toContainEqual({ code: 1000, reason: "subscription iterator closed" });
  });

  it("turns a policy close into a fresh-ticket retry", async () => {
    const controller = new AbortController();
    const socket = new FakeWebSocket();
    const updates = await connect(socket, controller);
    const iterator = updates[Symbol.asyncIterator]();
    await iterator.next();

    const next = iterator.next();
    socket.serverClose(1008);

    await expect(next).rejects.toMatchObject({
      code: "subscription_rejected",
      retryable: true,
      phase: "reconnecting",
    });
  });

  it("does not create a socket when cancellation wins the ticket exchange", async () => {
    const controller = new AbortController();
    let resolveAuthorization: ((value: SubscriptionAuthorization) => void) | undefined;
    const createSocket = vi.fn(() => new FakeWebSocket());
    const adapter = new BrowserRealtimeAdapter({
      url: "wss://game.example/realtime/game",
      openSubscription: async () => new Promise<SubscriptionAuthorization>((resolve) => {
        resolveAuthorization = resolve;
      }),
      createSocket,
    });
    const connecting = adapter.connect(null, controller.signal);
    controller.abort();
    resolveAuthorization?.(authorization());

    await expect(connecting).rejects.toMatchObject({ code: "subscription_cancelled", retryable: false });
    expect(createSocket).not.toHaveBeenCalled();
  });

  it("closes a socket when cancellation wins before abort listener registration", async () => {
    const controller = new AbortController();
    const socket = new FakeWebSocket();
    const adapter = new BrowserRealtimeAdapter({
      url: "wss://game.example/realtime/game",
      openSubscription: async () => authorization(),
      createSocket: () => {
        controller.abort();
        return socket;
      },
    });

    await expect(adapter.connect(null, controller.signal)).rejects.toMatchObject({
      code: "subscription_cancelled",
      retryable: false,
    });
    expect(socket.closes).toContainEqual({ code: 1000, reason: "subscription cancelled" });
  });

  it("rejects frames above the public transport size limit", async () => {
    const controller = new AbortController();
    const socket = new FakeWebSocket();
    const updates = await connect(socket, controller);
    const iterator = updates[Symbol.asyncIterator]();
    await iterator.next();

    const next = iterator.next();
    socket.message(new Uint8Array((64 << 10) + 1));

    await expect(next).rejects.toMatchObject({ code: "invalid_server_frame" });
    expect(socket.closes).toContainEqual({ code: 1002, reason: "invalid server frame" });
  });
});
