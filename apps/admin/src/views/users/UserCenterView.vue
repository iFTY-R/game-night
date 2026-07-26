<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NDescriptions,
  NDescriptionsItem,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
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
import { Eye, RefreshCw, Search, Tag, UserRound } from "lucide-vue-next";
import { AdminPermission } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminUserPIIField,
  AdminUserStatus,
  type AdminUserDetail,
  type AdminUserNote,
  type AdminUserPIIValue,
  type AdminUserSummary,
  type AdminUserTag
} from "../../../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import {
  appendUserNote,
  createUserTag,
  getUser,
  getUserPII,
  listUserNotes,
  listUsers,
  listUserTags,
  setUserTags
} from "../../api/admin-user";
import { createRequestId } from "../../api/connect";
import { AdminApiError } from "../../api/errors";
import PermissionGate from "../../components/PermissionGate.vue";
import { useAuthStore } from "../../stores/auth";
import { formatDateTime } from "../../utils/format";

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
  color: "#2563eb",
  reason: ""
});

const rules: FormRules = {
  body: { required: true, message: "请输入备注内容", trigger: ["input", "blur"] },
  reason: { required: true, message: "请输入操作原因", trigger: ["input", "blur"] },
  name: { required: true, message: "请输入标签名称", trigger: ["input", "blur"] },
  color: { required: true, message: "请输入标签颜色", trigger: ["input", "blur"] }
};

const users = ref<AdminUserSummary[]>([]);
const tags = ref<AdminUserTag[]>([]);
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
const nextPageToken = ref("");

// Request generation protects the detail drawer from stale writes when operators switch users quickly.
let requestGeneration = 0;
let activeController: AbortController | null = null;

