import { afterEach, describe, expect, it, vi } from "vitest";

import { gameClient, roomClient, type GameEnvelopeInput, type RoomSnapshot } from "../src/api/client";
import { gameProjectionFromConnect } from "../src/api/game-projection";

const room: RoomSnapshot = {
  roomId: "00000000-0000-4000-8000-000000000001",
  roomCode: "N789",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_PLAYING",
  hostUserId: "00000000-0000-4000-8000-000000000002",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_CLOSED",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [],
  activeSessionId: "00000000-0000-4000-8000-000000000003",
  activeGameId: "liars-dice",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "9", membershipVersion: "4" },
};

const command: GameEnvelopeInput = {
  gameId: "liars-dice",
  version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
  schemaVersion: 1,
  messageType: "session.finish",
  payload: new Uint8Array([1, 2, 3]),
};

const captureRequest = (responseBody: Record<string, unknown> = {}): { calls: Array<{ url: string; body: Record<string, unknown> }> } => {
  const calls: Array<{ url: string; body: Record<string, unknown> }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({
      url: String(input),
      body: JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>,
    });
    return new Response(JSON.stringify(responseBody), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  return { calls };
};

const expectDigest = (value: unknown): void => {
  expect(typeof value).toBe("string");
  expect(atob(String(value))).toHaveLength(32);
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Connect JSON mutation requests", () => {
  it("binds room creation state when starting a game", async () => {
    const { calls } = captureRequest();

    await roomClient.startGame(room);

    expect(calls[0]?.url).toBe("/platform.room.v1.RoomService/StartGame");
    expect(calls[0]?.body).toMatchObject({
      roomId: room.roomId,
      gameId: "liars-dice",
      expectedVersion: { roomVersion: "9", membershipVersion: "4" },
      config: { gameId: "liars-dice", schemaVersion: 1, messageType: "session.config", payload: "" },
    });
    expectDigest(calls[0]?.body.requestDigest);
  });

  it("uses the atomic room finish boundary and canonical uint64 strings", async () => {
    const { calls } = captureRequest();

    await roomClient.finishGame(room, room.activeSessionId, 12, command);

    expect(calls[0]?.url).toBe("/platform.room.v1.RoomService/FinishGame");
    expect(calls[0]?.body).toMatchObject({
      roomId: room.roomId,
      sessionId: room.activeSessionId,
      expectedVersion: { roomVersion: "9", membershipVersion: "4" },
      expectedStateVersion: "12",
      command: { gameId: "liars-dice", messageType: "session.finish", payload: "AQID" },
    });
    expect(calls[0]?.body.operationId).toEqual(expect.any(String));
    expect(calls[0]?.body.sourceEventId).toEqual(expect.any(String));
    expectDigest(calls[0]?.body.requestDigest);
  });

  it("serializes action versions and protobuf bytes using Connect JSON rules", async () => {
    const { calls } = captureRequest();

    await gameClient.action(room.roomId, room.activeSessionId, 7, "00000000-0000-4000-8000-000000000004", command);

    expect(calls[0]?.url).toBe("/platform.game.v1.GameService/GameAction");
    expect(calls[0]?.body).toMatchObject({
      expectedStateVersion: "7",
      command: { payload: "AQID" },
    });
    expectDigest(calls[0]?.body.requestDigest);
  });

  it("opens a cursor-bound subscription and decodes one-time credentials", async () => {
    const { calls } = captureRequest({ ticket: "AQI=", grant: "AwQ=" });

    const response = await gameClient.openSubscription(
      room.roomId,
      room.activeSessionId,
      "VIEWER_KIND_PLAYER",
      15,
    );

    expect(calls[0]?.url).toBe("/platform.game.v1.GameService/OpenSubscription");
    expect(calls[0]?.body).toEqual({
      roomId: room.roomId,
      sessionId: room.activeSessionId,
      viewerKind: "VIEWER_KIND_PLAYER",
      lastStateVersion: "15",
      lastEventOrdinal: 0,
    });
    expect([...response.ticket]).toEqual([1, 2]);
    expect([...response.grant]).toEqual([3, 4]);
  });

  it("fails closed when subscription credentials are malformed", async () => {
    captureRequest({ ticket: "not-base64", grant: "AwQ=" });

    await expect(gameClient.openSubscription(
      room.roomId,
      room.activeSessionId,
      "VIEWER_KIND_PLAYER",
      15,
    )).rejects.toMatchObject({
      code: "invalid_subscription_credentials",
      retryable: false,
    });
  });
});

describe("Connect JSON projection validation", () => {
  it("rejects viewer enum lookalikes instead of widening authorization", () => {
    expect(() => gameProjectionFromConnect({
      sessionId: room.activeSessionId,
      stateVersion: "1",
      viewerKind: "NOT_VIEWER_KIND_PLAYER",
      view: {
        gameId: "liars-dice",
        version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
        schemaVersion: 1,
        messageType: "session.view",
        payload: "",
      },
      allowedActions: [],
    })).toThrowError("game_viewer_kind_invalid");
  });
});
