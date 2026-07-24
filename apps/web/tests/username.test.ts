import { describe, expect, it } from "vitest";

import { USERNAME_RULE_MESSAGE, normalizeUsernameInput, validateUsernameInput } from "../src/username";

describe("username helpers", () => {
  it("normalizes NFKC input before previewing the submitted display name", () => {
    expect(normalizeUsernameInput("  Ａ9界２  ")).toBe("A9界2");
  });

  it("accepts 2-4 Han, Latin, and decimal-digit code points", () => {
    expect(validateUsernameInput("Ab").isValid).toBe(true);
    expect(validateUsernameInput("玩家").isValid).toBe(true);
    expect(validateUsernameInput("A9界2")).toMatchObject({ normalized: "A9界2", codePointCount: 4, isValid: true });
    expect(USERNAME_RULE_MESSAGE).toBe("2-4 个汉字、英文字母或数字");
  });

  it("rejects whitespace punctuation emoji and overlong input", () => {
    expect(validateUsernameInput("你").isValid).toBe(false);
    expect(validateUsernameInput("A9界2玩").isValid).toBe(false);
    expect(validateUsernameInput("ab_1").isValid).toBe(false);
    expect(validateUsernameInput("ab c").isValid).toBe(false);
    expect(validateUsernameInput("ab😀").isValid).toBe(false);
    expect(validateUsernameInput("ab\u200d").isValid).toBe(false);
  });
});
