<script setup lang="ts">
import { CloudOff, LoaderCircle, Radio, RotateCw } from "lucide-vue-next";
import { computed } from "vue";

import type { ConnectionState } from "../types";

const props = defineProps<{ state: ConnectionState }>();

const copy = computed(() => ({
  online: "已连接",
  reconnecting: "重连中",
  offline: "已离线",
  draining: "服务切换",
})[props.state]);

const icon = computed(() => ({
  online: Radio,
  reconnecting: RotateCw,
  offline: CloudOff,
  draining: LoaderCircle,
})[props.state]);
</script>

<template>
  <span class="gn-connection" :class="`is-${state}`" role="status" aria-live="polite">
    <component :is="icon" :size="14" aria-hidden="true" />
    <span>{{ copy }}</span>
  </span>
</template>

<style scoped>
.gn-connection {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-height: 28px;
  padding: 3px 7px;
  color: var(--platform-muted, #a8b5b4);
  border: 1px solid color-mix(in srgb, currentColor 24%, transparent);
  border-radius: 6px;
  font-size: 11px;
}

.gn-connection.is-online {
  color: #99d8b1;
}

.gn-connection.is-reconnecting,
.gn-connection.is-draining {
  color: var(--platform-accent, #e6b566);
}

.gn-connection.is-offline {
  color: var(--platform-danger, #e77c65);
}

.gn-connection.is-reconnecting svg,
.gn-connection.is-draining svg {
  animation: gn-spin 1.3s linear infinite;
}

@keyframes gn-spin {
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .gn-connection svg {
    animation: none !important;
  }
}
</style>
