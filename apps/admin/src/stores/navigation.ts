import { computed, ref } from "vue";
import { defineStore } from "pinia";
import type { RouteLocationNormalizedLoaded } from "vue-router";
import type { AdminPermission } from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { navigationItems, routeName, type AppRouteName } from "../constants/navigation";
import { useAuthStore } from "./auth";
import { usePreferencesStore } from "./preferences";

export type TabItem = {
  name: AppRouteName;
  title: string;
  closable: boolean;
};

const layoutRouteNames = new Set<AppRouteName>([routeName.overview, routeName.users, routeName.audit, routeName.security]);
const navigationItemByName = new Map(navigationItems.map((item) => [item.name, item] as const));

const isTabAllowed = (name: AppRouteName, permissions: readonly AdminPermission[]): boolean => {
  const item = navigationItemByName.get(name);
  return item != null && (item.permission == null || permissions.includes(item.permission));
};

// Keep the persisted tab list aligned with the current admin's access scope before rendering.
const normalizeTabNames = (names: readonly AppRouteName[], permissions: readonly AdminPermission[]): AppRouteName[] => {
  const restored = names.filter(
    (name, index, current) =>
      name !== routeName.overview &&
      layoutRouteNames.has(name) &&
      current.indexOf(name) === index &&
      isTabAllowed(name, permissions)
  );
  return [routeName.overview, ...restored];
};

export const useNavigationStore = defineStore("admin-navigation", () => {
  const auth = useAuthStore();
  const preferences = usePreferencesStore();
  const mobileOpen = ref(false);
  const tabs = ref<TabItem[]>([]);
  const activeTab = ref<AppRouteName>(routeName.overview);

  const syncPersistedTabs = (): void => {
    preferences.persistedTabs = tabs.value.map((tab) => tab.name);
  };

  const syncFromRoute = (
    route: RouteLocationNormalizedLoaded,
    permissions: readonly AdminPermission[] = auth.permissions
  ): void => {
    if (typeof route.name !== "string") {
      return;
    }
    const routeKey = route.name as AppRouteName;
    if (!layoutRouteNames.has(routeKey) || !isTabAllowed(routeKey, permissions)) {
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

  const restoreTabs = (permissions: readonly AdminPermission[] = auth.permissions): void => {
    const restoredNames = normalizeTabNames(preferences.persistedTabs, permissions);
    tabs.value = restoredNames.map((name) => ({
      name,
      title: navigationItems.find((item) => item.name === name)?.title ?? name,
      closable: name !== routeName.overview
    }));
    syncPersistedTabs();
  };

  const closeTab = (name: AppRouteName): AppRouteName => {
    if (name === routeName.overview) {
      return routeName.overview;
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
