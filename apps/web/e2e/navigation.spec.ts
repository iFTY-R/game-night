import { expect, test } from "@playwright/test";

import type { RoomSnapshot } from "../src/api/client";

const roomSnapshot = (roomId: string, roomCode: string): RoomSnapshot => ({
  roomId,
  roomCode,
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_LOBBY",
  hostUserId: "guest-device",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_OPEN",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [{ userId: "guest-device", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 }],
  activeSessionId: "",
  activeGameId: "",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "7", membershipVersion: "3" },
});

test("invite deep link carries the room code through first-time identity setup", async ({ page }) => {
  await page.goto("/invite/N789");

  await expect(page.getByRole("heading", { name: "先设置你的用户名" })).toBeVisible();
  await page.getByRole("textbox", { name: "用户名" }).fill("小满");
  await page.getByRole("button", { name: "继续" }).click();

  await expect(page).toHaveURL(/\/room\/n789$/);
  await expect(page.getByText("N789", { exact: true })).toBeVisible();
});

test("recognized device follows an invite without reopening the identity form", async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem(
      "game-night.room-context.v1",
      JSON.stringify({ schemaVersion: 1, displayName: "阿青", userId: "guest-device", roomId: null, roomCode: null, sessionId: null }),
    );
  });
  await page.goto("/invite/ROOM42");

  await expect(page).toHaveURL(/\/room\/room42$/);
  await expect(page.getByText("ROOM42", { exact: true })).toBeVisible();
  await expect(page.locator("article").filter({ hasText: "本机" }).locator("strong")).toHaveText("阿青");
});

test("switching invites never sends the previous room version", async ({ page }) => {
  const firstRoomId = "00000000-0000-4000-8000-000000000021";
  const nextRoomId = "00000000-0000-4000-8000-000000000022";
  await page.addInitScript(({ storedRoomId }) => {
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "阿青",
      userId: "guest-device",
      roomId: storedRoomId,
      roomCode: "ROOMA",
      sessionId: null,
    }));
  }, { storedRoomId: firstRoomId });
  await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: "guest-device", status: "USER_STATUS_ACTIVE", username: "阿青" } }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot(firstRoomId, "ROOMA") }) });
  });
  let joinBody: Record<string, unknown> | undefined;
  await page.route("**/platform.room.v1.RoomService/JoinRoom", async (route) => {
    joinBody = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot(nextRoomId, "ROOMB") }) });
  });

  await page.goto(`/room/${firstRoomId}`);
  await expect(page.getByText("ROOMA", { exact: true })).toBeVisible();
  await page.evaluate(() => {
    history.pushState({}, "", "/invite/ROOMB");
    dispatchEvent(new PopStateEvent("popstate"));
  });

  await expect(page).toHaveURL(new RegExp(`/room/${nextRoomId}$`));
  await expect.poll(() => joinBody).toMatchObject({ roomCode: "ROOMB", intent: "JOIN_INTENT_PARTICIPANT" });
  expect(joinBody).not.toHaveProperty("expectedVersion");
});

test("home prioritizes the actor's hosted room and restores it", async ({ page }) => {
  const hostedRoomId = "00000000-0000-4000-8000-000000000031";
  const joinedRoomId = "00000000-0000-4000-8000-000000000032";
  await seedRecognizedDevice(page);
  await routeRecognizedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/ListMyRooms", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        rooms: [
          myRoomCard(hostedRoomId, "MINE01", true, "阿青"),
          myRoomCard(joinedRoomId, "JOIN01", false, "小满"),
        ],
        page: {},
      }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/ListPublicRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot(hostedRoomId, "MINE01") }) });
  });

  await page.goto("/");

  const roomButtons = page.locator(".my-room-card");
  await expect(roomButtons).toHaveCount(2);
  await expect(roomButtons.first()).toContainText("我的房间");
  await expect(roomButtons.first()).toContainText("MINE01");
  await roomButtons.first().click();
  await expect(page).toHaveURL(new RegExp(`/room/${hostedRoomId}$`));
});

