import type { ThemeManifest } from "@game-night/theme-system";

const compatibility = ["1.0.0"] as const;

export interface ThreeRoundsSoundProfile {
  readonly selectHz: number;
  readonly confirmHz: number;
  readonly revealHz: number;
  readonly durationMs: number;
}

const shared = {
  version: "1.0.0",
  gameId: "three-rounds",
  compatibleGameVersions: compatibility,
  assets: [],
} as const;

export const classicTheme: ThemeManifest = {
  ...shared,
  themeId: "classic",
  tokens: {
    "--platform-surface": "#101616",
    "--platform-surface-raised": "#1a2625",
    "--platform-ink": "#f6efdf",
    "--platform-muted": "#a7b4b0",
    "--platform-accent": "#dfb261",
    "--platform-danger": "#d97761",
    "--platform-focus": "#7ed1bf",
    "--game-table": "#18433f",
    "--game-card-face": "#f6efdf",
    "--game-card-back": "#26443f",
    "--game-card-outline": "#d7c5a0",
    "--game-card-ink": "#231b17",
    "--game-card-accent": "#b24a39",
    "--game-private-surface": "#11201f",
    "--game-seat-glow": "#7ed1bf",
    "--game-success": "#98cd9d",
    "--game-motion-fast": "160ms",
    "--game-motion-reveal": "260ms",
  },
};

export const feltTheme: ThemeManifest = {
  ...shared,
  themeId: "felt",
  tokens: {
    "--platform-surface": "#141713",
    "--platform-surface-raised": "#232a21",
    "--platform-ink": "#f7f0e2",
    "--platform-muted": "#b7b39f",
    "--platform-accent": "#c79b4f",
    "--platform-danger": "#db705f",
    "--platform-focus": "#8fd0a2",
    "--game-table": "#36543a",
    "--game-card-face": "#f5edda",
    "--game-card-back": "#3f5b42",
    "--game-card-outline": "#d1c29d",
    "--game-card-ink": "#202017",
    "--game-card-accent": "#9d3f31",
    "--game-private-surface": "#1a2820",
    "--game-seat-glow": "#8fd0a2",
    "--game-success": "#a4d394",
    "--game-motion-fast": "170ms",
    "--game-motion-reveal": "290ms",
  },
};

export const noirTheme: ThemeManifest = {
  ...shared,
  themeId: "noir",
  tokens: {
    "--platform-surface": "#0d1113",
    "--platform-surface-raised": "#181f23",
    "--platform-ink": "#ece7de",
    "--platform-muted": "#98a3a8",
    "--platform-accent": "#d4b06a",
    "--platform-danger": "#d96f62",
    "--platform-focus": "#86c7d0",
    "--game-table": "#15262d",
    "--game-card-face": "#efe7dc",
    "--game-card-back": "#223842",
    "--game-card-outline": "#b8af9e",
    "--game-card-ink": "#17191a",
    "--game-card-accent": "#af4d43",
    "--game-private-surface": "#0f171b",
    "--game-seat-glow": "#86c7d0",
    "--game-success": "#8dc0a7",
    "--game-motion-fast": "140ms",
    "--game-motion-reveal": "220ms",
  },
};

export const threeRoundsThemes = [classicTheme, feltTheme, noirTheme] as const;

const soundProfiles: Record<string, ThreeRoundsSoundProfile> = {
  classic: { selectHz: 392, confirmHz: 523, revealHz: 220, durationMs: 90 },
  felt: { selectHz: 349, confirmHz: 494, revealHz: 196, durationMs: 110 },
  noir: { selectHz: 415, confirmHz: 554, revealHz: 247, durationMs: 75 },
};

export const threeRoundsSoundProfile = (themeId: string): ThreeRoundsSoundProfile => soundProfiles[themeId] ?? soundProfiles.classic!;
export const threeRoundsTheme = (themeId: string): ThemeManifest => threeRoundsThemes.find((theme) => theme.themeId === themeId) ?? classicTheme;
