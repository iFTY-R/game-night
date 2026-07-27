<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NDescriptions,
  NDescriptionsItem,
  NDrawer,
  NDrawerContent,
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
  NTooltip,
  useMessage,
  type FormInst,
  type FormRules,
  type SelectOption
} from "naive-ui";
import { Eye, Pencil, RefreshCw, Search, Tag, Trash2, UserRound } from "lucide-vue-next";
import { AdminElevationScope, AdminPermission } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminUserPIIField,
  AdminUserStatus,
  AdminUserCommandBlockerType,
  AdminUserCommandOutcome,
  AdminUserCommandType,
  type AdminUserDetail,
  type AdminUserNote,
  type AdminUserPIIValue,
  type AdminUserRoomSummary,
  type AdminUserSummary,
  type AdminUserTag,
  type PreviewUserCommandResponse
} from "../../../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import {
  appendUserNote,
  createUserTag,
  executeUserCommand,
  getUser,
  getUserPII,
  isValidTagColorInput,
  listUserNotes,
  listUsers,
  listUserTags,
  normalizeTagColor,
  previewUserCommand,
  setUserTags
} from "../../api/admin-user";
import type { AdminUserCommandInput } from "../../api/admin-user";
import { createOperationId } from "../../api/connect";
import { AdminApiError } from "../../api/errors";
import PermissionGate from "../../components/PermissionGate.vue";
import { useAuthStore } from "../../stores/auth";
import { formatDateTime } from "../../utils/format";
import BatchGovernancePanel from "./components/BatchGovernancePanel.vue";
import UserTagMutationDialog, { type UserTagMutationDialogPayload } from "./components/UserTagMutationDialog.vue";
import ElevationDialog, { type ElevationDialogPayload } from "../security/components/ElevationDialog.vue";

const message = useMessage();
const auth = useAuthStore();

const searchForm = reactive({
  username: "",
  status: 0,
  tagIds: [] as string[]
});

const noteFormRef = ref<FormInst | null>(null);
const noteForm = reactive({
  body: "",
  reason: ""
});
const piiFormRef = ref<FormInst | null>(null);
const piiForm = reactive({
  reason: ""
});
const tagFormRef = ref<FormInst | null>(null);
const tagForm = reactive({
  name: "",
  color: "#2563EB",
  reason: ""
});
const tagMutationDialogRef = ref<InstanceType<typeof UserTagMutationDialog> | null>(null);
const elevationDialogRef = ref<InstanceType<typeof ElevationDialog> | null>(null);
const governanceFormRef = ref<FormInst | null>(null);
const governanceForm = reactive({
  commandType: AdminUserCommandType.SUSPEND,
  roomId: "",
  reason: ""
});

const rules: FormRules = {
  body: { required: true, message: "请输入备注内容", trigger: ["input", "blur"] },
  reason: { required: true, message: "请输入操作原因", trigger: ["input", "blur"] },
  name: { required: true, message: "请输入标签名称", trigger: ["input", "blur"] },
  // The format rule mirrors the backend #RRGGBB contract so operators get inline feedback
  // instead of a server-side invalid_argument after submitting.
  color: [
    { required: true, message: "请输入标签颜色", trigger: ["input", "blur"] },
    {
      validator: (_rule, value: string) => !value || isValidTagColorInput(value),
      message: "颜色格式应为 #RRGGBB，例如 #2563EB",
      trigger: ["input", "blur"]
    }
  ],
  commandType: { required: true, type: "number", message: "请选择治理动作", trigger: ["change"] }
};

const users = ref<AdminUserSummary[]>([]);
const tags = ref<AdminUserTag[]>([]);
// Server-side catalog CAS version; tag creation must present it, and every catalog mutation
// refreshes it so a stale value cannot silently target an outdated catalog state.
const tagCatalogVersion = ref(0n);
const detail = ref<AdminUserDetail | null>(null);
const notes = ref<AdminUserNote[]>([]);
const piiValues = ref<AdminUserPIIValue[]>([]);
const piiAuditEventId = ref("");
const selectedUserId = ref("");
const drawerOpen = ref(false);
const loadingUsers = ref(false);
const loadingDetail = ref(false);
const savingTags = ref(false);
const savingNote = ref(false);
const loadingPii = ref(false);
const creatingTag = ref(false);
const previewingGovernance = ref(false);
const executingGovernance = ref(false);
const governancePreview = ref<PreviewUserCommandResponse | null>(null);
const nextPageToken = ref("");
// Batch selection tracks explicit targets by current list membership so version-bound batch tasks stay accurate.
const selectedBatchUserIds = ref<string[]>([]);
// Governance execution tokens prevent a late elevation callback from mutating a different user drawer state.
let governanceExecutionGeneration = 0;

// Request generation protects the detail drawer from stale writes when operators switch users quickly.
let requestGeneration = 0;
let activeController: AbortController | null = null;

const canReadPii = computed(() => auth.permissions.includes(AdminPermission.USERS_READ_PII));
const canAnnotate = computed(() => auth.permissions.includes(AdminPermission.USERS_ANNOTATE));
const canGovern = computed(() => auth.permissions.includes(AdminPermission.USERS_GOVERN));
const canControlRooms = computed(() => auth.permissions.includes(AdminPermission.ROOMS_CONTROL));
const canAccessGovernance = computed(() => canGovern.value || canControlRooms.value);
const selectedSummary = computed(() => detail.value?.summary ?? users.value.find((user) => user.userId === selectedUserId.value) ?? null);
const selectedTagIds = computed({
  get: () => selectedSummary.value?.tags.map((tag) => tag.tagId) ?? [],
  set: () => undefined
});
const selectedBatchUsers = computed(() => users.value
  .filter((user) => selectedBatchUserIds.value.includes(user.userId))
  .map((user) => ({
    userId: user.userId,
    username: user.username,
    expectedUserVersion: user.version
  })));
