<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, reactive, ref, shallowRef } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NDatePicker,
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
  NTag,
  NText,
  useMessage,
  type DataTableColumns
} from "naive-ui";
import { RefreshCw, Search } from "lucide-vue-next";
import { AdminApiError } from "../../api/errors";
import { listAuditEvents, type ListAuditEventsInput } from "../../api/admin-audit";
import { formatDateTime } from "../../utils/format";
import {
  type AdminAuditChainHead,
  type AdminAuditEvent
} from "../../../../../contracts/gen/ts/platform/admin/v1/admin_audit_pb";
import { AuditAction, AuditActorType, AuditTargetType } from "../../../../../contracts/gen/ts/platform/audit/v1/audit_pb";

const message = useMessage();
const pageSize = 20;

const filterForm = reactive({
  actions: [] as AuditAction[],
  actorTypes: [] as AuditActorType[],
  actorId: "",
  targetTypes: [] as AuditTargetType[],
  targetId: "",
  requestId: "",
  reasonCode: "",
  occurredRange: null as [number, number] | null
});

const events = ref<AdminAuditEvent[]>([]);
const chainHead = ref<AdminAuditChainHead | null>(null);
const sampledAt = ref<NonNullable<AdminAuditChainHead["updatedAt"]> | null>(null);
const nextPageToken = ref("");
const scannedEvents = ref(0);
const loadingEvents = ref(false);
const loadingMore = ref(false);
const detailOpen = ref(false);
const selectedEventId = ref("");

// Request generation protects the list from stale responses when operators refresh or narrow filters quickly.
const requestGeneration = ref(0);
const activeController = shallowRef<AbortController | null>(null);

const currentEvent = computed(() => events.value.find((event) => event.eventId === selectedEventId.value) ?? null);
const hasMore = computed(() => nextPageToken.value.length > 0);
const unverifiedCount = computed(() => events.value.filter((event) => !event.verified).length);
const chainHeadStatus = computed(() => {
  if (!chainHead.value) {
    return "尚未读取链头";
  }
  return hasMore.value ? "当前页之后仍有更晚事件" : "当前页已到达链头";
});

const errorMessage = (error: unknown, fallback: string): string =>
  error instanceof AdminApiError ? error.message : fallback;

const tokenLabels: Record<string, string> = {
  ADMIN: "管理员",
  AUDIT: "审计",
  BULK: "批量",
  CHANGE: "变更",
  CHANGED: "变更",
  CODE: "码",
  CODES: "码",
  COMPLETE: "完成",
  COMPLETED: "完成",
  CREATE: "创建",
  CREATED: "创建",
  CURRENT: "当前",
  DENIED: "拒绝",
  DELETE: "删除",
  DELETED: "删除",
  DEVICE: "设备",
  DISABLE: "停用",
  DISABLED: "停用",
  EVENT: "事件",
  EVENTS: "事件",
  EXPIRED: "过期",
  EXPORT: "导出",
  FAILED: "失败",
  FORCE: "强制",
  IDENTITY: "身份",
  KEY: "密钥",
  LOGIN: "登录",
  LOGOUT: "登出",
  MFA: "MFA",
  OFFLINE: "离线",
  ONBOARDED: "初始化",
  OPENED: "打开",
  PASSWORD: "密码",
  PROFILE: "资料",
  READ: "读取",
  REAL: "实名",
  RECOVERED: "恢复",
  RECOVERY: "恢复",
  REBOUND: "重绑",
  REPAIR: "修复",
  RESET: "重置",
  REVOKE: "撤销",
  REVOKED: "撤销",
  RESULT: "结果",
  ROTATED: "轮换",
  SECRET: "秘密",
  SESSION: "会话",
  SESSIONS: "会话",
  SETUP: "设置",
  START: "开始",
  STARTED: "开始",
  SUCCESS: "成功",
  SUCCEEDED: "成功",
  SYSTEM: "系统",
  TARGET: "目标",
  TOTP: "TOTP",
  UPDATE: "更新",
  UPDATED: "更新",
  USED: "使用",
  USER: "用户",
  UNSPECIFIED: "未指定",
  WRITE: "写入",
  ASSISTED: "辅助",
  ELEVATED: "提权",
  ELEVATION: "提权",
  ENABLED: "启用"
};

