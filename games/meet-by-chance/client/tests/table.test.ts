import { mount } from "@vue/test-utils";
import { beforeAll, describe, expect, it } from "vitest";

import MeetByChanceTable from "../src/MeetByChanceTable.vue";
import { meetByChanceFixtureContext, meetByChanceFixtureView } from "../src/fixture";
import type { MeetByChanceActionInput } from "../src/types";

beforeAll(() => {
  class ResizeObserverStub { observe(): void {} disconnect(): void {} }
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
});

describe("MeetByChanceTable", () => {
  it("requires confirmation for both target decisions and never exposes a challenge action", async () => {
    const view = meetByChanceFixtureView("active");
    const wrapper = mount(MeetByChanceTable, {
      attachTo: document.body,
      props: { view, context: meetByChanceFixtureContext(), allowedActions: view.allowedActions },
    });
    expect(wrapper.text()).not.toContain("挑战");
    await wrapper.get('[data-testid="reroll-action"]').trigger("click");
    expect(wrapper.emitted("submit")).toBeUndefined();
    document.querySelector<HTMLButtonElement>('.gn-confirm__actions .is-danger')?.click();
    expect((wrapper.emitted("submit") as unknown as Array<[MeetByChanceActionInput]>)[0]?.[0].action).toBe("round.reroll");

    await wrapper.get('[data-testid="stand-action"]').trigger("click");
    document.querySelector<HTMLButtonElement>('.gn-confirm__actions .is-danger')?.click();
    expect((wrapper.emitted("submit") as unknown as Array<[MeetByChanceActionInput]>)[1]?.[0].action).toBe("round.stand");
    wrapper.unmount();
  });

  it("shows only stand at the reroll limit and locks actions while reconnecting", () => {
    const limited = meetByChanceFixtureView("reroll-limit");
    const wrapper = mount(MeetByChanceTable, {
      props: { view: limited, context: meetByChanceFixtureContext(), allowedActions: limited.allowedActions },
    });
    expect(wrapper.find('[data-testid="reroll-action"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="stand-action"]').attributes("disabled")).toBeUndefined();

    const active = meetByChanceFixtureView("active");
    const reconnecting = mount(MeetByChanceTable, {
      props: { view: active, context: meetByChanceFixtureContext("你", "reconnecting"), allowedActions: active.allowedActions },
    });
    expect(reconnecting.get('[data-testid="reroll-action"]').attributes()).toHaveProperty("disabled");
    expect(reconnecting.find('[title="立即重连"]').exists()).toBe(true);
  });
});
