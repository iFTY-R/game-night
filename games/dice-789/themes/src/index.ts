import type { ThemeManifest } from "@game-night/theme-system";

const compatibility = ["1.0.0"] as const;

export interface Dice789SoundProfile {
  readonly rollHz: number;
  readonly effectHz: number;
  readonly durationMs: number;
}

export const classicTheme: ThemeManifest = {
  themeId: "classic", version: "1.0.0", gameId: "dice-789", compatibleGameVersions: compatibility,
  tokens: {
    "--platform-surface": "#111817", "--platform-surface-raised": "#202a27", "--platform-ink": "#f6efe1",
    "--platform-muted": "#a9b6b0", "--platform-accent": "#e6b85f", "--platform-danger": "#df705c", "--platform-focus": "#82d4c5",
    "--game-table": "#1e493f", "--game-dice": "#f3eee3", "--game-dice-edge": "#c8bca7", "--game-pip": "#221e1a",
    "--game-pool": "#a94336", "--game-cup-edge": "#d7c9ad", "--game-direction": "#83cdb8", "--game-success": "#8cc99b",
    "--game-motion-fast": "170ms", "--game-motion-roll": "280ms"
  }, assets: [],
};

export const stackedTheme: ThemeManifest = {
  themeId: "stacked", version: "1.0.0", gameId: "dice-789", compatibleGameVersions: compatibility,
  tokens: {
    "--platform-surface": "#171817", "--platform-surface-raised": "#2b2924", "--platform-ink": "#f5eee0",
    "--platform-muted": "#b5ad9e", "--platform-accent": "#d7a85b", "--platform-danger": "#d76552", "--platform-focus": "#8bd0bd",
    "--game-table": "#33483c", "--game-dice": "#eee5d4", "--game-dice-edge": "#b89a73", "--game-pip": "#2b211b",
    "--game-pool": "#a8362f", "--game-cup-edge": "#cba775", "--game-direction": "#a6c48e", "--game-success": "#96bd8d",
    "--game-motion-fast": "210ms", "--game-motion-roll": "340ms"
  }, assets: [],
};

export const arcadeTheme: ThemeManifest = {
  themeId: "arcade", version: "1.0.0", gameId: "dice-789", compatibleGameVersions: compatibility,
  tokens: {
    "--platform-surface": "#101616", "--platform-surface-raised": "#172525", "--platform-ink": "#f2f6e8",
    "--platform-muted": "#9db2aa", "--platform-accent": "#f1c94f", "--platform-danger": "#ee6758", "--platform-focus": "#67dfcb",
    "--game-table": "#173e39", "--game-dice": "#e8f0df", "--game-dice-edge": "#91aa9c", "--game-pip": "#14201d",
    "--game-pool": "#d13e45", "--game-cup-edge": "#a7d3bf", "--game-direction": "#65ddc6", "--game-success": "#70d49a",
    "--game-motion-fast": "120ms", "--game-motion-roll": "200ms"
  }, assets: [],
};

export const dice789Themes = [classicTheme, stackedTheme, arcadeTheme] as const;
const soundProfiles: Readonly<Record<string, Dice789SoundProfile>> = {
  classic: { rollHz: 392, effectHz: 523, durationMs: 90 },
  stacked: { rollHz: 330, effectHz: 440, durationMs: 125 },
  arcade: { rollHz: 523, effectHz: 659, durationMs: 70 },
};

export const dice789Theme = (themeId: string): ThemeManifest => dice789Themes.find((theme) => theme.themeId === themeId) ?? classicTheme;
export const dice789SoundProfile = (themeId: string): Dice789SoundProfile => soundProfiles[themeId] ?? soundProfiles.classic!;
