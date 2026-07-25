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

const findSeat = (wrapper: ReturnType<typeof mount>, displayName: string) =>
  wrapper.findAll(".gn-seat").find((seat) => seat.attributes("aria-label")?.includes(displayName));

const seatTop = (wrapper: ReturnType<typeof mount>, displayName: string): number => {
  const seatShell = wrapper.findAll(".gn-table__seat").find((seat) => seat.text().includes(displayName));
  const match = (seatShell?.attributes("style") ?? "").match(/top:\s*([\d.]+)px/);
  return Number(match?.[1] ?? NaN);
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

  it("renders one accessible table-edge direction marker only for multiplayer tables", async () => {
    installResizeObserver();
    const wrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: [
          { seatIndex: 0, userId: "u-1", displayName: "小满", connected: true, turn: true },
          { seatIndex: 1, userId: "u-2", displayName: "阿青", connected: true },
        ],
        selfSeatIndex: 0,
        turnDirection: "clockwise",
      },
    });

    await vi.waitFor(() => expect(wrapper.find(".gn-table__turn-order").exists()).toBe(true));
    expect(wrapper.findAll(".gn-table__turn-runner")).toHaveLength(1);
    expect(wrapper.find(".gn-table__turn-order").classes()).toContain("gn-table__turn-order--clockwise");
    expect(wrapper.find(".gn-table__sr-only").text()).toBe("游戏顺序：顺时针");

    await wrapper.setProps({ turnDirection: "counterclockwise" });
    expect(wrapper.find(".gn-table__turn-order").classes()).toContain("gn-table__turn-order--counterclockwise");
    expect(wrapper.find(".gn-table__sr-only").text()).toBe("游戏顺序：逆时针");

    await wrapper.setProps({ seats: [{ seatIndex: 0, userId: "u-1", displayName: "小满", connected: true, turn: true }] });
    expect(wrapper.find(".gn-table__turn-order").exists()).toBe(false);
    expect(wrapper.find(".gn-table__sr-only").exists()).toBe(false);
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
        seatHeight: 56,
      },
    });

    expect(wrapper.attributes("style")).toContain("--gn-safe-bottom: 160px");
    expect(wrapper.attributes("style")).toContain("--gn-safe-center-shift: 30px");
    expect(wrapper.find(".gn-table__seat").attributes("style")).toContain("--gn-seat-width: 118px");
    expect(wrapper.find(".gn-table__seat").attributes("style")).toContain("--gn-seat-height: 56px");
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

  it("renders rail cards with compact indicators and a self turn glow", async () => {
    installResizeObserver(420, 620);
    const wrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: [
          {
            seatIndex: 0,
            userId: "u-1",
            displayName: "东方未明",
            avatarText: "东",
            status: "已准备",
            connected: true,
            turn: true,
            host: true,
          },
          {
            seatIndex: 1,
            userId: "u-2",
            displayName: "阿青",
            status: "离线中",
            connected: false,
          },
          {
            seatIndex: 2,
            userId: "u-3",
            displayName: "小荷",
            status: "等待中",
            connected: true,
          },
        ],
        selfSeatIndex: 0,
      },
    });

    await vi.waitFor(() => expect(wrapper.findAll(".gn-seat")).toHaveLength(3));
    expect(wrapper.findAll(".gn-seat__connector")).toHaveLength(3);

    const selfSeat = findSeat(wrapper, "东方未明");
    expect(selfSeat).toBeDefined();
    expect(selfSeat?.find(".gn-seat__display-name").text()).toBe("东方未明");
    expect(selfSeat?.find(".gn-seat__status").text()).toBe("已准备");
    expect(selfSeat?.classes()).toContain("is-turn");
    expect(selfSeat?.attributes("aria-label")).toContain("轮到你");
    expect(selfSeat?.find(".gn-seat__turn-marker").exists()).toBe(false);
    expect(selfSeat?.find('.gn-seat__indicator[aria-label="你的座位"]').exists()).toBe(true);
    expect(selfSeat?.find('.gn-seat__indicator[aria-label="房主"]').exists()).toBe(true);

    const offlineSeat = findSeat(wrapper, "阿青");
    expect(offlineSeat).toBeDefined();
    expect(offlineSeat?.find(".gn-seat__status").text()).toBe("离线中");
    expect(offlineSeat?.find('.gn-seat__indicator[aria-label="已断线"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it("keeps generic active emphasis separate from turn semantics", async () => {
    installResizeObserver();
    const wrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: [
          { seatIndex: 0, userId: "u-1", displayName: "小满", status: "等待中", connected: true },
          { seatIndex: 1, userId: "u-2", displayName: "阿青", status: "思考中", connected: true, active: true },
        ],
        selfSeatIndex: 0,
      },
    });

    await vi.waitFor(() => expect(wrapper.findAll(".gn-seat")).toHaveLength(2));
    const activeSeat = findSeat(wrapper, "阿青");
    expect(activeSeat).toBeDefined();
    expect(activeSeat?.classes()).toContain("is-active");
    expect(activeSeat?.classes()).not.toContain("is-turn");
    expect(activeSeat?.find(".gn-seat__turn-marker").exists()).toBe(false);
    expect(activeSeat?.attributes("aria-label")).not.toContain("行动中");
    expect(activeSeat?.find(".gn-seat__status").text()).toBe("思考中");
    expect(wrapper.text()).not.toContain("当前行动");
    wrapper.unmount();
  });

  it("announces a remote turn without rendering visible turn copy", async () => {
    installResizeObserver();
    const wrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: [
          { seatIndex: 0, userId: "u-1", displayName: "小满", status: "等待中", connected: true },
          { seatIndex: 1, userId: "u-2", displayName: "阿青", status: "思考中", connected: true, turn: true },
        ],
        selfSeatIndex: 0,
      },
    });

    await vi.waitFor(() => expect(wrapper.findAll(".gn-seat")).toHaveLength(2));
    const turnSeat = findSeat(wrapper, "阿青");
    expect(turnSeat?.classes()).toContain("is-turn");
    expect(turnSeat?.classes()).not.toContain("is-active");
    expect(turnSeat?.attributes("aria-label")).toContain("行动中");
    expect(turnSeat?.find(".gn-seat__turn-marker").exists()).toBe(false);
    expect(turnSeat?.find(".gn-seat__status").text()).toBe("思考中");
    wrapper.unmount();
  });

  it("stacks self host and offline indicators without stealing the status line", async () => {
    installResizeObserver(390, 844);
    const wrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: [
          { seatIndex: 0, userId: "u-1", displayName: "普通", status: "等待中", connected: true },
          { seatIndex: 1, userId: "u-2", displayName: "房主", status: "主持中", connected: true, host: true },
          { seatIndex: 2, userId: "u-3", displayName: "断线", status: "离线中", connected: false },
          { seatIndex: 3, userId: "u-4", displayName: "房断", status: "重连中", connected: false, host: true },
          { seatIndex: 4, userId: "u-5", displayName: "自己", status: "已准备", connected: true },
        ],
        selfSeatIndex: 4,
      },
    });

    await vi.waitFor(() => expect(wrapper.findAll(".gn-seat")).toHaveLength(5));

    const plainSeat = findSeat(wrapper, "普通");
    expect(plainSeat?.find(".gn-seat__status").text()).toBe("等待中");
    expect(plainSeat?.find(".gn-seat__indicators").exists()).toBe(false);
    expect(plainSeat?.find(".gn-seat__turn-marker").exists()).toBe(false);

    const hostSeat = findSeat(wrapper, "房主");
    expect(hostSeat?.find('.gn-seat__indicator[aria-label="房主"]').exists()).toBe(true);
    expect(hostSeat?.find(".gn-seat__status").text()).toBe("主持中");

    const offlineSeat = findSeat(wrapper, "断线");
    expect(offlineSeat?.classes()).toContain("is-offline");
    expect(offlineSeat?.find('.gn-seat__indicator[aria-label="已断线"]').exists()).toBe(true);
    expect(offlineSeat?.find(".gn-seat__status").text()).toBe("离线中");

    const hostOfflineSeat = findSeat(wrapper, "房断");
    expect(hostOfflineSeat?.classes()).toContain("is-offline");
    expect(hostOfflineSeat?.find('.gn-seat__indicator[aria-label="房主"]').exists()).toBe(true);
    expect(hostOfflineSeat?.find('.gn-seat__indicator[aria-label="已断线"]').exists()).toBe(true);
    expect(hostOfflineSeat?.find(".gn-seat__status").text()).toBe("重连中");

    const selfSeat = findSeat(wrapper, "自己");
    expect(selfSeat?.find('.gn-seat__indicator[aria-label="你的座位"]').exists()).toBe(true);
    expect(selfSeat?.find(".gn-seat__turn-marker").exists()).toBe(false);
    expect(selfSeat?.find(".gn-seat__status").text()).toBe("已准备");
    wrapper.unmount();
  });

  it("keeps turn semantics separate from business status and role indicators", async () => {
    installResizeObserver(390, 844);
    const wrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: [
          { seatIndex: 0, userId: "u-1", displayName: "观战", status: "等待中", connected: true },
          { seatIndex: 1, userId: "u-2", displayName: "远端行动", status: "出价中", connected: true, turn: true },
          { seatIndex: 2, userId: "u-3", displayName: "自房行", status: "看牌中", connected: true, active: true, host: true },
        ],
        selfSeatIndex: 2,
      },
    });

    await vi.waitFor(() => expect(wrapper.findAll(".gn-seat")).toHaveLength(3));

    const remoteActiveSeat = findSeat(wrapper, "远端行动");
    expect(remoteActiveSeat?.classes()).toContain("is-turn");
    expect(remoteActiveSeat?.attributes("aria-label")).toContain("行动中");
    expect(remoteActiveSeat?.find(".gn-seat__turn-marker").exists()).toBe(false);
    expect(remoteActiveSeat?.find(".gn-seat__status").text()).toBe("出价中");

    const selfActiveHostSeat = findSeat(wrapper, "自房行");
    expect(selfActiveHostSeat?.classes()).toContain("is-active");
    expect(selfActiveHostSeat?.classes()).toContain("is-self");
    expect(selfActiveHostSeat?.classes()).not.toContain("is-turn");
    expect(selfActiveHostSeat?.attributes("aria-label")).not.toContain("轮到你");
    expect(selfActiveHostSeat?.find(".gn-seat__turn-marker").exists()).toBe(false);
    expect(selfActiveHostSeat?.find('.gn-seat__indicator[aria-label="你的座位"]').exists()).toBe(true);
    expect(selfActiveHostSeat?.find('.gn-seat__indicator[aria-label="房主"]').exists()).toBe(true);
    expect(selfActiveHostSeat?.find(".gn-seat__status").text()).toBe("看牌中");
    wrapper.unmount();
  });

  it("keeps non-zero local seats anchored to the bottom across four-seat and max-seat layouts", async () => {
    installResizeObserver(390, 844);
    const fourSeatWrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: [
          { seatIndex: 0, userId: "u-1", displayName: "甲", connected: true },
          { seatIndex: 1, userId: "u-2", displayName: "乙", connected: true },
          { seatIndex: 2, userId: "u-3", displayName: "丙", connected: true },
          { seatIndex: 3, userId: "u-4", displayName: "丁", connected: true },
        ],
        selfSeatIndex: 2,
      },
    });

    await vi.waitFor(() => expect(fourSeatWrapper.findAll(".gn-seat")).toHaveLength(4));
    const fourSeatSelfTop = seatTop(fourSeatWrapper, "丙");
    expect(fourSeatSelfTop).toBe(Math.max(...["甲", "乙", "丙", "丁"].map((name) => seatTop(fourSeatWrapper, name))));
    fourSeatWrapper.unmount();

    installResizeObserver(844, 390);
    const crowdedSeats = Array.from({ length: 12 }, (_, seatIndex) => ({
      seatIndex,
      userId: `u-${seatIndex}`,
      displayName: `座${seatIndex}`,
      connected: true,
    }));
    const maxSeatWrapper = mount(GameTable, {
      attachTo: document.body,
      props: {
        seats: crowdedSeats,
        selfSeatIndex: 7,
        seatWidth: 92,
        seatHeight: 52,
      },
    });

    await vi.waitFor(() => expect(maxSeatWrapper.findAll(".gn-seat")).toHaveLength(12));
    expect(findSeat(maxSeatWrapper, "座7")?.attributes("aria-label")).toContain("你的座位");
    expect(maxSeatWrapper.findAll(".gn-table__seat")[0]?.attributes("style")).toContain("--gn-seat-width: 92px");
    expect(maxSeatWrapper.findAll(".gn-table__seat")[0]?.attributes("style")).toContain("--gn-seat-height: 52px");
    maxSeatWrapper.unmount();
  });
});
