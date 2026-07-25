import { create } from "@bufbuild/protobuf";
import { mount } from "@vue/test-utils";
import { beforeAll, describe, expect, it } from "vitest";

import Dice789Table from "../src/Dice789Table.vue";
import { dice789FixtureContext, dice789FixtureView, finishDice789Fixture } from "../src/fixture";
import { ActionConstraintsSchema, ViewSchema } from "../src/generated/game/dice789/v1/dice_789_pb";
import type { Dice789ActionInput } from "../src/types";

beforeAll(() => {
  class ResizeObserverStub { observe(): void {} disconnect(): void {} }
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
});

describe("Dice789Table", () => {
  it("submits the roll action and confirms an irreversible eight effect", async () => {
    const active = mount(Dice789Table, {
      props: { view: dice789FixtureView("active"), context: dice789FixtureContext(), allowedActions: ["turn.roll"] },
    });
    await active.get('[data-testid="roll-action"]').trigger("click");
    expect((active.emitted("submit") as unknown as Array<[Dice789ActionInput]>)[0]?.[0].action).toBe("turn.roll");

    const pending = mount(Dice789Table, {
      attachTo: document.body,
      props: { view: dice789FixtureView("result-eight"), context: dice789FixtureContext(), allowedActions: ["turn.confirm_landed", "turn.report_dropped"] },
    });
    await pending.get('[data-testid="confirm-action"]').trigger("click");
    expect(pending.emitted("submit")).toBeUndefined();
    document.querySelector<HTMLButtonElement>(".gn-confirm__actions .is-danger")?.click();
    expect((pending.emitted("submit") as unknown as Array<[Dice789ActionInput]>)[0]?.[0].action).toBe("turn.confirm_landed");
    active.unmount();
    pending.unmount();
  });

  it("preserves an add draft through reconnect and resets a revoked target", async () => {
    const context = dice789FixtureContext();
    const wrapper = mount(Dice789Table, {
      props: { view: dice789FixtureView("add"), context, allowedActions: ["pot.add"] },
    });
    await wrapper.get('[title="增加加注"]').trigger("click");
    expect(wrapper.get(".add-stepper output").text()).toBe("1 单位");
    await wrapper.setProps({ context: { ...context, connection: "reconnecting" } });
    expect(wrapper.get(".add-stepper output").text()).toBe("1 单位");

    const targetView = dice789FixtureView("target-one");
    await wrapper.setProps({ view: targetView, allowedActions: ["turn.choose_target"], context });
    expect(wrapper.get<HTMLSelectElement>('[aria-label="目标玩家"]').element.value).toBe("user-qing");
    const revokedView = create(ViewSchema, {
      ...targetView,
      actionConstraints: create(ActionConstraintsSchema, { targetUserIds: ["user-man", "user-nan"] }),
    });
    await wrapper.setProps({ view: revokedView });
    expect(wrapper.get<HTMLSelectElement>('[aria-label="目标玩家"]').element.value).toBe("user-man");
  });

  it("maps direction and the phase authority to one turn seat", async () => {
    const context = dice789FixtureContext();
    const activeView = dice789FixtureView("active");
    const wrapper = mount(Dice789Table, {
      props: { view: activeView, context, allowedActions: activeView.allowedActions },
    });

    expect(wrapper.get(".gn-table__turn-order").classes()).toContain("gn-table__turn-order--clockwise");
    expect(wrapper.get(".gn-seat.is-turn").text()).toContain("你");
    expect(wrapper.find('[aria-label="顺时针"]').exists()).toBe(false);

    const reversedView = create(ViewSchema, { ...activeView, direction: 2 });
    await wrapper.setProps({ view: reversedView });
    expect(wrapper.get(".gn-table__turn-order").classes()).toContain("gn-table__turn-order--counterclockwise");

    const pendingView = dice789FixtureView("result-eight");
    const hostContext = {
      ...context,
      players: context.players.map((player) => ({ ...player, host: player.userId === "user-man" })),
    };
    await wrapper.setProps({ view: pendingView, context: hostContext, allowedActions: pendingView.allowedActions });
    expect(wrapper.get(".gn-seat.is-turn").text()).toContain("小满");
    expect(wrapper.get(".gn-seat.is-turn").text()).toContain("等待确认");

    await wrapper.setProps({ view: finishDice789Fixture(pendingView), allowedActions: [] });
    expect(wrapper.find(".gn-seat.is-turn").exists()).toBe(false);
    expect(wrapper.find(".gn-table__turn-order").exists()).toBe(false);
    wrapper.unmount();
  });
});
