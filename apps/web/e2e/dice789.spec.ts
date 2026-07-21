import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

const fixture = (state = "active") => `/fixtures/dice-789/${state}`;

test("789 portrait table keeps the public pool and complete roll action in one game surface", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture());
  await expect(page.getByTestId("dice-789-screen")).toBeVisible();
  await expect(page.getByRole("region", { name: "共同游戏桌" })).toBeVisible();
  await expect(page.getByRole("img", { name: /公共池 1.25 单位/ })).toBeVisible();
  await expect(page.getByTestId("roll-action")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);

  await page.setViewportSize({ width: 360, height: 740 });
  await expect(page.getByTestId("roll-action")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(360);
});

test("roll, host confirmation, and optional continue remain explicit and pending-safe", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture());
  const roll = page.getByTestId("roll-action");
  await roll.click();
  await expect(roll).toBeDisabled();
  await expect(page.getByText("正在摇骰")).toBeVisible();
  await expect(page.getByText("和为 8 · 半池")).toBeVisible({ timeout: 2_000 });

  await page.getByTestId("confirm-action").click();
  const dialog = page.getByRole("alertdialog", { name: "确认应用本次效果？" });
  await expect(dialog).toContainText("承担 0.75 单位");
  await dialog.getByRole("button", { name: "确认应用" }).click();
  await expect(page.getByRole("region", { name: "本回合操作" }).getByText("选择重摇或过牌")).toBeVisible({ timeout: 2_000 });
  await expect(page.getByTestId("reroll-action")).toBeVisible();
  await expect(page.getByTestId("pass-action")).toBeVisible();
});

test("host dropped-roll reporting requires confirmation and drains the public pool", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture("result-eight"));
  await page.getByLabel("掉骰原因").selectOption("left_table");
  await page.getByTestId("dropped-action").click();
  const dialog = page.getByRole("alertdialog", { name: "确认本次掉骰？" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "确认掉骰" }).click();
  await expect(page.getByRole("img", { name: /公共池 0 单位/ })).toBeVisible({ timeout: 2_000 });
});

test("add control includes the final capacity remainder and survives rotation", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture("add"));
  const output = page.locator(".add-stepper output");
  await expect(output).toHaveText("0.5 单位");
  await page.getByTitle("增加加注").click();
  await expect(output).toHaveText("1 单位");
  await page.getByTitle("增加加注").click();
  await expect(output).toHaveText("1.25 单位");
  await expect(page.getByTitle("增加加注")).toBeDisabled();
  await page.getByTitle("展开操作区").click();
  await page.setViewportSize({ width: 844, height: 390 });
  await expect(output).toHaveText("1.25 单位");
  await expect(page.getByRole("region", { name: "本回合操作" })).toHaveAttribute("data-state", "expanded");
});

test("server-selected priority labels cover special pairs before sums", async ({ page }) => {
  const cases = [
    ["add", "和为 7 · 加注"],
    ["result-eight", "和为 8 · 半池"],
    ["result-nine", "和为 9 · 全池"],
    ["double-four", "双 4 · 半池重摇"],
    ["pair", "普通对子 · 反转"],
    ["target-one", "双 1 · 指定全池"],
    ["target-six", "双 6 · 指定加注"],
  ] as const;
  for (const [state, label] of cases) {
    await page.goto(fixture(state));
    await expect(page.getByText(label, { exact: true })).toBeVisible();
  }
});

test("target selection exposes only server-authorized active players", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture("target-one"));
  const target = page.getByLabel("目标玩家");
  await expect(target.locator("option")).toHaveCount(3);
  await target.selectOption("user-nan");
  await page.getByTestId("target-action").click();
  await expect(page.getByText("正在选择目标")).toBeVisible();
});

test("forced continue modes never render forbidden actions", async ({ page }) => {
  await page.goto(fixture("forced-reroll"));
  await expect(page.getByTestId("reroll-action")).toBeVisible();
  await expect(page.getByTestId("pass-action")).toHaveCount(0);
  await page.goto(fixture("forced-pass"));
  await expect(page.getByTestId("pass-action")).toBeVisible();
  await expect(page.getByTestId("reroll-action")).toHaveCount(0);
});

test("spectator and reconnecting states cannot dispatch gameplay actions", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture("spectator"));
  await expect(page.getByTestId("roll-action")).toHaveCount(0);
  await expect(page.getByText("观战中")).toBeVisible();
  await page.goto(fixture("reconnecting"));
  await expect(page.getByText("重连中")).toBeVisible();
  await expect(page.getByTestId("roll-action")).toBeDisabled();
  await page.getByTitle("立即重连").click();
  await expect(page.getByTestId("roll-action")).toBeEnabled();
});

test("stacked pool and replay remain complete across portrait and landscape", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture("stacked"));
  await expect(page.getByRole("img", { name: /公共池 3.25 单位，2 层/ })).toBeVisible();
  await expect(page.locator(".pool-cup")).toHaveCount(2);

  await page.goto(fixture("replay"));
  await expect(page.getByTestId("dice-789-replay-screen")).toBeVisible();
  await expect(page.getByRole("region", { name: "回合复盘时间线" }).locator("li")).toHaveCount(2);
  await page.getByTitle("下一回合").click();
  await expect(page.getByRole("region", { name: "789 复盘桌面" }).getByText("双 4 · 半池重摇")).toBeVisible();
  await page.setViewportSize({ width: 844, height: 390 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(844);
  await expect(page.getByRole("region", { name: "回合复盘时间线" })).toBeVisible();
  // Landscape compacts the round summary between the top and local seats; any overlap hides review evidence.
  const focusBox = await page.locator(".replay-focus").boundingBox();
  const seatBoxes = await page.locator(".gn-seat").evaluateAll((seats) => seats.map((seat) => {
    const box = seat.getBoundingClientRect();
    return { label: seat.getAttribute("aria-label"), left: box.left, right: box.right, top: box.top, bottom: box.bottom };
  }));
  expect(focusBox).not.toBeNull();
  for (const seatBox of seatBoxes) {
    const overlaps = focusBox !== null
      && focusBox.x < seatBox.right
      && focusBox.x + focusBox.width > seatBox.left
      && focusBox.y < seatBox.bottom
      && focusBox.y + focusBox.height > seatBox.top;
    expect(overlaps, `${seatBox.label ?? "未知座位"} 不得遮挡复盘摘要：${JSON.stringify({ focusBox, seatBox })}`).toBe(false);
  }
});

test("789 themes, reduced motion, and accessibility remain presentation-only", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture("result-eight"));
  await expect(page.locator("html")).toHaveAttribute("data-theme-id", "classic");
  await expect(page.locator(".rolled-dice")).toHaveCSS("animation-name", "none");
  await page.getByTitle("切换桌面主题").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme-id", "stacked");
  await page.getByTitle("静音").click();
  await expect(page.locator("html")).toHaveAttribute("data-muted", "true");
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations).toEqual([]);
});