const canReadPii = computed(() => auth.permissions.includes(AdminPermission.USERS_READ_PII));
const canAnnotate = computed(() => auth.permissions.includes(AdminPermission.USERS_ANNOTATE));
const selectedSummary = computed(() => detail.value?.summary ?? users.value.find((user) => user.userId === selectedUserId.value) ?? null);
const selectedTagIds = computed({
  get: () => selectedSummary.value?.tags.map((tag) => tag.tagId) ?? [],
  set: () => undefined
});
const tagOptions = computed<SelectOption[]>(() => tags.value.map((tag) => ({
  label: tag.name,
  value: tag.tagId
})));
const statusOptions: SelectOption[] = [
  { label: "全部状态", value: 0 },
  { label: "注册中", value: AdminUserStatus.ONBOARDING },
  { label: "正常", value: AdminUserStatus.ACTIVE },
  { label: "已停权", value: AdminUserStatus.SUSPENDED },
  { label: "已删除", value: AdminUserStatus.DELETED }
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

const errorMessage = (error: unknown, fallback: string): string =>
  error instanceof AdminApiError ? error.message : fallback;

const beginRequest = (): { token: number; signal: AbortSignal } => {
  requestGeneration += 1;
  activeController?.abort();
  activeController = new AbortController();
  return { token: requestGeneration, signal: activeController.signal };
};

const isCurrentRequest = (token: number, signal: AbortSignal): boolean =>
  !signal.aborted && token === requestGeneration;

const clearSensitiveDetail = (): void => {
  piiValues.value = [];
  piiAuditEventId.value = "";
  piiForm.reason = "";
  piiFormRef.value?.restoreValidation();
};

const loadTags = async (): Promise<void> => {
  try {
    const response = await listUserTags({ pageSize: 100 });
    tags.value = response.tags;
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
    users.value = pageToken ? [...users.value, ...response.users] : response.users;
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

const handleAssignTags = async (tagIds: string[]): Promise<void> => {
  const summary = selectedSummary.value;
  if (!summary) {
    return;
  }
  savingTags.value = true;
  try {
    const response = await setUserTags({
      operationId: createRequestId(),
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
      operationId: createRequestId(),
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

const handleCreateTag = async (): Promise<void> => {
  await tagFormRef.value?.validate();
  creatingTag.value = true;
  try {
    const response = await createUserTag({
      operationId: createRequestId(),
      name: tagForm.name,
      color: tagForm.color,
      reason: tagForm.reason,
      expectedVersion: 0n
    });
    if (response.tag) {
      tags.value = [response.tag, ...tags.value];
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
    <header class="admin-view__header user-center__hero">
      <div>
        <p class="user-center__eyebrow">运营后台 / User Center</p>
        <h1 class="admin-view__title">用户中心</h1>
        <p class="admin-view__subtitle">查询用户、维护标签、追加审计备注，并按原因受控查看 PII。</p>
      </div>
      <NButton secondary :loading="loadingUsers" @click="handleSearch">
        <template #icon>
          <RefreshCw :size="16" />
        </template>
        刷新
      </NButton>
    </header>

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
                <NInput v-model:value="tagForm.color" placeholder="#2563eb" />
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
          </PermissionGate>
        </NSpace>
      </NCard>

      <NCard :bordered="false" class="user-center__list">
        <template #header>
          <span class="user-center__panel-title">用户列表</span>
        </template>
        <NSpin :show="loadingUsers">
          <div v-if="users.length" class="user-list">
            <button v-for="user in users" :key="user.userId" class="user-list__item" type="button" @click="openUser(user)">
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
            <NButton v-if="nextPageToken" secondary block :loading="loadingUsers" @click="loadUsers(nextPageToken)">
              加载更多
            </NButton>
          </div>
          <NEmpty v-else description="暂无匹配用户" />
        </NSpin>
      </NCard>
    </div>

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
  </div>
</template>

<style scoped>
.user-center {
  --user-ink: #172033;
  --user-muted: #667085;
  --user-card: rgba(255, 255, 255, 0.86);
  --user-line: rgba(44, 62, 80, 0.12);
}

.user-center__hero {
  align-items: flex-end;
  background:
    radial-gradient(circle at top left, rgba(14, 165, 233, 0.18), transparent 32rem),
    linear-gradient(135deg, rgba(15, 23, 42, 0.05), rgba(14, 165, 233, 0.08));
  border: 1px solid var(--user-line);
  border-radius: 28px;
  display: flex;
  justify-content: space-between;
  padding: 24px;
}

.user-center__eyebrow {
  color: #0369a1;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.14em;
  margin: 0 0 8px;
  text-transform: uppercase;
}

.user-center__grid {
  display: grid;
  gap: 18px;
  grid-template-columns: minmax(280px, 340px) minmax(0, 1fr);
  margin-top: 18px;
}

.user-center__panel,
.user-center__list {
  background: var(--user-card);
  border-radius: 22px;
  box-shadow: 0 24px 70px rgba(15, 23, 42, 0.08);
}

.user-center__panel-title {
  color: var(--user-ink);
  font-weight: 800;
}

.user-center__tag-form {
  border-top: 1px solid var(--user-line);
  padding-top: 16px;
}

.user-center__tag-form h3 {
  font-size: 14px;
  margin: 0 0 12px;
}

.user-list {
  display: grid;
  gap: 12px;
}

.user-list__item {
  align-items: center;
  background: linear-gradient(135deg, rgba(255, 255, 255, 0.92), rgba(248, 250, 252, 0.88));
  border: 1px solid var(--user-line);
  border-radius: 18px;
  color: inherit;
  cursor: pointer;
  display: grid;
  gap: 14px;
  grid-template-columns: 44px minmax(0, 1fr) minmax(140px, auto);
  padding: 14px;
  text-align: left;
  transition: border-color 0.18s ease, transform 0.18s ease, box-shadow 0.18s ease;
  width: 100%;
}

.user-list__item:hover {
  border-color: rgba(14, 165, 233, 0.46);
  box-shadow: 0 14px 34px rgba(14, 165, 233, 0.12);
  transform: translateY(-1px);
}

.user-list__avatar {
  align-items: center;
  background: #e0f2fe;
  border-radius: 16px;
  color: #0369a1;
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
.user-center__notes,
.user-center__pii-result {
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
  .user-center__hero {
    align-items: flex-start;
    flex-direction: column;
  }

  .user-center__grid {
    grid-template-columns: 1fr;
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