const batchFilterInput = computed(() => ({
  username: searchForm.username,
  status: Number(searchForm.status),
  tagIds: searchForm.tagIds
}));
const tagOptions = computed<SelectOption[]>(() => tags.value.map((tag) => ({
  label: tag.name,
  value: tag.tagId
})));
const roomCommandOptions = computed<SelectOption[]>(() => (detail.value?.rooms ?? []).map((room) => ({
  label: `${room.roomCode || room.roomId} · ${formatRoomRole(room.membershipRole)}`,
  value: room.roomId
})));
const statusOptions: SelectOption[] = [
  { label: "全部状态", value: 0 },
  { label: "注册中", value: AdminUserStatus.ONBOARDING },
  { label: "正常", value: AdminUserStatus.ACTIVE },
  { label: "已停权", value: AdminUserStatus.SUSPENDED },
  { label: "已删除", value: AdminUserStatus.DELETED }
];
const governanceCommandCatalog: Array<{ label: string; value: AdminUserCommandType }> = [
  { label: "停权用户", value: AdminUserCommandType.SUSPEND },
  { label: "解除停权", value: AdminUserCommandType.UNSUSPEND },
  { label: "撤销全部设备", value: AdminUserCommandType.REVOKE_ALL_DEVICES },
  { label: "移出当前房间", value: AdminUserCommandType.REMOVE_FROM_CURRENT_ROOM },
  { label: "删除用户", value: AdminUserCommandType.DELETE }
];

const userStatusType = (status: AdminUserStatus): "default" | "success" | "warning" | "error" => {
  switch (status) {
    case AdminUserStatus.ACTIVE:
      return "success";
    case AdminUserStatus.ONBOARDING:
      return "warning";
    case AdminUserStatus.SUSPENDED:
    case AdminUserStatus.DELETED:
      return "error";
    default:
      return "default";
  }
};

const formatUserStatus = (status: AdminUserStatus): string => {
  switch (status) {
    case AdminUserStatus.ONBOARDING:
      return "注册中";
    case AdminUserStatus.ACTIVE:
      return "正常";
    case AdminUserStatus.SUSPENDED:
      return "已停权";
    case AdminUserStatus.DELETED:
      return "已删除";
    default:
      return "未知";
  }
};

const formatCommand = (command: AdminUserCommandType): string => {
  switch (command) {
    case AdminUserCommandType.SUSPEND:
      return "停权用户";
    case AdminUserCommandType.UNSUSPEND:
      return "解除停权";
    case AdminUserCommandType.REVOKE_ALL_DEVICES:
      return "撤销全部设备";
    case AdminUserCommandType.REMOVE_FROM_CURRENT_ROOM:
      return "移出当前房间";
    case AdminUserCommandType.DELETE:
      return "删除用户";
    default:
      return "未知治理";
  }
};

const formatCommandOutcome = (outcome: AdminUserCommandOutcome): string => {
  switch (outcome) {
    case AdminUserCommandOutcome.EXECUTED:
      return "已执行";
    case AdminUserCommandOutcome.NO_CHANGE:
      return "无变化";
    case AdminUserCommandOutcome.REJECTED:
      return "已拒绝";
    default:
      return "未知结果";
  }
};

const formatCommandBlocker = (type: AdminUserCommandBlockerType): string => {
  switch (type) {
    case AdminUserCommandBlockerType.ACTIVE_GAME:
      return "存在进行中牌局";
    case AdminUserCommandBlockerType.PENDING_EXPORT:
      return "存在待处理导出";
    case AdminUserCommandBlockerType.VERSION_CHANGED:
      return "用户版本已变化";
    case AdminUserCommandBlockerType.ALREADY_DELETED:
      return "用户已删除";
    default:
      return "未知阻断";
  }
};

const formatRoomRole = (role: AdminUserRoomSummary["membershipRole"]): string => {
  switch (role) {
    case 1:
      return "玩家";
    case 2:
      return "观众";
    case 3:
      return "等待";
    default:
      return "未知角色";
  }
};

const errorMessage = (error: unknown, fallback: string): string =>
  error instanceof AdminApiError ? error.message : fallback;

const permissionForGovernanceCommand = (commandType: AdminUserCommandType): AdminPermission =>
  commandType === AdminUserCommandType.REMOVE_FROM_CURRENT_ROOM
    ? AdminPermission.ROOMS_CONTROL
    : AdminPermission.USERS_GOVERN;

const hasGovernancePermission = (commandType: AdminUserCommandType): boolean =>
  auth.permissions.includes(permissionForGovernanceCommand(commandType));

const governanceCommandOptions = computed<SelectOption[]>(() => governanceCommandCatalog
  .filter((option) => hasGovernancePermission(option.value))
  .map((option) => ({
    label: option.label,
    value: option.value
  })));

const beginRequest = (): { token: number; signal: AbortSignal } => {
  requestGeneration += 1;
  activeController?.abort();
  activeController = new AbortController();
  return { token: requestGeneration, signal: activeController.signal };
};

const isCurrentRequest = (token: number, signal: AbortSignal): boolean =>
  !signal.aborted && token === requestGeneration;

const invalidateGovernanceExecution = (): void => {
  governanceExecutionGeneration += 1;
  executingGovernance.value = false;
  elevationDialogRef.value?.toggleDialog(false);
};

const clearGovernancePreview = (): void => {
  governancePreview.value = null;
  invalidateGovernanceExecution();
};

