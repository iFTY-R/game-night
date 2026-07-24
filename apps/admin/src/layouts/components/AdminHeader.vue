<script setup lang="ts">
import { Menu, MoonStar, SunMedium, SunMoon } from "lucide-vue-next";
import { NButton, NDropdown, NTag } from "naive-ui";
import { computed } from "vue";
import { useAuthStore } from "../../stores/auth";
import { usePreferencesStore } from "../../stores/preferences";
import { summarizeSession } from "../../utils/format";

const emit = defineEmits<{
  mobile: [];
}>();

const auth = useAuthStore();
const preferences = usePreferencesStore();

const themeLabel = computed(() => {
  switch (preferences.theme) {
    case "light":
      return "亮色";
    case "dark":
      return "暗色";
    default:
      return "跟随系统";
  }
});

const themeOptions = [
  { label: "亮色", key: "light" },
  { label: "暗色", key: "dark" },
  { label: "跟随系统", key: "system" }
];
</script>

<template>
  <header class="admin-header">
    <div class="admin-header__cluster">
      <NButton tertiary circle class="mobile-only" aria-label="打开导航菜单" @click="emit('mobile')">
        <template #icon>
          <Menu :size="18" />
        </template>
      </NButton>
      <div class="admin-header__identity">
        <span>Game Night</span>
        <strong>管理工作台</strong>
      </div>
    </div>
    <div class="admin-header__cluster">
      <NTag class="admin-header__session" :bordered="false" type="warning">{{ summarizeSession(auth.session) }}</NTag>
      <NDropdown :options="themeOptions" @select="(value) => (preferences.theme = value as typeof preferences.theme)">
        <NButton tertiary :aria-label="`主题：${themeLabel}`">
          <template #icon>
            <SunMedium v-if="preferences.theme === 'light'" :size="18" />
            <MoonStar v-else-if="preferences.theme === 'dark'" :size="18" />
            <SunMoon v-else :size="18" />
          </template>
          <span class="admin-header__theme-label">{{ themeLabel }}</span>
        </NButton>
      </NDropdown>
    </div>
  </header>
</template>
