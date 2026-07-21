import type { ThemeManifest } from "@game-night/theme-system";

const compatibility = ["1.0.0"] as const;

export interface MeetByChanceSoundProfile {
  readonly revealHz: number;
  readonly targetHz: number;
  readonly durationMs: number;
}

export const classicTheme: ThemeManifest = {
  themeId: "classic", version: "1.0.0", gameId: "meet-by-chance", compatibleGameVersions: compatibility,
  tokens: {
    "--platform-surface": "#101716", "--platform-surface-raised": "#222c28", "--platform-ink": "#f5efe2",
    "--platform-muted": "#a7b6af", "--platform-accent": "#edbd61", "--platform-danger": "#df705c", "--platform-focus": "#82d7c7",
    "--game-table": "#1b493f", "--game-dice": "#f2ecdf", "--game-dice-edge": "#c6b9a3", "--game-pip": "#241e1a",
    "--game-seat": "#20302c", "--game-target": "#f0c467", "--game-match": "#79c9b5", "--game-success": "#8fca9c",
    "--game-motion-fast": "170ms", "--game-motion-reveal": "300ms"
  }, assets: [],
};

export const copperTheme: ThemeManifest = {
  themeId: "copper", version: "1.0.0", gameId: "meet-by-chance", compatibleGameVersions: compatibility,
  tokens: {
    "--platform-surface": "#171615", "--platform-surface-raised": "#2e2925", "--platform-ink": "#f4ede1",
    "--platform-muted": "#b7aca0", "--platform-accent": "#dca164", "--platform-danger": "#d96855", "--platform-focus": "#8ed3c5",
    "--game-table": "#35473d", "--game-dice": "#eee3d1", "--game-dice-edge": "#b7916f", "--game-pip": "#30201a",
    "--game-seat": "#332c28", "--game-target": "#e6ad69", "--game-match": "#9bc0a6", "--game-success": "#9bc493",
    "--game-motion-fast": "210ms", "--game-motion-reveal": "360ms"
  }, assets: [],
};

export const nightTheme: ThemeManifest = {
  themeId: "night", version: "1.0.0", gameId: "meet-by-chance", compatibleGameVersions: compatibility,
  tokens: {
    "--platform-surface": "#0c1414", "--platform-surface-raised": "#172523", "--platform-ink": "#edf2e9",
    "--platform-muted": "#9caeaa", "--platform-accent": "#d8ca73", "--platform-danger": "#e27669", "--platform-focus": "#6ed9c5",
    "--game-table": "#123a35", "--game-dice": "#dce8df", "--game-dice-edge": "#8ca89e", "--game-pip": "#12201d",
    "--game-seat": "#172925", "--game-target": "#e5d276", "--game-match": "#64d6c0", "--game-success": "#77cb9c",
    "--game-motion-fast": "125ms", "--game-motion-reveal": "230ms"
  }, assets: [],
};

export const meetByChanceThemes = [classicTheme, copperTheme, nightTheme] as const;
const soundProfiles: Readonly<Record<string, MeetByChanceSoundProfile>> = {
  classic: { revealHz: 392, targetHz: 523, durationMs: 95 },
  copper: { revealHz: 330, targetHz: 440, durationMs: 125 },
  night: { revealHz: 494, targetHz: 659, durationMs: 75 },
};

export const meetByChanceTheme = (themeId: string): ThemeManifest => meetByChanceThemes.find((theme) => theme.themeId === themeId) ?? classicTheme;
// Sound parameters are presentation-only and never participate in authoritative resolution.
export const meetByChanceSoundProfile = (themeId: string): MeetByChanceSoundProfile => soundProfiles[themeId] ?? soundProfiles.classic!;
