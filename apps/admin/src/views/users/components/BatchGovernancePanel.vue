<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NDescriptions,
  NDescriptionsItem,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NPopconfirm,
  NSelect,
  NSpace,
  NSpin,
  NTabPane,
  NTabs,
  NTag,
  NThing,
  useMessage,
  type FormInst,
  type FormRules,
  type SelectOption
} from "naive-ui";
import { Layers3, ListRestart, ShieldCheck } from "lucide-vue-next";
import { AdminElevationScope, AdminJobState } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminBatchUserCommandType,
  AdminBatchUserItemState,
  type AdminBatchUserOperation,
  type AdminBatchUserOperationItem,
  type PreviewBatchUserOperationResponse
} from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import {
  cancelBatchUserOperation,
  listBatchUserOperationItems,
  listBatchUserOperations,
  previewBatchUserOperation,
  retryBatchUserOperation,
  startBatchUserOperation,
  getBatchUserOperation,
  type BatchUserSelectionInput,
  type ListUsersFilterInput
} from "../../../api/admin-user";
import { createOperationId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { formatDateTime } from "../../../utils/format";
import ElevationDialog from "../../security/components/ElevationDialog.vue";

type BatchSelectedUser = {
  userId: string;
  username: string;
  expectedUserVersion: bigint;
};

type StartBatchOperationPayload = {
  previewId: string;
  previewDigest: string;
  expectedVersion: bigint;
  reason: string;
  scope: AdminElevationScope;
};

type CancelBatchOperationPayload = {
  batchOperationId: string;
  expectedVersion: bigint;
  reason: string;
};

type RetryBatchOperationPayload = {
  batchOperationId: string;
  itemIds: string[];
  expectedVersion: bigint;
  reason: string;
};

const props = defineProps<{
  selectedUsers: BatchSelectedUser[];
  currentFilter: ListUsersFilterInput;
}>();

type RequestLane = "preview" | "operations" | "detail" | "items";

const message = useMessage();

const previewFormRef = ref<FormInst | null>(null);
const cancelFormRef = ref<FormInst | null>(null);
const retryFormRef = ref<FormInst | null>(null);
const elevationDialogRef = ref<InstanceType<typeof ElevationDialog> | null>(null);

// Form state stays local to the batch panel so each operation flow can reset independently.
const previewForm = reactive({
  selectionMode: "explicit" as "explicit" | "filter",
  command: AdminBatchUserCommandType.SUSPEND,
  reason: ""
});
const cancelForm = reactive({
  reason: ""
});
const retryForm = reactive({
  reason: ""
});
const operationFilter = reactive({
  states: [] as AdminJobState[],
  commands: [] as AdminBatchUserCommandType[]
});
const itemFilter = reactive({
  states: [] as AdminBatchUserItemState[]
});

const rules: FormRules = {
  command: { required: true, type: "number", message: "请选择治理动作", trigger: ["change"] },
  reason: { required: true, message: "请输入操作原因", trigger: ["input", "blur"] }
};

const previewing = ref(false);
const starting = ref(false);
const loadingOperations = ref(false);
const loadingDetail = ref(false);
const loadingItems = ref(false);
const cancelling = ref(false);
const retrying = ref(false);

const currentPreview = ref<PreviewBatchUserOperationResponse | null>(null);
const operations = ref<AdminBatchUserOperation[]>([]);
const selectedOperation = ref<AdminBatchUserOperation | null>(null);
const items = ref<AdminBatchUserOperationItem[]>([]);
const selectedRetryItemIds = ref<string[]>([]);
const nextOperationPageToken = ref("");
const nextItemPageToken = ref("");

// Independent lanes prevent detail refreshes from canceling the durable job list load.
const requestGenerations: Record<RequestLane, number> = {
  preview: 0,
  operations: 0,
  detail: 0,
  items: 0
};
const activeControllers: Record<RequestLane, AbortController | null> = {
  preview: null,
  operations: null,
  detail: null,
  items: null
};
const elevationGeneration = ref(0);

const commandOptions: SelectOption[] = [
  { label: "批量停权", value: AdminBatchUserCommandType.SUSPEND },
  { label: "批量解除停权", value: AdminBatchUserCommandType.UNSUSPEND },
  { label: "批量移出当前房间", value: AdminBatchUserCommandType.REMOVE_FROM_CURRENT_ROOM }
];
const operationStateOptions: SelectOption[] = [
  { label: "排队中", value: AdminJobState.QUEUED },
  { label: "执行中", value: AdminJobState.RUNNING },
  { label: "已成功", value: AdminJobState.SUCCEEDED },
  { label: "部分成功", value: AdminJobState.PARTIALLY_SUCCEEDED },
  { label: "失败", value: AdminJobState.FAILED },
  { label: "取消中", value: AdminJobState.CANCELING },
  { label: "已取消", value: AdminJobState.CANCELED },
  { label: "已过期", value: AdminJobState.EXPIRED }
];
const itemStateOptions: SelectOption[] = [
  { label: "排队中", value: AdminBatchUserItemState.QUEUED },
  { label: "执行中", value: AdminBatchUserItemState.RUNNING },
  { label: "成功", value: AdminBatchUserItemState.SUCCEEDED },
  { label: "失败", value: AdminBatchUserItemState.FAILED },
  { label: "跳过", value: AdminBatchUserItemState.SKIPPED },
  { label: "已取消", value: AdminBatchUserItemState.CANCELED }
];

const selectedUserSummary = computed(() => {
  if (!props.selectedUsers.length) {
    return "未勾选用户";
  }
  const labels = props.selectedUsers.slice(0, 3).map((user) => user.username || user.userId);
  const extraCount = props.selectedUsers.length - labels.length;
  return extraCount > 0 ? `${labels.join("、")} 等 ${props.selectedUsers.length} 人` : labels.join("、");
});
const currentFilterSummary = computed(() => {
  const parts: string[] = [];
  if (props.currentFilter.username?.trim()) {
    parts.push(`用户名包含 ${props.currentFilter.username.trim()}`);
  }
  if (props.currentFilter.status) {
    parts.push(`状态 ${formatUserStatus(Number(props.currentFilter.status))}`);
  }
  if (props.currentFilter.tagIds?.length) {
    parts.push(`标签 ${props.currentFilter.tagIds.length} 个`);
  }
  return parts.length ? parts.join(" / ") : "当前筛选为空，将匹配全部用户。";
});
const previewSelectionDescription = computed(() =>
  previewForm.selectionMode === "explicit"
    ? `显式勾选 ${props.selectedUsers.length} 个用户`
    : `按当前筛选执行：${currentFilterSummary.value}`
);
const canStartPreview = computed(() => (currentPreview.value?.executableCount ?? 0n) > 0n);
const selectedOperationIsActive = computed(() => {
  switch (selectedOperation.value?.state) {
    case AdminJobState.QUEUED:
    case AdminJobState.RUNNING:
    case AdminJobState.CANCELING:
      return true;
    default:
      return false;
  }
});
const retryEligibleItems = computed(() =>
  items.value.filter((item) =>
    item.state === AdminBatchUserItemState.FAILED ||
    item.state === AdminBatchUserItemState.SKIPPED ||
    item.state === AdminBatchUserItemState.CANCELED
  )
);

const errorMessage = (error: unknown, fallback: string): string =>
  error instanceof AdminApiError ? error.message : fallback;

const beginRequest = (lane: RequestLane): { token: number; signal: AbortSignal } => {
  requestGenerations[lane] += 1;
  activeControllers[lane]?.abort();
  activeControllers[lane] = new AbortController();
  return { token: requestGenerations[lane], signal: activeControllers[lane].signal };
};

const isCurrentRequest = (lane: RequestLane, token: number, signal: AbortSignal): boolean =>
  !signal.aborted && token === requestGenerations[lane];

// Elevation callbacks can outlive the UI state that opened them, so every dialog session gets a token.
const invalidateElevationCallbacks = (): void => {
  elevationGeneration.value += 1;
};

const openElevationDialog = (scope: AdminElevationScope, onElevated: () => void): void => {
  invalidateElevationCallbacks();
  const token = elevationGeneration.value;
  elevationDialogRef.value?.toggleDialog(true, {
    scope,
    allowRecoveryCode: false,
    onElevated: () => {
      if (token !== elevationGeneration.value) {
        return;
      }
      onElevated();
    },
    onCancelled: () => {
      if (token === elevationGeneration.value) {
        invalidateElevationCallbacks();
      }
    }
  });
};

const clearPreview = (): void => {
  invalidateElevationCallbacks();
  currentPreview.value = null;
};

const clearOperationDetail = (): void => {
  invalidateElevationCallbacks();
  selectedOperation.value = null;
  items.value = [];
  selectedRetryItemIds.value = [];
  nextItemPageToken.value = "";
  cancelForm.reason = "";
  retryForm.reason = "";
  cancelFormRef.value?.restoreValidation();
  retryFormRef.value?.restoreValidation();
};

const batchSelection = (): BatchUserSelectionInput | null => {
  if (previewForm.selectionMode === "explicit") {
    if (!props.selectedUsers.length) {
      message.warning("请先勾选至少一个用户。");
      return null;
    }
    return {
      mode: "explicit",
      users: props.selectedUsers.map((user) => ({
        userId: user.userId,
        expectedUserVersion: user.expectedUserVersion
      }))
    };
  }
  return {
    mode: "filter",
    filter: props.currentFilter
  };
};

const formatUserStatus = (status: number): string => {
  switch (status) {
    case 1:
      return "注册中";
    case 2:
      return "正常";
    case 3:
      return "已停权";
    case 4:
      return "已删除";
    default:
      return "未筛选";
  }
};

const formatCommand = (command: AdminBatchUserCommandType): string => {
  switch (command) {
    case AdminBatchUserCommandType.SUSPEND:
      return "批量停权";
    case AdminBatchUserCommandType.UNSUSPEND:
      return "批量解除停权";
    case AdminBatchUserCommandType.REMOVE_FROM_CURRENT_ROOM:
      return "批量移出当前房间";
    default:
      return "未知动作";
  }
};

const formatJobState = (state: AdminJobState): string => {
  switch (state) {
    case AdminJobState.QUEUED:
      return "排队中";
    case AdminJobState.RUNNING:
      return "执行中";
    case AdminJobState.SUCCEEDED:
      return "已成功";
    case AdminJobState.PARTIALLY_SUCCEEDED:
      return "部分成功";
    case AdminJobState.FAILED:
      return "失败";
    case AdminJobState.CANCELING:
      return "取消中";
    case AdminJobState.CANCELED:
      return "已取消";
    case AdminJobState.EXPIRED:
      return "已过期";
    default:
      return "未知";
  }
};

const formatItemState = (state: AdminBatchUserItemState): string => {
  switch (state) {
    case AdminBatchUserItemState.QUEUED:
      return "排队中";
    case AdminBatchUserItemState.RUNNING:
      return "执行中";
    case AdminBatchUserItemState.SUCCEEDED:
      return "成功";
    case AdminBatchUserItemState.FAILED:
      return "失败";
    case AdminBatchUserItemState.SKIPPED:
      return "跳过";
    case AdminBatchUserItemState.CANCELED:
      return "已取消";
    default:
      return "未知";
  }
};

const formatJobType = (state: AdminJobState): "default" | "success" | "warning" | "error" => {
  switch (state) {
    case AdminJobState.SUCCEEDED:
      return "success";
    case AdminJobState.PARTIALLY_SUCCEEDED:
    case AdminJobState.RUNNING:
    case AdminJobState.CANCELING:
      return "warning";
    case AdminJobState.FAILED:
    case AdminJobState.CANCELED:
    case AdminJobState.EXPIRED:
      return "error";
    default:
      return "default";
  }
};

const formatItemType = (state: AdminBatchUserItemState): "default" | "success" | "warning" | "error" => {
  switch (state) {
    case AdminBatchUserItemState.SUCCEEDED:
      return "success";
    case AdminBatchUserItemState.QUEUED:
    case AdminBatchUserItemState.RUNNING:
      return "warning";
    case AdminBatchUserItemState.FAILED:
    case AdminBatchUserItemState.CANCELED:
      return "error";
    default:
      return "default";
  }
};

const upsertOperation = (operation?: AdminBatchUserOperation | null): void => {
  if (!operation) {
    return;
  }
  const existing = operations.value.find((entry) => entry.batchOperationId === operation.batchOperationId);
  operations.value = existing
    ? operations.value.map((entry) => entry.batchOperationId === operation.batchOperationId ? operation : entry)
    : [operation, ...operations.value];
  if (selectedOperation.value?.batchOperationId === operation.batchOperationId) {
    selectedOperation.value = operation;
  }
};

const loadOperations = async (pageToken = ""): Promise<void> => {
  const { token, signal } = beginRequest("operations");
  loadingOperations.value = true;
  try {
    const response = await listBatchUserOperations({
      states: operationFilter.states,
      commands: operationFilter.commands,
      pageSize: 20,
      pageToken,
      signal
    });
    if (!isCurrentRequest("operations", token, signal)) {
      return;
    }
    operations.value = pageToken ? [...operations.value, ...response.batchOperations] : response.batchOperations;
    nextOperationPageToken.value = response.page?.nextPageToken ?? "";
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "加载批量治理任务失败。"));
    }
  } finally {
    if (isCurrentRequest("operations", token, signal)) {
      loadingOperations.value = false;
    }
  }
};

