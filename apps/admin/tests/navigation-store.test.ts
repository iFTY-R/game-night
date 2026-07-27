import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";
import { AdminPermission } from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import type { RouteLocationNormalizedLoaded } from "vue-router";
import { routeName, type AppRouteName } from "../src/constants/navigation";
import { useNavigationStore } from "../src/stores/navigation";
import { usePreferencesStore } from "../src/stores/preferences";

describe("navigation store", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("restores the user center tab with its administrator-facing title", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = [routeName.users];

    const navigation = useNavigationStore();
    navigation.restoreTabs([AdminPermission.USERS_READ, AdminPermission.ROOMS_READ, AdminPermission.SECURITY_READ]);

    expect(navigation.tabs.map(({ name, title }) => ({ name, title }))).toEqual([
      { name: routeName.users, title: "用户中心" }
    ]);
    expect(preferences.persistedTabs).toEqual([routeName.users]);
  });

  it("drops retired persisted tabs and keeps real authorized modules", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = ["overview", routeName.users, routeName.rooms, routeName.security, routeName.audit] as unknown as AppRouteName[];

    const navigation = useNavigationStore();
    navigation.restoreTabs([
      AdminPermission.USERS_READ,
      AdminPermission.ROOMS_READ,
      AdminPermission.SECURITY_READ,
      AdminPermission.AUDIT_READ
    ]);

    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.users, routeName.rooms, routeName.security, routeName.audit]);
    expect(preferences.persistedTabs).toEqual([routeName.users, routeName.rooms, routeName.security, routeName.audit]);
  });

  it("restores the first authorized real module when persisted tabs contain only retired modules", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = ["overview", "legacy-dashboard"] as unknown as AppRouteName[];

    const navigation = useNavigationStore();
    navigation.restoreTabs([AdminPermission.USERS_READ, AdminPermission.SECURITY_READ]);

    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.users]);
    expect(preferences.persistedTabs).toEqual([routeName.users]);
  });

  it("does not restore user center without its backend permission", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = [routeName.users, routeName.security];

    const navigation = useNavigationStore();
    navigation.restoreTabs([AdminPermission.SECURITY_READ]);

    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.security]);
    expect(preferences.persistedTabs).toEqual([routeName.security]);
  });

  it("does not expose room control without the real room read permission", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = [routeName.rooms, routeName.security];

    const navigation = useNavigationStore();
    navigation.restoreTabs([AdminPermission.SECURITY_READ]);

    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.security]);
    expect(preferences.persistedTabs).toEqual([routeName.security]);
  });

  it("restores audit center only for operators with audit read permission", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = [routeName.audit];

    const navigation = useNavigationStore();
    navigation.restoreTabs([AdminPermission.AUDIT_READ]);

    expect(navigation.tabs.map(({ name, title }) => ({ name, title }))).toEqual([
      { name: routeName.audit, title: "审计中心" }
    ]);
    expect(preferences.persistedTabs).toEqual([routeName.audit]);
  });

  it("makes the authorized current route active instead of retaining an older persisted tab", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = [routeName.security];

    const navigation = useNavigationStore();
    const permissions = [AdminPermission.USERS_READ, AdminPermission.SECURITY_READ, AdminPermission.AUDIT_READ];
    navigation.restoreTabs(permissions);
    navigation.syncFromRoute(
      { name: routeName.audit, meta: { title: "审计中心", closable: true } } as unknown as RouteLocationNormalizedLoaded,
      permissions
    );

    expect(navigation.activeTab).toBe(routeName.audit);
    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.security, routeName.audit]);
  });
});
