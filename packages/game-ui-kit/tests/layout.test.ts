import { describe, expect, it } from "vitest";

import { computeSeatLayout } from "../src";

describe("computeSeatLayout", () => {
  it("keeps the local seat at the bottom in portrait and landscape", () => {
    for (const [width, height] of [[390, 560], [760, 300]] as const) {
      const result = computeSeatLayout({
        seatIndexes: [4, 1, 3, 2],
        selfSeatIndex: 3,
        width,
        height,
        seatWidth: 116,
        seatHeight: 50,
        shape: "adaptive",
      });
      const self = result.find((seat) => seat.seatIndex === 3);
      expect(self?.y).toBe(Math.max(...result.map((seat) => seat.y)));
      expect(result.map((seat) => seat.seatIndex)).toEqual([3, 4, 1, 2]);
    }
  });

  it("produces the same logical order regardless of input order", () => {
    const input = { selfSeatIndex: 2, width: 390, height: 560, seatWidth: 116, seatHeight: 50, shape: "adaptive" as const };
    const first = computeSeatLayout({ ...input, seatIndexes: [5, 2, 9, 7] });
    const second = computeSeatLayout({ ...input, seatIndexes: [9, 7, 2, 5] });
    expect(second).toEqual(first);
  });

  it("rejects duplicate or unsupported seats", () => {
    expect(() => computeSeatLayout({
      seatIndexes: [1, 1], selfSeatIndex: 1, width: 390, height: 560, seatWidth: 116, seatHeight: 50, shape: "adaptive",
    })).toThrow(RangeError);
  });
});