const loadItems = async (pageToken = ""): Promise<void> => {
  if (!selectedOperation.value) {
    items.value = [];
    nextItemPageToken.value = "";
    return;
  }
  const { token, signal } = beginRequest("items");
  loadingItems.value = true;
  try {
    const response = await listBatchUserOperationItems({
      batchOperationId: selectedOperation.value.batchOperationId,
      states: itemFilter.states,
      pageSize: 50,
      pageToken,
      signal
    });
    if (!isCurrentRequest("items", token, signal)) {
      return;
    }
    items.value = pageToken ? [...items.value, ...response.items] : response.items;
    nextItemPageToken.value = response.page?.nextPageToken ?? "";
    const eligibleIds = new Set(retryEligibleItems.value.map((item) => item.itemId));
    selectedRetryItemIds.value = selectedRetryItemIds.value.filter((itemId) => eligibleIds.has(itemId));
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "加载任务条目失败。"));
    }
  } finally {
    if (isCurrentRequest("items", token, signal)) {
      loadingItems.value = false;
    }
  }
};

const openOperation = async (operationId: string): Promise<void> => {
  const { token, signal } = beginRequest("detail");
  loadingDetail.value = true;
  clearOperationDetail();
  try {
    const response = await getBatchUserOperation({ batchOperationId: operationId, signal });
    if (!isCurrentRequest("detail", token, signal)) {
      return;
    }
    selectedOperation.value = response.batchOperation ?? null;
    if (selectedOperation.value) {
      await loadItems();
    }
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "加载任务详情失败。"));
    }
  } finally {
    if (isCurrentRequest("detail", token, signal)) {
      loadingDetail.value = false;
    }
  }
};

