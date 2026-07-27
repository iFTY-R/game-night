import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { createPinia, setActivePinia } from "pinia";
import { flushPromises, mount } from "@vue/test-utils";
import { NConfigProvider } from "naive-ui";
import { defineComponent, h } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  AdminAttentionKind,
  AdminOverviewGranularity,
  AdminOverviewMetric,
  AdminOverviewUnavailableReason,
  GetOverviewResponseSchema
} from "../../../contracts/gen/ts/platform/admin/v1/admin_overview_pb";

const api = vi.hoisted(() => ({ getOverview: vi.fn() }));
const router = vi.hoisted(() => ({ push: vi.fn() }));

vi.mock("../src/api/admin-overview", () => api);
vi.mock("vue-router", () => ({ useRouter: () => router }));

import OverviewView from "../src/views/overview/OverviewView.vue";

const sampledAt = create(TimestampSchema, { seconds: 2_000_000_000n });
const Host = defineComponent({
  setup() {
    return () => h(NConfigProvider, null, { default: () => h(OverviewView) });
  }
});

describe("overview view", () => {
  let pinia: ReturnType<typeof createPinia>;

  beforeEach(() => {
    pinia = createPinia();
    setActivePinia(pinia);
    vi.clearAllMocks();
    api.getOverview.mockResolvedValue(create(GetOverviewResponseSchema, {
      metrics: [{ metric: AdminOverviewMetric.ACTIVE_ROOMS, value: 4n, unavailableReason: AdminOverviewUnavailableReason.NONE, sampledAt }],
      trends: [],
      attention: [{
        kind: AdminAttentionKind.ROOM,
        resourceId: "room-1",
        roomId: "room-1",
        statusCode: "playing",
        reasonCodes: ["room.game_mismatch"],
        observedAt: sampledAt
      }],
      dependencies: [],
      highRiskOperations: [{
        auditEventId: "event-1",
        action: "admin_maintenance_changed",
        actorAdminId: "admin-1",
        targetId: "user_mutations",
        verified: true,
        occurredAt: sampledAt
      }],
      failedTasks: [],
      windowStart: sampledAt,
      windowEnd: sampledAt,
      sampledAt,
      freshUntil: sampledAt
    }));
  });

  it("renders real anomaly and verified risk sources and routes to their existing modules", async () => {
    const wrapper = mount(Host, {
      global: { plugins: [pinia] }
    });
    await flushPromises();

    expect(api.getOverview).toHaveBeenCalledWith(expect.objectContaining({
      granularity: AdminOverviewGranularity.HOUR,
      signal: expect.any(AbortSignal)
    }));
    expect(wrapper.text()).toContain("房间与会话游戏不一致");
    expect(wrapper.text()).toContain("切换维护状态");
    expect(wrapper.text()).not.toContain("暂无概览数据");

    await wrapper.find('button[title="打开房间与牌局"]').trigger("click");
    await wrapper.find('button[title="打开审计中心"]').trigger("click");
    expect(router.push).toHaveBeenNthCalledWith(1, { name: "rooms", query: { view: "rooms" } });
    expect(router.push).toHaveBeenNthCalledWith(2, { name: "audit" });
  });
});
