import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

const tableUrl = "/fixtures/table";

test("portrait table is complete at 390x844", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);

  await expect(page.getByTestId("liars-dice-screen")).toBeVisible();
  await expect(page.getByRole("region", { name: "共同游戏桌" })).toBeVisible();
  await expect(page.getByRole("region", { name: "本轮操作" })).toHaveAttribute("data-state", "compact");
  await expect(page.getByRole("region", { name: "你的私密骰子" })).toBeVisible();
  await expect(page.getByTestId("bid-action")).toBeVisible();
  await expect(page.getByTestId("open-action")).toBeVisible();
});

test("small Android portrait keeps complete bid controls reachable", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  await page.goto(tableUrl);
  await expect(page.getByTitle("减少数量")).toBeVisible();
  await expect(page.getByTitle("选择 6 点")).toBeVisible();
  await expect(page.getByRole("group", { name: "叫骰模式" })).toBeVisible();
});

test("rotation preserves expanded tray and unsubmitted draft", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);
  const tray = page.getByRole("region", { name: "本轮操作" });
  await page.getByTitle("增加数量").click();
  await expect(page.locator(".quantity-stepper output")).toHaveText("7");
  await page.getByTitle("展开操作区").click();
  await expect(tray).toHaveAttribute("data-state", "expanded");

  await page.setViewportSize({ width: 844, height: 390 });
  await expect(tray).toHaveAttribute("data-state", "expanded");
  await expect(page.locator(".quantity-stepper output")).toHaveText("7");
  await expect(page.getByText("万能 1")).toBeVisible();
});

test("turn runner stays on the table rail between seats in both orientations", async ({ page }) => {
  for (const viewport of [{ width: 390, height: 844 }, { width: 844, height: 390 }]) {
    await page.setViewportSize(viewport);
    await page.goto(tableUrl);

    const geometry = await page.getByRole("region", { name: "共同游戏桌" }).evaluate((table) => {
      const rail = table.querySelector<HTMLElement>(".gn-table__rail");
      const order = table.querySelector<HTMLElement>(".gn-table__turn-order");
      const runner = table.querySelector<HTMLElement>(".gn-table__turn-runner");
      if (rail === null || order === null || runner === null) return null;

      // A fixed gap between seats makes this geometry assertion deterministic without changing production motion.
      const runnerStyle = getComputedStyle(runner);
      const presentation = {
        width: runnerStyle.width,
        height: runnerStyle.height,
        duration: runnerStyle.animationDuration,
        path: runnerStyle.offsetPath,
      };
      runner.style.animation = "none";
      runner.style.offsetDistance = "62.5%";
      const toBox = (element: Element) => {
        const box = element.getBoundingClientRect();
        return { left: box.left, top: box.top, right: box.right, bottom: box.bottom, width: box.width, height: box.height };
      };
      return {
        table: toBox(table),
        rail: toBox(rail),
        order: toBox(order),
        runner: toBox(runner),
        presentation,
        seats: Array.from(table.querySelectorAll(".gn-seat"), toBox),
      };
    });

    expect(geometry).not.toBeNull();
    if (geometry === null) continue;
    expect(Math.abs(geometry.order.left - geometry.rail.left)).toBeLessThanOrEqual(1);
    expect(Math.abs(geometry.order.top - geometry.rail.top)).toBeLessThanOrEqual(1);
    expect(Math.abs(geometry.order.right - geometry.rail.right)).toBeLessThanOrEqual(1);
    expect(Math.abs(geometry.order.bottom - geometry.rail.bottom)).toBeLessThanOrEqual(1);
    expect(geometry.presentation).toMatchObject({ width: "16px", height: "16px", duration: "12s" });
    expect(geometry.presentation.path).toBe(viewport.width > viewport.height
      ? "inset(0px round 39% / 44%)"
      : "inset(0px round 44% / 38%)");
    expect(geometry.runner.left).toBeGreaterThanOrEqual(geometry.table.left);
    expect(geometry.runner.top).toBeGreaterThanOrEqual(geometry.table.top);
    expect(geometry.runner.right).toBeLessThanOrEqual(geometry.table.right);
    expect(geometry.runner.bottom).toBeLessThanOrEqual(geometry.table.bottom);
    for (const seat of geometry.seats) {
      const separate = geometry.runner.right <= seat.left || seat.right <= geometry.runner.left
        || geometry.runner.bottom <= seat.top || seat.bottom <= geometry.runner.top;
      expect(
        separate,
        `turn runner overlaps a seat at ${viewport.width}x${viewport.height}: ${JSON.stringify({ runner: geometry.runner, seat })}`,
      ).toBe(true);
    }
  }
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

test("pending bid locks duplicate submission and hands the turn glow to the next player", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);
  const turnSeat = page.locator(".gn-seat.is-turn");
  await expect(turnSeat).toHaveCount(1);
  await expect(turnSeat).toContainText("你");
  await expect(page.locator(".gn-seat__turn-marker")).toHaveCount(0);
  await expect(page.locator(".gn-table__turn-runner")).toHaveCount(1);
  await expect(page.locator(".gn-table__sr-only")).toHaveCSS("clip", "rect(0px, 0px, 0px, 0px)");

  const action = page.getByTestId("bid-action");
  await action.click();
  await expect(action).toBeDisabled();
  await expect(page.getByText("正在提交叫骰")).toBeVisible();
  await expect(page.getByText("等待 阿青")).toBeVisible({ timeout: 2_000 });
  await expect(turnSeat).toHaveCount(1);
  await expect(turnSeat).toContainText("阿青");
  await expect(page.locator(".gn-seat__turn-marker")).toHaveCount(0);
});