const clearSensitiveDetail = (): void => {
  piiValues.value = [];
  piiAuditEventId.value = "";
  piiForm.reason = "";
  clearGovernancePreview();
  piiFormRef.value?.restoreValidation();
  governanceFormRef.value?.restoreValidation();
};

const buildGovernanceCommand = (): AdminUserCommandInput | null => {
  if (governanceForm.commandType !== AdminUserCommandType.REMOVE_FROM_CURRENT_ROOM) {
    return { type: governanceForm.commandType };
  }
  const room = detail.value?.rooms.find((entry) => entry.roomId === governanceForm.roomId);
  if (!room) {
    message.warning("请选择要移出的房间。");
    return null;
  }
  return {
    type: governanceForm.commandType,
    roomId: room.roomId,
    expectedRoomVersion: room.roomVersion,
    expectedMembershipVersion: room.membershipVersion
  };
};

watch(governanceCommandOptions, (options) => {
  if (options.some((option) => option.value === governanceForm.commandType)) {
    return;
  }
  const fallbackCommand = options[0]?.value;
  if (typeof fallbackCommand === "number") {
    governanceForm.commandType = fallbackCommand as AdminUserCommandType;
  }
  governanceForm.roomId = "";
  clearGovernancePreview();
}, { immediate: true });

const loadTags = async (): Promise<void> => {
  try {
    const response = await listUserTags({ pageSize: 100 });
    tags.value = response.tags;
    tagCatalogVersion.value = response.catalogVersion ?? 0n;
  } catch (error) {
    message.error(errorMessage(error, "加载标签失败。"));
  }
};

const loadUsers = async (pageToken = ""): Promise<void> => {
  const { token, signal } = beginRequest();
  loadingUsers.value = true;
  try {
    const response = await listUsers({
      username: searchForm.username,
      status: Number(searchForm.status),
      tagIds: searchForm.tagIds,
      pageSize: 20,
      pageToken,
      signal
    });
    if (!isCurrentRequest(token, signal)) {
      return;
    }
    const nextUsers = pageToken ? [...users.value, ...response.users] : response.users;
    users.value = nextUsers;
    const availableIds = new Set(nextUsers.map((user) => user.userId));
    selectedBatchUserIds.value = selectedBatchUserIds.value.filter((userId) => availableIds.has(userId));
    nextPageToken.value = response.page?.nextPageToken ?? "";
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "加载用户列表失败。"));
    }
  } finally {
    if (isCurrentRequest(token, signal)) {
      loadingUsers.value = false;
    }
  }
};

const loadDetail = async (userId: string): Promise<void> => {
  const { token, signal } = beginRequest();
  loadingDetail.value = true;
  clearSensitiveDetail();
  try {
    const [detailResponse, notesResponse] = await Promise.all([
      getUser({ userId, signal }),
      listUserNotes({ userId, pageSize: 20, signal })
    ]);
    if (!isCurrentRequest(token, signal)) {
      return;
    }
    detail.value = detailResponse.user ?? null;
    notes.value = notesResponse.notes;
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "加载用户详情失败。"));
    }
  } finally {
    if (isCurrentRequest(token, signal)) {
      loadingDetail.value = false;
    }
  }
};

const openUser = (user: AdminUserSummary): void => {
  selectedUserId.value = user.userId;
  drawerOpen.value = true;
  detail.value = null;
  notes.value = [];
  void loadDetail(user.userId);
};

const handleCloseDrawer = (): void => {
  drawerOpen.value = false;
  selectedUserId.value = "";
  detail.value = null;
  notes.value = [];
  clearSensitiveDetail();
};

const handleSearch = (): void => {
  void loadUsers();
};

const isBatchUserSelected = (userId: string): boolean => selectedBatchUserIds.value.includes(userId);

const toggleBatchUserSelection = (userId: string, checked: boolean): void => {
  selectedBatchUserIds.value = checked
    ? [...selectedBatchUserIds.value, userId]
    : selectedBatchUserIds.value.filter((value) => value !== userId);
};

const handleSelectAllVisibleUsers = (): void => {
  selectedBatchUserIds.value = users.value.map((user) => user.userId);
};

const handleClearBatchUserSelection = (): void => {
  selectedBatchUserIds.value = [];
};

/**
 * Opens a version-bound mutation dialog for the selected catalog tag. The child owns the form,
 * reason, and request lifecycle so this list never retains mutation-only state between actions.
 */
const openTagMutationDialog = (tag: AdminUserTag, mode: UserTagMutationDialogPayload["mode"]): void => {
  tagMutationDialogRef.value?.toggleDialog(true, { tag, mode });
};

/**
 * Reconciles a changed tag through every local user projection without refetching unrelated data.
 */
const handleTagUpdated = (updatedTag: AdminUserTag): void => {
  const updateSummary = (summary: AdminUserSummary): AdminUserSummary => ({
    ...summary,
    tags: summary.tags.map((tag) => tag.tagId === updatedTag.tagId ? updatedTag : tag)
  });

  tags.value = tags.value.map((tag) => tag.tagId === updatedTag.tagId ? updatedTag : tag);
  users.value = users.value.map(updateSummary);
  if (detail.value?.summary) {
    detail.value = { ...detail.value, summary: updateSummary(detail.value.summary) };
  }
  // Tag updates advance the server catalog version but the update response does not carry it,
  // so resync the version (and list) to keep the next creation's CAS check valid.
  void loadTags();
};

/**
 * Mirrors the server's catalog delete semantics: assignments are removed from all cached summaries.
 */
