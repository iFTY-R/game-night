<script setup lang="ts">
import { computed } from "vue";
import { NConfigProvider, NGlobalStyle, NMessageProvider, NNotificationProvider, darkTheme, zhCN } from "naive-ui";
import { RouterView } from "vue-router";
import { usePreferencesStore } from "./stores/preferences";
import { naiveThemeOverrides } from "./theme/naive-theme";

const preferences = usePreferencesStore();

const activeTheme = computed(() => (preferences.resolvedTheme === "dark" ? darkTheme : null));
</script>

<template>
  <NConfigProvider :locale="zhCN" :theme="activeTheme" :theme-overrides="naiveThemeOverrides(preferences.resolvedTheme)">
    <NGlobalStyle />
    <NMessageProvider>
      <NNotificationProvider>
        <RouterView />
      </NNotificationProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>
