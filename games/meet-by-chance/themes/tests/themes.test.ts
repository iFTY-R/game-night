import { describe, expect, it } from "vitest";

import { validateThemeManifest } from "@game-night/theme-system";

import { meetByChanceSoundProfile, meetByChanceThemes } from "../src";

describe("meet-by-chance themes", () => {
  it("keeps the registered manifest variants valid and presentation-only", () => {
    const tables = new Set<string>();
    for (const theme of meetByChanceThemes) {
      expect(() => validateThemeManifest(theme)).not.toThrow();
      expect(meetByChanceSoundProfile(theme.themeId).durationMs).toBeGreaterThan(0);
      tables.add(theme.tokens["--game-table"] ?? "");
    }
    expect(meetByChanceThemes.map((theme) => theme.themeId)).toEqual(["classic", "copper", "night"]);
    expect(tables.size).toBe(3);
  });
});
