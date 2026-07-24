<script setup lang="ts">
import { NButton, NTag } from "naive-ui";

defineProps<{
  recoveryCodes: string[];
  pending: boolean;
}>();

defineEmits<{
  confirm: [];
}>();
</script>

<template>
  <section class="admin-inset-section">
    <h3>恢复码接收确认</h3>
    <p class="admin-muted">请先离线保存恢复码；确认后将无法在当前页面再次查看。</p>
    <div class="admin-toolbar__cluster">
      <NTag v-for="code in recoveryCodes" :key="code" round>{{ code }}</NTag>
    </div>
    <NButton type="primary" block :loading="pending" @click="$emit('confirm')">确认已保存，进入后台</NButton>
  </section>
</template>
