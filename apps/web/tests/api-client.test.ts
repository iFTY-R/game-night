import { afterEach, describe, expect, it, vi } from "vitest";

import { gameClient, roomClient, type GameEnvelopeInput, type RoomSnapshot } from "../src/api/client";

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

const captureRequest = (): { calls: Array<{ url: string; body: Record<string, unknown> }> } => {
  const calls: Array<{ url: string; body: Record<string, unknown> }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({
      url: String(input),
      body: JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>,
    });
    return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
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
});
