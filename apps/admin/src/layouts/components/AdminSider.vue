<script setup lang="ts">
import { ChevronLeft, ChevronRight } from "lucide-vue-next";
import { NButton, NTooltip } from "naive-ui";
import { computed } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { navigationItems } from "../../constants/navigation";
import { usePreferencesStore } from "../../stores/preferences";

const props = defineProps<{
  mobile?: boolean;
}>();

const preferences = usePreferencesStore();
const route = useRoute();

// Mobile navigation always stays expanded even when the desktop preference is collapsed.
const collapsed = computed(() => !props.mobile && preferences.siderCollapsed);
const visibleItems = computed(() => navigationItems);
</script>

<template>
  <aside class="admin-sider" :class="{ 'admin-sider--collapsed': collapsed, 'admin-sider--mobile': props.mobile }">
    <div class="admin-sider__brand">
      <img class="admin-sider__logo" :src="'/brand-mark.svg'" alt="" aria-hidden="true" />
      <span v-if="!collapsed" class="admin-sider__brand-copy">
        <strong>Game Night</strong>
        <small>管理后台</small>
      </span>
    </div>
    <nav class="admin-sider__nav" aria-label="管理导航">
      <RouterLink
        v-for="item in visibleItems"
        :key="item.name"
        :to="{ name: item.name }"
        class="admin-sider__link"
        :class="{ 'is-active': route.name === item.name }"
      >
        <component :is="item.icon" :size="18" />
        <span v-if="!collapsed">{{ item.title }}</span>
      </RouterLink>
    </nav>
    <div v-if="!props.mobile" class="admin-sider__footer">
      <NTooltip trigger="hover">
        <template #trigger>
          <NButton tertiary circle :aria-label="collapsed ? '展开侧栏' : '折叠侧栏'" @click="preferences.siderCollapsed = !preferences.siderCollapsed">
            <template #icon>
              <ChevronLeft v-if="!collapsed" :size="18" />
              <ChevronRight v-else :size="18" />
            </template>
          </NButton>
        </template>
        {{ collapsed ? "展开侧栏" : "折叠侧栏" }}
      </NTooltip>
    </div>
  </aside>
</template>
