<script setup lang="ts">
import { NCard, NTag } from "naive-ui";
import type { ReadinessState } from "../../api/readiness";

defineProps<{
  title: string;
  readiness: ReadinessState | null;
}>();

// Unknown backend component identifiers remain visible instead of being silently dropped.
const componentLabels: Record<string, string> = {
  postgresql: "数据库",
  redis: "缓存",
  keyring: "密钥环",
  bootstrap: "启动协调",
  checkpoint: "审计检查点",
};

const formatMode = (mode: string): string => {
  if (mode === "sensitive_write") {
    return "敏感写入";
  }
  return mode === "ordinary" ? "普通读取" : mode;
};

const formatState = (state: string): string => {
  if (state === "ready") {
    return "可用";
  }
  return state === "unavailable" ? "不可用" : state;
};
</script>

<template>
  <NCard class="admin-card-shell admin-section-card" :title="title">
    <div v-if="readiness" class="admin-grid">
      <div class="admin-toolbar">
        <span class="admin-muted">检查类型：{{ formatMode(readiness.mode) }}</span>
        <NTag :type="readiness.ready ? 'success' : 'warning'">{{ readiness.ready ? "可用" : "不可用" }}</NTag>
      </div>
      <div class="admin-list">
        <div v-for="(state, component) in readiness.components" :key="component" class="admin-list__row">
          <span>{{ componentLabels[component] ?? component }}</span>
          <span>{{ formatState(state) }}</span>
        </div>
      </div>
    </div>
    <p v-else class="admin-muted">尚未读取服务状态。</p>
  </NCard>
</template>
