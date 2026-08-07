<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
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

const statusCycleMs = 2200;
const statusIndex = ref(0);
const detailsOpen = ref(false);
let statusTimer: number | undefined;

// The card keeps game-specific status copy visible while turn remains a separate semantic and visual state.
const statusText = computed(() => props.seat.status ?? (props.seat.connected ? "在桌" : "离线中"));
const turnStatusText = computed(() => {
  if (!props.seat.turn) {
    return "";
  }
  return props.self ? "轮到你" : "行动中";
});

const normalizeInfoItems = (items: readonly string[]): readonly string[] => {
  const seen = new Set<string>();
  return items
    .map((item) => item.trim())
    .filter((item) => {
      if (item.length === 0 || seen.has(item)) return false;
      seen.add(item);
      return true;
    });
};

// The caller can split dense business state into short carousel beats without losing the full status in aria/details.
const statusItems = computed(() => {
  const explicitItems = normalizeInfoItems(props.seat.statusItems ?? []);
  return explicitItems.length > 0 ? explicitItems : [statusText.value];
});
const visibleStatusText = computed(() => statusItems.value[statusIndex.value] ?? statusText.value);
const roleInfoItems = computed(() => normalizeInfoItems([
  props.self ? "你的座位" : "",
  props.seat.host ? "房主" : "",
  props.seat.connected ? "" : "已断线",
  turnStatusText.value,
]));
// Explicit status items already carry the complete status breakdown; adding the composite status here duplicated the same labels in the popover.
const detailItems = computed(() => normalizeInfoItems([...statusItems.value, ...roleInfoItems.value]));
const detailId = computed(() => `gn-seat-info-${props.seat.userId.replace(/[^a-zA-Z0-9_-]/g, "-")}-${props.seat.seatIndex}`);
const detailHint = computed(() => detailItems.value.length > 1 ? "点击查看完整信息" : "点击查看状态");

const stopStatusTimer = (): void => {
  if (statusTimer === undefined) return;
  window.clearInterval(statusTimer);
  statusTimer = undefined;
};

const syncStatusTimer = (): void => {
  stopStatusTimer();
  if (typeof window === "undefined" || statusItems.value.length <= 1) return;
  // One lightweight timer per visible seat keeps the text readable without changing table geometry.
  statusTimer = window.setInterval(() => {
    statusIndex.value = (statusIndex.value + 1) % statusItems.value.length;
  }, statusCycleMs);
};

watch(statusItems, () => {
  statusIndex.value = 0;
  syncStatusTimer();
}, { immediate: true });
watch(() => props.seat.userId, () => {
  // Reused DOM nodes must not keep another player's opened detail card after room or turn changes.
  detailsOpen.value = false;
});
onBeforeUnmount(stopStatusTimer);

const toggleDetails = (): void => {
  detailsOpen.value = !detailsOpen.value;
};

/** Keeps the popover open while focus moves into a projected action such as host transfer. */
const handleFocusOut = (event: FocusEvent): void => {
  const nextTarget = event.relatedTarget;
  if (nextTarget instanceof Node && (event.currentTarget as HTMLElement).contains(nextTarget)) return;
  detailsOpen.value = false;
};

// The spoken label keeps one canonical player name while surfacing turn, host, and connection state that would otherwise be icon-only.
const ariaLabel = computed(() =>
  `${props.self ? "你的座位，" : ""}${props.seat.displayName}${props.seat.host ? "，房主" : ""}${props.seat.connected ? "" : "，已断线"}${turnStatusText.value ? `，${turnStatusText.value}` : ""}，状态${statusText.value}`,
);
</script>

