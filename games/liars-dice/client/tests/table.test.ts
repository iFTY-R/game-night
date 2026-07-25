import { mount } from "@vue/test-utils";
import { beforeAll, describe, expect, it } from "vitest";

import LiarsDiceTable from "../src/LiarsDiceTable.vue";
import { finishLiarsDiceFixture, liarsDiceFixtureContext, liarsDiceFixtureView, liarsDiceTimeoutFixture } from "../src/fixture";
import type { LiarsDiceActionInput } from "../src/types";

beforeAll(() => {
  class ResizeObserverStub {
    observe(): void {}
    disconnect(): void {}
  }
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
});

describe("LiarsDiceTable", () => {
  it("submits one encoded bid and requires confirmation before opening", async () => {
    const wrapper = mount(LiarsDiceTable, {
      attachTo: document.body,
      props: {
        view: liarsDiceFixtureView(),
        context: liarsDiceFixtureContext(),
        allowedActions: ["round.bid", "round.open", "session.finish"],
      },
    });
    await wrapper.get('[data-testid="bid-action"]').trigger("click");
    const bids = wrapper.emitted("submit") as unknown as Array<[LiarsDiceActionInput]>;
    expect(bids[0]?.[0].action).toBe("round.bid");

    await wrapper.get('[data-testid="open-action"]').trigger("click");
    expect(wrapper.emitted("submit")).toHaveLength(1);
    const confirm = document.querySelector<HTMLButtonElement>(".gn-confirm__actions .is-danger");
    confirm?.click();
    const opened = wrapper.emitted("submit") as unknown as Array<[LiarsDiceActionInput]>;
    expect(opened[1]?.[0].action).toBe("round.open");
    wrapper.unmount();
  });

  it("preserves the draft while reconnecting and locks submission", async () => {
    const context = liarsDiceFixtureContext();
    const wrapper = mount(LiarsDiceTable, {
      props: { view: liarsDiceFixtureView(), context, allowedActions: ["round.bid", "round.open"] },
    });
    await wrapper.get('[title="增加数量"]').trigger("click");
    expect(wrapper.get(".quantity-stepper output").text()).toBe("7");
    await wrapper.setProps({ context: { ...context, connection: "reconnecting" } });
    expect(wrapper.get(".quantity-stepper output").text()).toBe("7");
    expect(wrapper.get('[data-testid="bid-action"]').attributes("disabled")).toBeDefined();
  });

  it("moves the turn glow to the authoritative actor and hides order after finish", async () => {
    const context = liarsDiceFixtureContext();
    const wrapper = mount(LiarsDiceTable, {
      props: { view: liarsDiceFixtureView(), context, allowedActions: ["round.bid", "round.open"] },
    });

    expect(wrapper.get(".gn-table__turn-order").classes()).toContain("gn-table__turn-order--clockwise");
    expect(wrapper.get(".gn-seat.is-turn").text()).toContain("你");
    expect(wrapper.find(".gn-seat__turn-marker").exists()).toBe(false);

    const remoteTurn = liarsDiceTimeoutFixture();
    await wrapper.setProps({ view: remoteTurn, allowedActions: remoteTurn.allowedActions });
    expect(wrapper.get(".gn-seat.is-turn").text()).toContain("阿青");
    expect(wrapper.get(".gn-seat.is-turn").attributes("aria-label")).toContain("行动中");

    await wrapper.setProps({ view: finishLiarsDiceFixture(remoteTurn), allowedActions: [] });
    expect(wrapper.find(".gn-seat.is-turn").exists()).toBe(false);
    expect(wrapper.find(".gn-table__turn-order").exists()).toBe(false);
    wrapper.unmount();
  });
});
