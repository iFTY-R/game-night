import { ThemeLoadError } from "./errors";
import { assertIntegrity, validateThemeManifest } from "./validate";
import type { LoadedTheme, ThemeManifest, ThemeReference } from "./types";

export interface ThemeFetcher {
  fetch(input: string, init?: RequestInit): Promise<Response>;
}

export interface ThemeLoadOptions {
  readonly gameVersion: string;
  readonly fallback: ThemeManifest;
  readonly fetcher?: ThemeFetcher;
}

// ThemeLoader verifies every manifest and asset before exposing visual resources to a session.
export class ThemeLoader {
  readonly #fetcher: ThemeFetcher;

  public constructor(fetcher: ThemeFetcher = { fetch: globalThis.fetch.bind(globalThis) }) {
    this.#fetcher = fetcher;
  }

  public async load(reference: ThemeReference, options: ThemeLoadOptions): Promise<LoadedTheme> {
    validateThemeManifest(options.fallback);
    try {
      const manifestResponse = await this.#fetcher.fetch(reference.manifestUrl, { cache: "force-cache" });
      if (!manifestResponse.ok) {
        throw new ThemeLoadError("manifest_unavailable", `Theme manifest returned ${manifestResponse.status}`);
      }
      const manifestBytes = new Uint8Array(await manifestResponse.arrayBuffer());
      await assertIntegrity(manifestBytes, reference.manifestIntegrity);
      const manifest = parseManifest(manifestBytes);
      if (!manifest.compatibleGameVersions.includes(options.gameVersion)) {
        throw new ThemeLoadError("theme_incompatible", "Theme is not compatible with the pinned game version");
      }
      const assets = new Map<string, Uint8Array>();
      await Promise.all(
        manifest.assets.map(async (asset) => {
          const response = await this.#fetcher.fetch(new URL(asset.path, reference.manifestUrl).toString(), {
            cache: "force-cache",
          });
          if (!response.ok) {
            throw new ThemeLoadError("asset_unavailable", `Theme asset returned ${response.status}`);
          }
          const bytes = new Uint8Array(await response.arrayBuffer());
          await assertIntegrity(bytes, asset.integrity);
          assets.set(asset.path, bytes);
        }),
      );
      return { manifest, assets, usedFallback: false, errorCode: null };
    } catch (error) {
      const loadError = error instanceof ThemeLoadError ? error : new ThemeLoadError("theme_load_failed", "Theme load failed", { cause: error });
      return {
        manifest: options.fallback,
        assets: new Map(),
        usedFallback: true,
        errorCode: loadError.code,
      };
    }
  }
}

const parseManifest = (bytes: Uint8Array): ThemeManifest => {
  try {
    const value: unknown = JSON.parse(new TextDecoder().decode(bytes));
    validateThemeManifest(value as ThemeManifest);
    return value as ThemeManifest;
  } catch (error) {
    if (error instanceof ThemeLoadError) {
      throw error;
    }
    throw new ThemeLoadError("manifest_invalid", "Theme manifest is not valid JSON", { cause: error });
  }
};
