<script setup lang="ts">
import { NButton, NResult } from "naive-ui";

defineProps<{
  loading: boolean;
}>();

defineEmits<{
  retry: [];
}>();
</script>

<template>
  <div class="admin-grid">
    <NResult
      status="info"
      title="等待部署初始化"
      description="管理员 bootstrap 还未准备好。完成离线 secret 注入后，这里会切换为登录入口。"
    >
      <template #footer>
        <NButton secondary :loading="loading" @click="$emit('retry')">重新检查</NButton>
      </template>
    </NResult>
  </div>
</template>
