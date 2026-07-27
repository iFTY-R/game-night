import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { flushPromises, mount } from "@vue/test-utils";
import { NCheckbox, NMessageProvider, NPopconfirm } from "naive-ui";
import { defineComponent, h, type PropType } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AdminElevationScope, AdminJobState } from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminBatchUserCommandType,
  AdminBatchUserItemState,
  AdminBatchUserOperationItemSchema,
  AdminBatchUserOperationSchema,
  PreviewBatchUserOperationResponseSchema
} from "../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";

const api = vi.hoisted(() => ({
  cancelBatchUserOperation: vi.fn(),
  getBatchUserOperation: vi.fn(),
  listBatchUserOperationItems: vi.fn(),
  listBatchUserOperations: vi.fn(),
  previewBatchUserOperation: vi.fn(),
  retryBatchUserOperation: vi.fn(),
  startBatchUserOperation: vi.fn()
}));
const elevationDialog = vi.hoisted(() => ({
  payloads: [] as Array<{
    scope: unknown;
    allowRecoveryCode: boolean;
    onElevated?: (...args: unknown[]) => void;
    onCancelled?: () => void;
  }>
}));

vi.mock("../src/api/admin-user", async () => {
  const actual = await vi.importActual<typeof import("../src/api/admin-user")>("../src/api/admin-user");
  return {
    ...actual,
    cancelBatchUserOperation: api.cancelBatchUserOperation,
    getBatchUserOperation: api.getBatchUserOperation,
    listBatchUserOperationItems: api.listBatchUserOperationItems,
    listBatchUserOperations: api.listBatchUserOperations,
    previewBatchUserOperation: api.previewBatchUserOperation,
    retryBatchUserOperation: api.retryBatchUserOperation,
    startBatchUserOperation: api.startBatchUserOperation
  };
});

vi.mock("../src/views/security/components/ElevationDialog.vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    default: defineComponent({
      name: "ElevationDialogStub",
      setup(_, { expose }) {
        expose({
          toggleDialog(
            open: boolean,
            payload?: {
              scope: unknown;
              allowRecoveryCode: boolean;
              onElevated?: (...args: unknown[]) => void;
              onCancelled?: () => void;
            }
          ) {
            if (open) {
              if (payload) {
                elevationDialog.payloads.push(payload);
              }
            }
          }
        });
        return () => h("div");
      }
    })
  };
});

import BatchGovernancePanel from "../src/views/users/components/BatchGovernancePanel.vue";

const sampledAt = create(TimestampSchema, { seconds: 2_000_000_000n });

const operationFactory = (state: AdminJobState, overrides: Partial<Record<string, unknown>> = {}) =>
  create(AdminBatchUserOperationSchema, {
    batchOperationId: "batch-1",
    command: AdminBatchUserCommandType.SUSPEND,
    state,
    targetCount: 5n,
    queuedCount: 1n,
    runningCount: 0n,
    succeededCount: 3n,
    failedCount: 1n,
    skippedCount: 1n,
    canceledCount: 0n,
    requestedByAdminId: "admin-1",
    reason: "批量风控处置",
    version: 4n,
    createdAt: sampledAt,
    updatedAt: sampledAt,
    ...overrides
  });

const itemFactory = (state: AdminBatchUserItemState, overrides: Partial<Record<string, unknown>> = {}) =>
  create(AdminBatchUserOperationItemSchema, {
    itemId: "item-1",
    batchOperationId: "batch-1",
    userId: "user-1",
    expectedUserVersion: 2n,
    state,
    attemptCount: 1,
    errorMessageKey: "",
    auditEventId: "audit-1",
    version: 3n,
    startedAt: sampledAt,
    completedAt: sampledAt,
    ...overrides
  });

const previewFactory = () =>
  create(PreviewBatchUserOperationResponseSchema, {
    previewId: "preview-1",
    previewDigest: "digest-1",
    previewVersion: 6n,
    expiresAt: sampledAt,
    targetCount: 2n,
    executableCount: 2n,
    blockedCount: 0n,
    requiredElevation: AdminElevationScope.USERS_BULK_GOVERNANCE,
    sampledAt
  });

const Host = defineComponent({
  props: {
    selectedUsers: {
      type: Array as PropType<Array<{ userId: string; username: string; expectedUserVersion: bigint }>>,
      required: true
    },
    currentFilter: {
      type: Object as PropType<{ username?: string; status?: number; tagIds?: string[] }>,
      required: true
    }
  },
  setup(props) {
    return () => h(NMessageProvider, null, {
      default: () => h(BatchGovernancePanel, {
        selectedUsers: props.selectedUsers,
        currentFilter: props.currentFilter
      })
    });
  }
});