const handlePreview = async (): Promise<void> => {
  try {
    await previewFormRef.value?.validate();
  } catch {
    return;
  }
  const selection = batchSelection();
  if (!selection) {
    return;
  }
  const { token, signal } = beginRequest("preview");
  previewing.value = true;
  clearPreview();
  try {
    const response = await previewBatchUserOperation({
      selection,
      command: previewForm.command,
      reason: previewForm.reason,
      signal
    });
    if (!isCurrentRequest("preview", token, signal)) {
      return;
    }
    currentPreview.value = response;
    message.success("批量治理预览已生成。");
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "生成批量治理预览失败。"));
    }
  } finally {
    if (isCurrentRequest("preview", token, signal)) {
      previewing.value = false;
    }
  }
};

const handleStartAfterElevation = async (payload: StartBatchOperationPayload): Promise<void> => {
  starting.value = true;
  try {
    const response = await startBatchUserOperation({
      operationId: createOperationId(),
      previewId: payload.previewId,
      previewDigest: payload.previewDigest,
      reason: payload.reason,
      expectedVersion: payload.expectedVersion
    });
    upsertOperation(response.batchOperation);
    if (response.batchOperation?.batchOperationId) {
      await openOperation(response.batchOperation.batchOperationId);
    }
    clearPreview();
    message.success("批量治理任务已启动。");
  } catch (error) {
    message.error(errorMessage(error, "启动批量治理任务失败。"));
  } finally {
    starting.value = false;
  }
};

