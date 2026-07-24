import { mount } from "@vue/test-utils";
import { beforeAll, describe, expect, it } from "vitest";

import ThreeRoundsReplayTable from "../src/ThreeRoundsReplayTable.vue";
import { threeRoundsFixtureContext, threeRoundsReplayFixture } from "../src/fixture";

beforeAll(() => {
  class ResizeObserverStub { observe(): void {} disconnect(): void {} }
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
});

describe("ThreeRoundsReplayTable", () => {
  it("switches between round tabs and keeps cancelled sessions distinct from normal champions", async () => {
    const wrapper = mount(ThreeRoundsReplayTable, {
      props: {
        replay: threeRoundsReplayFixture(),
        context: {
          roomCode: "3RND",
          players: threeRoundsFixtureContext().players,
        },
      },
    });

    expect(wrapper.text()).toContain("房主取消");
    await wrapper.get(".tab-row button:last-child").trigger("click");
    expect(wrapper.text()).toContain("总结果");
    expect(wrapper.text()).toContain("取消局不会伪装成正常冠军");
    expect(wrapper.text()).toContain("冠军");
  });
});
