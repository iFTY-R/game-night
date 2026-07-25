import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";
import { routeName, type AppRouteName } from "../src/constants/navigation";
import { useNavigationStore } from "../src/stores/navigation";
import { usePreferencesStore } from "../src/stores/preferences";

describe("navigation store", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("restores the security tab with its administrator-facing title", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = [routeName.security];

    const navigation = useNavigationStore();
    navigation.restoreTabs();

    expect(navigation.tabs.map(({ name, title }) => ({ name, title }))).toEqual([
      { name: routeName.security, title: "安全设置" }
    ]);
    expect(preferences.persistedTabs).toEqual([routeName.security]);
  });

  it("drops retired persisted tabs and writes the normalized list back", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = ["overview", "users", routeName.security] as unknown as AppRouteName[];

    const navigation = useNavigationStore();
    navigation.restoreTabs();

    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.security]);
    expect(preferences.persistedTabs).toEqual([routeName.security]);
  });

  it("restores security when persisted tabs contain only retired modules", () => {
    const preferences = usePreferencesStore();
    preferences.persistedTabs = ["overview", "audit"] as unknown as AppRouteName[];

    const navigation = useNavigationStore();
    navigation.restoreTabs();

    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.security]);
    expect(preferences.persistedTabs).toEqual([routeName.security]);
  });
});
