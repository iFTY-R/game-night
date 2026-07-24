import { defineConfig, devices } from "@playwright/test";

const enabled = process.env.ADMIN_E2E === "1";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  forbidOnly: true,
  timeout: 60_000,
  retries: 0,
  reporter: [["list"]],
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:4174",
    trace: "off",
    video: "off",
    screenshot: "off"
  },
  projects: enabled
    ? [
        {
          name: "admin-secret-flow",
          use: {
            ...devices["Desktop Chrome"],
            storageState: undefined,
            trace: "off",
            video: "off",
            screenshot: "off"
          }
        },
        {
          name: "admin-visual",
          use: {
            ...devices["Desktop Chrome"],
            storageState: undefined,
            trace: "on-first-retry",
            video: "off",
            screenshot: "only-on-failure"
          }
        }
      ]
    : []
});
