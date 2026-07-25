<script setup lang="ts">
import { ChevronRight } from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

import { computeSeatLayout } from "../layout";
import type { TableSeat, TableShape, TableTurnDirection } from "../types";
import PlayerSeat from "./PlayerSeat.vue";

type SeatEdge = "top" | "right" | "bottom" | "left";

const props = withDefaults(
  defineProps<{
    seats: readonly TableSeat[];
    selfSeatIndex: number;
    shape?: TableShape;
    label?: string;
    bottomInset?: number;
    seatWidth?: number;
    seatHeight?: number;
    turnDirection?: TableTurnDirection | undefined;
  }>(),
  { shape: "adaptive", label: "共同游戏桌", bottomInset: 0, seatWidth: 118, seatHeight: 50 },
);

const root = ref<HTMLElement>();
const size = ref({ width: 390, height: 520 });
// Shared seats keep one compact geometry so every game view can align cards, action trays, and hit areas consistently.
const resolvedSeatHeight = computed(() => Math.max(props.seatHeight, 48));
// The center card only needs to react to the tray overflow beyond the local seat stack; larger shifts would make the table feel detached.
const centerShiftRatio = 0.35;
const maxCenterShift = 96;
let observer: ResizeObserver | undefined;

const resolvedSeatWidth = computed(() => Math.max(props.seatWidth, 48));
const showTurnDirection = computed(() => props.turnDirection !== undefined && props.seats.length > 1);
const turnDirectionLabel = computed(() => props.turnDirection === "counterclockwise" ? "游戏顺序：逆时针" : "游戏顺序：顺时针");

const safeBottomInset = computed(() => Math.max(0, Math.min(props.bottomInset, Math.max(size.value.height - resolvedSeatHeight.value - 1, 0))));
const safeCenterShift = computed(() => {
  const trayOverflow = Math.max(safeBottomInset.value - resolvedSeatHeight.value - 18, 0);
  return Math.min(Math.round(trayOverflow * centerShiftRatio), Math.floor(size.value.height * 0.16), maxCenterShift);
});
const tableStyle = computed(() => ({
  "--gn-safe-bottom": `${safeBottomInset.value}px`,
  "--gn-safe-center-shift": `${safeCenterShift.value}px`,
}));

const positions = computed(() =>
  props.seats.length === 0
    ? []
    : props.seats.length === 1
      ? [{
        seatIndex: props.seats[0]?.seatIndex ?? props.selfSeatIndex,
        x: size.value.width / 2,
        y: Math.max(resolvedSeatHeight.value / 2 + 24, size.value.height - safeBottomInset.value - resolvedSeatHeight.value / 2 - 18),
        angle: Math.PI / 2,
      }]
      : computeSeatLayout({
        seatIndexes: props.seats.map((seat) => seat.seatIndex),
        selfSeatIndex: props.selfSeatIndex,
        width: size.value.width,
        height: Math.max(size.value.height - safeBottomInset.value, resolvedSeatHeight.value + 1),
        seatWidth: resolvedSeatWidth.value,
        seatHeight: resolvedSeatHeight.value,
        shape: props.shape,
      }),
);

const positionedSeats = computed(() =>
  positions.value.map((position) => ({
    position,
    seat: props.seats.find((seat) => seat.seatIndex === position.seatIndex),
    edge: resolveSeatEdge(position.angle),
  })),
);

onMounted(() => {
  observer = new ResizeObserver(([entry]) => {
    if (
      entry !== undefined &&
      entry.contentRect.width > resolvedSeatWidth.value &&
      entry.contentRect.height > resolvedSeatHeight.value + safeBottomInset.value
    ) {
      // Route and orientation transitions may briefly collapse the table; preserve the last valid positions during that frame.
      size.value = { width: entry.contentRect.width, height: entry.contentRect.height };
    }
  });
  if (root.value !== undefined) {
    observer.observe(root.value);
  }
});

onBeforeUnmount(() => observer?.disconnect());

// Seats keep their text horizontal, so the radial layout angle is reduced to the nearest outward edge for connector and marker placement.
const resolveSeatEdge = (angle: number): SeatEdge => {
  const cosine = Math.cos(angle);
  const sine = Math.sin(angle);
  if (Math.abs(sine) >= Math.abs(cosine)) {
    return sine >= 0 ? "bottom" : "top";
  }
  return cosine >= 0 ? "right" : "left";
};
</script>

