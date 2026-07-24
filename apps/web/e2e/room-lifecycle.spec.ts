import { expect, test } from "@playwright/test";

import type { ReplayAccessWire, RoomSnapshot } from "../src/api/client";

// Stable IDs keep every mocked CAS transition and final route assertion deterministic.
const roomId = "00000000-0000-4000-8000-000000000061";
const roomCode = "POST79";
const hostUserId = "00000000-0000-4000-8000-000000000062";
const guestUserId = "00000000-0000-4000-8000-000000000063";
const lastFinishedSessionId = "00000000-0000-4000-8000-000000000064";
const nextSessionId = "00000000-0000-4000-8000-000000000065";
const dice789Revision = "11";

/** Supplies a server-owned draft whose revision matters to this lifecycle test; rule encoding is covered separately. */
const emptyConfig = (gameId: string) => ({
  gameId,
  version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
  schemaVersion: 1,
  messageType: "session.config",
  payload: "",
});

const roomSnapshot = (): RoomSnapshot => ({
  roomId,
  roomCode,
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_POST_GAME",
  hostUserId,
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_CLOSED",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [
    { userId: hostUserId, username: "小满", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 },
    { userId: guestUserId, username: "阿青", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 1 },
  ],
  activeSessionId: "",
  activeGameId: "",
  lastFinishedSessionId,
  lastFinishedGameId: "liars-dice",
  selectedGameId: "",
  gameConfigDrafts: [
    { gameId: "liars-dice", revision: "5", config: emptyConfig("liars-dice"), updatedBy: hostUserId },
  ],
  ownershipEpoch: "3",
  version: { roomVersion: "8", membershipVersion: "4" },
});

const replayAccess: ReplayAccessWire = {
  roomId,
  sessionId: lastFinishedSessionId,
  policy: "REPLAY_ACCESS_POLICY_ROOM_MEMBER",
  policyVersion: "2",
};

