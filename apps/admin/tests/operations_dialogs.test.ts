import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { flushPromises, mount } from "@vue/test-utils";
import { NButton, NSelect } from "naive-ui";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h, ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AdminElevationScope } from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminCacheNamespace,
  AdminMaintenanceScope,
  AdminMaintenanceStateSchema,
  AdminRetryTaskKind,
  PreviewCacheRefreshResponseSchema,
  PreviewMaintenanceChangeResponseSchema,
  PreviewTaskRetryResponseSchema
} from "../../../contracts/gen/ts/platform/admin/v1/admin_operations_pb";

const api = vi.hoisted(() => ({
  applyCacheRefresh: vi.fn(),
  applyMaintenanceChange: vi.fn(),
  applyTaskRetry: vi.fn(),
  previewCacheRefresh: vi.fn(),
  previewMaintenanceChange: vi.fn(),
  previewTaskRetry: vi.fn()
}));

const elevationDialog = vi.hoisted(() => ({
  payloads: [] as Array<{
    scope: unknown;
    allowRecoveryCode: boolean;
    onElevated?: (...args: unknown[]) => void;
  }>
}));

vi.mock("../src/api/admin-operations", async () => {
  const actual = await vi.importActual<typeof import("../src/api/admin-operations")>("../src/api/admin-operations");
  return {
    ...actual,
    applyCacheRefresh: api.applyCacheRefresh,
    applyMaintenanceChange: api.applyMaintenanceChange,
    applyTaskRetry: api.applyTaskRetry,
    previewCacheRefresh: api.previewCacheRefresh,
    previewMaintenanceChange: api.previewMaintenanceChange,
    previewTaskRetry: api.previewTaskRetry
  };
});

vi.mock("../src/api/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../src/api/connect")>()),
  createOperationId: () => "1WlKf0-pNZ2XEf9BzWw1QBDlLHbV_B8z"
}));

vi.mock("../src/views/security/components/ElevationDialog.vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    default: defineComponent({
      name: "ElevationDialogStub",
      setup(_, { expose }) {
        expose({
          toggleDialog(open: boolean, payload?: { scope: unknown; allowRecoveryCode: boolean; onElevated?: (...args: unknown[]) => void }) {
            if (open && payload) {
              elevationDialog.payloads.push(payload);
            }
          }
        });
        return () => h("div");
      }
    })
  };
});

import { AdminApiError } from "../src/api/errors";
import CacheRefreshDialog from "../src/views/operations/components/CacheRefreshDialog.vue";
import MaintenanceDialog from "../src/views/operations/components/MaintenanceDialog.vue";
import TaskRetryDialog from "../src/views/operations/components/TaskRetryDialog.vue";

const expiresAt = create(TimestampSchema, { seconds: 2_000_000_000n });

const AppDialogStub = defineComponent({
  name: "AppDialog",
  emits: ["closed"],
  setup(_props, { emit, expose, slots }) {
    const open = ref(false);
    const toggleDialog = (next: boolean): void => {
      const wasOpen = open.value;
      open.value = next;
      if (wasOpen && !next) {
        emit("closed");
      }
    };
    expose({ toggleDialog });
    return () => open.value ? h("section", { "data-testid": "dialog" }, slots.default?.({ close: () => toggleDialog(false) })) : null;
  }
});

const currentMaintenance = create(AdminMaintenanceStateSchema, {
  scope: AdminMaintenanceScope.USER_MUTATIONS,
  enabled: false,
  version: 7n,
  reason: "",
  changedAt: expiresAt,
  changedByAdminId: "admin-1"
});

const maintenancePreview = create(PreviewMaintenanceChangeResponseSchema, {
  current: currentMaintenance,
  previewDigest: "maintenance-digest",
  expiresAt,
  activeRooms: 3n,
  activeGames: 2n
});

const cachePreview = create(PreviewCacheRefreshResponseSchema, {
  namespace: AdminCacheNamespace.OVERVIEW_PROJECTION,
  currentGeneration: 5n,
  estimatedEntries: 12n,
  previewDigest: "cache-digest",
  expiresAt
});

const retryTaskId = "019f9aed-8df5-7d6c-98ef-4f36c1b3d8f2";
const retryPreview = create(PreviewTaskRetryResponseSchema, {
  taskKind: AdminRetryTaskKind.USER_BATCH,
  taskId: retryTaskId,
  taskVersion: 4n,
  manualRetryCount: 1,
  retryAllowed: true,
  stableErrorCode: "user.batch.failed",
  previewDigest: "retry-digest",
  expiresAt
});

