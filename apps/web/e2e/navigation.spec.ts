import { expect, test } from "@playwright/test";

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
