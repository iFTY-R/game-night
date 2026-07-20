<script setup lang="ts">
import { Crown, WifiOff } from "lucide-vue-next";

import type { TableSeat } from "../types";

defineProps<{
  seat: TableSeat;
  self?: boolean;
}>();
</script>

<template>
  <article
    class="gn-seat"
    :class="{ 'is-active': seat.active, 'is-self': self, 'is-offline': !seat.connected }"
    :aria-label="`${seat.displayName}${seat.active ? '，当前行动' : ''}${seat.connected ? '' : '，已断线'}`"
  >
    <span class="gn-seat__avatar" aria-hidden="true">{{ seat.avatarText ?? seat.displayName.slice(0, 1) }}</span>
    <span class="gn-seat__copy">
      <strong>{{ seat.displayName }}</strong>
      <small>{{ seat.status ?? (seat.connected ? "在桌" : "重连中") }}</small>
    </span>
    <Crown v-if="seat.host" class="gn-seat__mark" :size="15" aria-label="房主" />
    <WifiOff v-else-if="!seat.connected" class="gn-seat__mark" :size="15" aria-hidden="true" />
  </article>
</template>

<style scoped>
.gn-seat {
  width: var(--gn-seat-width, 116px);
  min-height: var(--platform-touch-min, 48px);
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr) 16px;
  align-items: center;
  gap: 7px;
  padding: 5px 7px;
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
  width: 34px;
  height: 34px;
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
  gap: 1px;
}

.gn-seat__copy strong,
.gn-seat__copy small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gn-seat__copy strong {
  font-size: 13px;
  line-height: 1.2;
}

.gn-seat__copy small {
  color: var(--platform-muted, #a8b5b4);
  font-size: 10px;
}

.gn-seat__mark {
  color: var(--platform-accent, #e6b566);
}

@media (max-width: 370px) {
  .gn-seat {
    --gn-seat-width: 104px;
    grid-template-columns: 30px minmax(0, 1fr) 14px;
  }

  .gn-seat__avatar {
    width: 30px;
    height: 30px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .gn-seat {
    transition: none;
  }
}
</style>