const specialActionLabels: Partial<Record<AuditAction, string>> = {
  [AuditAction.ADMIN_TOTP_REBOUND]: "管理员 TOTP 重绑",
  [AuditAction.ADMIN_SETUP_COMPLETED]: "管理员初始化完成",
  [AuditAction.ADMIN_RECOVERY_USED]: "管理员恢复码使用",
  [AuditAction.ADMIN_SECRET_RESULT_OPENED]: "管理员秘密结果打开",
  [AuditAction.ADMIN_SECRET_RESULT_CONFIRMED]: "管理员秘密结果确认",
  [AuditAction.ADMIN_ELEVATION_DENIED]: "管理员提权拒绝",
  [AuditAction.ADMIN_ELEVATION_EXPIRED]: "管理员提权过期",
  [AuditAction.ADMIN_SESSION_ELEVATED]: "管理员会话提权",
  [AuditAction.ADMIN_ELEVATION_REVOKED]: "管理员提权撤销",
  [AuditAction.ADMIN_MFA_ENABLED]: "管理员 MFA 启用",
  [AuditAction.ADMIN_MFA_DISABLED]: "管理员 MFA 停用",
  [AuditAction.ADMIN_RECOVERY_CODES_REGENERATED]: "管理员恢复码重新生成",
  [AuditAction.ADMIN_LOGIN_SUCCEEDED]: "管理员登录成功",
  [AuditAction.ADMIN_LOGIN_FAILED]: "管理员登录失败",
  [AuditAction.AUDIT_EVENTS_READ]: "审计事件读取",
  [AuditAction.KEY_ROTATION_STARTED]: "密钥轮换开始",
  [AuditAction.KEY_ROTATION_BATCH_COMPLETED]: "密钥轮换批次完成",
  [AuditAction.KEY_ROTATION_COMPLETED]: "密钥轮换完成"
};

const specialActorLabels: Partial<Record<AuditActorType, string>> = {
  [AuditActorType.USER]: "用户",
  [AuditActorType.ADMIN]: "管理员",
  [AuditActorType.SYSTEM]: "系统"
};

const specialTargetLabels: Partial<Record<AuditTargetType, string>> = {
  [AuditTargetType.USER]: "用户",
  [AuditTargetType.DEVICE]: "设备",
  [AuditTargetType.PROFILE_EXPORT]: "用户导出",
  [AuditTargetType.ADMIN]: "管理员",
  [AuditTargetType.SYSTEM]: "系统"
};

const humanizeEnumName = (name: string, mapping: Record<string, string>): string =>
  name
    .split("_")
    .map((part) => mapping[part] ?? part)
    .join("");

const formatAuditAction = (action: AuditAction): string =>
  specialActionLabels[action] ?? humanizeEnumName(AuditAction[action] ?? String(action), tokenLabels);

const formatAuditActorType = (type: AuditActorType): string =>
  specialActorLabels[type] ?? humanizeEnumName(AuditActorType[type] ?? String(type), tokenLabels);

const formatAuditTargetType = (type: AuditTargetType): string =>
  specialTargetLabels[type] ?? humanizeEnumName(AuditTargetType[type] ?? String(type), tokenLabels);

const formatAuditPrincipal = (type: AuditActorType | AuditTargetType | undefined, id?: string): string => {
  if (typeof type !== "number") {
    return id?.trim() ? id.trim() : "未指定";
  }
  const label =
    (type in specialActorLabels
      ? specialActorLabels[type as AuditActorType]
      : type in specialTargetLabels
        ? specialTargetLabels[type as AuditTargetType]
        : undefined) ?? "未指定";
  return id?.trim() ? `${label} · ${id.trim()}` : label;
};

