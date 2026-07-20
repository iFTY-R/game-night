import { describe, expect, it, vi } from "vitest";

import { ThemeLoader, ThemeRuntime, assertIntegrity, safeTheme } from "../src";
import type { ThemeManifest } from "../src";

const bytes = new TextEncoder().encode("table texture");

const digest = async (value: Uint8Array): Promise<string> => {
  const hash = await crypto.subtle.digest("SHA-256", Uint8Array.from(value).buffer);
  let binary = "";
  new Uint8Array(hash).forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return `sha256-${btoa(binary)}`;
};

describe("ThemeLoader", () => {
  it("verifies the manifest and every versioned asset", async () => {
    const assetIntegrity = await digest(bytes);
    const manifest: ThemeManifest = {
      themeId: "felt-night",
      version: "1.0.0",
      gameId: "liars-dice",
      compatibleGameVersions: ["1.0.0"],
      tokens: { "--game-table": "#173b38" },
      assets: [{ path: "table.bin", contentType: "application/octet-stream", integrity: assetIntegrity }],
    };
    const manifestBytes = new TextEncoder().encode(JSON.stringify(manifest));
    const manifestIntegrity = await digest(manifestBytes);
    const fetcher = {
      fetch: vi.fn(async (url: string) =>
        url.endsWith("manifest.json")
          ? new Response(manifestBytes, { status: 200 })
          : new Response(bytes, { status: 200 }),
      ),
    };
    const loader = new ThemeLoader(fetcher);

    const loaded = await loader.load(
      { manifestUrl: "https://assets.example.test/manifest.json", manifestIntegrity },
      { gameVersion: "1.0.0", fallback: safeTheme },
    );
    expect(loaded.usedFallback).toBe(false);
    expect(loaded.manifest.themeId).toBe("felt-night");
    expect(Array.from(loaded.assets.get("table.bin") ?? [])).toEqual(Array.from(bytes));
  });

  it("falls back on a bad hash and never throws into the game shell", async () => {
    const loader = new ThemeLoader({ fetch: vi.fn(async () => new Response(new TextEncoder().encode("bad"), { status: 200 })) });
    const loaded = await loader.load(
      { manifestUrl: "https://assets.example.test/manifest.json", manifestIntegrity: "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" },
      { gameVersion: "1.0.0", fallback: safeTheme },
    );
    expect(loaded.usedFallback).toBe(true);
    expect(loaded.errorCode).toBe("integrity_mismatch");
    expect(loaded.manifest.themeId).toBe("safe-table");
  });
});

describe("ThemeRuntime", () => {
  it("pins the first loaded version for the lifetime of a session", async () => {
    const loader = { load: vi.fn(async () => ({ manifest: safeTheme, assets: new Map(), usedFallback: false, errorCode: null })) } as unknown as ThemeLoader;
    const runtime = new ThemeRuntime(loader);
    const first = await runtime.loadForSession("session-1", { manifestUrl: "a", manifestIntegrity: "b" }, { gameVersion: "1.0.0" });
    const second = await runtime.loadForSession("session-1", { manifestUrl: "different", manifestIntegrity: "different" }, { gameVersion: "1.0.0" });
    expect(second).toBe(first);
    expect(loader.load).toHaveBeenCalledTimes(1);
  });

  it("applies visual tokens without giving themes an action channel", () => {
    const target = document.createElement("div");
    const runtime = new ThemeRuntime();
    runtime.apply({ manifest: safeTheme, assets: new Map(), usedFallback: false, errorCode: null }, target);
    expect(target.dataset.themeId).toBe("safe-table");
    expect(target.style.getPropertyValue("--platform-accent")).toBe("#e6b566");
    expect(target.dataset).not.toHaveProperty("allowedActions");
  });
});

it("rejects a mismatched integrity value", async () => {
  await expect(assertIntegrity(bytes, "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")).rejects.toMatchObject({ code: "integrity_mismatch" });
});