describe("batch governance panel", () => {
  const latestElevationPayload = () => {
    const payload = elevationDialog.payloads.at(-1);
    expect(payload).toBeTruthy();
    return payload!;
  };

  afterEach(() => {
    // Naive UI teleports confirmation content to document.body rather than the mounted wrapper.
    document.body.replaceChildren();
  });

  beforeEach(() => {
    vi.clearAllMocks();
    elevationDialog.payloads.length = 0;
    api.listBatchUserOperations.mockResolvedValue({
      batchOperations: [],
      page: {}
    });
    api.getBatchUserOperation.mockResolvedValue({
      batchOperation: operationFactory(AdminJobState.RUNNING),
      sampledAt
    });
    api.listBatchUserOperationItems.mockResolvedValue({
      items: [],
      page: {}
    });
  });

  it("previews explicit targets and starts the durable batch task after elevation", async () => {
    api.previewBatchUserOperation.mockResolvedValue(previewFactory());
    api.startBatchUserOperation.mockResolvedValue({
      batchOperation: operationFactory(AdminJobState.QUEUED, { batchOperationId: "batch-2", version: 7n })
    });

    const wrapper = mount(Host, {
      props: {
        selectedUsers: [
          { userId: "user-1", username: "alice", expectedUserVersion: 2n },
          { userId: "user-2", username: "bob", expectedUserVersion: 3n }
        ],
        currentFilter: { username: "alice", status: 2, tagIds: ["tag-1"] }
      }
    });
    await flushPromises();

    await wrapper.find("textarea").setValue("批量风控处置");
    const previewButton = wrapper.findAll("button").find((button) => button.text().includes("生成预览"));
    expect(previewButton).toBeTruthy();
    await previewButton!.trigger("click");
    await flushPromises();

    expect(api.previewBatchUserOperation).toHaveBeenCalledWith({
      selection: {
        mode: "explicit",
        users: [
          { userId: "user-1", expectedUserVersion: 2n },
          { userId: "user-2", expectedUserVersion: 3n }
        ]
      },
      command: AdminBatchUserCommandType.SUSPEND,
      reason: "批量风控处置",
      signal: expect.any(AbortSignal)
    });

    const launchButton = wrapper.findAll("button").find((button) => button.text().includes("提权并启动"));
    expect(launchButton).toBeTruthy();
    await launchButton!.trigger("click");
    await flushPromises();
    expect(api.startBatchUserOperation).not.toHaveBeenCalled();

    const startElevation = latestElevationPayload();
    expect(startElevation.scope).toBe(AdminElevationScope.USERS_BULK_GOVERNANCE);
    expect(startElevation.allowRecoveryCode).toBe(false);
    startElevation.onElevated?.();
    await flushPromises();

    expect(api.startBatchUserOperation).toHaveBeenCalledWith({
      operationId: expect.any(String),
      previewId: "preview-1",
      previewDigest: "digest-1",
      reason: "批量风控处置",
      expectedVersion: 6n
    });
    expect(api.getBatchUserOperation).toHaveBeenCalledWith({
      batchOperationId: "batch-2",
      signal: expect.any(AbortSignal)
    });
  });

  it("loads task detail, cancels active work, and retries eligible items", async () => {
    api.listBatchUserOperations.mockResolvedValue({
      batchOperations: [operationFactory(AdminJobState.RUNNING)],
      page: {}
    });
    api.getBatchUserOperation.mockResolvedValue({
      batchOperation: operationFactory(AdminJobState.RUNNING),
      sampledAt
    });
    api.listBatchUserOperationItems.mockResolvedValue({
      items: [
        itemFactory(AdminBatchUserItemState.FAILED),
        itemFactory(AdminBatchUserItemState.SUCCEEDED, { itemId: "item-2", userId: "user-2" })
      ],
      page: {}
    });
    api.cancelBatchUserOperation.mockResolvedValue({
      batchOperation: operationFactory(AdminJobState.CANCELING, { version: 5n })
    });
    api.retryBatchUserOperation.mockResolvedValue({
      batchOperation: operationFactory(AdminJobState.RUNNING, { version: 6n }),
      requeuedItems: 1n
    });

    const wrapper = mount(Host, {
      props: {
        selectedUsers: [],
        currentFilter: { username: "", status: 0, tagIds: [] }
      }
    });
    await flushPromises();

    const historyTab = wrapper.findAll(".n-tabs-tab").find((tab) => tab.text().includes("任务历史"));
    expect(historyTab).toBeTruthy();
    await historyTab!.trigger("click");
    await flushPromises();

    const operationRow = wrapper.find(".batch-governance__operation-item");
    expect(operationRow.exists()).toBe(true);
    await operationRow.trigger("click");
    await flushPromises();

    const cancelReason = wrapper.find('textarea[placeholder*="取消"]');
    expect(cancelReason.exists()).toBe(true);
    await cancelReason.setValue("人工撤回批量任务");
    const cancelTrigger = wrapper.findAll("button").find((button) => button.text().includes("取消任务"));
    expect(cancelTrigger).toBeTruthy();
    await cancelTrigger!.trigger("click");
    await flushPromises();
    const confirmation = wrapper.findComponent(NPopconfirm);
    expect(confirmation.exists()).toBe(true);
    await confirmation.vm.$emit("positive-click");
    await flushPromises();

    expect(api.cancelBatchUserOperation).not.toHaveBeenCalled();
    const cancelElevation = latestElevationPayload();
    expect(cancelElevation.scope).toBe(AdminElevationScope.USERS_BULK_GOVERNANCE);
    expect(cancelElevation.allowRecoveryCode).toBe(false);
    cancelElevation.onElevated?.();
    await flushPromises();

    expect(api.cancelBatchUserOperation).toHaveBeenCalledWith({
      operationId: expect.any(String),
      batchOperationId: "batch-1",
      reason: "人工撤回批量任务",
      expectedVersion: 4n
    });

    const checkboxes = wrapper.findAllComponents(NCheckbox);
    expect(checkboxes).toHaveLength(2);
    await checkboxes[0]!.vm.$emit("update:checked", true);
    await flushPromises();
    const retryReason = wrapper.find('textarea[placeholder*="重新排队"]');
    expect(retryReason.exists()).toBe(true);
    await retryReason.setValue("重试失败条目");
    const retryButton = wrapper.findAll("button").find((button) => button.text().includes("重试所选条目"));
    expect(retryButton).toBeTruthy();
    await retryButton!.trigger("click");
    await flushPromises();
    expect(api.retryBatchUserOperation).not.toHaveBeenCalled();

    const retryElevation = latestElevationPayload();
    expect(retryElevation.scope).toBe(AdminElevationScope.USERS_BULK_GOVERNANCE);
    expect(retryElevation.allowRecoveryCode).toBe(false);
    retryElevation.onElevated?.();
    await flushPromises();

    expect(api.retryBatchUserOperation).toHaveBeenCalledWith({
      operationId: expect.any(String),
      batchOperationId: "batch-1",
      itemIds: ["item-1"],
      reason: "重试失败条目",
      expectedVersion: 5n
    });
  });

  it("ignores stale elevation callbacks after dialog cancellation or operation switches", async () => {
    api.listBatchUserOperations.mockResolvedValue({
      batchOperations: [
        operationFactory(AdminJobState.RUNNING),
        operationFactory(AdminJobState.RUNNING, {
          batchOperationId: "batch-2",
          version: 9n,
          reason: "第二个任务"
        })
      ],
      page: {}
    });
    api.getBatchUserOperation.mockImplementation(({ batchOperationId }: { batchOperationId: string }) =>
      Promise.resolve({
        batchOperation: batchOperationId === "batch-2"
          ? operationFactory(AdminJobState.RUNNING, {
              batchOperationId: "batch-2",
              version: 9n,
              reason: "第二个任务"
            })
          : operationFactory(AdminJobState.RUNNING),
        sampledAt
      })
    );

    const wrapper = mount(Host, {
      props: {
        selectedUsers: [],
        currentFilter: { username: "", status: 0, tagIds: [] }
      }
    });
    await flushPromises();

    const historyTab = wrapper.findAll(".n-tabs-tab").find((tab) => tab.text().includes("任务历史"));
    expect(historyTab).toBeTruthy();
    await historyTab!.trigger("click");
    await flushPromises();

    const operationRows = wrapper.findAll(".batch-governance__operation-item");
    expect(operationRows).toHaveLength(2);
    await operationRows[0]!.trigger("click");
    await flushPromises();

    const cancelReason = wrapper.find('textarea[placeholder*="取消"]');
    await cancelReason.setValue("人工撤回批量任务");
    const cancelTrigger = wrapper.findAll("button").find((button) => button.text().includes("取消任务"));
    expect(cancelTrigger).toBeTruthy();
    await cancelTrigger!.trigger("click");
    await flushPromises();

    const confirmation = wrapper.findComponent(NPopconfirm);
    expect(confirmation.exists()).toBe(true);
    await confirmation.vm.$emit("positive-click");
    await flushPromises();

    const canceledElevation = latestElevationPayload();
    canceledElevation.onCancelled?.();
    canceledElevation.onElevated?.();
    await flushPromises();
    expect(api.cancelBatchUserOperation).not.toHaveBeenCalled();

    await cancelTrigger!.trigger("click");
    await flushPromises();
    await confirmation.vm.$emit("positive-click");
    await flushPromises();

    const switchedElevation = latestElevationPayload();
    await operationRows[1]!.trigger("click");
    await flushPromises();
    switchedElevation.onElevated?.();
    await flushPromises();

    expect(api.cancelBatchUserOperation).not.toHaveBeenCalled();
  });
});
