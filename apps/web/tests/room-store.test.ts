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
      },
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    const room = useRoomStore();
    const snapshot = await room.loadRoom("room-1");

    expect(snapshot?.status).toBe("ROOM_STATUS_POST_GAME");
    expect(room.remoteRoom?.version).toEqual({ roomVersion: "9", membershipVersion: "4" });
    expect(room.remoteRoom?.members[1]?.role).toBe("MEMBER_ROLE_WAITING");
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
        { userId: "user-1", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 },
        { userId: "user-2", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 1 },
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