const handleElevatedStart = (): void => {
  if (!currentPreview.value) {
    return;
  }
  const payload: StartBatchOperationPayload = {
    previewId: currentPreview.value.previewId,
    previewDigest: currentPreview.value.previewDigest,
    expectedVersion: currentPreview.value.previewVersion,
    reason: previewForm.reason,
    scope: currentPreview.value.requiredElevation || AdminElevationScope.USERS_BULK_GOVERNANCE
  };
  openElevationDialog(payload.scope, () => {
    void handleStartAfterElevation(payload);
  });
};

const handleCancelAfterElevation = async (payload: CancelBatchOperationPayload): Promise<void> => {
  cancelling.value = true;
  try {
    const response = await cancelBatchUserOperation({
      operationId: createOperationId(),
      batchOperationId: payload.batchOperationId,
      reason: payload.reason,
      expectedVersion: payload.expectedVersion
    });
    upsertOperation(response.batchOperation);
    cancelForm.reason = "";
    cancelFormRef.value?.restoreValidation();
    await loadItems();
    message.success("批量治理任务已请求取消。");
  } catch (error) {
    message.error(errorMessage(error, "取消批量治理任务失败。"));
  } finally {
    cancelling.value = false;
  }
};

const handleCancelOperation = async (): Promise<void> => {
  try {
    await cancelFormRef.value?.validate();
  } catch {
    return;
  }
  if (!selectedOperation.value) {
    return;
  }
  const payload: CancelBatchOperationPayload = {
    batchOperationId: selectedOperation.value.batchOperationId,
    expectedVersion: selectedOperation.value.version,
    reason: cancelForm.reason
  };
  openElevationDialog(AdminElevationScope.USERS_BULK_GOVERNANCE, () => {
    void handleCancelAfterElevation(payload);
  });
};

