<script setup lang="ts">
import { computed } from "vue";
import { Crown, UserRound, WifiOff } from "lucide-vue-next";

import type { TableSeat } from "../types";

type SeatEdge = "top" | "right" | "bottom" | "left";

const props = withDefaults(defineProps<{
  seat: TableSeat;
  self?: boolean;
  edge?: SeatEdge;
}>(), {
  self: false,
  edge: "bottom",
});

// The card keeps the short room-presence copy in one place, while the turn marker stays outside the body so names still fit at narrow widths.
const statusText = computed(() => props.seat.status ?? (props.seat.connected ? "在桌" : "离线中"));
const turnMarkerText = computed(() => {
  if (!props.seat.active) {
    return "";
  }
  return props.self ? "轮到你" : "行动中";
});

// The spoken label keeps one canonical player name while surfacing turn, host, and connection state that would otherwise be icon-only.
const ariaLabel = computed(() =>
  `${props.self ? "你的座位，" : ""}${props.seat.displayName}${props.seat.host ? "，房主" : ""}${props.seat.connected ? "" : "，已断线"}${turnMarkerText.value ? `，${turnMarkerText.value}` : ""}，状态${statusText.value}`,
);
</script>

<template>
  <article
    class="gn-seat"
    :class="[`is-edge-${edge}`, { 'is-active': seat.active, 'is-self': self, 'is-offline': !seat.connected }]"
    :aria-label="ariaLabel"
  >
    <span v-if="turnMarkerText" class="gn-seat__turn-marker">{{ turnMarkerText }}</span>
    <span class="gn-seat__connector" aria-hidden="true" />
    <div class="gn-seat__card">
      <span class="gn-seat__avatar" aria-hidden="true">{{ seat.avatarText ?? seat.displayName.slice(0, 1) }}</span>
      <span class="gn-seat__copy">
        <strong class="gn-seat__display-name">{{ seat.displayName }}</strong>
        <small class="gn-seat__status">{{ statusText }}</small>
      </span>
      <span v-if="self || seat.host || !seat.connected" class="gn-seat__indicators">
        <span v-if="self" class="gn-seat__indicator" aria-label="你的座位">
          <UserRound :size="11" aria-hidden="true" />
        </span>
        <span v-if="seat.host" class="gn-seat__indicator gn-seat__indicator--host" aria-label="房主">
          <Crown :size="11" aria-hidden="true" />
        </span>
        <span v-if="!seat.connected" class="gn-seat__indicator gn-seat__indicator--offline" aria-label="已断线">
          <WifiOff :size="11" aria-hidden="true" />
        </span>
      </span>
    </div>
  </article>
</template>