const handleTagDeleted = (tagId: string): void => {
  const removeFromSummary = (summary: AdminUserSummary): AdminUserSummary => ({
    ...summary,
    tags: summary.tags.filter((tag) => tag.tagId !== tagId)
  });

  tags.value = tags.value.filter((tag) => tag.tagId !== tagId);
  users.value = users.value.map(removeFromSummary);
  if (detail.value?.summary) {
    detail.value = { ...detail.value, summary: removeFromSummary(detail.value.summary) };
  }
  // Deletion advances the catalog version; resync so the next creation presents the live version.
  void loadTags();
};

const handleAssignTags = async (tagIds: string[]): Promise<void> => {
  const summary = selectedSummary.value;
  if (!summary) {
    return;
  }
  savingTags.value = true;
  try {
    const response = await setUserTags({
      operationId: createOperationId(),
      userId: summary.userId,
      tagIds,
      reason: "后台用户中心标签维护",
      expectedVersion: summary.version
    });
    if (response.user) {
      detail.value = detail.value ? { ...detail.value, summary: response.user } : detail.value;
      users.value = users.value.map((user) => user.userId === response.user?.userId ? response.user : user);
    }
    message.success("用户标签已更新。");
  } catch (error) {
    message.error(errorMessage(error, "更新用户标签失败。"));
  } finally {
    savingTags.value = false;
  }
};

const handleAppendNote = async (): Promise<void> => {
  await noteFormRef.value?.validate();
  const summary = selectedSummary.value;
  if (!summary) {
    return;
  }
  savingNote.value = true;
  try {
    const response = await appendUserNote({
      operationId: createOperationId(),
      userId: summary.userId,
      body: noteForm.body,
      reason: noteForm.reason,
      expectedVersion: summary.version
    });
    if (response.note) {
      notes.value = [response.note, ...notes.value];
      if (detail.value) {
        const nextDetail = {
          ...detail.value,
          recentNotes: [response.note, ...detail.value.recentNotes]
        };
        if (detail.value.summary) {
          nextDetail.summary = { ...detail.value.summary, version: response.userVersion };
        }
        detail.value = nextDetail;
      }
    }
    users.value = users.value.map((user) => user.userId === summary.userId ? { ...user, version: response.userVersion } : user);
    noteForm.body = "";
    noteForm.reason = "";
    noteFormRef.value?.restoreValidation();
    message.success("备注已追加。");
  } catch (error) {
    message.error(errorMessage(error, "追加备注失败。"));
  } finally {
    savingNote.value = false;
  }
};

const handleRevealPii = async (): Promise<void> => {
  await piiFormRef.value?.validate();
  const summary = selectedSummary.value;
  if (!summary) {
    return;
  }
  loadingPii.value = true;
  try {
    const response = await getUserPII({
      userId: summary.userId,
      fields: [AdminUserPIIField.ADMIN_USER_PII_FIELD_REAL_NAME],
      reason: piiForm.reason
    });
    piiValues.value = response.values;
    piiAuditEventId.value = response.accessAuditEventId;
  } catch (error) {
    message.error(errorMessage(error, "读取 PII 失败。"));
  } finally {
    loadingPii.value = false;
  }
};

const handlePreviewGovernance = async (): Promise<void> => {
  await governanceFormRef.value?.validate();
  const summary = selectedSummary.value;
  const command = buildGovernanceCommand();
  if (!summary || !command || !hasGovernancePermission(command.type)) {
    return;
  }
  previewingGovernance.value = true;
  clearGovernancePreview();
  try {
    governancePreview.value = await previewUserCommand({
      userId: summary.userId,
      command,
      reason: governanceForm.reason,
      expectedUserVersion: summary.version
    });
    message.success("治理预览已生成。");
  } catch (error) {
    message.error(errorMessage(error, "生成治理预览失败。"));
  } finally {
    previewingGovernance.value = false;
  }
};

const executeGovernanceWithPreview = async (
  summary: NonNullable<typeof selectedSummary.value>,
  preview: PreviewUserCommandResponse,
  command: AdminUserCommandInput,
  executionGeneration: number
): Promise<void> => {
  if (
    executionGeneration !== governanceExecutionGeneration ||
    selectedSummary.value?.userId !== summary.userId ||
    governancePreview.value?.previewId !== preview.previewId
  ) {
    return;
  }
  executingGovernance.value = true;
  try {
    const response = await executeUserCommand({
      operationId: createOperationId(),
      userId: summary.userId,
      command,
      previewId: preview.previewId,
      previewDigest: preview.previewDigest,
      reason: governanceForm.reason,
      expectedUserVersion: preview.expectedUserVersion
    });
    if (
      executionGeneration !== governanceExecutionGeneration ||
      selectedSummary.value?.userId !== summary.userId ||
      governancePreview.value?.previewId !== preview.previewId
    ) {
      return;
    }
    if (response.user) {
      detail.value = detail.value ? { ...detail.value, summary: response.user } : detail.value;
      users.value = users.value.map((user) => user.userId === response.user?.userId ? response.user : user);
    }
    clearGovernancePreview();
    message.success(`治理执行完成：${formatCommandOutcome(response.outcome)}。`);
  } catch (error) {
    if (executionGeneration === governanceExecutionGeneration) {
      message.error(errorMessage(error, "执行治理命令失败。"));
    }
  } finally {
    if (executionGeneration === governanceExecutionGeneration) {
      executingGovernance.value = false;
    }
  }
};