const handleRetryAfterElevation = async (payload: RetryBatchOperationPayload): Promise<void> => {
  retrying.value = true;
  try {
    const response = await retryBatchUserOperation({
      operationId: createOperationId(),
      batchOperationId: payload.batchOperationId,
      itemIds: payload.itemIds,
      reason: payload.reason,
      expectedVersion: payload.expectedVersion
    });
    upsertOperation(response.batchOperation);
    retryForm.reason = "";
    retryFormRef.value?.restoreValidation();
    await loadItems();
    message.success(`已重试 ${response.requeuedItems} 个条目。`);
  } catch (error) {
    message.error(errorMessage(error, "重试批量治理条目失败。"));
  } finally {
    retrying.value = false;
  }
};

const handleRetryOperation = async (): Promise<void> => {
  try {
    await retryFormRef.value?.validate();
  } catch {
    return;
  }
  if (!selectedOperation.value || !selectedRetryItemIds.value.length) {
    return;
  }
  const payload: RetryBatchOperationPayload = {
    batchOperationId: selectedOperation.value.batchOperationId,
    itemIds: [...selectedRetryItemIds.value],
    reason: retryForm.reason,
    expectedVersion: selectedOperation.value.version
  };
  openElevationDialog(AdminElevationScope.USERS_BULK_GOVERNANCE, () => {
    void handleRetryAfterElevation(payload);
  });
};

const toggleRetryItem = (itemId: string, checked: boolean): void => {
  selectedRetryItemIds.value = checked
    ? [...selectedRetryItemIds.value, itemId]
    : selectedRetryItemIds.value.filter((value) => value !== itemId);
};

onMounted(() => {
  void loadOperations();
});

onBeforeUnmount(() => {
  invalidateElevationCallbacks();
  (Object.keys(activeControllers) as RequestLane[]).forEach((lane) => activeControllers[lane]?.abort());
});
</script>