test("post-game host reopens admission, switches to 789, and auto-starts the next session with the selected config revision", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.addInitScript(({ storedRoomId, storedRoomCode, userId }) => {
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "本地主机名",
      userId,
      roomId: storedRoomId,
      roomCode: storedRoomCode,
      sessionId: null,
    }));
  }, { storedRoomId: roomId, storedRoomCode: roomCode, userId: hostUserId });

  let currentRoom = roomSnapshot();
  let setAdmissionBody: Record<string, unknown> | undefined;
  let selectGameBody: Record<string, unknown> | undefined;
  let beginStartBody: Record<string, unknown> | undefined;
  let startGameBody: Record<string, unknown> | undefined;
  let presetRequests = 0;

  await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: hostUserId, status: "USER_STATUS_ACTIVE", username: "小满" } }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: currentRoom }) });
  });
  await page.route("**/platform.room.v1.RoomService/HeartbeatRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ observedAt: "2026-07-23T12:00:00Z" }) });
  });
  await page.route("**/platform.game.v1.GameService/GetReplayAccess", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ access: replayAccess }) });
  });
  await page.route("**/platform.room.v1.RoomService/ListGameRulePresets", async (route) => {
    presetRequests += 1;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ presets: [] }) });
  });
  await page.route("**/platform.room.v1.RoomService/SetAdmission", async (route) => {
    setAdmissionBody = route.request().postDataJSON() as Record<string, unknown>;
    currentRoom = {
      ...currentRoom,
      participantAdmission: "ADMISSION_MODE_OPEN",
      version: { roomVersion: "9", membershipVersion: "4" },
    };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: currentRoom }) });
  });
  await page.route("**/platform.room.v1.RoomService/SelectRoomGame", async (route) => {
    selectGameBody = route.request().postDataJSON() as Record<string, unknown>;
    currentRoom = {
      ...currentRoom,
      selectedGameId: "dice-789",
      gameConfigDrafts: [
        ...currentRoom.gameConfigDrafts.filter((draft) => draft.gameId !== "dice-789"),
        { gameId: "dice-789", revision: dice789Revision, config: emptyConfig("dice-789"), updatedBy: hostUserId },
      ],
      ownershipEpoch: "4",
      version: { roomVersion: "10", membershipVersion: "4" },
    };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: currentRoom }) });
  });
  await page.route("**/platform.room.v1.RoomService/BeginGameStart", async (route) => {
    beginStartBody = route.request().postDataJSON() as Record<string, unknown>;
    currentRoom = {
      ...currentRoom,
      pendingStart: {
        pendingStartId: "pending-start-1",
        cancelToken: "cancel-token-1",
        deadline: new Date(Date.now() + 900).toISOString(),
        gameId: "dice-789",
        configRevision: dice789Revision,
        expectedVersion: { roomVersion: "10", membershipVersion: "4" },
        ownershipEpoch: "4",
      },
      version: { roomVersion: "11", membershipVersion: "4" },
    };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ room: currentRoom, pendingStart: currentRoom.pendingStart }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/StartGame", async (route) => {
    startGameBody = route.request().postDataJSON() as Record<string, unknown>;
    currentRoom = {
      ...currentRoom,
      status: "ROOM_STATUS_PLAYING",
      activeSessionId: nextSessionId,
      activeGameId: "dice-789",
      pendingStart: undefined,
      version: { roomVersion: "12", membershipVersion: "4" },
    };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        room: currentRoom,
        sessionId: nextSessionId,
        gameId: "dice-789",
        configRevision: dice789Revision,
      }),
    });
  });
  await page.route("**/platform.game.v1.GameService/GetProjection", async (route) => {
    await route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ code: "not_found", message: "projection unavailable in this mock" }),
    });
  });
  await page.route("**/platform.game.v1.GameService/OpenSubscription", async (route) => {
    await route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ code: "not_found", message: "subscription unavailable in this mock" }),
    });
  });

  await page.goto(`/room/${roomId}`);

  await expect(page.getByRole("heading", { name: "要不要再开一局？" })).toBeVisible();
  await expect(page.getByText("暂停进房", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "新玩家进入等候区" })).toBeVisible();

  await page.getByRole("button", { name: "新玩家进入等候区" }).click();
  await expect.poll(() => setAdmissionBody).toMatchObject({
    roomId,
    participantAdmission: "ADMISSION_MODE_OPEN",
    spectatorAdmission: "ADMISSION_MODE_OPEN",
    expectedVersion: { roomVersion: "8", membershipVersion: "4" },
  });
  await expect(page.getByText("允许进房", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "开放下一局加入" })).toBeVisible();

  await page.getByRole("button", { name: /查看吹牛骰子完整规则/ }).click();
  const dice789Button = page.getByRole("button", { name: /789.*两颗骰子/ });
  await expect(dice789Button).toBeVisible();
  await dice789Button.click();

  await expect.poll(() => selectGameBody).toMatchObject({
    roomId,
    gameId: "dice-789",
    expectedVersion: { roomVersion: "9", membershipVersion: "4" },
    ownershipEpoch: "3",
  });
  await expect(dice789Button).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText("规则 #11")).toBeVisible();
  await page.getByRole("button", { name: "收起操作区" }).click();

  await page.getByRole("button", { name: "准备再开一局" }).click();
  await expect.poll(() => beginStartBody).toMatchObject({
    roomId,
    gameId: "dice-789",
    configRevision: dice789Revision,
    expectedVersion: { roomVersion: "10", membershipVersion: "4" },
    ownershipEpoch: "4",
  });
  await expect(page.getByRole("status")).toContainText("789 正在倒计时开局");

  await expect.poll(() => startGameBody, { timeout: 8_000 }).toMatchObject({
    roomId,
    gameId: "dice-789",
    configRevision: dice789Revision,
    expectedVersion: { roomVersion: "11", membershipVersion: "4" },
    pendingStartId: "pending-start-1",
    cancelToken: "cancel-token-1",
    ownershipEpoch: "4",
  });
  await expect(page).toHaveURL(new RegExp(`/room/${roomId}/game/${nextSessionId}$`));
  expect(presetRequests).toBeGreaterThan(0);
});
