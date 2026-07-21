import { mount } from "@vue/test-utils";
import { beforeAll, describe, expect, it } from "vitest";

import LiarsDiceTable from "../src/LiarsDiceTable.vue";
import { liarsDiceFixtureContext, liarsDiceFixtureView } from "../src/fixture";
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
});
