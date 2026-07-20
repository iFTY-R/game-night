<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

import { computeSeatLayout } from "../layout";
import type { TableSeat, TableShape } from "../types";
import PlayerSeat from "./PlayerSeat.vue";

const props = withDefaults(
  defineProps<{
    seats: readonly TableSeat[];
    selfSeatIndex: number;
    shape?: TableShape;
    label?: string;
  }>(),
  { shape: "adaptive", label: "共同游戏桌" },
);

const root = ref<HTMLElement>();
const size = ref({ width: 390, height: 520 });
let observer: ResizeObserver | undefined;

const positions = computed(() =>
  computeSeatLayout({
    seatIndexes: props.seats.map((seat) => seat.seatIndex),
    selfSeatIndex: props.selfSeatIndex,
    width: size.value.width,
    height: size.value.height,
    seatWidth: size.value.width <= 370 ? 104 : 116,
    seatHeight: 50,
    shape: props.shape,
  }),
);

const positionedSeats = computed(() =>
  positions.value.map((position) => ({
    position,
    seat: props.seats.find((seat) => seat.seatIndex === position.seatIndex),
  })),
);

onMounted(() => {
  observer = new ResizeObserver(([entry]) => {
    if (entry !== undefined && entry.contentRect.width > 0 && entry.contentRect.height > 0) {
      size.value = { width: entry.contentRect.width, height: entry.contentRect.height };
    }
  });
  if (root.value !== undefined) {
    observer.observe(root.value);
  }
});

onBeforeUnmount(() => observer?.disconnect());
</script>

<template>
  <section ref="root" class="gn-table" :aria-label="label">
    <div class="gn-table__rail" aria-hidden="true" />
    <div class="gn-table__center">
      <slot name="center" />
    </div>
    <div
      v-for="item in positionedSeats"
      :key="item.position.seatIndex"
      class="gn-table__seat"
      :style="{ left: `${item.position.x}px`, top: `${item.position.y}px` }"
    >
      <PlayerSeat
        v-if="item.seat"
        :seat="item.seat"
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

.gn-table__center {
  position: absolute;
  inset: 28% 25% 34%;
  display: grid;
  place-items: center;
  text-align: center;
  z-index: 1;
}

.gn-table__seat {
  position: absolute;
  z-index: 2;
}

.gn-table__private {
  position: absolute;
  left: 50%;
  bottom: 3%;
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

  .gn-table__center {
    inset: 23% 31% 27%;
  }

  .gn-table__private {
    right: 3%;
    bottom: 1%;
    left: auto;
    transform: none;
  }
}
</style>
