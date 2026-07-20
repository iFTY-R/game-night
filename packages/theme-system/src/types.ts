export interface ThemeAsset {
  readonly path: string;
  readonly contentType: string;
  readonly integrity: string;
}

export interface ThemeManifest {
  readonly themeId: string;
  readonly version: string;
  readonly gameId: string;
  readonly compatibleGameVersions: readonly string[];
  readonly tokens: Readonly<Record<string, string>>;
  readonly assets: readonly ThemeAsset[];
}

export interface ThemeReference {
  readonly manifestUrl: string;
  readonly manifestIntegrity: string;
}

export interface LoadedTheme {
  readonly manifest: ThemeManifest;
  readonly assets: ReadonlyMap<string, Uint8Array>;
  readonly usedFallback: boolean;
  readonly errorCode: string | null;
}

export interface ThemePreferences {
  readonly contrast: "normal" | "high";
  readonly motion: "full" | "reduced";
  readonly colorScheme: "light" | "dark" | "system";
  readonly muted: boolean;
}