const actionOptions = Object.values(AuditAction)
  .filter((value): value is AuditAction => typeof value === "number")
  .map((value) => ({ label: formatAuditAction(value), value }));
const actorTypeOptions = Object.values(AuditActorType)
  .filter((value): value is AuditActorType => typeof value === "number")
  .map((value) => ({ label: formatAuditActorType(value), value }));
const targetTypeOptions = Object.values(AuditTargetType)
  .filter((value): value is AuditTargetType => typeof value === "number")
  .map((value) => ({ label: formatAuditTargetType(value), value }));

const beginRequest = (): { token: number; signal: AbortSignal } => {
  requestGeneration.value += 1;
  activeController.value?.abort();
  activeController.value = new AbortController();
  return { token: requestGeneration.value, signal: activeController.value.signal };
};

const isCurrentRequest = (token: number, signal: AbortSignal): boolean =>
  !signal.aborted && token === requestGeneration.value;

const clearResults = (): void => {
  events.value = [];
  chainHead.value = null;
  sampledAt.value = null;
  nextPageToken.value = "";
  scannedEvents.value = 0;
  selectedEventId.value = "";
  detailOpen.value = false;
};

const buildRequest = (): ListAuditEventsInput => {
  const [rawFrom, rawTo] = filterForm.occurredRange ?? [];
  const occurredFrom = typeof rawFrom === "number" ? new Date(rawFrom) : undefined;
  const occurredTo = typeof rawTo === "number" ? new Date(rawTo) : undefined;
  const request: ListAuditEventsInput = {
    actions: filterForm.actions,
    actorTypes: filterForm.actorTypes,
    actorId: filterForm.actorId.trim(),
    targetTypes: filterForm.targetTypes,
    targetId: filterForm.targetId.trim(),
    requestId: filterForm.requestId.trim(),
    reasonCode: filterForm.reasonCode.trim(),
    pageSize
  };
  if (occurredFrom) {
    request.occurredFrom = occurredFrom;
  }
  if (occurredTo) {
    request.occurredTo = occurredTo;
  }
  return request;
};

const loadEvents = async (pageToken = "", append = false): Promise<void> => {
  const { token, signal } = beginRequest();
  if (append) {
    loadingMore.value = true;
  } else {
    loadingEvents.value = true;
  }
  try {
    const response = await listAuditEvents({
      ...buildRequest(),
      pageToken,
      signal
    });
    if (!isCurrentRequest(token, signal)) {
      return;
    }
    events.value = append ? [...events.value, ...response.events] : response.events;
    nextPageToken.value = response.page?.nextPageToken ?? "";
    chainHead.value = response.chainHead ?? null;
    sampledAt.value = response.page?.sampledAt ?? null;
    scannedEvents.value = response.scannedEvents;
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, append ? "加载更多审计事件失败。" : "加载审计事件失败。"));
    }
  } finally {
    if (isCurrentRequest(token, signal)) {
      if (append) {
        loadingMore.value = false;
      } else {
        loadingEvents.value = false;
      }
    }
  }
};

const handleSearch = (): void => {
  clearResults();
  void loadEvents();
};

const handleReset = (): void => {
  filterForm.actions = [];
  filterForm.actorTypes = [];
  filterForm.actorId = "";
  filterForm.targetTypes = [];
  filterForm.targetId = "";
  filterForm.requestId = "";
  filterForm.reasonCode = "";
  filterForm.occurredRange = null;
  clearResults();
  void loadEvents();
};

const handleLoadMore = (): void => {
  if (!hasMore.value) {
    return;
  }
  void loadEvents(nextPageToken.value, true);
};

const openEventDetail = (event: AdminAuditEvent): void => {
  selectedEventId.value = event.eventId;
  detailOpen.value = true;
};

const closeEventDetail = (): void => {
  detailOpen.value = false;
};

