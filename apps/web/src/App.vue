<script setup lang="ts">
import { onMounted } from "vue";
import { RouterView } from "vue-router";

import { ThemeRuntime, safeTheme } from "@game-night/theme-system";

const themeRuntime = new ThemeRuntime();

onMounted(() => {
  // The shell starts with a verified built-in theme; remote themes are pinned by a game session later.
  themeRuntime.apply({ manifest: safeTheme, assets: new Map(), usedFallback: true, errorCode: null }, document.documentElement);
});
</script>

<template>
  <div class="app-root">
    <RouterView v-slot="{ Component, route }">
      <Transition name="page" mode="out-in">
        <component :is="Component" :key="route.fullPath" />
      </Transition>
    </RouterView>
  </div>
</template>
