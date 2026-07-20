import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

const tableUrl = "/fixtures/table";

test("portrait table is complete at 390x844", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);

  await expect(page.getByTestId("game-screen")).toBeVisible();
  await expect(page.getByRole("region", { name: "共同游戏桌" })).toBeVisible();
  await expect(page.getByRole("region", { name: "本轮操作" })).toHaveAttribute("data-state", "compact");
  await expect(page.locator("body")).toHaveCSS("overflow-x", "visible");
});

test("small Android portrait keeps actions reachable", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  await page.goto(tableUrl);
  await expect(page.getByTestId("roll-action")).toBeVisible();
  await expect(page.getByTestId("challenge-action")).toBeVisible();
});

test("rotation preserves tray and action state", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);
  const tray = page.getByRole("region", { name: "本轮操作" });
  await page.getByTitle("展开操作区").click();
  await expect(tray).toHaveAttribute("data-state", "expanded");

  await page.setViewportSize({ width: 844, height: 390 });
  await expect(tray).toHaveAttribute("data-state", "expanded");
  await expect(page.getByText("上次叫骰")).toBeVisible();
});

test("tray drag collapses and click expands it", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);
  const tray = page.getByRole("region", { name: "本轮操作" });
  const handle = page.getByTitle("展开操作区");
  const box = await handle.boundingBox();
  expect(box).not.toBeNull();
  if (box === null) return;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height + 60);
  await page.mouse.up();
  await expect(tray).toHaveAttribute("data-state", "collapsed");
  await page.getByTitle("展开操作区").click();
  await expect(tray).toHaveAttribute("data-state", "compact");
});

test("pending action locks duplicate submission", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);
  const action = page.getByTestId("roll-action");
  await action.click();
  await expect(action).toBeDisabled();
  await expect(page.getByText("正在提交：叫 7 个 4")).toBeVisible();
  await expect(page.getByText("叫 7 个 4已提交")).toBeVisible({ timeout: 2_000 });
});

test("built-in theme fallback remains usable and accessible", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);
  await expect(page.locator("html")).toHaveAttribute("data-theme-id", "safe-table");
  await expect(page.locator("html")).toHaveAttribute("data-theme-fallback", "true");
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations).toEqual([]);
});