<template>
  <NCard :bordered="false" class="batch-governance">
    <template #header>
      <div class="batch-governance__header">
        <div>
          <p class="batch-governance__eyebrow">Bulk Governance</p>
          <h2>批量用户治理</h2>
          <p>支持按显式勾选用户或当前筛选条件发起任务，并持续查看执行条目。</p>
        </div>
        <NButton secondary :loading="loadingOperations" @click="loadOperations()">
          <template #icon>
            <ListRestart :size="16" />
          </template>
          刷新任务
        </NButton>
      </div>
    </template>

    <NTabs type="line" animated>
      <NTabPane name="compose" tab="发起治理">
        <div class="batch-governance__compose">
          <NCard title="任务草稿" size="small" :bordered="false">
            <NForm ref="previewFormRef" :model="previewForm" :rules="rules" label-placement="top">
              <NFormItem label="选人方式">
                <NSelect
                  v-model:value="previewForm.selectionMode"
                  :options="[
                    { label: `显式勾选 (${props.selectedUsers.length})`, value: 'explicit' },
                    { label: '当前筛选条件', value: 'filter' }
                  ]"
                  @update:value="clearPreview"
                />
              </NFormItem>
              <NAlert type="info" :bordered="false" class="batch-governance__selection-alert">
                {{ previewSelectionDescription }}
              </NAlert>
              <NFormItem label="治理动作" path="command">
                <NSelect v-model:value="previewForm.command" :options="commandOptions" @update:value="clearPreview" />
              </NFormItem>
              <NFormItem label="操作原因" path="reason">
                <NInput
                  v-model:value="previewForm.reason"
                  type="textarea"
                  placeholder="说明为什么要批量处置这些用户，审计会保留该原因。"
                  @input="clearPreview"
                />
              </NFormItem>
              <NSpace>
                <NButton type="warning" :loading="previewing" @click="handlePreview">
                  <template #icon>
                    <Layers3 :size="16" />
                  </template>
                  生成预览
                </NButton>
                <NButton type="primary" :disabled="!currentPreview || !canStartPreview" :loading="starting" @click="handleElevatedStart">
                  <template #icon>
                    <ShieldCheck :size="16" />
                  </template>
                  提权并启动
                </NButton>
              </NSpace>
            </NForm>
          </NCard>

          <NCard title="预览结果" size="small" :bordered="false">
            <template v-if="currentPreview">
              <NDescriptions bordered :column="1" label-placement="left">
                <NDescriptionsItem label="预览 ID">{{ currentPreview.previewId }}</NDescriptionsItem>
                <NDescriptionsItem label="目标数量">{{ currentPreview.targetCount }}</NDescriptionsItem>
                <NDescriptionsItem label="可执行数量">{{ currentPreview.executableCount }}</NDescriptionsItem>
                <NDescriptionsItem label="阻断数量">{{ currentPreview.blockedCount }}</NDescriptionsItem>
                <NDescriptionsItem label="过期时间">{{ formatDateTime(currentPreview.expiresAt) }}</NDescriptionsItem>
                <NDescriptionsItem label="提权范围">{{ currentPreview.requiredElevation }}</NDescriptionsItem>
              </NDescriptions>
              <NAlert v-if="!canStartPreview" type="warning" :bordered="false" class="batch-governance__preview-alert">
                当前没有可执行条目，请先调整选人范围或处理阻断项。
              </NAlert>
              <div class="batch-governance__sampled-blockers">
                <NThing
                  v-for="blocker in currentPreview.sampledBlockers"
                  :key="`${blocker.type}-${blocker.resourceId}`"
                  :title="blocker.messageKey || blocker.resourceId || '阻断项'"
                >
                  <template #description>{{ blocker.resourceId || "无资源 ID" }}</template>
                  <p>阻断类型 {{ blocker.type }}</p>
                </NThing>
                <NEmpty v-if="!currentPreview.sampledBlockers.length" description="预览未抽样到阻断项" />
              </div>
            </template>
            <NEmpty v-else description="先生成预览，确认命中范围和阻断项后再启动任务" />
          </NCard>
        </div>
      </NTabPane>

      <NTabPane name="history" tab="任务历史">
        <div class="batch-governance__history-grid">
          <NCard title="任务列表" size="small" :bordered="false">
            <NForm label-placement="top" class="batch-governance__operation-filter">
              <NFormItem label="任务状态">
                <NSelect v-model:value="operationFilter.states" multiple clearable :options="operationStateOptions" @update:value="loadOperations()" />
              </NFormItem>
              <NFormItem label="治理动作">
                <NSelect v-model:value="operationFilter.commands" multiple clearable :options="commandOptions" @update:value="loadOperations()" />
              </NFormItem>
            </NForm>
            <NSpin :show="loadingOperations">
              <div v-if="operations.length" class="batch-governance__operation-list">
                <button
                  v-for="operation in operations"
                  :key="operation.batchOperationId"
                  class="batch-governance__operation-item"
                  type="button"
                  @click="openOperation(operation.batchOperationId)"
                >
                  <div class="batch-governance__operation-topline">
                    <strong>{{ formatCommand(operation.command) }}</strong>
                    <NTag size="small" :type="formatJobType(operation.state)">{{ formatJobState(operation.state) }}</NTag>
                  </div>
                  <div class="batch-governance__operation-meta">
                    <span>{{ operation.batchOperationId }}</span>
                    <span>目标 {{ operation.targetCount }}</span>
                    <span>版本 {{ operation.version }}</span>
                  </div>
                  <div class="batch-governance__operation-meta">
                    <span>成功 {{ operation.succeededCount }}</span>
                    <span>失败 {{ operation.failedCount }}</span>
                    <span>跳过 {{ operation.skippedCount }}</span>
                    <span>取消 {{ operation.canceledCount }}</span>
                  </div>
                  <div class="batch-governance__operation-updated">
                    更新时间 {{ formatDateTime(operation.updatedAt) }}
                  </div>
                </button>
                <NButton v-if="nextOperationPageToken" secondary block :loading="loadingOperations" @click="loadOperations(nextOperationPageToken)">
                  加载更多任务
                </NButton>
              </div>
              <NEmpty v-else description="暂无匹配任务" />
            </NSpin>
          </NCard>

          <NCard title="任务详情" size="small" :bordered="false">
            <NSpin :show="loadingDetail || loadingItems">
              <template v-if="selectedOperation">
                <NDescriptions bordered :column="1" label-placement="left">
                  <NDescriptionsItem label="任务 ID">{{ selectedOperation.batchOperationId }}</NDescriptionsItem>
                  <NDescriptionsItem label="状态">
                    <NTag size="small" :type="formatJobType(selectedOperation.state)">{{ formatJobState(selectedOperation.state) }}</NTag>
                  </NDescriptionsItem>
                  <NDescriptionsItem label="原因">{{ selectedOperation.reason || "未填写" }}</NDescriptionsItem>
                  <NDescriptionsItem label="创建时间">{{ formatDateTime(selectedOperation.createdAt) }}</NDescriptionsItem>
                  <NDescriptionsItem label="更新时间">{{ formatDateTime(selectedOperation.updatedAt) }}</NDescriptionsItem>
                  <NDescriptionsItem label="错误键">{{ selectedOperation.errorMessageKey || "无" }}</NDescriptionsItem>
                </NDescriptions>

                <NCard v-if="selectedOperationIsActive" title="取消任务" size="small" :bordered="false" class="batch-governance__detail-card">
                  <NForm ref="cancelFormRef" :model="cancelForm" :rules="rules" label-placement="top">
                    <NFormItem label="取消原因" path="reason">
                      <NInput v-model:value="cancelForm.reason" type="textarea" placeholder="说明为什么取消正在运行的批量任务" />
                    </NFormItem>
                    <NPopconfirm @positive-click="handleCancelOperation">
                      <template #trigger>
                        <NButton type="error" :loading="cancelling">取消任务</NButton>
                      </template>
                      确认取消当前批量治理任务？
                    </NPopconfirm>
                  </NForm>
                </NCard>

                <NCard title="条目筛选" size="small" :bordered="false" class="batch-governance__detail-card">
                  <NSelect
                    v-model:value="itemFilter.states"
                    multiple
                    clearable
                    :options="itemStateOptions"
                    placeholder="筛选失败、跳过、取消等条目"
                    @update:value="loadItems()"
                  />
                </NCard>

                <NCard title="重试条目" size="small" :bordered="false" class="batch-governance__detail-card">
                  <NForm ref="retryFormRef" :model="retryForm" :rules="rules" label-placement="top">
                    <NFormItem label="重试原因" path="reason">
                      <NInput v-model:value="retryForm.reason" type="textarea" placeholder="说明为什么允许这些条目重新排队" />
                    </NFormItem>
                    <NButton type="primary" :disabled="!selectedRetryItemIds.length" :loading="retrying" @click="handleRetryOperation">
                      重试所选条目
                    </NButton>
                  </NForm>
                </NCard>

                <div class="batch-governance__items">
                  <div v-for="item in items" :key="item.itemId" class="batch-governance__item">
                    <div class="batch-governance__item-check">
                      <NCheckbox
                        :checked="selectedRetryItemIds.includes(item.itemId)"
                        :disabled="!retryEligibleItems.some((entry) => entry.itemId === item.itemId)"
                        @update:checked="(checked) => toggleRetryItem(item.itemId, checked)"
                      />
                    </div>
                    <div class="batch-governance__item-main">
                      <div class="batch-governance__item-topline">
                        <strong>{{ item.userId }}</strong>
                        <NTag size="small" :type="formatItemType(item.state)">{{ formatItemState(item.state) }}</NTag>
                      </div>
                      <div class="batch-governance__operation-meta">
                        <span>条目 {{ item.itemId }}</span>
                        <span>尝试 {{ item.attemptCount }}</span>
                        <span>版本 {{ item.expectedUserVersion }}</span>
                      </div>
                      <div class="batch-governance__operation-meta">
                        <span>审计 {{ item.auditEventId || "未生成" }}</span>
                        <span>错误 {{ item.errorMessageKey || "无" }}</span>
                      </div>
                      <div class="batch-governance__operation-meta">
                        <span>开始 {{ formatDateTime(item.startedAt) }}</span>
                        <span>结束 {{ formatDateTime(item.completedAt) }}</span>
                      </div>
                    </div>
                  </div>
                  <NEmpty v-if="!items.length" description="暂无任务条目" />
                  <NButton v-if="nextItemPageToken" secondary block :loading="loadingItems" @click="loadItems(nextItemPageToken)">
                    加载更多条目
                  </NButton>
                </div>
              </template>
              <NEmpty v-else description="选择左侧任务后可查看执行条目、取消和重试" />
            </NSpin>
          </NCard>
        </div>
      </NTabPane>
    </NTabs>

    <ElevationDialog ref="elevationDialogRef" />
  </NCard>