<style scoped>
.gn-seat {
  position: relative;
  width: var(--gn-seat-width, 118px);
  min-height: max(var(--gn-seat-height, 50px), var(--platform-touch-min, 48px));
  color: var(--platform-ink, #f5f1e8);
  transform: translate(-50%, -50%);
  overflow: visible;
}

.gn-seat__card {
  position: relative;
  min-height: inherit;
  display: grid;
  grid-template-columns: 30px minmax(4em, 1fr);
  align-items: center;
  gap: 5px;
  padding: 5px 19px 5px 7px;
  color: inherit;
  background: color-mix(in srgb, var(--platform-surface-raised, #1b292d) 90%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-muted, #a8b5b4) 34%, transparent);
  border-radius: 8px;
  box-shadow: 0 8px 20px rgb(0 0 0 / 20%);
  transition: border-color 180ms ease, box-shadow 180ms ease, background-color 180ms ease;
}

.gn-seat.is-active .gn-seat__card {
  border-color: var(--platform-accent, #e6b566);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--platform-accent, #e6b566) 28%, transparent), 0 10px 26px rgb(0 0 0 / 30%);
}

.gn-seat.is-self .gn-seat__card {
  background: color-mix(in srgb, var(--game-table, #173b38) 42%, var(--platform-surface-raised, #1b292d));
}

.gn-seat.is-active.is-self .gn-seat__card {
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--platform-accent, #e6b566) 36%, transparent), 0 12px 28px rgb(0 0 0 / 34%);
}

.gn-seat.is-offline .gn-seat__card {
  opacity: 0.76;
}

.gn-seat__avatar {
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  color: #151b1c;
  background: var(--platform-accent, #e6b566);
  font-weight: 800;
}

.gn-seat__copy {
  min-width: 0;
  display: grid;
  gap: 2px;
}

.gn-seat__display-name,
.gn-seat__status {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gn-seat__display-name {
  min-width: 4em;
  font-size: 12px;
  line-height: 1.2;
}

.gn-seat__status {
  color: var(--platform-muted, #a8b5b4);
  font-size: 10px;
  line-height: 1.1;
}

.gn-seat__indicators {
  position: absolute;
  top: 4px;
  right: 4px;
  display: grid;
  gap: 2px;
}

.gn-seat__indicator {
  width: 13px;
  height: 13px;
  display: grid;
  place-items: center;
  border-radius: 999px;
  color: var(--platform-ink, #f5f1e8);
  background: color-mix(in srgb, var(--platform-surface-raised, #1b292d) 88%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--platform-muted, #a8b5b4) 30%, transparent);
}

.gn-seat__indicator--host {
  color: var(--platform-accent, #e6b566);
}

.gn-seat__indicator--offline {
  color: var(--platform-muted, #a8b5b4);
}

.gn-seat__connector {
  position: absolute;
  background: color-mix(in srgb, var(--platform-accent, #e6b566) 45%, transparent);
  box-shadow: 0 0 0 1px rgb(0 0 0 / 12%);
}

.gn-seat__connector::after {
  content: "";
  position: absolute;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: color-mix(in srgb, var(--platform-accent, #e6b566) 72%, var(--platform-surface-raised, #1b292d));
}

.gn-seat__turn-marker {
  position: absolute;
  z-index: 1;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 44px;
  padding: 3px 8px;
  border-radius: 999px;
  border: 1px solid color-mix(in srgb, var(--platform-accent, #e6b566) 44%, transparent);
  color: color-mix(in srgb, var(--platform-accent, #e6b566) 88%, white);
  background: color-mix(in srgb, var(--platform-surface-raised, #1b292d) 92%, transparent);
  box-shadow: 0 8px 16px rgb(0 0 0 / 20%);
  font-size: 10px;
  font-weight: 700;
  line-height: 1;
  letter-spacing: 0;
  white-space: nowrap;
  pointer-events: none;
}

.gn-seat.is-edge-bottom .gn-seat__connector {
  top: -10px;
  left: 50%;
  width: 2px;
  height: 10px;
  transform: translateX(-50%);
}

.gn-seat.is-edge-bottom .gn-seat__connector::after {
  top: -3px;
  left: 50%;
  transform: translateX(-50%);
}

.gn-seat.is-edge-bottom .gn-seat__turn-marker {
  bottom: calc(100% + 14px);
  left: 50%;
  transform: translateX(-50%);
}

.gn-seat.is-edge-top .gn-seat__connector {
  bottom: -10px;
  left: 50%;
  width: 2px;
  height: 10px;
  transform: translateX(-50%);
}

.gn-seat.is-edge-top .gn-seat__connector::after {
  bottom: -3px;
  left: 50%;
  transform: translateX(-50%);
}

.gn-seat.is-edge-top .gn-seat__turn-marker {
  top: calc(100% + 14px);
  left: 50%;
  transform: translateX(-50%);
}

.gn-seat.is-edge-left .gn-seat__connector {
  top: 50%;
  right: -10px;
  width: 10px;
  height: 2px;
  transform: translateY(-50%);
}

.gn-seat.is-edge-left .gn-seat__connector::after {
  top: 50%;
  right: -3px;
  transform: translateY(-50%);
}

.gn-seat.is-edge-left .gn-seat__turn-marker {
  top: 50%;
  left: calc(100% + 14px);
  transform: translateY(-50%);
}

.gn-seat.is-edge-right .gn-seat__connector {
  top: 50%;
  left: -10px;
  width: 10px;
  height: 2px;
  transform: translateY(-50%);
}

.gn-seat.is-edge-right .gn-seat__connector::after {
  top: 50%;
  left: -3px;
  transform: translateY(-50%);
}

.gn-seat.is-edge-right .gn-seat__turn-marker {
  top: 50%;
  right: calc(100% + 14px);
  transform: translateY(-50%);
}

@media (max-width: 370px) {
  .gn-seat__card {
    padding-inline-end: 19px;
  }

  .gn-seat__turn-marker {
    min-width: 40px;
    padding-inline: 7px;
    font-size: 9px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .gn-seat__card {
    transition: none;
  }
}
</style>