const columns: DataTableColumns<AdminAuditEvent> = [
  {
    title: "序号",
    key: "sequence",
    width: 104,
    render: (row) => row.sequence.toString()
  },
  {
    title: "时间",
    key: "occurredAt",
    width: 180,
    ellipsis: { tooltip: true },
    render: (row) => formatDateTime(row.occurredAt)
  },
  {
    title: "动作",
    key: "action",
    width: 180,
    ellipsis: { tooltip: true },
    render: (row) => formatAuditAction(row.action)
  },
  {
    title: "执行者",
    key: "actor",
    width: 220,
    ellipsis: { tooltip: true },
    render: (row) => formatAuditPrincipal(row.actor?.type, row.actor?.actorId)
  },
  {
    title: "目标",
    key: "target",
    width: 220,
    ellipsis: { tooltip: true },
    render: (row) => formatAuditPrincipal(row.target?.type, row.target?.targetId)
  },
  {
    title: "请求 ID",
    key: "requestId",
    width: 220,
    ellipsis: { tooltip: true },
    render: (row) => row.requestId || "-"
  },
  {
    title: "原因码",
    key: "reasonCode",
    width: 160,
    ellipsis: { tooltip: true },
    render: (row) => row.reasonCode || "-"
  },
  {
    title: "完整性",
    key: "verified",
    width: 112,
    render: (row) => h(
      NTag,
      { type: row.verified ? "success" : "error", size: "small", bordered: false },
      { default: () => row.verified ? "已验证" : "异常" }
    )
  },
  {
    title: "详情摘要",
    key: "detailDigest",
    width: 220,
    ellipsis: { tooltip: true },
    render: (row) => row.detailDigest || "-"
  }
];

const rowProps = (row: AdminAuditEvent) => ({
  onClick: () => openEventDetail(row),
  class: [
    "audit-table__row",
    selectedEventId.value === row.eventId ? "audit-table__row--active" : "",
    row.verified ? "" : "audit-table__row--unverified"
  ].filter(Boolean).join(" "),
  style: { cursor: "pointer" }
});

onMounted(() => {
  void loadEvents();
});

onBeforeUnmount(() => {
  activeController.value?.abort();
});
</script>

