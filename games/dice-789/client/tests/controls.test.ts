import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { legalAddValues } from "../src/controls";
import { ActionConstraintsSchema } from "../src/generated/game/dice789/v1/dice_789_pb";

describe("legalAddValues", () => {
  it("keeps step values and the final capacity remainder", () => {
    const constraints = create(ActionConstraintsSchema, {
      minimumAddTicks: 2,
      maximumAddTicks: 5,
      addStepTicks: 2,
      allowCapacityRemainder: true,
    });
    expect(legalAddValues(constraints)).toEqual([2, 4, 5]);
  });

  it("resolves a full pool to the server-authorized zero amount", () => {
    expect(legalAddValues(create(ActionConstraintsSchema))).toEqual([0]);
  });
});
