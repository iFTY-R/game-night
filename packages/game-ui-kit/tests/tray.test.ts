import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";

import { ActionTray, DangerConfirm } from "../src";

describe("ActionTray", () => {
  it("moves between stable states without unmounting detail content", async () => {
    const wrapper = mount(ActionTray, {
      props: { modelValue: "compact" },
      slots: { details: "draft survives" },
    });
    await wrapper.get("button").trigger("click");
    expect(wrapper.emitted("update:modelValue")?.[0]).toEqual(["expanded"]);
    expect(wrapper.text()).toContain("draft survives");
  });

  it("snaps upward and downward after a meaningful drag", async () => {
    const wrapper = mount(ActionTray, { props: { modelValue: "compact" } });
    const handle = wrapper.get("button");
    handle.element.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientY: 200 }));
    handle.element.dispatchEvent(new MouseEvent("pointerup", { bubbles: true, clientY: 150 }));
    expect(wrapper.emitted("update:modelValue")?.[0]).toEqual(["expanded"]);
  });
});

describe("DangerConfirm", () => {
  it("requires an explicit confirm event for a dangerous action", async () => {
    const wrapper = mount(DangerConfirm, {
      attachTo: document.body,
      props: { open: true, title: "离开本局", confirmLabel: "确认离开" },
    });
    await document.querySelector<HTMLButtonElement>(".is-danger")?.click();
    expect(wrapper.emitted("confirm")).toHaveLength(1);
    wrapper.unmount();
  });
});
