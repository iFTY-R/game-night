import { describe, expect, it } from "vitest";

import { validateThemeManifest } from "@game-night/theme-system";

import { liarsDiceSoundProfile, liarsDiceThemes } from "../src";

describe("liars dice themes", () => {
  it("keeps every registered variant valid and visually distinct", () => {
    const ids = new Set<string>();
    const tables = new Set<string>();
    for (const theme of liarsDiceThemes) {
      expect(() => validateThemeManifest(theme)).not.toThrow();
      ids.add(theme.themeId);
      tables.add(theme.tokens["--game-table"] ?? "");
      expect(theme.tokens["--game-motion-fast"]).toMatch(/^[0-9]+ms$/);
      expect(liarsDiceSoundProfile(theme.themeId).durationMs).toBeGreaterThan(0);
    }
    expect([...ids]).toEqual(["classic", "copper", "night"]);
    expect(tables.size).toBe(3);
  });
});
