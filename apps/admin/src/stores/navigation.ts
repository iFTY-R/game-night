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

const layoutRouteNames = new Set<AppRouteName>([
	routeName.overview,
	routeName.users,
	routeName.rooms,
	routeName.audit,
	routeName.security,
	routeName.operations
]);

const routePermissions = new Map(navigationItems.map((item) => [item.name, item.permission] as const));

const canAccess = (name: AppRouteName, permissions: readonly unknown[]): boolean => {
  const permission = routePermissions.get(name);
  return !permission || permissions.includes(permission);
};

export const useNavigationStore = defineStore("admin-navigation", () => {
  const preferences = usePreferencesStore();
  const mobileOpen = ref(false);
  const tabs = ref<TabItem[]>([]);
	const activeTab = ref<AppRouteName>(routeName.overview);

  const syncPersistedTabs = (): void => {
    preferences.persistedTabs = tabs.value.map((tab) => tab.name);
  };

  const syncFromRoute = (
    route: RouteLocationNormalizedLoaded,
    permissions: readonly unknown[] = []
  ): void => {
    if (typeof route.name !== "string") {
      return;
    }
    const routeKey = route.name as AppRouteName;
    if (!layoutRouteNames.has(routeKey) || !canAccess(routeKey, permissions)) {
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

  const restoreTabs = (permissions: readonly unknown[] = []): void => {
    const allowedNames = navigationItems
      .map((item) => item.name)
      .filter((name) => layoutRouteNames.has(name) && canAccess(name, permissions));
    const restoredNames = preferences.persistedTabs
      .filter((name) => allowedNames.includes(name));
    const names = restoredNames.length ? restoredNames : allowedNames.slice(0, 1);
    // Retired or unauthorized modules must never be revived from an older local-storage snapshot.
    tabs.value = names.map((name, index) => {
      const item = navigationItems.find((entry) => entry.name === name);
      return {
        name,
        title: item?.title ?? name,
        closable: index !== 0
      };
    });
	activeTab.value = tabs.value[0]?.name ?? routeName.overview;
    syncPersistedTabs();
  };

  const closeTab = (name: AppRouteName): AppRouteName => {
    if (tabs.value.length <= 1 || tabs.value.find((tab) => tab.name === name)?.closable === false) {
		return tabs.value[0]?.name ?? routeName.overview;
    }
    const index = tabs.value.findIndex((tab) => tab.name === name);
    if (index >= 0) {
      tabs.value.splice(index, 1);
    }
    syncPersistedTabs();
	return tabs.value[Math.max(index - 1, 0)]?.name ?? routeName.overview;
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
