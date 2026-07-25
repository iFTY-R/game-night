import { createApp, nextTick } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import GameGovernanceControls from "../src/components/game/GameGovernanceControls.vue";
import type { RoomSnapshot } from "../src/api/client";
import { useRoomStore } from "../src/stores/room";

const sessionId = "session-1";
const roomSnapshot = (hostUserId = "user-1"): RoomSnapshot => ({
  roomId: "room-1",
  roomCode: "TABLE1",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_PLAYING",
  hostUserId,
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

describe("GameGovernanceControls", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.body.replaceChildren();
  });

  it("lets a seated member submit one pause request", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const room = useRoomStore();
    room.userId = "user-2";
    room.displayName = "阿青";
    room.identityState = "active";
    room.remoteRoom = roomSnapshot();
    const requestPause = vi.spyOn(room, "requestRemotePause").mockResolvedValue(room.remoteRoom);
    const root = document.createElement("div");
    document.body.appendChild(root);
    const app = createApp(GameGovernanceControls, { sessionId, ownershipEpoch: "7" });
    app.use(pinia);
    app.mount(root);

    root.querySelector<HTMLButtonElement>('[title="向房主申请暂停"]')?.click();
    await settle();

    expect(requestPause).toHaveBeenCalledWith(sessionId);
    app.unmount();
  });

  it("requires host confirmation before approving a named pause request", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const room = useRoomStore();
    room.userId = "user-1";
    room.displayName = "小满";
    room.identityState = "active";
    room.remoteRoom = {
      ...roomSnapshot(),
      pendingPauseRequest: {
        requestId: "request-1",
        sessionId,
        requestedByUserId: "user-2",
        requestedAt: "2026-07-26T02:00:00Z",
      },
    };
    const pauseGame = vi.spyOn(room, "pauseRemoteGame").mockResolvedValue(room.remoteRoom);
    const root = document.createElement("div");
    document.body.appendChild(root);
    const app = createApp(GameGovernanceControls, { sessionId, ownershipEpoch: "7" });
    app.use(pinia);
    app.mount(root);

    expect(root.textContent).toContain("阿青");
    root.querySelector<HTMLButtonElement>('[title="同意暂停"]')?.click();
    await nextTick();
    expect(pauseGame).not.toHaveBeenCalled();
    document.querySelector<HTMLButtonElement>(".is-danger")?.click();
    await settle();

    expect(pauseGame).toHaveBeenCalledWith(sessionId, "request-1", "7");
    app.unmount();
  });

  it("shows paused state to everyone and reserves resume for the host", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const room = useRoomStore();
    room.userId = "user-1";
    room.displayName = "小满";
    room.identityState = "active";
    room.remoteRoom = {
      ...roomSnapshot(),
      activePause: {
        pauseId: "pause-1",
        sessionId,
        source: "PAUSE_SOURCE_HOST",
        pausedByUserId: "user-1",
        pausedAt: "2026-07-26T02:00:00Z",
      },
    };
    const resumeGame = vi.spyOn(room, "resumeRemoteGame").mockResolvedValue(room.remoteRoom);
    const root = document.createElement("div");
    document.body.appendChild(root);
    const app = createApp(GameGovernanceControls, { sessionId, ownershipEpoch: "7" });
    app.use(pinia);
    app.mount(root);

    expect(root.textContent).toContain("已暂停");
    root.querySelector<HTMLButtonElement>('[title="恢复游戏"]')?.click();
    await nextTick();
    document.querySelector<HTMLButtonElement>(".is-danger")?.click();
    await settle();

    expect(resumeGame).toHaveBeenCalledWith(sessionId, "7");
    app.unmount();
  });
});
