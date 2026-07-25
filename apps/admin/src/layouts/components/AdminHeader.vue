<script setup lang="ts">
import { LogOut, Menu, MoonStar, SunMedium, SunMoon } from "lucide-vue-next";
import { NButton, NDropdown, NTag } from "naive-ui";
import { computed, ref } from "vue";
import { useRouter } from "vue-router";
import LogoutConfirmDialog from "../../components/session/LogoutConfirmDialog.vue";
import { routeName } from "../../constants/navigation";
import { useAuthStore } from "../../stores/auth";
import { usePreferencesStore } from "../../stores/preferences";
import { summarizeSession } from "../../utils/format";

const emit = defineEmits<{
  mobile: [];
}>();

const auth = useAuthStore();
const preferences = usePreferencesStore();
const router = useRouter();
const logoutDialogRef = ref<InstanceType<typeof LogoutConfirmDialog> | null>(null);
const loggingOut = ref(false);

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

const handleLogout = async (): Promise<void> => {
  loggingOut.value = true;
  try {
    await auth.logoutCurrentSession();
  } catch {
    // Local authentication state is cleared even when the remote logout call is unavailable.
  } finally {
    loggingOut.value = false;
    logoutDialogRef.value?.toggleDialog(false);
    await router.replace({ name: routeName.authLogin });
  }
};
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
      <NButton tertiary aria-label="退出管理后台" @click="logoutDialogRef?.toggleDialog(true)">
        <template #icon>
          <LogOut :size="18" />
        </template>
        <span class="admin-header__theme-label">退出</span>
      </NButton>
    </div>
  </header>
  <LogoutConfirmDialog ref="logoutDialogRef" :pending="loggingOut" @confirm="handleLogout" />
</template>