const latestElevationPayload = () => {
  const payload = elevationDialog.payloads.at(-1);
  expect(payload).toBeTruthy();
  return payload!;
};

describe("operations dialogs", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
    elevationDialog.payloads.length = 0;
  });

  it("preserves the reviewed maintenance preview through elevation before apply", async () => {
    api.previewMaintenanceChange.mockResolvedValue(maintenancePreview);
    api.applyMaintenanceChange.mockRejectedValueOnce(new AdminApiError({ message: "需要提权", status: 403, code: "permission_denied", businessKey: "admin.elevation.required" }));
    api.applyMaintenanceChange.mockResolvedValueOnce({});
    const onApplied = vi.fn();

    const wrapper = mount(MaintenanceDialog, { global: { stubs: { AppDialog: AppDialogStub } } });
    (wrapper.vm as unknown as { toggleDialog: (open: boolean, payload: object) => void }).toggleDialog(true, { current: currentMaintenance, onApplied, onConflict: vi.fn() });
    await flushPromises();

    const switchInput = wrapper.find("button[role='switch']");
    if (switchInput.exists()) {
      await switchInput.trigger("click");
    }
    await wrapper.find("textarea").setValue("计划维护窗口");
    await wrapper.findAll("button").find((button) => button.text().includes("生成预览"))!.trigger("click");
    await flushPromises();

    expect(api.previewMaintenanceChange).toHaveBeenCalledWith({
      enabled: true,
      scope: AdminMaintenanceScope.USER_MUTATIONS,
      reason: "计划维护窗口",
      plannedEndAt: null,
      signal: expect.any(AbortSignal)
    });

    await wrapper.findAll("button").find((button) => button.text().includes("确认执行"))!.trigger("click");
    await flushPromises();
    expect(api.applyMaintenanceChange).toHaveBeenCalledTimes(1);

    const elevation = latestElevationPayload();
    expect(elevation.scope).toBe(AdminElevationScope.OPERATIONS_MAINTENANCE);
    expect(elevation.allowRecoveryCode).toBe(true);
    elevation.onElevated?.();
    await flushPromises();

    expect(api.applyMaintenanceChange).toHaveBeenNthCalledWith(2, {
      operationId: "1WlKf0-pNZ2XEf9BzWw1QBDlLHbV_B8z",
      enabled: true,
      scope: AdminMaintenanceScope.USER_MUTATIONS,
      reason: "计划维护窗口",
      plannedEndAt: null,
      expectedVersion: 7n,
      previewDigest: "maintenance-digest",
      signal: expect.any(AbortSignal)
    });
    expect(onApplied).toHaveBeenCalledTimes(1);
  });

  it("clears the maintenance preview and notifies reload on version conflict", async () => {
    api.previewMaintenanceChange.mockResolvedValue(maintenancePreview);
    api.applyMaintenanceChange.mockRejectedValue(new AdminApiError({ message: "版本冲突", status: 409, code: "aborted", businessKey: "admin.version.conflict" }));
    const onConflict = vi.fn();

    const wrapper = mount(MaintenanceDialog, { global: { stubs: { AppDialog: AppDialogStub } } });
    (wrapper.vm as unknown as { toggleDialog: (open: boolean, payload: object) => void }).toggleDialog(true, { current: currentMaintenance, onApplied: vi.fn(), onConflict });
    await flushPromises();

    const switchInput = wrapper.find("button[role='switch']");
    if (switchInput.exists()) {
      await switchInput.trigger("click");
    }
    await wrapper.find("textarea").setValue("冲突验证");
    await wrapper.findAll("button").find((button) => button.text().includes("生成预览"))!.trigger("click");
    await flushPromises();
    await wrapper.findAll("button").find((button) => button.text().includes("确认执行"))!.trigger("click");
    await flushPromises();

    expect(onConflict).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain("版本冲突");
    expect(wrapper.text()).not.toContain("活跃房间");
  });

  it("invalidates the cache preview when the operator changes namespace before apply", async () => {
    api.previewCacheRefresh.mockResolvedValue(cachePreview);

    const wrapper = mount(CacheRefreshDialog, { global: { stubs: { AppDialog: AppDialogStub } } });
    (wrapper.vm as unknown as { toggleDialog: (open: boolean, payload: object) => void }).toggleDialog(true, { onApplied: vi.fn(), onConflict: vi.fn() });
    await flushPromises();

    await wrapper.find("textarea").setValue("刷新缓存");
    await wrapper.findAll("button").find((button) => button.text().includes("生成预览"))!.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("当前代次");

    await wrapper.findComponent(NSelect).vm.$emit("update:value", AdminCacheNamespace.OPERATIONS_PROBES);
    await flushPromises();

    expect(wrapper.text()).not.toContain("当前代次");
    const confirm = wrapper.findAllComponents(NButton).find((button) => button.text().includes("确认刷新"));
    expect(confirm?.props("disabled")).toBe(true);
  });

  it("replays the reviewed cache refresh after elevation using the preview generation", async () => {
    api.previewCacheRefresh.mockResolvedValue(cachePreview);
    api.applyCacheRefresh.mockRejectedValueOnce(new AdminApiError({ message: "需要提权", status: 403, code: "permission_denied", businessKey: "admin.elevation.required" }));
    api.applyCacheRefresh.mockResolvedValueOnce({});
    const onApplied = vi.fn();

    const wrapper = mount(CacheRefreshDialog, { global: { stubs: { AppDialog: AppDialogStub } } });
    (wrapper.vm as unknown as { toggleDialog: (open: boolean, payload: object) => void }).toggleDialog(true, { onApplied, onConflict: vi.fn() });
    await flushPromises();

    await wrapper.find("textarea").setValue("刷新缓存");
    await wrapper.findAll("button").find((button) => button.text().includes("生成预览"))!.trigger("click");
    await flushPromises();
    await wrapper.findAll("button").find((button) => button.text().includes("确认刷新"))!.trigger("click");
    await flushPromises();

    latestElevationPayload().onElevated?.();
    await flushPromises();

    expect(api.applyCacheRefresh).toHaveBeenNthCalledWith(2, {
      operationId: "1WlKf0-pNZ2XEf9BzWw1QBDlLHbV_B8z",
      namespace: AdminCacheNamespace.OVERVIEW_PROJECTION,
      reason: "刷新缓存",
      expectedGeneration: 5n,
      previewDigest: "cache-digest",
      signal: expect.any(AbortSignal)
    });
    expect(onApplied).toHaveBeenCalledTimes(1);
  });

  it("keeps task retry blocked and shows the reviewed refusal when retry is not allowed", async () => {
    api.previewTaskRetry.mockResolvedValue(create(PreviewTaskRetryResponseSchema, {
      ...retryPreview,
      retryAllowed: false,
      manualRetryCount: 3
    }));

    const wrapper = mount(TaskRetryDialog, { global: { stubs: { AppDialog: AppDialogStub } } });
    (wrapper.vm as unknown as { toggleDialog: (open: boolean, payload: object) => void }).toggleDialog(true, { taskKind: AdminRetryTaskKind.USER_BATCH, taskId: retryTaskId, onApplied: vi.fn(), onConflict: vi.fn() });
    await flushPromises();

    const inputs = wrapper.findAll("input");
    await inputs[0]!.setValue(retryTaskId);
    await wrapper.find("textarea").setValue("人工复核后重试");
    await wrapper.findAll("button").find((button) => button.text().includes("生成预览"))!.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("3 / 3");
    expect(wrapper.text()).toContain("该任务不是可重试的失败状态");
    const confirm = wrapper.findAllComponents(NButton).find((button) => button.text().includes("确认重试"));
    expect(confirm?.props("disabled")).toBe(true);
    expect(api.applyTaskRetry).not.toHaveBeenCalled();
  });

  it("drops stale task retry requests when the dialog is closed before the preview resolves", async () => {
    let resolvePreview!: (value: typeof retryPreview) => void;
    api.previewTaskRetry.mockReturnValue(new Promise((resolve) => {
      resolvePreview = resolve as (value: typeof retryPreview) => void;
    }));

    const wrapper = mount(TaskRetryDialog, { global: { stubs: { AppDialog: AppDialogStub } } });
    const vm = wrapper.vm as unknown as { toggleDialog: (open: boolean, payload?: object) => void };
    vm.toggleDialog(true, { taskKind: AdminRetryTaskKind.USER_BATCH, taskId: retryTaskId, onApplied: vi.fn(), onConflict: vi.fn() });
    await flushPromises();

    const inputs = wrapper.findAll("input");
    await inputs[0]!.setValue(retryTaskId);
    await wrapper.find("textarea").setValue("人工复核后重试");
    await wrapper.findAll("button").find((button) => button.text().includes("生成预览"))!.trigger("click");
    await flushPromises();

    vm.toggleDialog(false);
    await flushPromises();
    resolvePreview(retryPreview);
    await flushPromises();

    expect(wrapper.text()).not.toContain("任务版本");
    expect(wrapper.find('[data-testid="dialog"]').exists()).toBe(false);
  });
});
