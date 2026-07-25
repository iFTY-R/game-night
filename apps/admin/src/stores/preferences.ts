import { computed, onScopeDispose, ref, watch } from "vue";
import { defineStore } from "pinia";
import { approvedPreferenceKeys } from "../api/cookies";
import type { AppRouteName } from "../constants/navigation";
import type { ResolvedTheme } from "../theme/naive-theme";

type ThemePreference = ResolvedTheme | "system";

const storageKey = {
  theme: approvedPreferenceKeys[0],
  collapsed: approvedPreferenceKeys[1],
  tabs: approvedPreferenceKeys[2]
};

const readStored = <T>(key: string, fallback: T): T => {
  if (typeof localStorage === "undefined") {
    return fallback;
  }
  try {
    const raw = localStorage.getItem(key);
    return raw == null ? fallback : (JSON.parse(raw) as T);
  } catch {
    return fallback;
  }
};

export const usePreferencesStore = defineStore("admin-preferences", () => {
  // Only non-sensitive UI preferences may survive a page refresh.
  const theme = ref<ThemePreference>(readStored(storageKey.theme, "system"));
  const siderCollapsed = ref<boolean>(readStored(storageKey.collapsed, false));
  const persistedTabs = ref<AppRouteName[]>(readStored(storageKey.tabs, ["security"]));

  const systemThemeQuery =
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia("(prefers-color-scheme: dark)")
      : null;
  // This reactive value bridges browser media-query changes into Vue's computed theme state.
  const systemTheme = ref<ResolvedTheme>(systemThemeQuery?.matches ? "dark" : "light");

  const handleSystemThemeChange = (event: MediaQueryListEvent): void => {
    systemTheme.value = event.matches ? "dark" : "light";
  };

  if (systemThemeQuery) {
    // Keep a long-lived system preference listener because "system" remains selectable after store creation.
    systemThemeQuery.addEventListener("change", handleSystemThemeChange);
    onScopeDispose(() => {
      // Pinia store disposal must release the browser listener used by tests and hot module replacement.
      systemThemeQuery.removeEventListener("change", handleSystemThemeChange);
    });
  }

  const resolvedTheme = computed<ResolvedTheme>(() => {
    if (theme.value !== "system") {
      return theme.value;
    }
    return systemTheme.value;
  });

  watch(theme, (value) => localStorage.setItem(storageKey.theme, JSON.stringify(value)), { deep: false });
  watch(siderCollapsed, (value) => localStorage.setItem(storageKey.collapsed, JSON.stringify(value)), { deep: false });
  watch(persistedTabs, (value) => localStorage.setItem(storageKey.tabs, JSON.stringify(value)), { deep: true });

  return {
    theme,
    siderCollapsed,
    persistedTabs,
    resolvedTheme
  };
});