<template>
  <div class="audit-center">
    <header class="audit-center__toolbar">
      <h1 class="audit-center__title">审计中心</h1>
      <NSpace align="center" size="small">
        <NTag round type="info">只读</NTag>
        <NTag round type="success">审计读取权限</NTag>
      </NSpace>
    </header>

    <div class="audit-center__grid">
    <NCard :bordered="false" class="audit-center__panel audit-center__filter-panel">
      <template #header>
        <div class="audit-center__panel-header">
          <span class="audit-center__panel-title">筛选条件</span>
          <NSpace size="small">
            <NButton secondary @click="handleReset">
              <template #icon>
                <RefreshCw :size="16" />
              </template>
              重置
            </NButton>
            <NButton type="primary" @click="handleSearch">
              <template #icon>
                <Search :size="16" />
              </template>
              查询
            </NButton>
          </NSpace>
        </div>
      </template>

      <NForm label-placement="top" class="audit-center__filter-grid">
        <NFormItem label="动作">
          <NSelect v-model:value="filterForm.actions" multiple clearable filterable :options="actionOptions" placeholder="按动作筛选" />
        </NFormItem>
        <NFormItem label="执行者类型">
          <NSelect v-model:value="filterForm.actorTypes" multiple clearable filterable :options="actorTypeOptions" placeholder="按执行者类型筛选" />
        </NFormItem>
        <NFormItem label="执行者 ID">
          <NInput v-model:value="filterForm.actorId" clearable placeholder="输入执行者 ID" />
        </NFormItem>
        <NFormItem label="目标类型">
          <NSelect v-model:value="filterForm.targetTypes" multiple clearable filterable :options="targetTypeOptions" placeholder="按目标类型筛选" />
        </NFormItem>
        <NFormItem label="目标 ID">
          <NInput v-model:value="filterForm.targetId" clearable placeholder="输入目标 ID" />
        </NFormItem>
        <NFormItem label="请求 ID">
          <NInput v-model:value="filterForm.requestId" clearable placeholder="输入请求 ID" />
        </NFormItem>
        <NFormItem label="原因码">
          <NInput v-model:value="filterForm.reasonCode" clearable placeholder="输入原因码" />
        </NFormItem>
        <NFormItem label="时间范围">
          <NDatePicker
            v-model:value="filterForm.occurredRange"
            type="datetimerange"
            clearable
            format="yyyy-MM-dd HH:mm:ss"
          />
        </NFormItem>
      </NForm>
    </NCard>

      <NCard :bordered="false" class="audit-center__panel audit-center__events-panel">
        <template #header>
          <div class="audit-center__panel-header">
            <span class="audit-center__panel-title">事件列表</span>
            <NSpace size="small">
              <NTag v-if="chainHead" size="small" :type="hasMore ? 'warning' : 'success'">{{ chainHeadStatus }}</NTag>
              <NTag v-if="unverifiedCount" size="small" type="error">完整性异常 {{ unverifiedCount }} 条</NTag>
              <NTag size="small" type="info">当前 {{ events.length }} 条</NTag>
            </NSpace>
          </div>
        </template>

        <NAlert :type="unverifiedCount ? 'error' : 'info'" :bordered="false" class="audit-center__hint">
          {{ unverifiedCount ? `检测到 ${unverifiedCount} 条签名完整性异常事件` : "当前页事件签名均已验证" }}
        </NAlert>

        <NSpin :show="loadingEvents">
          <div v-if="events.length" class="audit-center__table-shell">
            <NDataTable
              :columns="columns"
              :data="events"
              :bordered="false"
              :row-key="(row) => row.eventId"
              :row-props="rowProps"
              :scroll-x="1460"
              :max-height="520"
              size="small"
            />
            <NButton v-if="hasMore" class="audit-center__load-more" secondary block :loading="loadingMore" @click="handleLoadMore">
              加载更多
            </NButton>
          </div>
          <NEmpty v-else description="暂无匹配审计事件" />
        </NSpin>
      </NCard>

      <NCard :bordered="false" class="audit-center__panel audit-center__chain-panel">
        <template #header>
          <div class="audit-center__panel-header">
            <span class="audit-center__panel-title">链头状态</span>
            <NTag v-if="chainHead" :type="hasMore ? 'warning' : 'success'">{{ chainHeadStatus }}</NTag>
            <NTag v-else type="default">等待读取</NTag>
          </div>
        </template>
        <NSpin :show="loadingEvents">
          <NDescriptions v-if="chainHead" bordered :column="2" label-placement="left">
            <NDescriptionsItem label="链头序号">{{ chainHead.sequence.toString() }}</NDescriptionsItem>
            <NDescriptionsItem label="链头哈希">
              <NText code>{{ chainHead.eventHash }}</NText>
            </NDescriptionsItem>
            <NDescriptionsItem label="链头更新时间">{{ formatDateTime(chainHead.updatedAt) }}</NDescriptionsItem>
            <NDescriptionsItem label="页面采样时间">{{ formatDateTime(sampledAt ?? undefined) }}</NDescriptionsItem>
            <NDescriptionsItem label="扫描事件数">{{ scannedEvents }}</NDescriptionsItem>
            <NDescriptionsItem label="当前返回">{{ events.length }} 条</NDescriptionsItem>
          </NDescriptions>
          <NEmpty v-else description="尚未加载审计链头" />
        </NSpin>
      </NCard>
    </div>

    <NDrawer :show="detailOpen" width="760" placement="right" @update:show="(value) => !value && closeEventDetail()">
      <NDrawerContent :title="currentEvent ? `${formatAuditAction(currentEvent.action)} · ${currentEvent.eventId}` : '事件详情'" closable>
        <NSpace v-if="currentEvent" vertical size="large">
          <NAlert :type="currentEvent.verified ? 'success' : 'error'" :bordered="false">
            {{ currentEvent.verified ? "事件签名验证通过" : "事件签名验证失败，禁止将该记录视为可信审计证据" }}
          </NAlert>
          <NDescriptions bordered :column="2" label-placement="left">
            <NDescriptionsItem label="事件 ID">{{ currentEvent.eventId }}</NDescriptionsItem>
            <NDescriptionsItem label="序号">{{ currentEvent.sequence.toString() }}</NDescriptionsItem>
            <NDescriptionsItem label="发生时间">{{ formatDateTime(currentEvent.occurredAt) }}</NDescriptionsItem>
            <NDescriptionsItem label="动作">{{ formatAuditAction(currentEvent.action) }}</NDescriptionsItem>
            <NDescriptionsItem label="执行者">{{ formatAuditPrincipal(currentEvent.actor?.type, currentEvent.actor?.actorId) }}</NDescriptionsItem>
            <NDescriptionsItem label="目标">{{ formatAuditPrincipal(currentEvent.target?.type, currentEvent.target?.targetId) }}</NDescriptionsItem>
            <NDescriptionsItem label="请求 ID">{{ currentEvent.requestId || "-" }}</NDescriptionsItem>
            <NDescriptionsItem label="原因码">{{ currentEvent.reasonCode || "-" }}</NDescriptionsItem>
            <NDescriptionsItem label="前序哈希">
              <NText code>{{ currentEvent.previousHash }}</NText>
            </NDescriptionsItem>
            <NDescriptionsItem label="事件哈希">
              <NText code>{{ currentEvent.eventHash }}</NText>
            </NDescriptionsItem>
            <NDescriptionsItem label="详情摘要">
              <NText code>{{ currentEvent.detailDigest }}</NText>
            </NDescriptionsItem>
            <NDescriptionsItem label="签名版本">{{ currentEvent.signingKeyVersion }}</NDescriptionsItem>
            <NDescriptionsItem label="完整性">{{ currentEvent.verified ? "已验证" : "异常" }}</NDescriptionsItem>
          </NDescriptions>
        </NSpace>
        <NEmpty v-else description="请选择事件" />
      </NDrawerContent>
    </NDrawer>
  </div>