test("open dice requires explicit confirmation", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);
  await page.getByTestId("open-action").click();
  const dialog = page.getByRole("alertdialog", { name: "确认开骰？" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "确认开骰" }).click();
  await expect(page.getByText("正在开骰")).toBeVisible();
  await expect(page.getByText("上轮 你 负 · 实际 7 个")).toBeVisible({ timeout: 2_000 });
  await expect(page.getByRole("region", { name: "上一轮公开骰子" })).toBeVisible();
});

test("revealed dice stay clear of player seats on narrow portraits", async ({ page }) => {
  for (const viewport of [{ width: 390, height: 844 }, { width: 360, height: 740 }, { width: 844, height: 390 }]) {
    await page.setViewportSize(viewport);
    await page.goto("/fixtures/table/revealed");

    const geometry = await page.getByRole("region", { name: "共同游戏桌" }).evaluate((table) => {
      const result = table.querySelector<HTMLElement>(".revealed-grid")?.getBoundingClientRect();
      const toBox = (box: DOMRect) => ({ x: box.x, y: box.y, width: box.width, height: box.height });
      return {
        result: result === undefined ? null : toBox(result),
        seats: Array.from(table.querySelectorAll("article"), (seat) => toBox(seat.getBoundingClientRect())),
      };
    });
    expect(geometry.result).not.toBeNull();
    if (geometry.result === null) continue;

    for (const seatBox of geometry.seats) {
      const resultBox = geometry.result;
      const overlapX = Math.max(0, Math.min(resultBox.x + resultBox.width, seatBox.x + seatBox.width) - Math.max(resultBox.x, seatBox.x));
      const overlapY = Math.max(0, Math.min(resultBox.y + resultBox.height, seatBox.y + seatBox.height) - Math.max(resultBox.y, seatBox.y));
      expect(
        overlapX <= 1 || overlapY <= 1,
        `result panel overlaps a seat at ${viewport.width}x${viewport.height}: ${JSON.stringify({ resultBox, seatBox })}`,
      ).toBe(true);
    }

    const feedbackBox = await page.locator(".bid-feedback").boundingBox();
    expect(feedbackBox).not.toBeNull();
    // Fractional transforms may round the final paint edge below one physical CSS pixel.
    expect((feedbackBox?.y ?? 0) + (feedbackBox?.height ?? 0)).toBeLessThanOrEqual(viewport.height + 1);
  }
});

test("spectator fixture never renders private dice", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/fixtures/table/spectator");
  await expect(page.locator(".own-dice")).toHaveCount(0);
  await expect(page.getByText("观战视角")).toBeVisible();
  await expect(page.getByTestId("bid-action")).toBeDisabled();
});

