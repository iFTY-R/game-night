import { expect, test } from "@playwright/test";

const fixture = (state = "active") => `/fixtures/three-rounds/${state}`;

test("third-round fixture keeps the tray compact and exposes no submit button in portrait or landscape", async ({ page }) => {
  for (const viewport of [{ width: 390, height: 844 }, { width: 844, height: 390 }] as const) {
    await page.setViewportSize(viewport);
    await page.goto(fixture("round-three"));

    await expect(page.getByTestId("three-rounds-screen")).toBeVisible();
    await expect(page.getByText("第三关自动公开，当前不需要再选牌。")).toBeVisible();
    await expect(page.getByTestId("submit-selection-action")).toHaveCount(0);

    const trayBox = await page.getByRole("region", { name: "三关操作区" }).boundingBox();
    expect(trayBox).not.toBeNull();
    if (trayBox === null) continue;
    if (viewport.width < viewport.height) expect(trayBox.height).toBeLessThanOrEqual(viewport.height * 0.42 + 12);
    else expect(trayBox.height).toBeLessThanOrEqual(viewport.height * 0.5 + 12);
  }
});

test("replay fixture switches from round tabs to the final cancelled summary", async ({ page }) => {
  await page.goto(fixture("replay"));

  await expect(page.getByTestId("three-rounds-replay-screen")).toBeVisible();
  await page.getByRole("button", { name: /总结果/ }).click();
  await expect(page.getByText("取消局不会伪装成正常冠军")).toBeVisible();
  await expect(page.locator(".replay-detail").getByText("冠军").first()).toBeVisible();
});
