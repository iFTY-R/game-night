import { expect, test, type Page } from "@playwright/test";

import type { RoomSnapshot } from "../src/api/client";

const roomId = "00000000-0000-4000-8000-000000000011";
const hostUserId = "user-self";
const targetUserId = "user-target";

const roomSnapshot = (host = hostUserId): RoomSnapshot => ({
  roomId,
  roomCode: "GOV123",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_LOBBY",
  hostUserId: host,
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_OPEN",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [
    { userId: hostUserId, role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 },
    { userId: targetUserId, role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 1 },
  ],
  activeSessionId: "",
  activeGameId: "",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "2", membershipVersion: "1" },
});

/** Seeds a recognized device so room governance can be tested without onboarding noise. */
const seedIdentity = async (page: Page): Promise<void> => {
  await page.addInitScript(({ storedRoomId, userId }) => {
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "房主",
      userId,
      roomId: storedRoomId,
      roomCode: "GOV123",
      sessionId: null,
    }));
  }, { storedRoomId: roomId, userId: hostUserId });
};

test("host confirms member removal and idle-room closure on mobile", async ({ page }) => {
  await seedIdentity(page);
  await page.setViewportSize({ width: 390, height: 844 });
  let currentRoom = roomSnapshot();
  let removalBody: Record<string, unknown> | undefined;
  let closeBody: Record<string, unknown> | undefined;
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: currentRoom }) });
  });
  await page.route("**/platform.room.v1.RoomService/RemoveMember", async (route) => {
    removalBody = route.request().postDataJSON() as Record<string, unknown>;
    currentRoom = {
      ...currentRoom,
      members: currentRoom.members.filter((member) => member.userId !== targetUserId),
      version: { roomVersion: "3", membershipVersion: "2" },
    };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: currentRoom }) });
  });
  await page.route("**/platform.room.v1.RoomService/CloseRoom", async (route) => {
    closeBody = route.request().postDataJSON() as Record<string, unknown>;
    currentRoom = { ...currentRoom, status: "ROOM_STATUS_CLOSED", version: { roomVersion: "4", membershipVersion: "2" } };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: currentRoom }) });
  });

  await page.goto(`/room/${roomId}`);
  const remove = page.getByRole("button", { name: "移出 玩家 user-t" });
  await remove.click();
  await expect(page.getByRole("dialog", { name: "确认移出成员？" })).toBeVisible();
  expect(removalBody).toBeUndefined();
  await page.getByRole("button", { name: "取消" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(removalBody).toBeUndefined();

  await remove.click();
  await page.getByRole("button", { name: "确认移出" }).click();
  await expect.poll(() => removalBody).toMatchObject({
    roomId,
    userId: targetUserId,
    expectedVersion: { roomVersion: "2", membershipVersion: "1" },
  });
  await expect(page.getByRole("button", { name: "移出 玩家 user-t" })).toHaveCount(0);

  await page.getByRole("button", { name: "解散房间" }).click();
  await expect(page.getByRole("dialog", { name: "确认解散房间？" })).toBeVisible();
  expect(closeBody).toBeUndefined();
  await page.getByRole("button", { name: "确认解散" }).click();

  await expect.poll(() => closeBody).toMatchObject({
    roomId,
    expectedVersion: { roomVersion: "3", membershipVersion: "2" },
  });
  await expect(page).toHaveURL(/\/$/);
  await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem("game-night.room-context.v1") ?? "{}") as { roomId?: unknown })).toMatchObject({ roomId: null });
});

test("non-host members never receive governance controls", async ({ page }) => {
  await seedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot("another-host") }) });
  });

  await page.goto(`/room/${roomId}`);

  await expect(page.getByRole("button", { name: /移出/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "解散房间" })).toHaveCount(0);
});

test("governance conflicts stay in context and refresh the authoritative room", async ({ page }) => {
  await seedIdentity(page);
  let currentRoom = roomSnapshot();
  let roomReads = 0;
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    roomReads += 1;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: currentRoom }) });
  });
  await page.route("**/platform.room.v1.RoomService/RemoveMember", async (route) => {
    currentRoom = { ...currentRoom, version: { roomVersion: "3", membershipVersion: "2" } };
    await route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ code: "aborted", message: "房间状态已更新" }) });
  });

  await page.goto(`/room/${roomId}`);
  await page.getByRole("button", { name: "移出 玩家 user-t" }).click();
  await page.getByRole("button", { name: "确认移出" }).click();

  const dialog = page.getByRole("dialog", { name: "确认移出成员？" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("alert")).toHaveText("房间状态已更新");
  await expect.poll(() => roomReads).toBeGreaterThanOrEqual(2);
  await expect(page.getByRole("button", { name: "移出 玩家 user-t" })).toBeVisible();
});
