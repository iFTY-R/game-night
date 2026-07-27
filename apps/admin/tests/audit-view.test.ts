import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { flushPromises, mount } from "@vue/test-utils";
import { NConfigProvider, NDatePicker, NMessageProvider, NSelect } from "naive-ui";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AdminAuditChainHeadSchema, AdminAuditEventSchema } from "../../../contracts/gen/ts/platform/admin/v1/admin_audit_pb";
import { AdminPageInfoSchema, AdminPermission } from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { AuditAction, AuditActorType, AuditTargetType } from "../../../contracts/gen/ts/platform/audit/v1/audit_pb";

const api = vi.hoisted(() => ({
  listAuditEvents: vi.fn()
}));

vi.mock("../src/api/admin-audit", async () => {
  const actual = await vi.importActual<typeof import("../src/api/admin-audit")>("../src/api/admin-audit");
  return {
    ...actual,
    listAuditEvents: api.listAuditEvents
  };
});

import { navigationItems, routeName } from "../src/constants/navigation";
import { routes } from "../src/router/routes";
import AuditCenterView from "../src/views/audit/AuditCenterView.vue";

const sampledAt = create(TimestampSchema, { seconds: 2_000_000_000n });

const eventFactory = (overrides: Partial<Record<string, unknown>> = {}) =>
  create(AdminAuditEventSchema, {
    eventId: "evt-1",
    sequence: 101n,
    previousHash: "prev-hash-1",
    eventHash: "event-hash-1",
    requestId: "req-1",
    occurredAt: sampledAt,
    actor: {
      type: AuditActorType.ADMIN,
      actorId: "admin-1"
    },
    target: {
      type: AuditTargetType.USER,
      targetId: "user-1"
    },
    action: AuditAction.ADMIN_LOGIN_SUCCEEDED,
    reasonCode: "admin.login.success",
    detailDigest: "detail-digest-1",
    signingKeyVersion: 3,
    verified: true,
    ...overrides
  });

const listResponse = (overrides: Partial<Record<string, unknown>> = {}) => ({
  events: [eventFactory()],
  page: create(AdminPageInfoSchema, {
    nextPageToken: "next-page-token",
    sampledAt
  }),
  chainHead: create(AdminAuditChainHeadSchema, {
    sequence: 105n,
    eventHash: "head-hash-105",
    updatedAt: sampledAt
  }),
  scannedEvents: 8,
  ...overrides
});

const Host = defineComponent({
  setup() {
    return () => h(NConfigProvider, null, {
      default: () => h(NMessageProvider, null, {
        default: () => h(AuditCenterView)
      })
    });
  }
});

const drawerStubs = {
  NDrawer: defineComponent({
    name: "NDrawerStub",
    props: {
      show: {
        type: Boolean,
        default: false
      }
    },
    setup(props, { slots }) {
      return () => props.show ? h("div", { "data-testid": "drawer" }, slots.default?.()) : null;
    }
  }),
  NDrawerContent: defineComponent({
    name: "NDrawerContentStub",
    props: {
      title: {
        type: String,
        default: ""
      }
    },
    setup(props, { slots }) {
      return () => h("section", { "data-testid": "drawer-content" }, [h("h2", props.title), slots.default?.()]);
    }
  })
};

const deferred = <T>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((fulfill) => {
    resolve = fulfill;
  });
  return { promise, resolve };
};