const handleExecuteGovernance = async (): Promise<void> => {
  const summary = selectedSummary.value;
  const preview = governancePreview.value;
  const command = buildGovernanceCommand();
  if (!summary || !preview || !command || preview.blockers.length || !hasGovernancePermission(command.type)) {
    return;
  }
  const executionGeneration = governanceExecutionGeneration + 1;
  governanceExecutionGeneration = executionGeneration;
  if (Number(preview.requiredElevation) > AdminElevationScope.UNSPECIFIED) {
    const elevationPayload: ElevationDialogPayload = {
      scope: preview.requiredElevation,
      allowRecoveryCode: false,
      onElevated: () => {
        void executeGovernanceWithPreview(summary, preview, command, executionGeneration);
      }
    };
    elevationDialogRef.value?.toggleDialog(true, elevationPayload);
    return;
  }
  await executeGovernanceWithPreview(summary, preview, command, executionGeneration);
};

const handleCreateTag = async (): Promise<void> => {
  await tagFormRef.value?.validate();
  // Canonicalize before the request so the catalog, the audit digest, and the echoed form value
  // all use the uppercase form enforced by the backend contract.
  tagForm.color = normalizeTagColor(tagForm.color);
  // The catalog CAS is 1-based; an unloaded (0) version would be rejected server-side, so fetch
  // the current version first instead of sending a request that cannot succeed.
  if (tagCatalogVersion.value === 0n) {
    await loadTags();
  }
  creatingTag.value = true;
  try {
    const response = await createUserTag({
      operationId: createOperationId(),
      name: tagForm.name,
      color: tagForm.color,
      reason: tagForm.reason,
      expectedVersion: tagCatalogVersion.value
    });
    if (response.tag) {
      tags.value = [response.tag, ...tags.value];
    }
    if (response.catalogVersion) {
      tagCatalogVersion.value = response.catalogVersion;
    }
    tagForm.name = "";
    tagForm.reason = "";
    tagFormRef.value?.restoreValidation();
    message.success("标签已创建。");
  } catch (error) {
    message.error(errorMessage(error, "创建标签失败。"));
  } finally {
    creatingTag.value = false;
  }
};

onMounted(() => {
  void loadTags();
  void loadUsers();
});

onBeforeUnmount(() => {
  activeController?.abort();
  clearSensitiveDetail();
});
</script>

