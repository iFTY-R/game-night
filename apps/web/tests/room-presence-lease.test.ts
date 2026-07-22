import { createApp, h } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";

import { roomPresenceHeartbeatInterval, useRoomPresenceLease } from "../src/composables/use-room-presence-lease";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("room presence lease", () => {
  it("renews immediately and every interval only while the page is visible", async () => {
    vi.useFakeTimers();
    let visibility: DocumentVisibilityState = "visible";
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
    const fetch = vi.fn(async () => new Response(JSON.stringify({ observedAt: "2026-07-22T12:00:00Z" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetch);
    const root = document.createElement("div");
    document.body.appendChild(root);
    const app = createApp({
      setup() {
        useRoomPresenceLease("room-1");
        return () => h("div");
      },
    });

    app.mount(root);
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    await vi.advanceTimersByTimeAsync(roomPresenceHeartbeatInterval);
    expect(fetch).toHaveBeenCalledTimes(2);

    visibility = "hidden";
    await vi.advanceTimersByTimeAsync(roomPresenceHeartbeatInterval);
    expect(fetch).toHaveBeenCalledTimes(2);

    visibility = "visible";
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));

    app.unmount();
    await vi.advanceTimersByTimeAsync(roomPresenceHeartbeatInterval);
    expect(fetch).toHaveBeenCalledTimes(3);
  });

  it("stays disabled for fixture game tables", async () => {
    vi.useFakeTimers();
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const root = document.createElement("div");
    document.body.appendChild(root);
    const app = createApp({
      setup() {
        useRoomPresenceLease("fixture-room", { enabled: false });
        return () => h("div");
      },
    });

    app.mount(root);
    await vi.advanceTimersByTimeAsync(roomPresenceHeartbeatInterval * 2);
    expect(fetch).not.toHaveBeenCalled();
    app.unmount();
  });
});