test("reconnecting locks actions and keeps an explicit retry", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/fixtures/table/reconnecting");
  await expect(page.getByText("重连中")).toBeVisible();
  await expect(page.getByTestId("bid-action")).toBeDisabled();
  await page.getByTitle("立即重连").click();
  await expect(page.getByText("已连接")).toBeVisible();
  await expect(page.getByTestId("bid-action")).toBeEnabled();
});

test("timeout settlement remains explicit without revealing private dice", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/fixtures/table/timeout");
  await expect(page.getByText("上轮 阿青 超时 · 4 罚点")).toBeVisible();
  await expect(page.getByRole("region", { name: "上一轮公开骰子" })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "你的私密骰子" })).toBeVisible();
});

test("replay shows the complete bid chain and all revealed dice without actions", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/fixtures/table/replay");
  await expect(page.getByTestId("liars-dice-replay-screen")).toBeVisible();
  await expect(page.getByRole("region", { name: "本轮公开骰子" })).toBeVisible();
  const chain = page.getByRole("region", { name: "完整叫数链" });
  await expect(chain.locator("li")).toHaveCount(4);
  await expect(chain.getByText("阿青")).toBeVisible();
  await expect(chain.getByText("你")).toBeVisible();
  await expect(page.getByTestId("bid-action")).toHaveCount(0);

  await page.setViewportSize({ width: 844, height: 390 });
  await expect(chain).toBeVisible();
  const dice = page.getByRole("region", { name: "本轮公开骰子" });
  await expect(dice).toBeVisible();
  const geometry = await page.getByRole("region", { name: "吹牛骰子复盘桌面" }).evaluate((stage) => {
    const toBox = (element: Element) => {
      const box = element.getBoundingClientRect();
      return { left: box.left, top: box.top, right: box.right, bottom: box.bottom };
    };
    const diceElement = stage.querySelector('[aria-label="本轮公开骰子"]');
    const dock = document.querySelector('[aria-label="完整叫数链"]');
    return {
      dice: diceElement === null ? null : toBox(diceElement),
      diceFits: diceElement instanceof HTMLElement && diceElement.scrollHeight <= diceElement.clientHeight && diceElement.scrollWidth <= diceElement.clientWidth,
      seats: Array.from(stage.querySelectorAll("article"), toBox),
      dock: dock === null ? null : toBox(dock),
    };
  });
  expect(geometry.dice).not.toBeNull();
  expect(geometry.dock).not.toBeNull();
  if (geometry.dice !== null && geometry.dock !== null) {
    expect(geometry.diceFits).toBe(true);
    expect(geometry.dice.bottom).toBeLessThanOrEqual(geometry.dock.top);
    for (const seat of geometry.seats) {
      const separate = geometry.dice.right <= seat.left || seat.right <= geometry.dice.left || geometry.dice.bottom <= seat.top || seat.bottom <= geometry.dice.top;
      expect(separate, `replay dice overlap seat: ${JSON.stringify({ dice: geometry.dice, seat })}`).toBe(true);
    }
  }
});

test("reduced motion disables game-state entrance animations", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);
  await expect(page.locator(".gn-table__turn-runner")).toHaveCSS("animation-name", "none");
  await expect(page.locator(".gn-table__turn-runner")).toHaveCSS("offset-distance", "12.5%");
  const turnPulseAnimation = await page.locator(".gn-seat.is-turn .gn-seat__card").evaluate(
    (card) => getComputedStyle(card, "::after").animationName,
  );
  expect(turnPulseAnimation).toBe("none");

  await page.goto("/fixtures/table/revealed");
  await expect(page.locator(".revealed-grid")).toHaveCSS("animation-name", "none");
});

test("built-in game theme remains usable and accessible", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(tableUrl);
  await expect(page.locator("html")).toHaveAttribute("data-theme-id", "classic");
  await expect(page.locator("html")).toHaveAttribute("data-theme-fallback", "false");
  await page.getByTitle("静音").click();
  await expect(page.locator("html")).toHaveAttribute("data-muted", "true");
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations).toEqual([]);
});