<template>
  <div class="admin-view user-center">
    <div class="admin-toolbar user-center__toolbar">
      <div class="admin-page-title">
        <h1>用户中心</h1>
      </div>
      <NButton secondary :loading="loadingUsers" @click="handleSearch">
        <template #icon>
          <RefreshCw :size="16" />
        </template>
        刷新
      </NButton>
    </div>

    <div class="user-center__grid">
      <NCard :bordered="false" class="user-center__panel">
        <template #header>
          <span class="user-center__panel-title">检索条件</span>
        </template>
        <NSpace vertical size="large">
          <NForm :model="searchForm" label-placement="top">
            <NFormItem label="用户名">
              <NInput v-model:value="searchForm.username" clearable placeholder="输入用户名关键字" @keyup.enter="handleSearch" />
            </NFormItem>
            <NFormItem label="状态">
              <NSelect v-model:value="searchForm.status" :options="statusOptions" />
            </NFormItem>
            <NFormItem label="标签">
              <NSelect v-model:value="searchForm.tagIds" multiple clearable :options="tagOptions" placeholder="按标签过滤" />
            </NFormItem>
            <NButton type="primary" block :loading="loadingUsers" @click="handleSearch">
              <template #icon>
                <Search :size="16" />
              </template>
              查询用户
            </NButton>
          </NForm>

          <PermissionGate :permission="AdminPermission.USERS_ANNOTATE">
            <NForm ref="tagFormRef" :model="tagForm" :rules="rules" label-placement="top" class="user-center__tag-form">
              <h3>新建标签</h3>
              <NFormItem label="名称" path="name">
                <NInput v-model:value="tagForm.name" maxlength="24" placeholder="例如：高价值玩家" />
              </NFormItem>
              <NFormItem label="颜色" path="color">
                <NInput v-model:value="tagForm.color" placeholder="#2563EB">
                  <template #suffix>
                    <span
                      v-if="isValidTagColorInput(tagForm.color)"
                      :style="{ display: 'inline-block', width: '14px', height: '14px', borderRadius: '4px', background: normalizeTagColor(tagForm.color) }"
                    />
                  </template>
                </NInput>
              </NFormItem>
              <NFormItem label="原因" path="reason">
                <NInput v-model:value="tagForm.reason" type="textarea" placeholder="说明创建这个标签的运营原因" />
              </NFormItem>
              <NButton secondary block :loading="creatingTag" @click="handleCreateTag">
                <template #icon>
                  <Tag :size="16" />
                </template>
                创建标签
              </NButton>
            </NForm>
            <div class="user-center__tag-catalog">
              <div class="user-center__tag-catalog-header">
                <h3>标签目录</h3>
                <NTag size="small" type="info">{{ tags.length }}</NTag>
              </div>
              <div v-if="tags.length" class="user-center__tag-catalog-list">
                <div v-for="tag in tags" :key="tag.tagId" class="user-center__tag-catalog-row">
                  <NTag size="small" :color="{ color: tag.color, textColor: '#fff' }">{{ tag.name }}</NTag>
                  <span>版本 {{ tag.version }}</span>
                  <NSpace size="small">
                    <NTooltip>
                      <template #trigger>
                        <NButton
                          circle
                          quaternary
                          size="small"
                          :aria-label="`编辑标签 ${tag.name}`"
                          @click="openTagMutationDialog(tag, 'edit')"
                        >
                          <template #icon>
                            <Pencil :size="15" />
                          </template>
                        </NButton>
                      </template>
                      编辑标签
                    </NTooltip>
                    <NTooltip>
                      <template #trigger>
                        <NButton
                          circle
                          quaternary
                          size="small"
                          type="error"
                          :aria-label="`删除标签 ${tag.name}`"
                          @click="openTagMutationDialog(tag, 'delete')"
                        >
                          <template #icon>
                            <Trash2 :size="15" />
                          </template>
                        </NButton>
                      </template>
                      删除标签
                    </NTooltip>
                  </NSpace>
                </div>
              </div>
              <NEmpty v-else size="small" description="尚未创建标签" />
            </div>
          </PermissionGate>
        </NSpace>
      </NCard>

      <NCard :bordered="false" class="user-center__list">
        <template #header>
          <div class="user-center__list-header">
            <span class="user-center__panel-title">用户列表</span>
            <PermissionGate :permission="AdminPermission.USERS_GOVERN">
              <NSpace size="small">
                <NTag size="small" type="info">已勾选 {{ selectedBatchUserIds.length }}</NTag>
                <NButton size="small" secondary @click="handleSelectAllVisibleUsers">全选当前页</NButton>
                <NButton size="small" quaternary @click="handleClearBatchUserSelection">清空</NButton>
              </NSpace>
            </PermissionGate>
          </div>
        </template>
        <NSpin :show="loadingUsers">
          <div v-if="users.length" class="user-list">
            <div v-for="user in users" :key="user.userId" class="user-list__row">
              <PermissionGate :permission="AdminPermission.USERS_GOVERN">
                <div class="user-list__check">
                  <NCheckbox
                    :checked="isBatchUserSelected(user.userId)"
                    @update:checked="(checked) => toggleBatchUserSelection(user.userId, checked)"
                  />
                </div>
              </PermissionGate>
              <button class="user-list__item" type="button" @click="openUser(user)">
                <div class="user-list__avatar">
                  <UserRound :size="18" />
                </div>
                <div class="user-list__main">
                  <div class="user-list__topline">
                    <strong>{{ user.username || "未命名用户" }}</strong>
                    <NTag size="small" :type="userStatusType(user.status)">{{ formatUserStatus(user.status) }}</NTag>
                  </div>
                  <div class="user-list__meta">
                    <span>{{ user.userId }}</span>
                    <span>版本 {{ user.version }}</span>
                    <span>{{ user.online ? "在线" : "离线" }}</span>
                  </div>
                  <div class="user-list__tags">
                    <NTag v-for="tag in user.tags" :key="tag.tagId" size="small" :color="{ color: tag.color, textColor: '#fff' }">
                      {{ tag.name }}
                    </NTag>
                    <span v-if="!user.tags.length">暂无标签</span>
                  </div>
                </div>
                <div class="user-list__time">
                  <span>最近活跃</span>
                  <strong>{{ formatDateTime(user.lastActivityAt) }}</strong>
                </div>
              </button>
            </div>
            <NButton v-if="nextPageToken" secondary block :loading="loadingUsers" @click="loadUsers(nextPageToken)">
              加载更多
            </NButton>
          </div>
          <NEmpty v-else description="暂无匹配用户" />
        </NSpin>
      </NCard>
    </div>

    <PermissionGate :permission="AdminPermission.USERS_GOVERN">
      <BatchGovernancePanel :selected-users="selectedBatchUsers" :current-filter="batchFilterInput" />
    </PermissionGate>

    <NDrawer :show="drawerOpen" width="720" placement="right" @update:show="(value) => !value && handleCloseDrawer()">
      <NDrawerContent :title="selectedSummary?.username || '用户详情'" closable>
        <NSpin :show="loadingDetail">
          <NSpace v-if="selectedSummary" vertical size="large">
            <NDescriptions bordered :column="2" label-placement="left">
              <NDescriptionsItem label="用户 ID">{{ selectedSummary.userId }}</NDescriptionsItem>
              <NDescriptionsItem label="状态">
                <NTag :type="userStatusType(selectedSummary.status)">{{ formatUserStatus(selectedSummary.status) }}</NTag>
              </NDescriptionsItem>
              <NDescriptionsItem label="创建时间">{{ formatDateTime(selectedSummary.createdAt) }}</NDescriptionsItem>
              <NDescriptionsItem label="最近活跃">{{ formatDateTime(selectedSummary.lastActivityAt) }}</NDescriptionsItem>
              <NDescriptionsItem label="在线状态">{{ selectedSummary.online ? "在线" : "离线" }}</NDescriptionsItem>
              <NDescriptionsItem label="用户版本">{{ selectedSummary.version }}</NDescriptionsItem>
            </NDescriptions>

            <PermissionGate :permission="AdminPermission.USERS_ANNOTATE">
              <NCard title="标签分配" :bordered="false" size="small">
                <NSelect
                  :value="selectedTagIds"
                  multiple
                  clearable
                  :options="tagOptions"
                  :loading="savingTags"
                  placeholder="选择用户标签"
                  @update:value="handleAssignTags"
                />
              </NCard>
            </PermissionGate>

            <NTabs type="line" animated>
              <NTabPane name="notes" tab="备注">
                <PermissionGate :permission="AdminPermission.USERS_ANNOTATE">
                  <NForm ref="noteFormRef" :model="noteForm" :rules="rules" label-placement="top" class="user-center__note-form">
                    <NFormItem label="备注内容" path="body">
                      <NInput v-model:value="noteForm.body" type="textarea" placeholder="记录客服、风控或运营结论" />
                    </NFormItem>
                    <NFormItem label="原因" path="reason">
                      <NInput v-model:value="noteForm.reason" placeholder="写入备注的业务原因" />
                    </NFormItem>
                    <NButton type="primary" :loading="savingNote" @click="handleAppendNote">追加备注</NButton>
                  </NForm>
                </PermissionGate>
                <div class="user-center__notes">
                  <NThing v-for="note in notes" :key="note.noteId" :title="note.reason || '无原因备注'">
                    <template #description>
                      {{ formatDateTime(note.createdAt) }} · {{ note.authorAdminId }}
                    </template>
                    <p>{{ note.body }}</p>
                  </NThing>
                  <NEmpty v-if="!notes.length" description="暂无备注" />
                </div>
              </NTabPane>
              <NTabPane name="pii" tab="PII">
                <PermissionGate :permission="AdminPermission.USERS_READ_PII">
                  <NAlert type="warning" :bordered="false">
                    PII 明文只在当前抽屉内短暂展示；切换用户、关闭抽屉或离开页面会立即清空。
                  </NAlert>
                  <NForm ref="piiFormRef" :model="piiForm" :rules="rules" label-placement="top" class="user-center__pii-form">
                    <NFormItem label="查看原因" path="reason">
                      <NInput v-model:value="piiForm.reason" placeholder="例如：用户申诉核验实名信息" />
                    </NFormItem>
                    <NButton type="warning" :loading="loadingPii" @click="handleRevealPii">
                      <template #icon>
                        <Eye :size="16" />
                      </template>
                      揭示实名信息
                    </NButton>
                  </NForm>
                  <div v-if="piiValues.length" class="user-center__pii-result">
                    <NDescriptions bordered :column="1">
                      <NDescriptionsItem v-for="value in piiValues" :key="value.field" label="实名">
                        {{ value.value || "未提供" }}
                      </NDescriptionsItem>
                      <NDescriptionsItem label="审计事件">{{ piiAuditEventId }}</NDescriptionsItem>
                    </NDescriptions>
                  </div>
                </PermissionGate>
                <NEmpty v-if="!canReadPii" description="当前管理员无 PII 读取权限" />
              </NTabPane>
              <NTabPane name="governance" tab="治理">
                <template v-if="canAccessGovernance && governanceCommandOptions.length">
                  <NAlert type="warning" :bordered="false">
                    单用户治理必须先预览影响；移出当前房间依赖房间控制权限，其余动作依赖用户治理权限，所有执行都会写入审计。
                  </NAlert>
                  <NForm ref="governanceFormRef" :model="governanceForm" :rules="rules" label-placement="top" class="user-center__governance-form">
                    <NFormItem label="治理动作" path="commandType">
                      <NSelect v-model:value="governanceForm.commandType" :options="governanceCommandOptions" @update:value="clearGovernancePreview" />
                    </NFormItem>
                    <NFormItem v-if="governanceForm.commandType === AdminUserCommandType.REMOVE_FROM_CURRENT_ROOM" label="目标房间" path="roomId">
                      <NSelect v-model:value="governanceForm.roomId" :options="roomCommandOptions" placeholder="选择要移出的当前房间" @update:value="clearGovernancePreview" />
                    </NFormItem>
                    <NFormItem label="原因" path="reason">
                      <NInput v-model:value="governanceForm.reason" type="textarea" placeholder="说明治理原因，供审计和复核使用" @input="clearGovernancePreview" />
                    </NFormItem>
                    <NSpace>
                      <NButton type="warning" :loading="previewingGovernance" @click="handlePreviewGovernance">预览治理</NButton>
                      <NPopconfirm :disabled="!governancePreview || !!governancePreview.blockers.length" @positive-click="handleExecuteGovernance">
                        <template #trigger>
                          <NButton type="error" :disabled="!governancePreview || !!governancePreview.blockers.length" :loading="executingGovernance">
                            执行治理
                          </NButton>
                        </template>
                        确认执行 {{ formatCommand(governanceForm.commandType) }}？
                      </NPopconfirm>
                    </NSpace>
                  </NForm>
                  <NDescriptions v-if="governancePreview" bordered :column="1" label-placement="left" class="user-center__governance-preview">
                    <NDescriptionsItem label="预览 ID">{{ governancePreview.previewId }}</NDescriptionsItem>
                      <NDescriptionsItem label="影响设备">{{ governancePreview.affectedDevices }}</NDescriptionsItem>
                      <NDescriptionsItem label="影响房间">{{ governancePreview.affectedRooms }}</NDescriptionsItem>
                      <NDescriptionsItem v-if="Number(governancePreview.requiredElevation) !== AdminElevationScope.UNSPECIFIED" label="提权范围">
                        {{ governancePreview.requiredElevation }}
                      </NDescriptionsItem>
                      <NDescriptionsItem label="过期时间">{{ formatDateTime(governancePreview.expiresAt) }}</NDescriptionsItem>
                      <NDescriptionsItem label="阻断项">
                        <NTag v-for="blocker in governancePreview.blockers" :key="`${blocker.type}-${blocker.resourceId}`" type="error" size="small">
                        {{ formatCommandBlocker(blocker.type) }} {{ blocker.resourceId }}
                      </NTag>
                      <span v-if="!governancePreview.blockers.length">无阻断，可执行</span>
                      </NDescriptionsItem>
                    </NDescriptions>
                </template>
                <NEmpty v-else description="当前管理员无可用的单用户治理动作权限" />
              </NTabPane>
              <NTabPane name="activity" tab="活动快照">
                <div class="user-center__activity">
                  <NCard title="设备" size="small" :bordered="false">
                    <p>{{ detail?.devices.length ?? 0 }} 个设备凭据</p>
                  </NCard>
                  <NCard title="房间" size="small" :bordered="false">
                    <p>{{ detail?.rooms.length ?? 0 }} 个近期房间</p>
                  </NCard>
                  <NCard title="牌局" size="small" :bordered="false">
                    <p>{{ detail?.recentGames.length ?? 0 }} 个近期牌局</p>
                  </NCard>
                  <NCard title="治理记录" size="small" :bordered="false">
                    <p>{{ detail?.recentGovernance.length ?? 0 }} 条近期记录</p>
                  </NCard>
                </div>
              </NTabPane>
            </NTabs>
          </NSpace>
          <NEmpty v-else description="请选择用户" />
        </NSpin>
      </NDrawerContent>
    </NDrawer>
    <UserTagMutationDialog ref="tagMutationDialogRef" @updated="handleTagUpdated" @deleted="handleTagDeleted" />
    <ElevationDialog ref="elevationDialogRef" />
  </div>
