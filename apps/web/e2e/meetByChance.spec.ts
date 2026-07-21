import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

const fixture = (state = "active") => `/fixtures/meet-by-chance/${state}`;

test("portrait table keeps every public hand, target, and decision together", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture());
  await expect(page.getByTestId("meet-by-chance-screen")).toBeVisible();
  await expect(page.getByRole("region", { name: "喜相逢共同桌面" })).toBeVisible();
  await expect(page.locator(".meet-seat")).toHaveCount(4);
  await expect(page.locator(".meet-seat .meet-die")).toHaveCount(12);
  await expect(page.getByLabel(/你.*当前靶子/)).toBeVisible();
  await expect(page.getByTestId("reroll-action")).toBeVisible();
  await expect(page.getByTestId("stand-action")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);

  await page.setViewportSize({ width: 360, height: 740 });
  await expect(page.locator(".meet-seat .meet-die")).toHaveCount(12);
  await expect(page.getByTestId("reroll-action")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(360);
});

test("235 outcome text and target come only from the server projection", async ({ page }) => {
  await page.goto(fixture("special-235-leopards"));
  await expect(page.locator(".special-note")).toContainText("235 克制全场豹子");
  await expect(page.getByLabel(/南风.*当前靶子/)).toBeVisible();

  await page.goto(fixture("special-235-minimum"));
  await expect(page.locator(".special-note")).toContainText("235 按全场最小散骰");
  await expect(page.getByLabel(/你.*当前靶子/)).toBeVisible();
});

test("automatic exact, high, low, and capped batches never expose a challenge action", async ({ page }) => {
  await page.goto(fixture("match-exact"));
  await expect(page.getByText("完全同牌", { exact: true }).first()).toBeVisible();
  await expect(page.getByText(/阿青、小满/)).toBeVisible();
  await expect(page.getByText("挑战", { exact: true })).toHaveCount(0);

  await page.goto(fixture("match-high-low"));
  await expect(page.getByText(/多人同大 \+ 多人同小/)).toBeVisible();
  await expect(page.getByText(/南风 额外 0.5 单位/)).toBeVisible();

  await page.goto(fixture("match-capped"));
  await expect(page.getByText("同牌解析达到上限 3", { exact: true })).toBeVisible();
  await expect(page.getByTestId("reroll-action")).toBeVisible();
});

test("reroll and stand require confirmation and lock duplicate submissions", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture());
  await page.getByTestId("reroll-action").click();
  const rerollDialog = page.getByRole("alertdialog", { name: "确认重摇？" });
  await expect(rerollDialog).toContainText("增加 0.5 单位");
  await rerollDialog.getByRole("button", { name: "确认重摇" }).click();
  await expect(page.getByText("正在提交重摇")).toBeVisible();
  await expect(page.getByTestId("reroll-action")).toBeDisabled();
  await expect(page.getByText(/重摇 1 \/ 2/).first()).toBeVisible({ timeout: 2_000 });

  await page.goto(fixture());
  await page.getByTestId("stand-action").click();
  const standDialog = page.getByRole("alertdialog", { name: "确认结束本轮？" });
  await expect(standDialog).toContainText("立即结算");
  await standDialog.getByRole("button", { name: "确认结束" }).click();
  await expect(page.getByText("第 13 轮 · MEET")).toBeVisible({ timeout: 2_000 });
  await expect(page.getByText("阿青", { exact: true }).last()).toBeVisible();
});

test("target history, reroll cap, and timeout settlement remain explicit", async ({ page }) => {
  await page.goto(fixture("target-transferred"));
  await expect(page.getByLabel(/阿青.*本轮曾为靶子/)).toBeVisible();
  await expect(page.getByLabel(/你.*当前靶子/)).toBeVisible();
  await expect(page.getByText(/重摇 1 \/ 2/).first()).toBeVisible();

  await page.goto(fixture("reroll-limit"));
  await expect(page.getByTestId("reroll-action")).toHaveCount(0);
  await expect(page.getByTestId("stand-action")).toBeVisible();

  await page.goto(fixture("timeout"));
  await page.getByTitle("展开操作区").click();
  await expect(page.getByText(/你 · 超时结束本轮/)).toBeVisible();
  await expect(page.getByText("第 13 轮 · MEET")).toBeVisible();
});

