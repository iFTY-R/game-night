import { readFileSync, existsSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const workspaceRoot = resolve(import.meta.dirname, "../../..");
const adminRoot = resolve(workspaceRoot, "apps/admin");

describe("admin manifest and notices", () => {
  it("uses workspace catalog naive-ui and records Soybean upstream notice", () => {
    const packageJson = JSON.parse(readFileSync(resolve(adminRoot, "package.json"), "utf8")) as {
      name: string;
      dependencies: Record<string, string>;
    };
    const workspaceYaml = readFileSync(resolve(workspaceRoot, "pnpm-workspace.yaml"), "utf8");
    const notices = readFileSync(resolve(adminRoot, "THIRD_PARTY_NOTICES.md"), "utf8");

    expect(packageJson.name).toBe("@game-night/admin");
    expect(packageJson.dependencies["naive-ui"]).toBe("catalog:");
    expect(workspaceYaml).toMatch(/naive-ui:\s*2\.44\.1/);
    expect(notices).toContain("soybeanjs/soybean-admin@3d3613f20cd4add3cd20fd6cc884abead165c6d2");
    expect(existsSync(resolve(adminRoot, "licenses/soybean-admin-MIT.txt"))).toBe(true);
  });

  it("rejects forbidden direct dependencies", () => {
    const packageJson = JSON.parse(readFileSync(resolve(adminRoot, "package.json"), "utf8")) as {
      dependencies?: Record<string, string>;
      devDependencies?: Record<string, string>;
    };
    const combined = { ...(packageJson.dependencies ?? {}), ...(packageJson.devDependencies ?? {}) };
    const forbidden = Object.keys(combined).filter(
      (name) =>
        name.startsWith("@sa/") ||
        ["axios", "@iconify/vue", "@better-scroll/core", "better-scroll"].includes(name)
    );
    expect(forbidden).toEqual([]);
  });
});
