import { mount } from "@vue/test-utils";
import { flushPromises } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import UserLookupForm from "../src/components/users/UserLookupForm.vue";

describe("UserLookupForm", () => {
  it("emits canonical uuid lookups after validation", async () => {
    const wrapper = mount(UserLookupForm, {
      props: {
        loading: false
      }
    });

    await wrapper.find('input[placeholder="00000000-0000-0000-0000-000000000000"]').setValue("11111111-1111-1111-8111-111111111111");
    const buttons = wrapper.findAll("button");
    await buttons[buttons.length - 1]?.trigger("click");
    await flushPromises();

    const emitted = wrapper.emitted("lookup");
    expect(emitted?.[0]?.[0]).toEqual({
      kind: "userId",
      value: "11111111-1111-1111-8111-111111111111"
    });
  });
});
