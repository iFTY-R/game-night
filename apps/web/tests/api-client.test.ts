import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError, gameClient, roomClient, type GameEnvelopeInput, type RoomSnapshot } from "../src/api/client";
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
  selectedGameId: "liars-dice",
  gameConfigDrafts: [],
  ownershipEpoch: "2",
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

const captureFailedRequest = (status: number, responseBody: Record<string, unknown>): void => {
  vi.stubGlobal("fetch", vi.fn(async () =>
    new Response(JSON.stringify(responseBody), { status, headers: { "Content-Type": "application/json" } })));
};

const expectDigest = (value: unknown): void => {
  expect(typeof value).toBe("string");
  expect(atob(String(value))).toHaveLength(32);
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Connect JSON mutation requests", () => {
  it("translates room duplicate-name failures without changing API status or code", async () => {
    captureFailedRequest(409, { code: "already_exists", message: "room.username.taken" });

    await expect(roomClient.joinRoom(room.roomCode)).rejects.toMatchObject({
      name: "ApiError",
      status: 409,
      code: "already_exists",
      message: "房间内已有同名玩家",
    } satisfies Partial<ApiError>);
  });

  it("restores an omitted zero seat index before room snapshots reach the UI", async () => {
    captureRequest({
      room: {
        ...room,
        members: [{
          userId: room.hostUserId,
          username: "小满",
          role: "MEMBER_ROLE_PARTICIPANT",
          requestedRole: "MEMBER_ROLE_UNSPECIFIED",
        }],
      },
      member: {
        userId: room.hostUserId,
        username: "小满",
        role: "MEMBER_ROLE_PARTICIPANT",
        requestedRole: "MEMBER_ROLE_UNSPECIFIED",
      },
      participants: [{ userId: room.hostUserId }],
    });

    const response = await roomClient.getRoom(room.roomId);

    expect(response.room?.members[0]?.seatIndex).toBe(0);
    expect(response.member?.seatIndex).toBe(0);
    expect(response.participants?.[0]?.seatIndex).toBe(0);
  });

  it("renews a room lease through the authenticated write boundary", async () => {
    const { calls } = captureRequest({ observedAt: "2026-07-22T12:00:00Z" });

    await roomClient.heartbeatRoom(room.roomId);

    expect(calls[0]).toEqual({
      url: "/platform.room.v1.RoomService/HeartbeatRoom",
      body: { roomId: room.roomId },
    });
  });

  it("binds room creation state when starting a game", async () => {
    const { calls } = captureRequest();

    vi.spyOn(crypto, "randomUUID").mockReturnValue("00000000-0000-4000-8000-000000000004");

    await roomClient.startGame(room, room.hostUserId);

    expect(calls[0]?.url).toBe("/platform.room.v1.RoomService/StartGame");
    expect(calls[0]?.body).toMatchObject({
      roomId: room.roomId,
      gameId: "liars-dice",
      configRevision: "0",
      expectedVersion: { roomVersion: "9", membershipVersion: "4" },
      config: { gameId: "liars-dice", schemaVersion: 1, messageType: "session.config", payload: "" },
    });
    expect(calls[0]?.body.requestDigest).toBe("drAey0lL07qe/lTQrmkvJ251Re6+SjAa6kgs+ZRKhMU=");
  });

  it("uses the atomic room finish boundary and canonical uint64 strings", async () => {
    const { calls } = captureRequest();

    vi.spyOn(crypto, "randomUUID")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000004")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000005");

    await roomClient.finishGame(room, room.hostUserId, room.activeSessionId, 12, command);

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
    expect(calls[0]?.body.requestDigest).toBe("XICV/mY8PglL1TVFqbbS7JaV2PTHkcCeLxPtDG/JVEU=");
  });

  it("binds member removal and room closure to the current room version", async () => {
    const { calls } = captureRequest();

    await roomClient.removeMember(room, "00000000-0000-4000-8000-000000000006");
    await roomClient.closeRoom(room);

    expect(calls).toEqual([
      {
        url: "/platform.room.v1.RoomService/RemoveMember",
        body: {
          roomId: room.roomId,
          userId: "00000000-0000-4000-8000-000000000006",
          expectedVersion: { roomVersion: "9", membershipVersion: "4" },
        },
      },
      {
        url: "/platform.room.v1.RoomService/CloseRoom",
        body: {
          roomId: room.roomId,
          expectedVersion: { roomVersion: "9", membershipVersion: "4" },
        },
      },
    ]);
  });

  it("serializes room rule selection and config draft updates with ownership fencing", async () => {
    const { calls } = captureRequest({ room });
    const config: GameEnvelopeInput = {
      gameId: "liars-dice",
      version: { engine: "1.2.0", protocol: "1.1.0", client: "1.0.0" },
      schemaVersion: 2,
      messageType: "rules.config",
      payload: new Uint8Array([9, 8, 7]),
    };

    await roomClient.selectRoomGame(room, "liars-dice");
    await roomClient.updateGameConfig(room, "liars-dice", config, "3");

    expect(calls[0]?.url).toBe("/platform.room.v1.RoomService/SelectRoomGame");
    expect(calls[0]?.body).toMatchObject({
      roomId: room.roomId,
      gameId: "liars-dice",
      expectedVersion: { roomVersion: "9", membershipVersion: "4" },
      ownershipEpoch: "2",
    });
    expectDigest(calls[0]?.body.requestDigest);
    expect(calls[1]?.url).toBe("/platform.room.v1.RoomService/UpdateGameConfig");
    expect(calls[1]?.body).toMatchObject({
      roomId: room.roomId,
      gameId: "liars-dice",
      config: { gameId: "liars-dice", schemaVersion: 2, messageType: "rules.config", payload: "CQgH" },
      expectedRevision: "3",
      expectedVersion: { roomVersion: "9", membershipVersion: "4" },
      ownershipEpoch: "2",
    });
    expectDigest(calls[1]?.body.requestDigest);
  });

  it("serializes personal preset CRUD requests with optimistic revisions", async () => {
    const { calls } = captureRequest({ preset: { presetId: "preset-1" }, presetId: "preset-1" });

    await roomClient.listGameRulePresets("liars-dice");
    await roomClient.saveGameRulePreset({
      presetId: "preset-1",
      gameId: "liars-dice",
      name: "Fast table",
      mode: "GAME_RULE_PRESET_WRITE_MODE_OVERWRITE",
      expectedPresetRevision: "7",
      config: { ...command, payload: "AQID" },
    });
    await roomClient.deleteGameRulePreset("preset-1", "8");

    expect(calls[0]).toEqual({
      url: "/platform.room.v1.RoomService/ListGameRulePresets",
      body: { gameId: "liars-dice" },
    });
    expect(calls[1]?.url).toBe("/platform.room.v1.RoomService/SaveGameRulePreset");
    expect(calls[1]?.body).toMatchObject({
      presetId: "preset-1",
      gameId: "liars-dice",
      name: "Fast table",
      mode: "GAME_RULE_PRESET_WRITE_MODE_OVERWRITE",
      expectedPresetRevision: "7",
      config: { payload: "AQID" },
    });
    expectDigest(calls[1]?.body.requestDigest);
    expect(calls[2]?.url).toBe("/platform.room.v1.RoomService/DeleteGameRulePreset");
    expect(calls[2]?.body).toMatchObject({ presetId: "preset-1", expectedPresetRevision: "8" });
    expectDigest(calls[2]?.body.requestDigest);
  });

  it("serializes begin, cancel, and pending-aware start requests", async () => {
    const pendingRoom: RoomSnapshot = {
      ...room,
      pendingStart: {
        pendingStartId: "00000000-0000-4000-8000-000000000010",
        cancelToken: "cancel-token-1",
        gameId: "liars-dice",
        configRevision: "5",
        ownershipEpoch: "2",
      },
      gameConfigDrafts: [{
        gameId: "liars-dice",
        revision: "5",
        updatedBy: room.hostUserId,
        config: {
          gameId: "liars-dice",
          version: { engine: "1.2.0", protocol: "1.1.0", client: "1.0.0" },
          schemaVersion: 2,
          messageType: "rules.config",
          payload: "BQQ=",
        },
      }],
    };
    const { calls } = captureRequest();
    vi.spyOn(crypto, "randomUUID")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000011")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000012")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000013");

    await roomClient.beginGameStart(room, "liars-dice", "5");
    await roomClient.cancelGameStart(pendingRoom, pendingRoom.pendingStart!);
    await roomClient.startGame(pendingRoom, room.hostUserId, "liars-dice");

    expect(calls[0]?.url).toBe("/platform.room.v1.RoomService/BeginGameStart");
    expect(calls[0]?.body).toMatchObject({
      roomId: room.roomId,
      gameId: "liars-dice",
      configRevision: "5",
      expectedVersion: { roomVersion: "9", membershipVersion: "4" },
      ownershipEpoch: "2",
    });
    expectDigest(calls[0]?.body.requestDigest);
    expect(calls[1]?.url).toBe("/platform.room.v1.RoomService/CancelGameStart");
    expect(calls[1]?.body).toMatchObject({
      pendingStartId: "00000000-0000-4000-8000-000000000010",
      cancelToken: "cancel-token-1",
      ownershipEpoch: "2",
    });
    expectDigest(calls[1]?.body.requestDigest);
    expect(calls[2]?.url).toBe("/platform.room.v1.RoomService/StartGame");
    expect(calls[2]?.body).toMatchObject({
      pendingStartId: "00000000-0000-4000-8000-000000000010",
      cancelToken: "cancel-token-1",
      configRevision: "5",
      ownershipEpoch: "2",
      config: {
        gameId: "liars-dice",
        version: { engine: "1.2.0", protocol: "1.1.0", client: "1.0.0" },
        schemaVersion: 2,
        messageType: "rules.config",
        payload: "BQQ=",
      },
    });
    expect(calls[2]?.body.requestDigest).toBe("W+E6COC59InWHbGusQOWRxsQGnJXedJh0xRmxKAOMdc=");
  });

  it("serializes action versions and protobuf bytes using Connect JSON rules", async () => {
    const { calls } = captureRequest();

    await gameClient.action(room.roomId, room.hostUserId, room.activeSessionId, 7, "00000000-0000-4000-8000-000000000004", command);

    expect(calls[0]?.url).toBe("/platform.game.v1.GameService/GameAction");
    expect(calls[0]?.body).toMatchObject({
      expectedStateVersion: "7",
      command: { payload: "AQID" },
    });
    expect(calls[0]?.body.requestDigest).toBe("7qbUc9o04q9LvdThmOOhdfikCYvziClTVZ/uX4+a8wU=");
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

  it("requests an immutable replay projection with validated terminal metadata", async () => {
    const { calls } = captureRequest({
      complete: true,
      session: {
        sessionId: room.activeSessionId,
        roomId: room.roomId,
        gameId: "liars-dice",
        stateVersion: "9",
        status: "GAME_SESSION_STATUS_FINISHED",
      },
      terminalMeta: {
        finished: true,
        cancelled: false,
        endedAt: "2026-07-23T12:00:00Z",
      },
    });

    const response = await gameClient.getReplayProjection(room.roomId, room.activeSessionId);

    expect(calls[0]?.url).toBe("/platform.game.v1.GameService/GetReplayProjection");
    expect(calls[0]?.body).toEqual({
      roomId: room.roomId,
      sessionId: room.activeSessionId,
      viewerKind: "VIEWER_KIND_REPLAY",
      throughStateVersion: "0",
    });
    expect(response.terminalMeta).toMatchObject({
      finished: true,
      cancelled: false,
      endedAt: "2026-07-23T12:00:00Z",
    });
  });

  it("fails closed when replay terminal metadata is missing", async () => {
    captureRequest({
      complete: true,
      session: {
        sessionId: room.activeSessionId,
        roomId: room.roomId,
        gameId: "liars-dice",
        stateVersion: "9",
        status: "GAME_SESSION_STATUS_FINISHED",
      },
    });

    await expect(gameClient.getReplayProjection(room.roomId, room.activeSessionId)).rejects.toThrowError("replay_terminal_meta_missing");
  });

  it("fails closed when replay terminal metadata disagrees with the session status", async () => {
    captureRequest({
      complete: true,
      session: {
        sessionId: room.activeSessionId,
        roomId: room.roomId,
        gameId: "liars-dice",
        stateVersion: "9",
        status: "GAME_SESSION_STATUS_CANCELLED",
      },
      terminalMeta: {
        finished: true,
        cancelled: false,
        endedAt: "2026-07-23T12:00:00Z",
      },
    });

    await expect(gameClient.getReplayProjection(room.roomId, room.activeSessionId)).rejects.toThrowError("replay_terminal_status_inconsistent");
  });

  it("reads and updates replay access with an explicit policy version", async () => {
    const { calls } = captureRequest({ access: { policy: "REPLAY_ACCESS_POLICY_ROOM_MEMBER", policyVersion: "2" } });

    await gameClient.getReplayAccess(room.roomId, room.activeSessionId);
    await gameClient.setReplayAccess(
      room.roomId,
      room.activeSessionId,
      "REPLAY_ACCESS_POLICY_ROOM_MEMBER",
      "1",
    );

    expect(calls[0]).toMatchObject({
      url: "/platform.game.v1.GameService/GetReplayAccess",
      body: { roomId: room.roomId, sessionId: room.activeSessionId },
    });
    expect(calls[1]).toMatchObject({
      url: "/platform.game.v1.GameService/SetReplayAccess",
      body: {
        roomId: room.roomId,
        sessionId: room.activeSessionId,
        policy: "REPLAY_ACCESS_POLICY_ROOM_MEMBER",
        expectedPolicyVersion: "1",
      },
    });
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
