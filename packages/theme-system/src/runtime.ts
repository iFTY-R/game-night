import { ThemeLoadError } from "./errors";
import { ThemeLoader } from "./loader";
import type { LoadedTheme, ThemeManifest, ThemePreferences, ThemeReference } from "./types";

export const safeTheme: ThemeManifest = {
  themeId: "safe-table",
  version: "1.0.0",
  gameId: "platform",
  compatibleGameVersions: ["1.0.0"],
  tokens: {
    "--platform-surface": "#121a1d",
    "--platform-surface-raised": "#1b292d",
    "--platform-ink": "#f5f1e8",
    "--platform-muted": "#a8b5b4",
    "--platform-accent": "#e6b566",
    "--platform-danger": "#e77c65",
    "--game-table": "#173b38",
  },
  assets: [],
};

const defaultPreferences: ThemePreferences = {
  contrast: "normal",
  motion: "full",
  colorScheme: "system",
  muted: false,
};

// ThemeRuntime pins one loaded visual version per active session and never exposes actions or game state to themes.
export class ThemeRuntime {
  readonly #loader: ThemeLoader;
  readonly #sessions = new Map<string, LoadedTheme>();
  #preferences: ThemePreferences = defaultPreferences;

  public constructor(loader = new ThemeLoader()) {
    this.#loader = loader;
  }

  public preferences(): ThemePreferences {
    return this.#preferences;
  }

  public setPreferences(preferences: ThemePreferences): void {
    this.#preferences = { ...preferences };
  }

  public async loadForSession(
    sessionId: string,
    reference: ThemeReference,
    options: { readonly gameVersion: string; readonly fallback?: ThemeManifest },
  ): Promise<LoadedTheme> {
    if (!sessionId) {
      throw new ThemeLoadError("session_invalid", "Theme session id is required");
    }
    const pinned = this.#sessions.get(sessionId);
    if (pinned !== undefined) {
      return pinned;
    }
    const loaded = await this.#loader.load(reference, {
      gameVersion: options.gameVersion,
      fallback: options.fallback ?? safeTheme,
    });
    this.#sessions.set(sessionId, loaded);
    return loaded;
  }

  public pinned(sessionId: string): LoadedTheme | undefined {
    return this.#sessions.get(sessionId);
  }

  public apply(loaded: LoadedTheme, target: HTMLElement): void {
    target.dataset.themeId = loaded.manifest.themeId;
    target.dataset.themeVersion = loaded.manifest.version;
    Object.entries(loaded.manifest.tokens).forEach(([key, value]) => target.style.setProperty(key, value));
    target.dataset.contrast = this.#preferences.contrast;
    target.dataset.motion = this.#preferences.motion;
    target.dataset.muted = String(this.#preferences.muted);
  }
}
