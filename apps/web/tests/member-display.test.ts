import { describe, expect, it } from "vitest";

import { memberDisplayName } from "../src/member-display";

describe("memberDisplayName", () => {
  it("prefers the server-projected username", () => {
    expect(memberDisplayName("user-a", " 小满 ")).toBe("小满");
  });

  it("uses the same stable fallback for every viewer", () => {
    const userId = "12345678-1234-4000-8000-123456789abc";
    expect(memberDisplayName(userId)).toBe("玩家 123456");
    expect(memberDisplayName(userId, "")).toBe("玩家 123456");
  });
});
