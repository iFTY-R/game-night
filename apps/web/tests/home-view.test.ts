import { createApp, nextTick, reactive } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../src/api/client";
import HomeView from "../src/views/HomeView.vue";

const routerPush = vi.fn();
let roomStore: ReturnType<typeof createStore>;

vi.mock("vue-router", () => ({
  useRouter: () => ({ push: routerPush }),
}));

vi.mock("../src/stores/room", () => ({
  useRoomStore: () => roomStore,
}));

const createRoomSnapshot = () => ({
  roomId: "room-1",
  roomCode: "ABCD12",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_LOBBY",
  hostUserId: "host-user",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_OPEN",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [],
  activeSessionId: "",
  activeGameId: "",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "1", membershipVersion: "1" },
});

const createStore = () => ({
  userId: "user-1",
  displayName: "小满",
  hasIdentity: true,
  identityState: "active",
  busy: false,
  notice: "",
  myRoomsLoading: false,
  myRooms: [] as Array<Record<string, unknown>>,
  myRoomsNextPageToken: "",
  publicRoomsLoading: false,
  publicRooms: [] as Array<Record<string, unknown>>,
  publicRoomsNextPageToken: "",
  clearNotice: vi.fn(),
  recoverIdentity: vi.fn(async () => undefined),
  ensureIdentity: vi.fn(async () => undefined),
  changeUsername: vi.fn(async (username: string) => {
    roomStore.displayName = username;
  }),
  joinRemote: vi.fn(async () => createRoomSnapshot()),
  joinPublicRemote: vi.fn(async () => createRoomSnapshot()),
  createRemoteRoom: vi.fn(async () => createRoomSnapshot()),
  selectRemoteGame: vi.fn(async () => createRoomSnapshot()),
  loadRoom: vi.fn(async () => createRoomSnapshot()),
  loadMyRooms: vi.fn(async () => undefined),
  loadPublicRooms: vi.fn(async () => undefined),
  enterRoom: vi.fn(),
  setRemoteRoom: vi.fn(),
});

const mountHomeView = async (inviteCode = "") => {
  const root = document.createElement("div");
  document.body.appendChild(root);
  const app = createApp(HomeView, { inviteCode });
  app.component("RouterLink", { template: "<a><slot /></a>" });
  app.mount(root);
  await vi.waitFor(() => expect(roomStore.recoverIdentity).toHaveBeenCalledOnce());
  await nextTick();
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
  roomStore = reactive(createStore()) as ReturnType<typeof createStore>;
  routerPush.mockReset();
});

afterEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
});

