import { shallowMount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import App from "../src/App.vue";
import { usePreferencesStore } from "../src/stores/preferences";

describe("admin theme synchronization", () => {
  beforeEach(() => {
    document.documentElement.removeAttribute("data-theme");
    setActivePinia(createPinia());
  });

  afterEach(() => {
    document.documentElement.removeAttribute("data-theme");
  });

  it("applies the resolved theme before an authenticated layout is mounted", async () => {
    const pinia = createPinia();
    const wrapper = shallowMount(App, {
      global: {
        plugins: [pinia]
      }
    });

    await nextTick();

    expect(document.documentElement.dataset.theme).toBe("dark");
    wrapper.unmount();
  });

  it("keeps global CSS tokens aligned when the explicit preference changes", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const wrapper = shallowMount(App, {
      global: {
        plugins: [pinia]
      }
    });
    const preferences = usePreferencesStore(pinia);

    preferences.theme = "light";
    await nextTick();

    expect(document.documentElement.dataset.theme).toBe("light");
    wrapper.unmount();
  });

  it("updates a system preference when the operating-system color scheme changes", async () => {
    const listeners = new Set<(event: Pick<MediaQueryListEvent, "matches">) => void>();
    const mediaQuery = {
      matches: true,
      media: "(prefers-color-scheme: dark)",
      onchange: null,
      addEventListener: (_type: string, listener: (event: Pick<MediaQueryListEvent, "matches">) => void) => listeners.add(listener),
      removeEventListener: (_type: string, listener: (event: Pick<MediaQueryListEvent, "matches">) => void) => listeners.delete(listener),
      addListener: () => undefined,
      removeListener: () => undefined,
      dispatchEvent: () => true
    } as unknown as MediaQueryList;
    window.matchMedia = () => mediaQuery;
    const pinia = createPinia();
    setActivePinia(pinia);
    const wrapper = shallowMount(App, {
      global: {
        plugins: [pinia]
      }
    });

    expect(document.documentElement.dataset.theme).toBe("dark");

    for (const listener of listeners) {
      listener({ matches: false });
    }
    await nextTick();

    expect(document.documentElement.dataset.theme).toBe("light");
    wrapper.unmount();
  });
});
