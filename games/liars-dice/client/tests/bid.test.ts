import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { validateBidDraft } from "../src/bid";
import { BidMode, BidSchema, ViewSchema } from "../src/generated/game/liars_dice/v1/liars_dice_pb";
import { liarsDiceFixtureView } from "../src/fixture";

describe("validateBidDraft", () => {
  it("matches same-mode and strict/flying conversion anchors", () => {
    const view = liarsDiceFixtureView();
    expect(validateBidDraft(view, { quantity: 6, face: 5, mode: BidMode.FLYING }).valid).toBe(true);
    expect(validateBidDraft(view, { quantity: 6, face: 4, mode: BidMode.FLYING }).code).toBe("bid_not_higher");
    expect(validateBidDraft(view, { quantity: 4, face: 4, mode: BidMode.STRICT }).valid).toBe(true);

    const strictPrevious = create(ViewSchema, {
      ...view,
      currentBid: create(BidSchema, { quantity: 4, face: 3, mode: BidMode.STRICT }),
    });
    expect(validateBidDraft(strictPrevious, { quantity: 8, face: 3, mode: BidMode.FLYING }).valid).toBe(true);
    expect(validateBidDraft(strictPrevious, { quantity: 7, face: 6, mode: BidMode.FLYING }).code).toBe("conversion_low");
  });

  it("forces face one to strict and keeps over-total bids legal but explicit", () => {
    const { currentBid: _currentBid, ...withoutBid } = liarsDiceFixtureView();
    const view = create(ViewSchema, { ...withoutBid, hasCurrentBid: false });
    expect(validateBidDraft(view, { quantity: 4, face: 1, mode: BidMode.FLYING }).code).toBe("one_requires_strict");
    const risky = validateBidDraft(view, { quantity: 25, face: 2, mode: BidMode.FLYING });
    expect(risky.valid).toBe(true);
    expect(risky.risky).toBe(true);
  });
});
