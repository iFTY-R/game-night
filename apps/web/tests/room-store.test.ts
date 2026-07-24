import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { STORAGE_KEY, useRoomStore } from "../src/stores/room";

describe("room context recovery", () => {
  beforeEach(() => {
    window.localStorage.clear();
    setActivePinia(createPinia());
  });

  it("persists only the viewer-safe room context", () => {
    const room = useRoomStore();
    expect(room.setIdentity("小满")).toBe(true);
    room.enterRoom("room-1", "N789");
    room.setSession("session-1");

    const persisted = window.localStorage.getItem(STORAGE_KEY) ?? "";
    expect(persisted).toContain("小满");
    expect(persisted).not.toMatch(/fingerprint|deviceSecret|token/i);
  });

  it("ignores incompatible schema versions", () => {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ schemaVersion: 99, displayName: "旧用户", userId: "old" }));
    const room = useRoomStore();
    room.recover();
    expect(room.hasIdentity).toBe(false);
  });

  it("removes corrupt recovery data without blocking startup", () => {
    window.localStorage.setItem(STORAGE_KEY, "not-json");
    const room = useRoomStore();
    expect(() => room.recover()).not.toThrow();
    expect(window.localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("completes first-device onboarding through the server challenge sequence", async () => {
    const calls: Array<{ url: string; body: Record<string, unknown> }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      calls.push({ url: String(input), body });
      const index = calls.length;
      const payload = index === 1
        ? { error: { code: "unauthenticated", message: "device required" } }
        : index === 2
          ? { challenge: { challengeProof: "proof-1" } }
          : index === 3
            ? { user: { userId: "user-1", status: "USER_STATUS_ONBOARDING", username: "" } }
            : { user: { userId: "user-1", status: "USER_STATUS_ACTIVE", username: "小满" } };
      return new Response(JSON.stringify(payload), { status: index === 1 ? 401 : 200, headers: { "Content-Type": "application/json" } });
    }));

    const room = useRoomStore();
    await room.ensureIdentity("小满");

    expect(calls.map((call) => call.url)).toEqual([
      "/platform.identity.v1.IdentityService/GetCurrentIdentity",
      "/platform.identity.v1.IdentityService/BeginIdentityBootstrap",
      "/platform.identity.v1.IdentityService/BootstrapIdentity",
      "/platform.identity.v1.IdentityService/CompleteOnboarding",
    ]);
    expect(calls[2]?.body).toMatchObject({ challengeProof: "proof-1", deviceLabel: "Game Night 浏览器" });
    expect(room.userId).toBe("user-1");
    expect(room.displayName).toBe("小满");
    expect(room.hasIdentity).toBe(true);
  });

  it("does not trust stale local identity when the device credential is rejected", async () => {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify({
      schemaVersion: 1,
      displayName: "旧用户名",
      userId: "guest-stale",
      roomId: null,
      roomCode: null,
      sessionId: null,
    }));
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      code: "permission_denied",
      message: "request.origin.not_allowed",
    }), { status: 403, headers: { "Content-Type": "application/json" } })));

    const room = useRoomStore();
    room.recover();
    expect(room.hasIdentity).toBe(true);

    await room.recoverIdentity();

    expect(room.hasIdentity).toBe(false);
    expect(room.identityState).toBe("anonymous");
  });

  it("fails closed when device bootstrap is denied", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      code: "permission_denied",
      message: "request.origin.not_allowed",
    }), { status: 403, headers: { "Content-Type": "application/json" } })));

    const room = useRoomStore();

    await expect(room.ensureIdentity("小满")).rejects.toBeInstanceOf(Error);
    expect(room.hasIdentity).toBe(false);
    expect(room.userId).toBe("");
  });

  it("does not turn a rejected room creation into a local demo room", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      code: "permission_denied",
      message: "identity.device.invalid",
    }), { status: 403, headers: { "Content-Type": "application/json" } })));

    const room = useRoomStore();

    await expect(room.createRemoteRoom()).rejects.toBeInstanceOf(Error);
    expect(room.remoteRoom).toBeNull();
    expect(room.roomId).toBeNull();
  });

  it("rejects an incomplete room creation response", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    const room = useRoomStore();

    await expect(room.createRemoteRoom()).rejects.toThrow("创建房间响应缺少状态");
  });

  it("keeps authoritative room versions and waiting roles for host controls", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      room: {
        roomId: "room-1",
        roomCode: "N789",
        visibility: "ROOM_VISIBILITY_PRIVATE",
        status: "ROOM_STATUS_POST_GAME",
        hostUserId: "user-1",
        participantCapacity: 8,
        participantAdmission: "ADMISSION_MODE_CLOSED",
        spectatorAdmission: "ADMISSION_MODE_OPEN",
        members: [
          { userId: "user-1", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 },
          { userId: "user-2", role: "MEMBER_ROLE_WAITING", requestedRole: "MEMBER_ROLE_PARTICIPANT", seatIndex: 0 },
        ],
        version: { roomVersion: "9", membershipVersion: "4" },
        selectedGameId: "meet-by-chance",
        gameConfigDrafts: [{
          gameId: "meet-by-chance",
          revision: "2",
          updatedBy: "user-1",
          config: {
            gameId: "meet-by-chance",
            version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
            schemaVersion: 1,
            messageType: "rules.config",
            payload: "",
          },
        }],
        pendingStart: {
          pendingStartId: "pending-1",
          cancelToken: "cancel-1",
          gameId: "meet-by-chance",
          configRevision: "2",
          ownershipEpoch: "6",
        },
        ownershipEpoch: "6",
      },
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    const room = useRoomStore();
    const snapshot = await room.loadRoom("room-1");

    expect(snapshot?.status).toBe("ROOM_STATUS_POST_GAME");
    expect(room.remoteRoom?.version).toEqual({ roomVersion: "9", membershipVersion: "4" });
    expect(room.remoteRoom?.members[1]?.role).toBe("MEMBER_ROLE_WAITING");
    expect(room.selectedGameId).toBe("meet-by-chance");
    expect(room.currentGameConfigDraft?.revision).toBe("2");
    expect(room.pendingStart?.cancelToken).toBe("cancel-1");
    expect(room.ownershipEpoch).toBe("6");
  });

  it("adopts rule snapshots returned by selection, config, countdown, and preset actions", async () => {
    const calls: Array<{ url: string; body: Record<string, unknown> }> = [];
    const baseRoom = {
      roomId: "00000000-0000-4000-8000-000000000001",
      roomCode: "N789",
      visibility: "ROOM_VISIBILITY_PRIVATE",
      status: "ROOM_STATUS_LOBBY",
      hostUserId: "00000000-0000-4000-8000-000000000002",
      participantCapacity: 8,
      participantAdmission: "ADMISSION_MODE_OPEN",
      spectatorAdmission: "ADMISSION_MODE_OPEN",
      members: [],
      activeSessionId: "",
      activeGameId: "",
      lastFinishedSessionId: "",
      lastFinishedGameId: "",
      version: { roomVersion: "9", membershipVersion: "4" },
      selectedGameId: "liars-dice",
      gameConfigDrafts: [],
      ownershipEpoch: "3",
    };
    const config = {
      gameId: "liars-dice",
      version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
      schemaVersion: 1,
      messageType: "rules.config",
      payload: new Uint8Array([1]),
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      calls.push({ url, body });
      if (url.endsWith("/UpdateGameConfig")) {
        const draft = { gameId: "liars-dice", revision: "4", updatedBy: baseRoom.hostUserId, config: { ...(body.config as Record<string, unknown>) } };
        return new Response(JSON.stringify({ room: { ...baseRoom, gameConfigDrafts: [draft] }, draft }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (url.endsWith("/BeginGameStart")) {
        const pendingStart = {
          pendingStartId: "00000000-0000-4000-8000-000000000003",
          cancelToken: "cancel-3",
          gameId: "liars-dice",
          configRevision: "4",
          ownershipEpoch: "3",
        };
        return new Response(JSON.stringify({ room: { ...baseRoom, gameConfigDrafts: [{ gameId: "liars-dice", revision: "4", updatedBy: baseRoom.hostUserId }], pendingStart }, pendingStart }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (url.endsWith("/CancelGameStart")) {
        return new Response(JSON.stringify({
          room: {
            ...baseRoom,
            gameConfigDrafts: [{ gameId: "liars-dice", revision: "4", updatedBy: baseRoom.hostUserId }],
          },
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (url.endsWith("/SaveGameRulePreset")) {
        return new Response(JSON.stringify({ preset: { presetId: "preset-1", gameId: "liars-dice", name: "Fast", presetRevision: "1", compatible: true } }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (url.endsWith("/DeleteGameRulePreset")) {
        return new Response(JSON.stringify({ presetId: "preset-1" }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({ room: { ...baseRoom, selectedGameId: "dice-789" } }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    const room = useRoomStore();
    room.setRemoteRoom(baseRoom);
    await room.selectRemoteGame("dice-789");
    await room.updateRemoteGameConfig("liars-dice", config);
    await room.beginRemoteGameStart("liars-dice");
    await room.cancelRemoteGameStart();
    await room.saveGameRulePreset({ name: "Fast", config, mode: "GAME_RULE_PRESET_WRITE_MODE_CREATE" });
    await room.deleteGameRulePreset("preset-1", "1");

    expect(room.selectedGameId).toBe("liars-dice");
    expect(room.gameConfigDrafts[0]?.revision).toBe("4");
    expect(room.pendingStart).toBeNull();
    expect(room.gameRulePresets).toEqual([]);
    expect(calls.map((call) => call.url)).toEqual([
      "/platform.room.v1.RoomService/SelectRoomGame",
      "/platform.room.v1.RoomService/UpdateGameConfig",
      "/platform.room.v1.RoomService/BeginGameStart",
      "/platform.room.v1.RoomService/CancelGameStart",
      "/platform.room.v1.RoomService/SaveGameRulePreset",
      "/platform.room.v1.RoomService/DeleteGameRulePreset",
    ]);
  });

  it("uses the removal result version for the next room closure command", async () => {
    const calls: Array<{ url: string; body: Record<string, unknown> }> = [];
    const baseRoom = {
      roomId: "room-1",
      roomCode: "N789",
      visibility: "ROOM_VISIBILITY_PRIVATE",
      status: "ROOM_STATUS_LOBBY",
      hostUserId: "user-1",
      participantCapacity: 8,
      participantAdmission: "ADMISSION_MODE_OPEN",
      spectatorAdmission: "ADMISSION_MODE_OPEN",
      members: [
        { userId: "user-1", username: "小满", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 },
        { userId: "user-2", username: "阿青", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 1 },
      ],
      activeSessionId: "",
      activeGameId: "",
      lastFinishedSessionId: "",
      lastFinishedGameId: "",
      version: { roomVersion: "9", membershipVersion: "4" },
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      calls.push({ url, body });
      const room = url.endsWith("/RemoveMember")
        ? { ...baseRoom, members: baseRoom.members.slice(0, 1), version: { roomVersion: "10", membershipVersion: "5" } }
        : { ...baseRoom, members: baseRoom.members.slice(0, 1), status: "ROOM_STATUS_CLOSED", version: { roomVersion: "11", membershipVersion: "5" } };
      return new Response(JSON.stringify({ room }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    const room = useRoomStore();
    room.setRemoteRoom(baseRoom);
    await room.removeRemoteMember("user-2");
    await room.closeRemoteRoom();

    expect(calls[0]).toMatchObject({
      url: "/platform.room.v1.RoomService/RemoveMember",
      body: { expectedVersion: { roomVersion: "9", membershipVersion: "4" } },
    });
    expect(calls[1]).toMatchObject({
      url: "/platform.room.v1.RoomService/CloseRoom",
      body: { expectedVersion: { roomVersion: "10", membershipVersion: "5" } },
    });
    expect(room.remoteRoom?.status).toBe("ROOM_STATUS_CLOSED");
  });
});
