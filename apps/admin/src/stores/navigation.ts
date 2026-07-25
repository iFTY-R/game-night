import { computed, ref } from "vue";
import { defineStore } from "pinia";
import type { RouteLocationNormalizedLoaded } from "vue-router";
import { navigationItems, routeName, type AppRouteName } from "../constants/navigation";
import { usePreferencesStore } from "./preferences";

export type TabItem = {
  name: AppRouteName;
  title: string;
  closable: boolean;
};

const layoutRouteNames = new Set<AppRouteName>([routeName.security]);

export const useNavigationStore = defineStore("admin-navigation", () => {
  const preferences = usePreferencesStore();
  const mobileOpen = ref(false);
  const tabs = ref<TabItem[]>([]);
  const activeTab = ref<AppRouteName>(routeName.security);

  const syncPersistedTabs = (): void => {
    preferences.persistedTabs = tabs.value.map((tab) => tab.name);
  };

  const syncFromRoute = (
    route: RouteLocationNormalizedLoaded,
    _permissions: readonly unknown[] = []
  ): void => {
    if (typeof route.name !== "string") {
      return;
    }
    const routeKey = route.name as AppRouteName;
    if (!layoutRouteNames.has(routeKey)) {
      return;
    }
    activeTab.value = routeKey;
    const title = route.meta.title ?? routeKey;
    const existing = tabs.value.find((tab) => tab.name === routeKey);
    if (!existing) {
      tabs.value.push({ name: routeKey, title, closable: route.meta.closable !== false });
    }
    syncPersistedTabs();
  };

  const restoreTabs = (_permissions: readonly unknown[] = []): void => {
    // Retired modules must never be revived from an older local-storage snapshot.
    tabs.value = [{
      name: routeName.security,
      title: navigationItems[0]?.title ?? "安全设置",
      closable: false
    }];
    syncPersistedTabs();
  };

  const closeTab = (name: AppRouteName): AppRouteName => {
    if (name === routeName.security) {
      return routeName.security;
    }
    const index = tabs.value.findIndex((tab) => tab.name === name);
    if (index >= 0) {
      tabs.value.splice(index, 1);
    }
    syncPersistedTabs();
    return tabs.value[Math.max(index - 1, 0)]?.name ?? routeName.security;
  };

  const breadcrumbs = computed(() => tabs.value.filter((tab) => tab.name === activeTab.value));

  return {
    activeTab,
    breadcrumbs,
    closeTab,
    mobileOpen,
    restoreTabs,
    syncFromRoute,
    tabs
  };
});
