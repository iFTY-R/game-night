import { mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

import { GameTable } from "../src";

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

const installResizeObserver = (width = 390, height = 560): void => {
  vi.stubGlobal("ResizeObserver", class {
    private readonly callback: ResizeObserverCallback;

    constructor(callback: ResizeObserverCallback) {
      this.callback = callback;
    }

    observe(): void {
      this.callback([{ contentRect: { width, height } } as ResizeObserverEntry], this as unknown as ResizeObserver);
    }

    disconnect(): void {}
    unobserve(): void {}
  });
};

describe("GameTable", () => {
  it("renders a single seated player without throwing during solo-room setup", async () => {
    installResizeObserver();
    const wrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: [{ seatIndex: 2, userId: "u-1", displayName: "小满", connected: true }],
        selfSeatIndex: 2,
      },
    });

    await vi.waitFor(() => expect(wrapper.findAll(".gn-table__seat")).toHaveLength(1));
    expect(wrapper.text()).toContain("小满");
    wrapper.unmount();
  });

  it("publishes the portrait tray safe inset as a CSS variable for layout-sensitive pages", () => {
    installResizeObserver();
    const wrapper = mount(GameTable, {
      props: {
        seats: [
          { seatIndex: 0, userId: "u-1", displayName: "小满", connected: true },
          { seatIndex: 1, userId: "u-2", displayName: "阿青", connected: true },
        ],
        selfSeatIndex: 0,
        bottomInset: 160,
      },
    });

    expect(wrapper.attributes("style")).toContain("--gn-safe-bottom: 160px");
    expect(wrapper.attributes("style")).toContain("--gn-safe-center-shift: 32px");
    expect(wrapper.find(".gn-table__seat").attributes("style")).toContain("--gn-seat-width: 118px");
    expect(wrapper.find(".gn-table__seat").attributes("style")).toContain("--gn-seat-height: 50px");
    wrapper.unmount();
  });

  it("announces the local seat without changing the visible display name", async () => {
    installResizeObserver();
    const wrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: [{ seatIndex: 2, userId: "u-1", displayName: "小满", connected: true }],
        selfSeatIndex: 2,
        seatWidth: 92,
      },
    });

    await vi.waitFor(() => expect(wrapper.find(".gn-seat").exists()).toBe(true));
    expect(wrapper.find(".gn-seat").attributes("aria-label")).toContain("你的座位");
    expect(wrapper.find(".gn-table__seat").attributes("style")).toContain("--gn-seat-width: 92px");
    expect(wrapper.find(".gn-seat").text()).toContain("小满");
    wrapper.unmount();
  });
});