</template>

<style scoped>
.audit-center {
  --audit-card: var(--admin-surface);
  --audit-ink: var(--admin-ink);
  --audit-line: var(--admin-line);
  --audit-muted: var(--admin-muted);
  min-width: 0;
}

.audit-center__toolbar {
  align-items: center;
  display: flex;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.audit-center__title {
  color: var(--audit-ink);
  font-size: 22px;
  line-height: 1.25;
  margin: 0;
}

.audit-center__panel,
.audit-center__list {
  background: var(--audit-card);
  border: 1px solid var(--audit-line);
  border-radius: 8px;
  box-shadow: var(--admin-shadow);
  min-width: 0;
}

.audit-center__panel-header {
  align-items: center;
  display: flex;
  justify-content: space-between;
  gap: 12px;
}

.audit-center__panel-title {
  color: var(--audit-ink);
  font-weight: 800;
}

.audit-center__filter-grid {
  display: grid;
  gap: 8px 12px;
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.audit-center__filter-grid :deep(.n-form-item) {
  margin-bottom: 0;
}

.audit-center__grid {
  display: grid;
  gap: 18px;
  margin-top: 18px;
  min-width: 0;
}

.audit-center__hint,
.audit-center__table-shell,
.audit-center__load-more {
  margin-top: 16px;
}

.audit-center__table-shell {
  max-width: 100%;
  min-width: 0;
  overflow-x: auto;
}

.audit-table__row--active td {
  background: var(--admin-brand-soft) !important;
}

.audit-table__row--unverified td {
  background: color-mix(in srgb, var(--admin-danger) 8%, transparent) !important;
}

@media (max-width: 1100px) {
  .audit-center__filter-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 720px) {
  .audit-center__toolbar {
    align-items: flex-start;
    flex-direction: column;
    gap: 8px;
  }

  .audit-center__filter-grid {
    grid-template-columns: 1fr;
  }

  .audit-center__events-panel {
    order: 1;
  }

  .audit-center__filter-panel {
    order: 2;
  }

  .audit-center__chain-panel {
    order: 3;
  }
}
</style>
