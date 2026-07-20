import { describe, expect, it } from "vitest";

import { fixedViewports, fixtureProjection, fixtureSeats } from "../src";

describe("fixed visual fixtures", () => {
  it("keeps the required mobile baselines stable", () => {
    expect(fixedViewports).toEqual({
      portrait: { width: 390, height: 844 },
      landscape: { width: 844, height: 390 },
      androidSmall: { width: 360, height: 740 },
    });
    expect(fixtureSeats).toHaveLength(4);
    expect(fixtureProjection().stateVersion).toBe(12);
  });
});
