<script setup lang="ts">
import { NButton, NResult } from "naive-ui";
import { useRouter } from "vue-router";

const router = useRouter();
</script>

<template>
  <NResult class="admin-card-shell admin-section-card" status="403" title="无权访问" description="当前会话没有打开此页面所需的权限。">
    <template #footer>
      <NButton type="primary" @click="router.push({ name: 'overview' })">返回概览</NButton>
    </template>
  </NResult>
</template>
