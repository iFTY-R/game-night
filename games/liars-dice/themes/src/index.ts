import type { ThemeManifest } from "@game-night/theme-system";

const compatibility = ["1.0.0"] as const;

export interface LiarsDiceSoundProfile {
  readonly bidHz: number;
  readonly revealHz: number;
  readonly durationMs: number;
}

export const classicTheme: ThemeManifest = {
  themeId: "classic",
  version: "1.0.0",
  gameId: "liars-dice",
  compatibleGameVersions: compatibility,
  tokens: {
    "--platform-surface": "#101718",
    "--platform-surface-raised": "#1d2929",
    "--platform-ink": "#f5efe2",
    "--platform-muted": "#aab8b4",
    "--platform-accent": "#e5b45f",
    "--platform-danger": "#df705c",
    "--platform-focus": "#87d9cf",
    "--game-table": "#16443d",
    "--game-dice": "#f0eadc",
    "--game-dice-edge": "#c9bda8",
    "--game-pip": "#241f1b",
    "--game-success": "#91cda6",
    "--game-motion-fast": "160ms",
    "--game-motion-reveal": "260ms"
  },
  assets: []
};

export const copperTheme: ThemeManifest = {
  themeId: "copper",
  version: "1.0.0",
  gameId: "liars-dice",
  compatibleGameVersions: compatibility,
  tokens: {
    "--platform-surface": "#151716",
    "--platform-surface-raised": "#292823",
    "--platform-ink": "#f4eee2",
    "--platform-muted": "#b8b0a2",
    "--platform-accent": "#d39a5b",
    "--platform-danger": "#d8614f",
    "--platform-focus": "#8fd6c7",
    "--game-table": "#2f493f",
    "--game-dice": "#eee4d2",
    "--game-dice-edge": "#b99670",
    "--game-pip": "#332119",
    "--game-success": "#9ac393",
    "--game-motion-fast": "190ms",
    "--game-motion-reveal": "320ms"
  },
  assets: []
};

export const nightTheme: ThemeManifest = {
  themeId: "night",
  version: "1.0.0",
  gameId: "liars-dice",
  compatibleGameVersions: compatibility,
  tokens: {
    "--platform-surface": "#0d1312",
    "--platform-surface-raised": "#17221f",
    "--platform-ink": "#edf2e9",
    "--platform-muted": "#9caaa5",
    "--platform-accent": "#d8c56e",
    "--platform-danger": "#e07065",
    "--platform-focus": "#72d5c2",
    "--game-table": "#113630",
    "--game-dice": "#dce7df",
    "--game-dice-edge": "#91a99e",
    "--game-pip": "#13201d",
    "--game-success": "#79c89c",
    "--game-motion-fast": "130ms",
    "--game-motion-reveal": "220ms"
  },
  assets: []
};

export const liarsDiceThemes = [classicTheme, copperTheme, nightTheme] as const;

const soundProfiles: Readonly<Record<string, LiarsDiceSoundProfile>> = {
  classic: { bidHz: 440, revealHz: 220, durationMs: 90 },
  copper: { bidHz: 392, revealHz: 196, durationMs: 120 },
  night: { bidHz: 523, revealHz: 262, durationMs: 75 },
};

export const liarsDiceTheme = (themeId: string): ThemeManifest =>
  liarsDiceThemes.find((theme) => theme.themeId === themeId) ?? classicTheme;

// Audio profiles are presentation-only theme data and never receive game state or action authority.
export const liarsDiceSoundProfile = (themeId: string): LiarsDiceSoundProfile => soundProfiles[themeId] ?? soundProfiles.classic!;