</template>

<style scoped>
.user-center {
  --user-ink: var(--admin-ink);
  --user-muted: var(--admin-muted);
  --user-card: var(--admin-surface);
  --user-line: var(--admin-line);
}

.user-center__grid {
  /* Keep the result list content-sized when the adjacent filters or tag form are taller. */
  align-items: start;
  display: grid;
  gap: 18px;
  grid-template-columns: minmax(280px, 340px) minmax(0, 1fr);
  margin-top: 12px;
}

.user-center__panel,
.user-center__list {
  background: var(--user-card);
  border: 1px solid var(--user-line);
  border-radius: 8px;
  box-shadow: var(--admin-shadow);
}

.user-center__panel-title {
  color: var(--user-ink);
  font-weight: 800;
}

.user-center__list-header {
  align-items: center;
  display: flex;
  gap: 12px;
  justify-content: space-between;
}

.user-center__tag-form {
  border-top: 1px solid var(--user-line);
  padding-top: 16px;
}

.user-center__tag-form h3 {
  font-size: 14px;
  margin: 0 0 12px;
}

.user-center__tag-catalog {
  border-top: 1px solid var(--user-line);
  margin-top: 16px;
  padding-top: 16px;
}

.user-center__tag-catalog-header,
.user-center__tag-catalog-row {
  align-items: center;
  display: flex;
}

