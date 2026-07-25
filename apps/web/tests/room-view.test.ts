import { createApp, nextTick, reactive } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../src/api/client";
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
    changeUsername: vi.fn(async (username: string) => {
      roomStore.displayName = username;
    }),
    leaveRoom: vi.fn(),
    exitRoom: vi.fn(),
    setSession: vi.fn(),
    loadRoom: vi.fn(async () => roomStore.remoteRoom),
    loadMyRooms: vi.fn(async () => undefined),
    loadPublicRooms: vi.fn(async () => undefined),
    listGameRulePresets: vi.fn(async () => []),
    selectRemoteGame: vi.fn(async () => remoteRoom),
    updateRemoteGameConfig: vi.fn(async () => ({ room: remoteRoom })),
    beginRemoteGameStart: vi.fn(async () => ({ room: remoteRoom })),
    cancelRemoteGameStart: vi.fn(async () => ({ room: remoteRoom })),
    approveRemoteMember: vi.fn(async () => remoteRoom),
    removeRemoteMember: vi.fn(async () => remoteRoom),
    transferRemoteHost: vi.fn(async (userId: string) => {
      roomStore.remoteRoom.hostUserId = userId;
      return roomStore.remoteRoom;
    }),
    closeRemoteRoom: vi.fn(async () => ({ ...remoteRoom, status: "ROOM_STATUS_CLOSED" })),
    setAdmissionRemote: vi.fn(async () => undefined),
    saveGameRulePreset: vi.fn(async () => undefined),
    deleteGameRulePresetById: vi.fn(async () => undefined),
  };
};

const roomStore = reactive(createStore()) as ReturnType<typeof createStore>;

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

const buttonByText = (scope: ParentNode, text: string): HTMLButtonElement | undefined =>
  Array.from(scope.querySelectorAll<HTMLButtonElement>("button")).find((button) => button.textContent?.includes(text));

const submitChangedUsername = async (username: string): Promise<void> => {
  const input = document.body.querySelector<HTMLInputElement>("#username-dialog-input");
  if (input) {
    input.value = username;
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }
  await nextTick();
  document.body.querySelector<HTMLFormElement>(".username-dialog__form")
    ?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
};

beforeEach(() => {
  Object.assign(roomStore, createStore());
});

afterEach(() => {
  vi.clearAllMocks();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("RoomView", () => {
  it("shows the profile trigger in lobby and post-game states but hides it while playing", async () => {
    vi.useFakeTimers();
    const lobby = await mountRoomView();
    expect(lobby.root.querySelector(".profile-trigger")).not.toBeNull();
    lobby.app.unmount();
    lobby.root.remove();

    roomStore.remoteRoom.status = "ROOM_STATUS_POST_GAME";
    roomStore.remoteRoom.lastFinishedGameId = "liars-dice";
    const postGame = await mountRoomView();
    expect(postGame.root.querySelector(".profile-trigger")).not.toBeNull();
    postGame.app.unmount();
    postGame.root.remove();

    roomStore.remoteRoom.status = "ROOM_STATUS_PLAYING";
    roomStore.remoteRoom.activeGameId = "liars-dice";
    roomStore.remoteRoom.activeSessionId = "session-1";
    const playing = await mountRoomView();
    expect(playing.root.querySelector(".profile-trigger")).toBeNull();
    playing.app.unmount();
  });

  it("refreshes the current room and both discovery lists after a successful rename", async () => {
    vi.useFakeTimers();
    const { app, root } = await mountRoomView();
    roomStore.loadRoom.mockClear();
    roomStore.loadMyRooms.mockClear();
    roomStore.loadPublicRooms.mockClear();

    root.querySelector<HTMLButtonElement>(".profile-trigger")?.click();
    await nextTick();
    await submitChangedUsername("阿青");

    await vi.waitFor(() => expect(roomStore.changeUsername).toHaveBeenCalledWith("阿青"));
    await vi.waitFor(() => expect(roomStore.loadRoom).toHaveBeenCalledWith("room-1"));
    expect(roomStore.loadMyRooms).toHaveBeenCalledWith(true);
    expect(roomStore.loadPublicRooms).toHaveBeenCalledWith(true);
    expect(root.querySelector(".profile-trigger")?.textContent?.trim()).toBe("阿");

    app.unmount();
  });

  it("keeps the committed username when derived room refreshes fail", async () => {
    vi.useFakeTimers();
    const { app, root } = await mountRoomView();
    roomStore.loadRoom.mockRejectedValueOnce(new Error("房间同步失败"));
    roomStore.loadMyRooms.mockRejectedValueOnce(new Error("我的房间同步失败"));
    roomStore.loadPublicRooms.mockResolvedValueOnce(undefined);

    root.querySelector<HTMLButtonElement>(".profile-trigger")?.click();
    await nextTick();
    await submitChangedUsername("北屿");

    await vi.waitFor(() => expect(root.textContent).toContain("用户名已更新，部分房间信息同步失败"));
    expect(roomStore.displayName).toBe("北屿");
    expect(roomStore.remoteRoom.members[0]?.username).toBe("小满");
    expect(roomStore.changeUsername).toHaveBeenCalledOnce();

    app.unmount();
  });

  it("keeps the original identity and room snapshot when the rename transaction conflicts", async () => {
    vi.useFakeTimers();
    roomStore.changeUsername.mockRejectedValueOnce(
      new ApiError(409, "already_exists", "房间内已有同名玩家", "room.username.taken"),
    );
    const { app, root } = await mountRoomView();
    roomStore.loadRoom.mockClear();
    roomStore.loadMyRooms.mockClear();
    roomStore.loadPublicRooms.mockClear();

    root.querySelector<HTMLButtonElement>(".profile-trigger")?.click();
    await nextTick();
    await submitChangedUsername("阿青");

    await vi.waitFor(() => expect(document.body.textContent).toContain("房间内已有同名玩家"));
    expect(roomStore.displayName).toBe("小满");
    expect(roomStore.remoteRoom.members[0]?.username).toBe("小满");
    expect(roomStore.loadRoom).not.toHaveBeenCalled();
    expect(roomStore.loadMyRooms).not.toHaveBeenCalled();
    expect(roomStore.loadPublicRooms).not.toHaveBeenCalled();
    expect(buttonByText(document.body, "保存用户名")).toBeDefined();

    app.unmount();
  });

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

  it("transfers the host from the lobby roster only after confirmation", async () => {
    vi.useFakeTimers();
    const { app, root } = await mountRoomView();

    root.querySelector<HTMLButtonElement>('.mini-action--transfer[aria-label*="阿青"]')?.click();
    await nextTick();
    expect(roomStore.transferRemoteHost).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("确认转移房主？");

    buttonByText(document.body, "确认转移")?.click();
    await nextTick();
    await Promise.resolve();

    expect(roomStore.transferRemoteHost).toHaveBeenCalledWith("seat-2");
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
