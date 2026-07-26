import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";
import { AdminPermission } from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
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
    preferences.persistedTabs = ["overview", routeName.users, routeName.rooms, routeName.security] as unknown as AppRouteName[];

    const navigation = useNavigationStore();
    navigation.restoreTabs([AdminPermission.USERS_READ, AdminPermission.ROOMS_READ, AdminPermission.SECURITY_READ]);

    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.users, routeName.rooms, routeName.security]);
    expect(preferences.persistedTabs).toEqual([routeName.users, routeName.rooms, routeName.security]);
  });

  it("restores the first authorized real module when persisted tabs contain only retired modules", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = ["overview", "audit"] as unknown as AppRouteName[];

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
});