test("reconnect and spectator views retain public hands without action authority", async ({ page }) => {
  await page.goto(fixture("spectator"));
  await expect(page.locator(".meet-seat .meet-die")).toHaveCount(12);
  await expect(page.getByText("观战中")).toBeVisible();
  await expect(page.getByTestId("reroll-action")).toHaveCount(0);

  await page.goto(fixture("reconnecting"));
  await expect(page.getByText("重连中")).toBeVisible();
  await expect(page.getByTestId("reroll-action")).toBeDisabled();
  await page.getByTitle("立即重连").click();
  await expect(page.getByTestId("reroll-action")).toBeEnabled();
});

test("expanded tray and pending confirmation survive rotation without submitting", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture());
  await page.getByTitle("展开操作区").click();
  await page.getByTestId("reroll-action").click();
  await page.setViewportSize({ width: 844, height: 390 });
  await expect(page.getByRole("region", { name: "靶子操作" })).toHaveAttribute("data-state", "expanded");
  await expect(page.getByRole("alertdialog", { name: "确认重摇？" })).toBeVisible();
  await expect(page.getByText("正在提交重摇")).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(844);
  const localSeatBox = await page.getByLabel(/你.*当前靶子/).boundingBox();
  const trayHandleBox = await page.getByTitle("收起操作区").boundingBox();
  expect(localSeatBox).not.toBeNull();
  expect(trayHandleBox).not.toBeNull();
  expect((localSeatBox?.y ?? 0) + (localSeatBox?.height ?? 0)).toBeLessThanOrEqual(trayHandleBox?.y ?? 0);
});

test("replay preserves ordered public events and remains separated in landscape", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture("replay"));
  await expect(page.getByTestId("meet-by-chance-replay-screen")).toBeVisible();
  await expect(page.getByRole("region", { name: "完整事件链" }).locator("li")).toHaveCount(9);
  await expect(page.getByRole("region", { name: "完整事件链" }).getByText(/235 克制全场豹子/).first()).toBeVisible();
  await page.getByTitle("下一轮").click();
  await expect(page.getByRole("region", { name: "完整事件链" }).locator("li")).toHaveCount(6);
  await expect(page.getByText(/完全同牌 阿青、小满/)).toBeVisible();
  await expect(page.getByTestId("reroll-action")).toHaveCount(0);

  await page.setViewportSize({ width: 844, height: 390 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(844);
  const focusBox = await page.locator(".replay-focus").boundingBox();
  const dockBox = await page.getByRole("region", { name: "完整事件链" }).boundingBox();
  const seatBoxes = await page.locator(".meet-seat").evaluateAll((seats) => seats.map((seat) => {
    const box = seat.getBoundingClientRect();
    return { label: seat.getAttribute("aria-label"), left: box.left, right: box.right, top: box.top, bottom: box.bottom };
  }));
  expect(focusBox).not.toBeNull();
  expect(dockBox).not.toBeNull();
  for (const seatBox of seatBoxes) {
    const focusOverlap = focusBox !== null && focusBox.x < seatBox.right && focusBox.x + focusBox.width > seatBox.left && focusBox.y < seatBox.bottom && focusBox.y + focusBox.height > seatBox.top;
    const dockOverlap = dockBox !== null && dockBox.y < seatBox.bottom;
    expect(focusOverlap, `${seatBox.label ?? "未知座位"} 不得遮挡复盘摘要`).toBe(false);
    expect(dockOverlap, `${seatBox.label ?? "未知座位"} 不得进入事件栏`).toBe(false);
  }
});

test("themes, reduced motion, mute, and accessibility remain presentation-only", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture("match-high-low"));
  await expect(page.locator("html")).toHaveAttribute("data-theme-id", "classic");
  await expect(page.locator(".table-focus")).toHaveCSS("animation-name", "none");
  await page.getByTitle("切换桌面主题").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme-id", "copper");
  await expect(page.getByText(/多人同大 \+ 多人同小/)).toBeVisible();
  await page.getByTitle("静音").click();
  await expect(page.locator("html")).toHaveAttribute("data-muted", "true");
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations).toEqual([]);
});
