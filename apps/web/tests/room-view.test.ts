import { createApp, nextTick } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";

import RoomView from "../src/views/RoomView.vue";

const createRemoteRoom = () => ({
  roomId: "room-1",
  roomCode: "ABCD12",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_LOBBY",
  hostUserId: "host-user",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_OPEN",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [
    { userId: "host-user", username: "小满", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 },
    { userId: "seat-2", username: "阿青", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 1 },
    { userId: "seat-3", username: "北屿", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 2 },
    { userId: "seat-4", username: "南风", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 3 },
    { userId: "wait-1", username: "候场玩家", role: "MEMBER_ROLE_WAITING", requestedRole: "MEMBER_ROLE_PARTICIPANT", seatIndex: 4 },
  ],
  activeSessionId: "",
  activeGameId: "",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "9", membershipVersion: "4" },
  selectedGameId: "liars-dice",
  gameConfigDrafts: [],
  ownershipEpoch: "2",
});

const createStore = () => {
  const remoteRoom = createRemoteRoom();
  return {
    userId: "host-user",
    displayName: "小满",
    roomId: "room-1",
    roomCode: "ABCD12",
    remoteRoom,
    leaveRoom: vi.fn(),
    exitRoom: vi.fn(),
    setSession: vi.fn(),
    loadRoom: vi.fn(async () => remoteRoom),
    listGameRulePresets: vi.fn(async () => []),
    selectRemoteGame: vi.fn(async () => remoteRoom),
    updateRemoteGameConfig: vi.fn(async () => ({ room: remoteRoom })),
    beginRemoteGameStart: vi.fn(async () => ({ room: remoteRoom })),
    cancelRemoteGameStart: vi.fn(async () => ({ room: remoteRoom })),
    approveRemoteMember: vi.fn(async () => remoteRoom),
    removeRemoteMember: vi.fn(async () => remoteRoom),
    closeRemoteRoom: vi.fn(async () => ({ ...remoteRoom, status: "ROOM_STATUS_CLOSED" })),
    setAdmissionRemote: vi.fn(async () => undefined),
    saveGameRulePreset: vi.fn(async () => undefined),
    deleteGameRulePresetById: vi.fn(async () => undefined),
  };
};

const roomStore = createStore();

vi.mock("../src/stores/room", () => ({
  useRoomStore: () => roomStore,
}));

vi.mock("../src/composables/use-room-presence-lease", () => ({
  useRoomPresenceLease: () => undefined,
}));

const routerPush = vi.fn();
const routerReplace = vi.fn();

vi.mock("vue-router", () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: routerPush, replace: routerReplace }),
}));

const installViewportMocks = (): void => {
  vi.stubGlobal("matchMedia", vi.fn(() => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })));
  vi.stubGlobal("ResizeObserver", class {
    private readonly callback: ResizeObserverCallback;

    constructor(callback: ResizeObserverCallback) {
      this.callback = callback;
    }

    observe(): void {
      this.callback([{ contentRect: { width: 390, height: 560 } } as ResizeObserverEntry], this as unknown as ResizeObserver);
    }

    disconnect(): void {}
    unobserve(): void {}
  });
};

const mountRoomView = async (): Promise<{ app: ReturnType<typeof createApp>; root: HTMLDivElement }> => {
  installViewportMocks();
  const root = document.createElement("div");
  document.body.appendChild(root);
  const app = createApp(RoomView, { roomId: "room-1" });
  app.mount(root);
  await nextTick();
  await Promise.resolve();
  return { app, root };
};

afterEach(() => {
  vi.clearAllMocks();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("RoomView", () => {
  it("renders seated participants around the shared table and keeps waiting members in the roster", async () => {
    vi.useFakeTimers();
    const { app, root } = await mountRoomView();

    expect(root.querySelectorAll(".gn-table__seat")).toHaveLength(4);
    expect(root.textContent).toContain("候场玩家");
    expect(root.querySelector(".member-roster")).not.toBeNull();

    app.unmount();
  });

  it("opens the rule tray from the table CTA and reuses DangerConfirm for destructive actions", async () => {
    vi.useFakeTimers();
    const { app, root } = await mountRoomView();

    root.querySelector<HTMLButtonElement>(".room-center-card__rules")?.click();
    await nextTick();
    expect(root.querySelector(".gn-tray")?.getAttribute("data-state")).toBe("expanded");

    root.querySelector<HTMLButtonElement>(".member-row .mini-action--danger")?.click();
    await nextTick();
    expect(document.body.textContent).toContain("确认移出成员？");

    app.unmount();
  });

  it("keeps a terminal notice when the host closes the room", async () => {
    vi.useFakeTimers();
    const { app, root } = await mountRoomView();

    root.querySelector<HTMLButtonElement>(".danger-control")?.click();
    await nextTick();
    const confirmButton = Array.from(document.body.querySelectorAll<HTMLButtonElement>("button"))
      .find((button) => button.textContent?.includes("确认解散"));
    confirmButton?.click();
    await nextTick();
    await Promise.resolve();

    expect(roomStore.exitRoom).toHaveBeenCalledWith("房间已解散");
    expect(roomStore.leaveRoom).not.toHaveBeenCalled();
    expect(routerReplace).toHaveBeenCalledWith({ name: "home" });

    app.unmount();
  });

  it("renders dedicated three-round timing inputs instead of stakes and variant toggles", async () => {
    vi.useFakeTimers();
    roomStore.remoteRoom.selectedGameId = "three-rounds";
    const { app, root } = await mountRoomView();

    const selected = Array.from(root.querySelectorAll<HTMLButtonElement>(".game-option")).find((button) => button.textContent?.includes("三关定胜负"));
    selected?.click();
    await nextTick();

    const inputs = root.querySelectorAll<HTMLInputElement>(".rule-controls input[type='number']");
    expect(inputs).toHaveLength(4);
    expect(root.querySelector(".rule-switch")).toBeNull();
    expect(root.textContent).toContain("第一关时限");
    expect(root.textContent).toContain("总榜停留");

    app.unmount();
  });
});
