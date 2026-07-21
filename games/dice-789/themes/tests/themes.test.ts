import { describe, expect, it } from "vitest";

import { validateThemeManifest } from "@game-night/theme-system";

import { dice789SoundProfile, dice789Themes } from "../src";

describe("dice 789 themes", () => {
  it("keeps all registered themes valid and distinct", () => {
    const tables = new Set<string>();
    for (const theme of dice789Themes) {
      expect(() => validateThemeManifest(theme)).not.toThrow();
      expect(dice789SoundProfile(theme.themeId).durationMs).toBeGreaterThan(0);
      tables.add(theme.tokens["--game-table"] ?? "");
    }
    expect(dice789Themes.map((theme) => theme.themeId)).toEqual(["classic", "stacked", "arcade"]);
    expect(tables.size).toBe(3);
  });
});
