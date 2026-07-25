<script setup lang="ts">
import { computed, watchEffect } from "vue";
import { NConfigProvider, NGlobalStyle, NMessageProvider, NNotificationProvider, darkTheme, zhCN } from "naive-ui";
import { RouterView } from "vue-router";
import { usePreferencesStore } from "./stores/preferences";
import { naiveThemeOverrides } from "./theme/naive-theme";

const preferences = usePreferencesStore();

const activeTheme = computed(() => (preferences.resolvedTheme === "dark" ? darkTheme : null));

// Global CSS and Naive UI must resolve from the same theme on every route, including authentication and error pages.
watchEffect(() => {
  if (typeof document !== "undefined") {
    document.documentElement.dataset.theme = preferences.resolvedTheme;
  }
});
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
