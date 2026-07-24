import { describe, expect, it } from "vitest";

import { validateThemeManifest } from "@game-night/theme-system";

import { threeRoundsSoundProfile, threeRoundsThemes } from "../src";

describe("three-rounds themes", () => {
  it("keeps all registered themes valid and distinct without a purple primary surface", () => {
    const tables = new Set<string>();
    for (const theme of threeRoundsThemes) {
      expect(() => validateThemeManifest(theme)).not.toThrow();
      expect(threeRoundsSoundProfile(theme.themeId).durationMs).toBeGreaterThan(0);
      expect(theme.tokens["--platform-accent"]?.toLowerCase()).not.toContain("7f00ff");
      tables.add(theme.tokens["--game-table"] ?? "");
    }
    expect(threeRoundsThemes.map((theme) => theme.themeId)).toEqual(["classic", "felt", "noir"]);
    expect(tables.size).toBe(3);
  });
});
