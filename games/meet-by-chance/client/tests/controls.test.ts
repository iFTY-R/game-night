import { describe, expect, it } from "vitest";

import { formatTicks, matchKindLabel, special235Label } from "../src/controls";
import { MatchKind, Special235Outcome } from "../src/generated/game/meet_by_chance/v1/meet_by_chance_pb";

describe("meet-by-chance presentation helpers", () => {
  it("formats quarter-unit penalties without floating-point rule math", () => {
    expect(formatTicks(2)).toBe("0.5 单位");
    expect(formatTicks(3)).toBe("0.75 单位");
    expect(formatTicks(4)).toBe("1 单位");
  });

  it("maps only authoritative match and 235 outcomes", () => {
    expect(matchKindLabel(MatchKind.EXACT)).toBe("完全同牌");
    expect(matchKindLabel(MatchKind.HIGHEST)).toBe("多人同大");
    expect(matchKindLabel(MatchKind.LOWEST)).toBe("多人同小");
    expect(special235Label(Special235Outcome.BEATS_LEOPARDS)).toContain("克制");
    expect(special235Label(Special235Outcome.MINIMUM_SINGLE)).toContain("最小散骰");
  });
});
