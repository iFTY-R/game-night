<script setup lang="ts">
import { NButton, NResult } from "naive-ui";
import { useRouter } from "vue-router";

const router = useRouter();
</script>

<template>
  <div class="auth-shell">
    <NResult class="admin-card-shell auth-shell__card" status="404" title="页面不存在" description="请返回登录页或检查当前链接。">
      <template #footer>
        <NButton type="primary" @click="router.push({ name: 'auth-login' })">返回登录</NButton>
      </template>
    </NResult>
  </div>
</template>
