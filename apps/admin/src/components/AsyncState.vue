<script setup lang="ts">
import { NButton, NEmpty, NResult, NSpin } from "naive-ui";

defineProps<{
  loading?: boolean;
  error?: string;
  empty?: boolean;
  emptyDescription?: string;
  retryLabel?: string;
}>();

defineEmits<{
  retry: [];
}>();
</script>

<template>
  <div v-if="loading" class="admin-card-shell admin-section-card">
    <NSpin size="large">
      <template #description>正在读取最新状态…</template>
    </NSpin>
  </div>
  <NResult
    v-else-if="error"
    class="admin-card-shell admin-section-card"
    status="error"
    title="操作没有完成"
    :description="error"
  >
    <template #footer>
      <NButton secondary @click="$emit('retry')">{{ retryLabel ?? "重试" }}</NButton>
    </template>
  </NResult>
  <NEmpty v-else-if="empty" class="admin-card-shell admin-section-card" :description="emptyDescription ?? '暂无数据'" />
  <slot v-else />
</template>