describe("audit center view", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
    api.listAuditEvents.mockResolvedValue(listResponse());
  });

  it("registers the audit module in navigation and routes", () => {
    const navigation = navigationItems.find((item) => item.name === routeName.audit);
    expect(navigation).toMatchObject({
      name: routeName.audit,
      title: "审计中心",
      permission: AdminPermission.AUDIT_READ
    });

    const adminRoot = routes.find((route) => route.path === "/");
    const auditRoute = adminRoot?.children?.find((route) => route.name === routeName.audit);
    expect(auditRoute?.path).toBe("audit");
    expect(auditRoute?.meta).toMatchObject({
      title: "审计中心",
      menu: true,
      permission: AdminPermission.AUDIT_READ
    });
  });

  it("loads the redacted audit timeline and chain head on mount", async () => {
    const wrapper = mount(Host, {
      global: {
        stubs: {
          teleport: true,
          ...drawerStubs
        }
      }
    });
    await flushPromises();

    expect(api.listAuditEvents).toHaveBeenCalledWith(expect.objectContaining({
      pageSize: 20,
      pageToken: "",
      signal: expect.any(AbortSignal)
    }));
    expect(wrapper.text()).toContain("审计中心");
    expect(wrapper.text()).toContain("管理员登录成功");
    expect(wrapper.text()).toContain("head-hash-105");
    expect(wrapper.text()).toContain("当前页之后仍有更晚事件");
    expect(wrapper.text()).toContain("当前页事件签名均已验证");
  });

  it("surfaces an unverified event without hiding the rest of the audit page", async () => {
    api.listAuditEvents.mockResolvedValue(listResponse({
      events: [eventFactory({ eventId: "evt-unverified", verified: false })]
    }));

    const wrapper = mount(Host, {
      global: {
        stubs: {
          teleport: true,
          ...drawerStubs
        }
      }
    });
    await flushPromises();

    expect(wrapper.text()).toContain("完整性异常 1 条");
    expect(wrapper.text()).toContain("检测到 1 条签名完整性异常事件");
    expect(wrapper.find(".audit-table__row--unverified").exists()).toBe(true);
  });

  it("submits all supported filters, including actor, target, request, reason, and time", async () => {
    const wrapper = mount(Host, {
      global: {
        stubs: {
          teleport: true,
          ...drawerStubs
        }
      }
    });
    await flushPromises();
    vi.clearAllMocks();
    api.listAuditEvents.mockResolvedValue(listResponse({ page: create(AdminPageInfoSchema, { sampledAt }) }));

    const selects = wrapper.findAllComponents(NSelect);
    expect(selects).toHaveLength(3);
    await selects[0]!.vm.$emit("update:value", [AuditAction.ADMIN_LOGIN_SUCCEEDED]);
    await selects[1]!.vm.$emit("update:value", [AuditActorType.ADMIN]);
    await selects[2]!.vm.$emit("update:value", [AuditTargetType.USER]);

    await wrapper.find('input[placeholder="输入执行者 ID"]').setValue("admin-9");
    await wrapper.find('input[placeholder="输入目标 ID"]').setValue("user-8");
    await wrapper.find('input[placeholder="输入请求 ID"]').setValue("req-9");
    await wrapper.find('input[placeholder="输入原因码"]').setValue("audit.review");

    const startedAt = Date.UTC(2026, 6, 20, 8, 30, 0);
    const endedAt = Date.UTC(2026, 6, 21, 21, 45, 30);
    await wrapper.findComponent(NDatePicker).vm.$emit("update:value", [startedAt, endedAt]);

    await wrapper.findAll("button").find((button) => button.text().includes("查询"))!.trigger("click");
    await flushPromises();

    expect(api.listAuditEvents).toHaveBeenCalledWith({
      actions: [AuditAction.ADMIN_LOGIN_SUCCEEDED],
      actorTypes: [AuditActorType.ADMIN],
      actorId: "admin-9",
      targetTypes: [AuditTargetType.USER],
      targetId: "user-8",
      requestId: "req-9",
      reasonCode: "audit.review",
      occurredFrom: new Date(startedAt),
      occurredTo: new Date(endedAt),
      pageSize: 20,
      pageToken: "",
      signal: expect.any(AbortSignal)
    });
  });

  it("appends the next page when the operator loads more history", async () => {
    api.listAuditEvents
      .mockResolvedValueOnce(listResponse())
      .mockResolvedValueOnce(listResponse({
        events: [eventFactory({
          eventId: "evt-2",
          sequence: 102n,
          requestId: "req-2",
          eventHash: "event-hash-2",
          detailDigest: "detail-digest-2",
          reasonCode: "audit.second.page"
        })],
        page: create(AdminPageInfoSchema, {
          nextPageToken: "",
          sampledAt
        })
      }));

    const wrapper = mount(Host, {
      global: {
        stubs: {
          teleport: true,
          ...drawerStubs
        }
      }
    });
    await flushPromises();

    await wrapper.findAll("button").find((button) => button.text().includes("加载更多"))!.trigger("click");
    await flushPromises();

    expect(api.listAuditEvents).toHaveBeenNthCalledWith(2, expect.objectContaining({
      pageSize: 20,
      pageToken: "next-page-token",
      signal: expect.any(AbortSignal)
    }));
    expect(wrapper.text()).toContain("audit.second.page");
  });

  it("opens event details from the already-loaded signed projection", async () => {
    const wrapper = mount(Host, {
      global: {
        stubs: {
          teleport: true,
          ...drawerStubs
        }
      }
    });
    await flushPromises();

    const firstRow = wrapper.find(".audit-table__row");
    expect(firstRow.exists()).toBe(true);
    await firstRow.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("管理员登录成功 · evt-1");
    expect(wrapper.text()).toContain("event-hash-1");
    expect(wrapper.text()).toContain("detail-digest-1");
    expect(wrapper.text()).toContain("管理员 · admin-1");
  });

  it("ignores a late stale response after a newer filtered search wins", async () => {
    const wrapper = mount(Host, {
      global: {
        stubs: {
          teleport: true,
          ...drawerStubs
        }
      }
    });
    await flushPromises();

    const first = deferred<ReturnType<typeof listResponse>>();
    const secondResponse = listResponse({
      events: [eventFactory({
        eventId: "evt-second",
        sequence: 202n,
        action: AuditAction.ADMIN_MFA_ENABLED,
        requestId: "req-second",
        reasonCode: "audit.newest.wins",
        eventHash: "event-hash-second"
      })],
      page: create(AdminPageInfoSchema, {
        nextPageToken: "",
        sampledAt
      })
    });

    vi.clearAllMocks();
    api.listAuditEvents
      .mockImplementationOnce(() => first.promise)
      .mockResolvedValueOnce(secondResponse);

    await wrapper.find('input[placeholder="输入请求 ID"]').setValue("req-first");
    await wrapper.findAll("button").find((button) => button.text().includes("查询"))!.trigger("click");
    await flushPromises();

    await wrapper.find('input[placeholder="输入请求 ID"]').setValue("req-second");
    await wrapper.findAll("button").find((button) => button.text().includes("查询"))!.trigger("click");
    await flushPromises();

    expect(api.listAuditEvents).toHaveBeenCalledTimes(2);
    expect(wrapper.text()).toContain("管理员 MFA 启用");
    expect(wrapper.text()).toContain("req-second");

    first.resolve(listResponse({
      events: [eventFactory({
        eventId: "evt-stale",
        sequence: 999n,
        action: AuditAction.ADMIN_LOGIN_FAILED,
        requestId: "req-first",
        reasonCode: "audit.stale.should.drop",
        eventHash: "event-hash-stale"
      })]
    }));
    await flushPromises();

    expect(wrapper.text()).toContain("req-second");
    expect(wrapper.text()).not.toContain("evt-stale");
    expect(wrapper.text()).not.toContain("audit.stale.should.drop");
  });
});
