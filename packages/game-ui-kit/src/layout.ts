import type { SeatPosition, TableShape } from "./types";

export interface SeatLayoutInput {
  readonly seatIndexes: readonly number[];
  readonly selfSeatIndex: number;
  readonly width: number;
  readonly height: number;
  readonly seatWidth: number;
  readonly seatHeight: number;
  readonly shape: TableShape;
}

// computeSeatLayout keeps logical order stable while adapting only the table geometry to each viewport.
export const computeSeatLayout = (input: SeatLayoutInput): readonly SeatPosition[] => {
  const seats = [...input.seatIndexes].sort((left, right) => left - right);
  if (
    seats.length < 2 ||
    seats.length > 12 ||
    new Set(seats).size !== seats.length ||
    !seats.includes(input.selfSeatIndex) ||
    input.width <= input.seatWidth ||
    input.height <= input.seatHeight
  ) {
    throw new RangeError("Seat layout input is invalid");
  }
  const selfOffset = seats.indexOf(input.selfSeatIndex);
  const ordered = [...seats.slice(selfOffset), ...seats.slice(0, selfOffset)];
  const portrait = input.height >= input.width;
  const resolvedShape = resolveShape(input.shape, seats.length, portrait);
  const horizontalPadding = Math.max(input.seatWidth / 2 + 8, 34);
  const verticalPadding = Math.max(input.seatHeight / 2 + 8, 34);
  const radiusX = Math.max((input.width - horizontalPadding * 2) / 2, 1);
  // Landscape uses the full safe radius; its private zone is moved beside the local seat instead of stacked below it.
  const shapeScale = resolvedShape === "compact-oval" ? (portrait ? 0.7 : 1) : resolvedShape === "elongated-oval" ? 0.94 : 0.82;
  const radiusY = Math.max((input.height - verticalPadding * 2) / 2 * shapeScale, 1);
  const centerX = input.width / 2;
  const centerY = input.height / 2;
  const exponent = resolvedShape === "rounded-table" ? 0.65 : 1;

  return ordered.map((seatIndex, logicalIndex) => {
    // The local player begins at the bottom; clockwise rank remains unchanged across viewport rotation.
    const angle = Math.PI / 2 + (logicalIndex * Math.PI * 2) / ordered.length;
    const cosine = Math.cos(angle);
    const sine = Math.sin(angle);
    const projectedX = Math.sign(cosine) * Math.abs(cosine) ** exponent;
    const projectedY = Math.sign(sine) * Math.abs(sine) ** exponent;
    return {
      seatIndex,
      x: clamp(centerX + radiusX * projectedX, horizontalPadding, input.width - horizontalPadding),
      y: clamp(centerY + radiusY * projectedY, verticalPadding, input.height - verticalPadding),
      angle,
    };
  });
};

const resolveShape = (shape: TableShape, count: number, portrait: boolean): Exclude<TableShape, "adaptive"> => {
  if (shape !== "adaptive") {
    return shape;
  }
  if (count <= 4) {
    return "compact-oval";
  }
  if (count <= 6) {
    return portrait ? "elongated-oval" : "compact-oval";
  }
  return count >= 9 ? "rounded-table" : "elongated-oval";
};

const clamp = (value: number, minimum: number, maximum: number): number => Math.min(Math.max(value, minimum), maximum);