describe("HomeView profile and room-entry recovery", () => {
  it("opens room visibility from the selected game's create action", async () => {
    const { app, root } = await mountHomeView();

    root.querySelector<HTMLButtonElement>('[aria-label="创建789房间"]')?.click();
    await nextTick();
    expect(roomStore.createRemoteRoom).not.toHaveBeenCalled();
    expect(document.body.querySelector(".create-room-dialog")?.hasAttribute("open")).toBe(true);
    expect(document.body.querySelector(".create-room-dialog")?.textContent).toContain("789");

    buttonByText(document.body, "公开房间")?.click();
    await vi.waitFor(() => expect(roomStore.createRemoteRoom).toHaveBeenCalledWith("ROOM_VISIBILITY_PUBLIC"));
    expect(roomStore.selectRemoteGame).toHaveBeenCalledWith("dice-789");

    app.unmount();
  });

  it("limits onboarding username input to four characters", async () => {
    Object.assign(roomStore, { userId: "", displayName: "", hasIdentity: false, identityState: "anonymous" });
    const { app, root } = await mountHomeView();

    expect(root.querySelector<HTMLInputElement>("#display-name")?.maxLength).toBe(4);

    app.unmount();
  });

  it("shows the profile trigger and refreshes both room lists after a normal rename", async () => {
    const { app, root } = await mountHomeView();
    roomStore.loadMyRooms.mockClear();
    roomStore.loadPublicRooms.mockClear();

    root.querySelector<HTMLButtonElement>(".profile-trigger")?.click();
    await nextTick();
    expect(document.body.textContent).toContain("当前用户名：小满");

    await submitChangedUsername("阿青");
    await vi.waitFor(() => expect(roomStore.changeUsername).toHaveBeenCalledWith("阿青"));
    await vi.waitFor(() => expect(roomStore.loadMyRooms).toHaveBeenCalledOnce());
    expect(roomStore.loadPublicRooms).toHaveBeenCalledOnce();
    expect(roomStore.joinRemote).not.toHaveBeenCalled();
    expect(root.querySelector(".profile-trigger")?.textContent?.trim()).toBe("阿");

    app.unmount();
  });

  it("keeps a room-code conflict actionable and performs only one retry for one rename", async () => {
    roomStore.joinRemote
      .mockRejectedValueOnce(new ApiError(409, "already_exists", "房间内已有同名玩家", "room.username.taken"))
      .mockRejectedValueOnce(new ApiError(409, "already_exists", "房间内已有同名玩家", "room.username.taken"));
    const { app, root } = await mountHomeView();
    const input = root.querySelector<HTMLInputElement>("#room-code");
    if (input) {
      input.value = "ABCD12";
      input.dispatchEvent(new Event("input", { bubbles: true }));
    }
    input?.closest("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await vi.waitFor(() => expect(root.textContent).toContain("房间内已有同名玩家"));
    const conflictButton = buttonByText(root, "修改用户名后重试");
    await vi.waitFor(() => expect(document.activeElement).toBe(conflictButton));
    conflictButton?.click();
    await nextTick();
    await submitChangedUsername("阿青");
    await vi.waitFor(() => expect(roomStore.joinRemote).toHaveBeenCalledTimes(2));
    await vi.waitFor(() => expect(root.textContent).toContain("修改用户名后重试"));

    expect(roomStore.joinRemote).toHaveBeenNthCalledWith(1, "ABCD12");
    expect(roomStore.joinRemote).toHaveBeenNthCalledWith(2, "ABCD12");
    expect(routerPush).not.toHaveBeenCalled();

    app.unmount();
  });

  it("recovers an invite-link conflict after rename and enters the authoritative room", async () => {
    const joined = createRoomSnapshot();
    roomStore.joinRemote
      .mockRejectedValueOnce(new ApiError(409, "already_exists", "房间内已有同名玩家", "room.username.taken"))
      .mockResolvedValueOnce(joined);
    const { app, root } = await mountHomeView("ABCD12");
    await vi.waitFor(() => expect(root.textContent).toContain("房间内已有同名玩家"));

    buttonByText(root, "修改用户名后重试")?.click();
    await nextTick();
    await submitChangedUsername("北屿");
    await vi.waitFor(() => expect(routerPush).toHaveBeenCalledWith({ name: "room", params: { roomId: "room-1" } }));

    expect(roomStore.joinRemote).toHaveBeenCalledTimes(2);
    expect(roomStore.enterRoom).toHaveBeenCalledWith("room-1", "ABCD12");
    expect(roomStore.setRemoteRoom).toHaveBeenCalledWith(joined);

    app.unmount();
  });

  it("uses the same conflict recovery for public rooms and cancel does not retry", async () => {
    roomStore.publicRooms = [{
      roomId: "public-room",
      hostUsername: "阿青",
      status: "ROOM_STATUS_LOBBY",
      activeGameId: "liars-dice",
      participantCount: 2,
      participantCapacity: 8,
      primaryAction: "PUBLIC_ROOM_PRIMARY_ACTION_JOIN",
    }];
    roomStore.joinPublicRemote.mockRejectedValueOnce(
      new ApiError(409, "already_exists", "房间内已有同名玩家", "room.username.taken"),
    );
    const { app, root } = await mountHomeView();

    buttonByText(root, "加入房间")?.click();
    await vi.waitFor(() => expect(root.textContent).toContain("房间内已有同名玩家"));
    buttonByText(root, "修改用户名后重试")?.click();
    await nextTick();
    buttonByText(document.body, "取消")?.click();
    await nextTick();

    expect(roomStore.joinPublicRemote).toHaveBeenCalledOnce();
    expect(roomStore.joinPublicRemote).toHaveBeenCalledWith("public-room", "JOIN_INTENT_PARTICIPANT");
    expect(root.textContent).toContain("修改用户名后重试");

    app.unmount();
  });
});