</template>

<style scoped>
.batch-governance {
  background: var(--admin-surface);
  border: 1px solid var(--admin-line);
  border-radius: 8px;
  box-shadow: var(--admin-shadow);
  margin-top: 18px;
}

.batch-governance__header {
  align-items: flex-end;
  display: flex;
  gap: 16px;
  justify-content: space-between;
}

.batch-governance__header h2 {
  color: var(--admin-ink);
  font-size: 24px;
  margin: 0;
}

.batch-governance__header p:last-child {
  color: var(--admin-muted);
  margin: 8px 0 0;
}

.batch-governance__eyebrow {
  color: var(--admin-brand);
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 0.14em;
  margin: 0 0 8px;
  text-transform: uppercase;
}

.batch-governance__compose,
.batch-governance__history-grid {
  display: grid;
  gap: 18px;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
}

.batch-governance__selection-alert,
.batch-governance__preview-alert,
.batch-governance__detail-card,
.batch-governance__operation-filter {
  margin-top: 16px;
}

.batch-governance__sampled-blockers,
.batch-governance__operation-list,
.batch-governance__items {
  display: grid;
  gap: 12px;
  margin-top: 16px;
}

.batch-governance__operation-item,
.batch-governance__item {
  background: var(--admin-surface-muted);
  border: 1px solid var(--admin-line);
  border-radius: 6px;
}