.user-center__tag-catalog-header {
  gap: 8px;
  justify-content: space-between;
  margin-bottom: 10px;
}

.user-center__tag-catalog-header h3 {
  font-size: 14px;
  margin: 0;
}

.user-center__tag-catalog-list {
  display: grid;
  gap: 6px;
  max-height: 240px;
  overflow-y: auto;
  padding-right: 2px;
  scrollbar-gutter: stable;
}

.user-center__tag-catalog-row {
  background: var(--admin-surface-muted);
  border: 1px solid var(--user-line);
  border-radius: 6px;
  gap: 8px;
  min-width: 0;
  padding: 6px 8px;
}

.user-center__tag-catalog-row > span {
  color: var(--user-muted);
  flex: 1;
  font-size: 12px;
  min-width: 0;
}

.user-list {
  display: grid;
  gap: 12px;
}

.user-list__row {
  align-items: stretch;
  display: grid;
  gap: 10px;
  grid-template-columns: auto minmax(0, 1fr);
}

.user-list__check {
  align-items: center;
  display: flex;
  justify-content: center;
  padding: 0 4px;
}

.user-list__item {
  align-items: center;
  background: var(--admin-surface-muted);
  border: 1px solid var(--user-line);
  border-radius: 6px;
  color: inherit;
  cursor: pointer;
  display: grid;
  gap: 14px;
  grid-template-columns: 44px minmax(0, 1fr) minmax(140px, auto);
  padding: 14px;
  text-align: left;
  transition: border-color 0.18s ease, background-color 0.18s ease;
  width: 100%;
}

.user-list__item:hover {
  background: var(--admin-brand-soft);
  border-color: var(--admin-brand);
}

.user-list__avatar {
  align-items: center;
  background: var(--admin-brand-soft);
  border-radius: 6px;
  color: var(--admin-brand);
  display: flex;
  height: 44px;
  justify-content: center;
  width: 44px;
}

.user-list__main {
  min-width: 0;
}

.user-list__topline,
.user-list__meta,
.user-list__tags {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.user-list__topline strong {
  color: var(--user-ink);
  font-size: 15px;
}

.user-list__meta {
  color: var(--user-muted);
  font-size: 12px;
  margin: 4px 0 8px;
}

.user-list__tags span {
  color: var(--user-muted);
  font-size: 12px;
}

.user-list__time {
  color: var(--user-muted);
  display: grid;
  font-size: 12px;
  gap: 4px;
  justify-items: end;
}

.user-list__time strong {
  color: var(--user-ink);
  font-weight: 600;
}

.user-center__note-form,
.user-center__pii-form,
.user-center__governance-form,
.user-center__notes,
.user-center__pii-result,
.user-center__governance-preview {
  margin-top: 16px;
}

.user-center__notes {
  display: grid;
  gap: 14px;
}

.user-center__activity {
  display: grid;
  gap: 12px;
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

@media (max-width: 900px) {
  .user-center__list-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .user-center__grid {
    grid-template-columns: 1fr;
  }

  /* Mobile operators need the live result set before secondary search and tag-maintenance controls. */
  .user-center__list {
    order: -1;
  }

  .user-list__row {
    grid-template-columns: 1fr;
  }

  .user-list__check {
    justify-content: flex-start;
    padding: 0;
  }

  .user-list__item {
    grid-template-columns: 44px minmax(0, 1fr);
  }

  .user-list__time {
    grid-column: 1 / -1;
    justify-items: start;
  }

  .user-center__activity {
    grid-template-columns: 1fr;
  }
}
</style>
