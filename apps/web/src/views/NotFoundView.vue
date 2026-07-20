<script setup lang="ts">
import { ArrowLeft } from "lucide-vue-next";
</script>

<template>
  <main class="screen-shell not-found">
    <img src="/brand-mark.svg" alt="" />
    <p class="eyebrow">404</p>
    <h1 class="display-title">这张桌不存在。</h1>
    <p class="muted">房间可能已经关闭，或者邀请链接已经失效。</p>
    <RouterLink class="button" to="/"><ArrowLeft :size="18" aria-hidden="true" /> 返回首页</RouterLink>
  </main>
</template>

<style scoped>
.not-found { display: grid; place-content: center; justify-items: start; gap: 16px; }
.not-found img { width: 64px; height: 64px; border-radius: 14px; }
.not-found .display-title { max-width: 620px; }
</style>
