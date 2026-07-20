import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";

import { STORAGE_KEY, useRoomStore } from "../src/stores/room";

describe("room context recovery", () => {
  beforeEach(() => {
    window.localStorage.clear();
    setActivePinia(createPinia());
  });

  it("persists only the viewer-safe room context", () => {
    const room = useRoomStore();
    expect(room.setIdentity("小满")).toBe(true);
    room.enterRoom("room-1", "N789");
    room.setSession("session-1");

    const persisted = window.localStorage.getItem(STORAGE_KEY) ?? "";
    expect(persisted).toContain("小满");
    expect(persisted).not.toMatch(/fingerprint|deviceSecret|token/i);
  });

  it("ignores incompatible schema versions", () => {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ schemaVersion: 99, displayName: "旧用户", userId: "old" }));
    const room = useRoomStore();
    room.recover();
    expect(room.hasIdentity).toBe(false);
  });

  it("removes corrupt recovery data without blocking startup", () => {
    window.localStorage.setItem(STORAGE_KEY, "not-json");
    const room = useRoomStore();
    expect(() => room.recover()).not.toThrow();
    expect(window.localStorage.getItem(STORAGE_KEY)).toBeNull();
  });
});
