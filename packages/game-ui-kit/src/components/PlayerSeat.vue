<script setup lang="ts">
import { computed } from "vue";
import { Crown, WifiOff } from "lucide-vue-next";

import type { TableSeat } from "../types";

const props = defineProps<{
  seat: TableSeat;
  self?: boolean;
}>();

// The spoken label keeps one canonical player name while still telling the local player which seat is theirs.
const ariaLabel = computed(() =>
  `${props.self ? "你的座位，" : ""}${props.seat.displayName}${props.seat.active ? "，当前行动" : ""}${props.seat.connected ? "" : "，已断线"}`,
);
</script>

<template>
  <article
    class="gn-seat"
    :class="{ 'is-active': seat.active, 'is-self': self, 'is-offline': !seat.connected }"
    :aria-label="ariaLabel"
  >
    <span class="gn-seat__avatar" aria-hidden="true">{{ seat.avatarText ?? seat.displayName.slice(0, 1) }}</span>
    <span class="gn-seat__copy">
      <strong>{{ seat.displayName }}</strong>
      <small>{{ seat.status ?? (seat.connected ? "在桌" : "重连中") }}</small>
    </span>
    <Crown v-if="seat.host" class="gn-seat__mark" :size="14" aria-label="房主" />
    <WifiOff v-else-if="!seat.connected" class="gn-seat__mark gn-seat__mark--offline" :size="14" aria-hidden="true" />
  </article>
</template>

<style scoped>
.gn-seat {
  position: relative;
  width: var(--gn-seat-width, 118px);
  min-height: max(var(--gn-seat-height, 50px), var(--platform-touch-min, 48px));
  display: grid;
  grid-template-columns: 32px minmax(0, 1fr);
  align-items: center;
  gap: 7px;
  padding: 6px 24px 6px 8px;
  color: var(--platform-ink, #f5f1e8);
  background: color-mix(in srgb, var(--platform-surface-raised, #1b292d) 90%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-muted, #a8b5b4) 34%, transparent);
  border-radius: 7px;
  box-shadow: 0 8px 20px rgb(0 0 0 / 20%);
  transform: translate(-50%, -50%);
  transition: border-color 180ms ease, box-shadow 180ms ease;
}

.gn-seat.is-active {
  border-color: var(--platform-accent, #e6b566);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--platform-accent, #e6b566) 24%, transparent), 0 9px 24px rgb(0 0 0 / 28%);
}

.gn-seat.is-self {
  background: color-mix(in srgb, var(--game-table, #173b38) 42%, var(--platform-surface-raised, #1b292d));
}

.gn-seat.is-offline {
  opacity: 0.72;
}

.gn-seat__avatar {
  width: 32px;
  height: 32px;
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

.gn-seat__copy strong,
.gn-seat__copy small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gn-seat__copy strong {
  font-size: 12px;
  line-height: 1.2;
}

.gn-seat__copy small {
  color: var(--platform-muted, #a8b5b4);
  font-size: 10px;
  line-height: 1.1;
}

.gn-seat__mark {
  position: absolute;
  top: 6px;
  right: 6px;
  width: 16px;
  height: 16px;
  padding: 1px;
  border-radius: 999px;
  color: var(--platform-accent, #e6b566);
  background: color-mix(in srgb, var(--platform-surface-raised, #1b292d) 88%, transparent);
}

.gn-seat__mark--offline {
  color: var(--platform-muted, #a8b5b4);
}

@media (max-width: 370px) {
  .gn-seat {
    width: var(--gn-seat-width, 118px);
  }
}

@media (prefers-reduced-motion: reduce) {
  .gn-seat {
    transition: none;
  }
}
</style>
