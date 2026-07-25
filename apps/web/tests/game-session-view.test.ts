import { createApp, nextTick } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const router = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn() }));

vi.mock("vue-router", () => ({ useRouter: () => router }));
vi.mock("../src/views/GameView.vue", () => ({
  default: {
    props: ["pausedAt"],
    emits: ["lifecycle-change"],
    template: `
      <div class="game-view-stub" :data-paused-at="pausedAt">
        <slot name="governance" />
        <slot name="seat-details" user-id="user-2" display-name="阿青" />
        <button class="emit-resumed" @click="$emit('lifecycle-change', { known: true, paused: false, suspendedAt: null })">resume</button>
      </div>
    `,
  },
}));

import type { RoomSnapshot } from "../src/api/client";
import { useRoomStore } from "../src/stores/room";
import GameSessionView from "../src/views/GameSessionView.vue";

const sessionId = "session-1";
const snapshot = (): RoomSnapshot => ({
  roomId: "room-1",
  roomCode: "TABLE1",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_PLAYING",
  hostUserId: "user-1",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_CLOSED",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [
    { userId: "user-1", username: "小满", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 },
    { userId: "user-2", username: "阿青", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 1 },
  ],
  activeSessionId: sessionId,
  activeGameId: "liars-dice",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "5", membershipVersion: "2" },
  ownershipEpoch: "3",
});

const settle = async (): Promise<void> => {
  await Promise.resolve();
  await nextTick();
};

describe("GameSessionView governance", () => {
  beforeEach(() => {
    router.push.mockReset();
    router.replace.mockReset();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.body.replaceChildren();
  });

  const mountSession = async (roomSnapshot: RoomSnapshot) => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const room = useRoomStore();
    room.userId = "user-1";
    room.displayName = "小满";
    room.identityState = "active";
    room.roomId = roomSnapshot.roomId;
    room.roomCode = roomSnapshot.roomCode;
    room.sessionId = sessionId;
    room.remoteRoom = roomSnapshot;
    vi.spyOn(room, "loadRoom").mockResolvedValue(roomSnapshot);
    const root = document.createElement("div");
    document.body.appendChild(root);
    const app = createApp(GameSessionView, { roomId: roomSnapshot.roomId, sessionId });
    app.use(pinia);
    app.mount(root);
    await settle();
    return { app, room, root };
  };

  it("keeps the game mounted and exposes the authoritative pause state", async () => {
    const pausedAt = "2026-07-26T02:00:00Z";
    const { app, root } = await mountSession({
      ...snapshot(),
      activePause: {
        pauseId: "pause-1",
        sessionId,
        source: "PAUSE_SOURCE_APPROVED_REQUEST",
        requestedByUserId: "user-2",
        pausedByUserId: "user-1",
        pausedAt,
      },
    });

    expect(root.querySelector(".game-view-stub")?.getAttribute("data-paused-at")).toBe(pausedAt);
    expect(root.querySelector(".room-pause")?.textContent).toContain("游戏已暂停");
    expect(root.querySelector(".room-pause")?.textContent).toContain("阿青 申请");
    expect(root.querySelector(".game-session-shell")?.classList).toContain("is-paused");

    app.unmount();
  });

  it("transfers host only after confirming from another participant's details", async () => {
    const { app, room, root } = await mountSession(snapshot());
    const transferHost = vi.spyOn(room, "transferRemoteHost").mockResolvedValue(room.remoteRoom);

    root.querySelector<HTMLButtonElement>(".transfer-host-action")?.click();
    await nextTick();
    expect(transferHost).not.toHaveBeenCalled();
    document.querySelector<HTMLButtonElement>(".is-danger")?.click();
    await settle();

    expect(transferHost).toHaveBeenCalledWith("user-2");
    app.unmount();
  });

  it("unfreezes the shell when the session lifecycle arrives before the room refresh", async () => {
    const pausedAt = "2026-07-26T02:00:00Z";
    const { app, root } = await mountSession({
      ...snapshot(),
      activePause: {
        pauseId: "pause-1",
        sessionId,
        source: "PAUSE_SOURCE_HOST",
        pausedByUserId: "user-1",
        pausedAt,
      },
    });

    root.querySelector<HTMLButtonElement>(".emit-resumed")?.click();
    await nextTick();

    expect(root.querySelector(".room-pause")).toBeNull();
    expect(root.querySelector(".game-session-shell")?.classList).not.toContain("is-paused");
    expect(root.querySelector(".game-view-stub")?.getAttribute("data-paused-at")).toBeNull();
    app.unmount();
  });
});