<template>
  <section ref="root" class="gn-table" :style="tableStyle" :aria-label="label">
    <div class="gn-table__rail" aria-hidden="true" />
    <span v-if="showTurnDirection" class="gn-table__sr-only">{{ turnDirectionLabel }}</span>
    <div
      v-if="showTurnDirection"
      class="gn-table__turn-order"
      :class="`gn-table__turn-order--${turnDirection}`"
      aria-hidden="true"
    >
      <span class="gn-table__turn-runner"><ChevronRight :size="18" :stroke-width="3" /></span>
    </div>
    <div class="gn-table__center">
      <slot name="center" />
    </div>
    <div
      v-for="item in positionedSeats"
      :key="item.position.seatIndex"
      class="gn-table__seat"
      :style="{ left: `${item.position.x}px`, top: `${item.position.y}px`, '--gn-seat-width': `${resolvedSeatWidth}px`, '--gn-seat-height': `${resolvedSeatHeight}px` }"
    >
      <PlayerSeat
        v-if="item.seat"
        :seat="item.seat"
        :edge="item.edge"
        :self="item.position.seatIndex === selfSeatIndex"
      />
    </div>
    <div class="gn-table__private">
      <slot name="private" />
    </div>
  </section>
</template>

<style scoped>
.gn-table {
  position: relative;
  width: 100%;
  height: 100%;
  min-height: 360px;
  overflow: hidden;
  isolation: isolate;
}

.gn-table__rail {
  position: absolute;
  inset: 11% 10% 13%;
  border: 2px solid color-mix(in srgb, var(--platform-accent, #e6b566) 42%, transparent);
  border-radius: 44% / 38%;
  background:
    repeating-linear-gradient(115deg, rgb(255 255 255 / 2%) 0 1px, transparent 1px 7px),
    var(--game-table, #173b38);
  box-shadow: inset 0 0 0 7px rgb(0 0 0 / 14%), inset 0 18px 36px rgb(255 255 255 / 3%), 0 22px 46px rgb(0 0 0 / 24%);
}

.gn-table__sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

/* The inset keeps the runner on the felt; seat cards stay above it so the orbit passes behind players without covering names. */
.gn-table__turn-order {
  position: absolute;
  z-index: 1;
  inset: calc(11% + 10px) calc(10% + 10px) calc(13% + 10px);
  pointer-events: none;
}

.gn-table__turn-runner {
  position: absolute;
  top: 0;
  left: 0;
  width: 24px;
  height: 24px;
  display: grid;
  place-items: center;
  color: #151b1c;
  background: var(--game-direction, var(--platform-accent, #e6b566));
  border: 1px solid color-mix(in srgb, var(--game-direction, var(--platform-accent, #e6b566)) 76%, white);
  border-radius: 50%;
  box-shadow: 0 0 16px color-mix(in srgb, var(--game-direction, var(--platform-accent, #e6b566)) 58%, transparent);
  offset-path: ellipse(50% 50% at 50% 50%);
  offset-rotate: auto;
  animation: gn-table-turn-orbit var(--game-turn-orbit-duration, 5.2s) linear infinite;
  will-change: offset-distance;
}

.gn-table__turn-order--counterclockwise .gn-table__turn-runner {
  animation-direction: reverse;
}

.gn-table__turn-order--counterclockwise .gn-table__turn-runner svg {
  transform: rotate(180deg);
}

.gn-table__center {
  position: absolute;
  inset: 24% 21% calc(29% + min(var(--gn-safe-bottom), 108px)) 21%;
  display: grid;
  place-items: center;
  text-align: center;
  z-index: 1;
  transform: translateY(calc(var(--gn-safe-center-shift, 0px) * -1));
}

.gn-table__seat {
  position: absolute;
  z-index: 2;
}

.gn-table__private {
  position: absolute;
  left: 50%;
  bottom: calc(3% + min(var(--gn-safe-bottom), 132px));
  z-index: 3;
  transform: translateX(-50%);
}

@media (orientation: landscape) {
  .gn-table {
    min-height: 0;
  }

  .gn-table__rail {
    inset: 9% 13% 10%;
    border-radius: 39% / 44%;
  }

  .gn-table__turn-order {
    inset: calc(9% + 9px) calc(13% + 9px) calc(10% + 9px);
  }

  .gn-table__center {
    inset: 20% 27% calc(21% + min(var(--gn-safe-bottom), 72px)) 27%;
  }

  .gn-table__private {
    right: 3%;
    bottom: 1%;
    left: auto;
    transform: none;
  }
}

@keyframes gn-table-turn-orbit {
  from { offset-distance: 0%; }
  to { offset-distance: 100%; }
}

@media (prefers-reduced-motion: reduce) {
  .gn-table__turn-runner {
    animation: none;
    offset-distance: 75%;
    will-change: auto;
  }
}
</style>