test("public lobby joins through the server-projected primary action", async ({ page }) => {
  const publicRoomId = "00000000-0000-4000-8000-000000000033";
  await seedRecognizedDevice(page);
  await routeRecognizedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/ListMyRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  await page.route("**/platform.room.v1.RoomService/ListPublicRooms", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ rooms: [{
        roomId: publicRoomId,
        hostUsername: "南风",
        status: "ROOM_STATUS_LOBBY",
        participantCapacity: 8,
        participantCount: 2,
        spectatorCount: 0,
        waitingCount: 0,
        participantAdmission: "ADMISSION_MODE_OPEN",
        spectatorAdmission: "ADMISSION_MODE_OPEN",
        activeGameId: "",
        viewerRole: "MEMBER_ROLE_UNSPECIFIED",
        viewerRequestedRole: "MEMBER_ROLE_UNSPECIFIED",
        primaryAction: "PUBLIC_ROOM_PRIMARY_ACTION_JOIN",
      }], page: {} }),
    });
  });
  let joinBody: Record<string, unknown> | undefined;
  await page.route("**/platform.room.v1.RoomService/JoinRoom", async (route) => {
    joinBody = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot(publicRoomId, "OPEN01") }) });
  });

  await page.goto("/");
  await page.getByRole("button", { name: "加入房间" }).click();

  await expect.poll(() => joinBody).toMatchObject({ roomId: publicRoomId, intent: "JOIN_INTENT_PARTICIPANT" });
  await expect(page).toHaveURL(new RegExp(`/room/${publicRoomId}$`));
});

test("creating a public room preserves the selected game", async ({ page }) => {
  const createdRoomId = "00000000-0000-4000-8000-000000000034";
  await seedRecognizedDevice(page);
  await routeRecognizedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/ListMyRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  await page.route("**/platform.room.v1.RoomService/ListPublicRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  let createBody: Record<string, unknown> | undefined;
  await page.route("**/platform.room.v1.RoomService/CreateRoom", async (route) => {
    createBody = route.request().postDataJSON() as Record<string, unknown>;
    const created = { ...roomSnapshot(createdRoomId, "PUB789"), visibility: "ROOM_VISIBILITY_PUBLIC" };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: created }) });
  });

  await page.goto("/");
  await page.getByRole("button", { name: /789.*两颗骰子/ }).click();
  await page.getByRole("button", { name: "公开大厅" }).click();
  await page.getByRole("button", { name: "创建789房间" }).click();

  await expect.poll(() => createBody).toMatchObject({ visibility: "ROOM_VISIBILITY_PUBLIC" });
  await expect(page).toHaveURL(new RegExp(`/room/${createdRoomId}\\?game=dice-789$`));
  await expect(page.getByRole("button", { name: /789.*两颗骰子/ })).toHaveAttribute("aria-pressed", "true");
});

const seedRecognizedDevice = async (page: import("@playwright/test").Page): Promise<void> => {
  await page.addInitScript(() => {
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "阿青",
      userId: "guest-device",
      roomId: null,
      roomCode: null,
      sessionId: null,
    }));
  });
};

const routeRecognizedIdentity = async (page: import("@playwright/test").Page): Promise<void> => {
  await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: "guest-device", status: "USER_STATUS_ACTIVE", username: "阿青" } }),
    });
  });
};

const myRoomCard = (roomId: string, roomCode: string, isHost: boolean, hostUsername: string) => ({
  roomId,
  roomCode,
  visibility: "ROOM_VISIBILITY_PRIVATE",
  hostUsername,
  status: "ROOM_STATUS_LOBBY",
  isHost,
  participantCapacity: 8,
  participantCount: 1,
  spectatorCount: 0,
  waitingCount: 0,
  participantAdmission: "ADMISSION_MODE_OPEN",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  activeGameId: "",
  lastFinishedGameId: "",
  viewerRole: "MEMBER_ROLE_PARTICIPANT",
  viewerRequestedRole: "MEMBER_ROLE_UNSPECIFIED",
});