.batch-governance__operation-item {
  color: inherit;
  cursor: pointer;
  display: grid;
  gap: 8px;
  padding: 14px;
  text-align: left;
  transition: border-color 0.18s ease, background-color 0.18s ease;
  width: 100%;
}

.batch-governance__operation-item:hover {
  background: var(--admin-brand-soft);
  border-color: var(--admin-brand);
}

.batch-governance__operation-topline,
.batch-governance__item-topline,
.batch-governance__operation-meta {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.batch-governance__operation-topline strong,
.batch-governance__item-topline strong {
  color: var(--admin-ink);
  font-size: 15px;
}

.batch-governance__operation-meta,
.batch-governance__operation-updated {
  color: var(--admin-muted);
  font-size: 12px;
}

.batch-governance__item {
  display: grid;
  gap: 12px;
  grid-template-columns: 36px minmax(0, 1fr);
  padding: 14px;
}

.batch-governance__item-check {
  align-items: flex-start;
  display: flex;
  justify-content: center;
  padding-top: 2px;
}

.batch-governance__item-main {
  min-width: 0;
}

@media (max-width: 960px) {
  .batch-governance__header,
  .batch-governance__compose,
  .batch-governance__history-grid {
    grid-template-columns: 1fr;
  }

  .batch-governance__header {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
