import { ThemeLoadError } from "./errors";
import type { ThemeManifest } from "./types";

const idPattern = /^[a-z0-9]+(?:[._-][a-z0-9]+)*$/;
const semverPattern = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;
const integrityPattern = /^sha256-[A-Za-z0-9+/]{43}={1,2}$/;

export const validateThemeManifest = (manifest: ThemeManifest): void => {
  if (
    !idPattern.test(manifest.themeId) ||
    !idPattern.test(manifest.gameId) ||
    !semverPattern.test(manifest.version) ||
    manifest.compatibleGameVersions.length === 0 ||
    manifest.compatibleGameVersions.some((version) => !semverPattern.test(version))
  ) {
    throw new ThemeLoadError("manifest_invalid", "Theme manifest identity or compatibility is invalid");
  }
  const assetPaths = new Set<string>();
  for (const asset of manifest.assets) {
    if (
      asset.path.startsWith("/") ||
      asset.path.includes("..") ||
      asset.path.includes("\\") ||
      !asset.contentType ||
      !integrityPattern.test(asset.integrity) ||
      assetPaths.has(asset.path)
    ) {
      throw new ThemeLoadError("manifest_invalid", "Theme asset manifest is unsafe");
    }
    assetPaths.add(asset.path);
  }
  for (const [key, value] of Object.entries(manifest.tokens)) {
    if (
      !/^--(?:game|platform)-[a-z0-9-]+$/.test(key) ||
      value.length > 512 ||
      /[{};]|url\s*\(/i.test(value)
    ) {
      throw new ThemeLoadError("manifest_invalid", "Theme token is outside the visual token boundary");
    }
  }
};

export const assertIntegrity = async (bytes: Uint8Array, expected: string): Promise<void> => {
  if (!integrityPattern.test(expected)) {
    throw new ThemeLoadError("integrity_invalid", "Theme integrity descriptor is malformed");
  }
  // Web Crypto requires an ArrayBuffer-backed view; copy untrusted bytes to exclude SharedArrayBuffer input.
  const digest = await crypto.subtle.digest("SHA-256", Uint8Array.from(bytes).buffer);
  const encoded = `sha256-${toBase64(new Uint8Array(digest))}`;
  if (encoded !== expected) {
    throw new ThemeLoadError("integrity_mismatch", "Theme content hash does not match its manifest");
  }
};

const toBase64 = (bytes: Uint8Array): string => {
  let binary = "";
  bytes.forEach((value) => {
    binary += String.fromCharCode(value);
  });
  return btoa(binary);
};