<template>
  <article
    class="gn-seat"
    :class="[`is-edge-${edge}`, { 'is-active': seat.active, 'is-turn': seat.turn, 'is-self': self, 'is-offline': !seat.connected }]"
    :aria-label="ariaLabel"
    @focusout="handleFocusOut"
  >
    <span class="gn-seat__connector" aria-hidden="true" />
    <button
      class="gn-seat__card"
      type="button"
      :aria-label="`${ariaLabel}，${detailHint}`"
      :aria-expanded="detailsOpen"
      :aria-controls="detailId"
      @click="toggleDetails"
      @keydown.escape.stop="detailsOpen = false"
    >
      <span class="gn-seat__avatar" aria-hidden="true">{{ seat.avatarText ?? seat.displayName.slice(0, 1) }}</span>
      <span class="gn-seat__copy">
        <strong class="gn-seat__display-name">{{ seat.displayName }}</strong>
        <small class="gn-seat__status" :aria-label="`状态：${statusText}`">
          <span :key="visibleStatusText" class="gn-seat__status-text">{{ visibleStatusText }}</span>
        </small>
      </span>
      <span v-if="self || seat.host || !seat.connected" class="gn-seat__indicators">
        <span v-if="self" class="gn-seat__indicator" role="img" aria-label="你的座位">
          <UserRound :size="11" aria-hidden="true" />
        </span>
        <span v-if="seat.host" class="gn-seat__indicator gn-seat__indicator--host" role="img" aria-label="房主">
          <Crown :size="11" aria-hidden="true" />
        </span>
        <span v-if="!seat.connected" class="gn-seat__indicator gn-seat__indicator--offline" role="img" aria-label="已断线">
          <WifiOff :size="11" aria-hidden="true" />
        </span>
      </span>
    </button>
    <div v-if="detailsOpen" :id="detailId" class="gn-seat__details" role="dialog" :aria-label="`${seat.displayName} 的完整信息`">
      <strong>{{ seat.displayName }}</strong>
      <ul>
        <li v-for="item in detailItems" :key="item">{{ item }}</li>
      </ul>
      <slot name="details" :seat="seat" :self="self" />
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
  width: 100%;
  min-height: inherit;
  display: grid;
  grid-template-columns: 30px minmax(4em, 1fr);
  align-items: center;
  gap: 5px;
  padding: 5px 19px 5px 7px;
  font: inherit;
  text-align: left;
  color: inherit;
  background: color-mix(in srgb, var(--platform-surface-raised, #1b292d) 90%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-muted, #a8b5b4) 34%, transparent);
  border-radius: 8px;
  box-shadow: 0 8px 20px rgb(0 0 0 / 20%);
  cursor: pointer;
  transition: border-color 180ms ease, box-shadow 180ms ease, background-color 180ms ease;
}

.gn-seat__card:focus-visible {
  outline: 2px solid color-mix(in srgb, var(--platform-focus, #8bd7d2) 78%, transparent);
  outline-offset: 3px;
}

.gn-seat.is-active .gn-seat__card {
  border-color: var(--platform-accent, #e6b566);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--platform-accent, #e6b566) 28%, transparent), 0 10px 26px rgb(0 0 0 / 30%);
}

.gn-seat.is-turn .gn-seat__card {
  border-color: var(--game-turn, var(--platform-accent, #e6b566));
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--game-turn, var(--platform-accent, #e6b566)) 44%, transparent), 0 12px 28px rgb(0 0 0 / 34%);
}

/* The pulse is a paint-only overlay, so the active turn never changes seat geometry or pointer hit areas. */
.gn-seat.is-turn .gn-seat__card::after {
  position: absolute;
  inset: -5px;
  border: 2px solid color-mix(in srgb, var(--game-turn, var(--platform-accent, #e6b566)) 66%, transparent);
  border-radius: 11px;
  box-shadow: 0 0 18px color-mix(in srgb, var(--game-turn, var(--platform-accent, #e6b566)) 42%, transparent);
  content: "";
  opacity: 0.72;
  pointer-events: none;
  transform: scale(0.97);
  animation: gn-seat-turn-pulse var(--game-turn-pulse-duration, 1.8s) ease-in-out infinite;
}

.gn-seat.is-self .gn-seat__card {
  background: color-mix(in srgb, var(--game-table, #173b38) 42%, var(--platform-surface-raised, #1b292d));
}

.gn-seat.is-active.is-self .gn-seat__card {
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--platform-accent, #e6b566) 36%, transparent), 0 12px 28px rgb(0 0 0 / 34%);
}

.gn-seat.is-turn.is-self .gn-seat__card {
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--game-turn, var(--platform-accent, #e6b566)) 52%, transparent), 0 12px 28px rgb(0 0 0 / 34%);
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

.gn-seat__status-text {
  display: block;
  animation: gn-seat-status-enter 260ms ease-out;
}

.gn-seat__details {
  position: absolute;
  z-index: 5;
  width: max-content;
  max-width: min(210px, calc(100vw - 24px));
  padding: 8px 9px;
  color: var(--platform-ink, #f5f1e8);
  background:
    linear-gradient(145deg, color-mix(in srgb, var(--platform-surface-raised, #1b292d) 96%, transparent), color-mix(in srgb, var(--game-table, #173b38) 42%, var(--platform-surface-raised, #1b292d))),
    var(--platform-surface-raised, #1b292d);
  border: 1px solid color-mix(in srgb, var(--platform-accent, #e6b566) 42%, transparent);
  border-radius: 10px;
  box-shadow: 0 14px 34px rgb(0 0 0 / 36%);
  pointer-events: auto;
}

.gn-seat__details strong {
  display: block;
  margin-bottom: 5px;
  color: var(--platform-accent, #e6b566);
  font-size: 12px;
  line-height: 1.1;
}

.gn-seat__details ul {
  display: grid;
  gap: 3px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.gn-seat__details li {
  overflow-wrap: anywhere;
  color: var(--platform-muted, #a8b5b4);
  font-size: 11px;
  line-height: 1.25;
}

.gn-seat.is-edge-bottom .gn-seat__details {
  bottom: calc(100% + 8px);
  left: 50%;
  transform: translateX(-50%);
}

.gn-seat.is-edge-top .gn-seat__details {
  top: calc(100% + 8px);
  left: 50%;
  transform: translateX(-50%);
}

.gn-seat.is-edge-left .gn-seat__details {
  top: 50%;
  left: calc(100% + 8px);
  transform: translateY(-50%);
}

.gn-seat.is-edge-right .gn-seat__details {
  top: 50%;
  right: calc(100% + 8px);
  transform: translateY(-50%);
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

@keyframes gn-seat-turn-pulse {
  0%, 100% {
    opacity: 0.54;
    transform: scale(0.97);
  }

  50% {
    opacity: 1;
    transform: scale(1.025);
  }
}

@keyframes gn-seat-status-enter {
  from {
    opacity: 0;
    transform: translateY(3px);
  }

  to {
    opacity: 1;
    transform: translateY(0);
  }
}

@media (prefers-reduced-motion: reduce) {
  .gn-seat__card {
    transition: none;
  }

  .gn-seat__status-text {
    animation: none;
  }

  .gn-seat.is-turn .gn-seat__card::after {
    animation: none;
    opacity: 1;
    transform: none;
  }
}
</style>
