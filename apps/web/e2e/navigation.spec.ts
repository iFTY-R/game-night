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
