import { mount } from "@vue/test-utils";
import { beforeAll, describe, expect, it } from "vitest";

import ThreeRoundsTable from "../src/ThreeRoundsTable.vue";
import { threeRoundsFixtureContext, threeRoundsFixtureView } from "../src/fixture";
import type { ThreeRoundsActionInput } from "../src/types";

beforeAll(() => {
  class ResizeObserverStub { observe(): void {} disconnect(): void {} }
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
});

describe("ThreeRoundsTable", () => {
  it("locks extra cards after the round-one quota and only emits after confirmation", async () => {
    const view = threeRoundsFixtureView("active");
    const wrapper = mount(ThreeRoundsTable, {
      attachTo: document.body,
      props: { view, context: threeRoundsFixtureContext(), allowedActions: view.allowedActions },
    });

    const handCards = wrapper.findAll(".hand-card");
    await handCards[0]!.trigger("click");
    expect(wrapper.find('[data-testid="submit-selection-action"]').attributes("disabled")).toBeUndefined();
    expect(handCards[1]!.attributes()).toHaveProperty("disabled");
    expect(wrapper.emitted("submit")).toBeUndefined();

    await wrapper.get('[data-testid="submit-selection-action"]').trigger("click");
    document.querySelector<HTMLButtonElement>(".gn-confirm__actions .is-danger")?.click();
    expect((wrapper.emitted("submit") as Array<[ThreeRoundsActionInput]>)?.[0]?.[0].action).toBe("round.submit_selection");
    wrapper.unmount();
  });

  it("removes the submit action in the third-round result phase", () => {
    const view = threeRoundsFixtureView("round-three");
    const wrapper = mount(ThreeRoundsTable, {
      props: { view, context: threeRoundsFixtureContext(), allowedActions: view.allowedActions },
    });
    expect(wrapper.find('[data-testid="submit-selection-action"]').exists()).toBe(false);
    expect(wrapper.text()).toContain("第三关自动公开");
  });
});
