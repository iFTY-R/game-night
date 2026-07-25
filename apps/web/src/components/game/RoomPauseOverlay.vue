<script setup lang="ts">
import { CirclePause } from "lucide-vue-next";

defineProps<{ pausedByName: string; requestedByName?: string | undefined }>();
</script>

<template>
  <section class="room-pause" role="status" aria-live="polite" aria-label="游戏已暂停">
    <CirclePause :size="21" aria-hidden="true" />
    <strong>游戏已暂停</strong>
    <span>{{ requestedByName ? `${requestedByName} 申请 · ${pausedByName} 已批准` : `${pausedByName} 已暂停` }}</span>
  </section>
</template>

<style scoped>
.room-pause { position: fixed; z-index: 24; top: 50%; left: 50%; width: max-content; max-width: calc(100vw - 40px); display: grid; grid-template-columns: auto auto; align-items: center; gap: 2px 8px; padding: 9px 12px; color: var(--platform-ink); background: color-mix(in srgb, var(--platform-surface-raised) 92%, transparent); border-block: 1px solid color-mix(in srgb, var(--platform-accent) 50%, transparent); box-shadow: 0 12px 36px rgb(0 0 0 / 32%); transform: translate(-50%, -50%); pointer-events: none; backdrop-filter: blur(10px); }
.room-pause svg { grid-row: 1 / 3; color: var(--platform-accent); }
.room-pause strong { font-size: 14px; letter-spacing: 0; }
.room-pause span { overflow: hidden; color: var(--platform-muted); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
</style>
