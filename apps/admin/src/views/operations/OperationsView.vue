<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref } from "vue";
import { NAlert, NButton, NDataTable, NEmpty, NSpin, NTag, type DataTableColumns } from "naive-ui";
import { RefreshCw, RotateCcw, Settings2, Wrench } from "lucide-vue-next";
import { AdminPermission } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminBacklogKind,
  AdminDependencyKind,
  AdminHealthStatus,
  AdminServiceKind,
  type AdminBacklogSummary,
  type AdminDependencyHealth,
  type AdminServiceInstance
} from "../../../../../contracts/gen/ts/platform/admin/v1/admin_operations_pb";
import { formatDateTime } from "../../utils/format";
import PermissionGate from "../../components/PermissionGate.vue";
import { useOperationsStore } from "./operations-store";
import CacheRefreshDialog from "./components/CacheRefreshDialog.vue";
import MaintenanceDialog from "./components/MaintenanceDialog.vue";
import TaskRetryDialog from "./components/TaskRetryDialog.vue";

const store = useOperationsStore();
const maintenanceDialogRef = ref<InstanceType<typeof MaintenanceDialog> | null>(null);
const cacheRefreshDialogRef = ref<InstanceType<typeof CacheRefreshDialog> | null>(null);
const taskRetryDialogRef = ref<InstanceType<typeof TaskRetryDialog> | null>(null);

const healthLabels: Partial<Record<AdminHealthStatus, string>> = {
  [AdminHealthStatus.HEALTHY]: "正常",
  [AdminHealthStatus.DEGRADED]: "降级",
  [AdminHealthStatus.UNAVAILABLE]: "不可用",
  [AdminHealthStatus.STALE]: "已过期"
};
const healthLabel = (status: AdminHealthStatus): string => healthLabels[status] ?? "未知";

const healthTagType = (status: AdminHealthStatus): "success" | "warning" | "error" | "default" => {
  if (status === AdminHealthStatus.HEALTHY) return "success";
  if (status === AdminHealthStatus.DEGRADED || status === AdminHealthStatus.STALE) return "warning";
  if (status === AdminHealthStatus.UNAVAILABLE) return "error";
  return "default";
};

const serviceLabels: Partial<Record<AdminServiceKind, string>> = {
  [AdminServiceKind.API]: "API",
  [AdminServiceKind.EDGE]: "Edge",
  [AdminServiceKind.REALTIME]: "Realtime",
  [AdminServiceKind.WORKER]: "Worker"
};
const serviceLabel = (kind: AdminServiceKind): string => serviceLabels[kind] ?? "未知";

const dependencyLabels: Partial<Record<AdminDependencyKind, string>> = {
  [AdminDependencyKind.POSTGRESQL]: "PostgreSQL",
  [AdminDependencyKind.REDIS]: "Redis",
  [AdminDependencyKind.EXPORT_RESULT_STORE]: "结果存储",
  [AdminDependencyKind.CHECKPOINT_SINK]: "审计检查点存储",
  [AdminDependencyKind.CHECKPOINT_PROGRESS]: "审计检查点进度",
  [AdminDependencyKind.REALTIME_PRESENCE]: "实时在线投影",
  [AdminDependencyKind.RATE_LIMITER]: "限流器"
};
const dependencyLabel = (kind: AdminDependencyKind): string => dependencyLabels[kind] ?? "未知依赖";

const backlogLabels: Partial<Record<AdminBacklogKind, string>> = {
  [AdminBacklogKind.AUDIT_OUTBOX]: "审计事件",
  [AdminBacklogKind.ROOM_OUTBOX]: "房间事件",
  [AdminBacklogKind.REALTIME_TIMER]: "实时定时器",
  [AdminBacklogKind.USER_BATCH]: "批量用户任务",
  [AdminBacklogKind.USER_ERASURE]: "用户擦除任务"
};
const backlogLabel = (kind: AdminBacklogKind): string => backlogLabels[kind] ?? "未知队列";

const serviceColumns: DataTableColumns<AdminServiceInstance> = [
  { title: "服务", key: "kind", width: 110, render: (row) => serviceLabel(row.kind) },
  { title: "实例", key: "instanceId", minWidth: 180, ellipsis: { tooltip: true } },
  { title: "版本", key: "buildVersion", minWidth: 120, ellipsis: { tooltip: true } },
  { title: "状态", key: "status", width: 100, render: (row) => h(NTag, { type: healthTagType(row.status), size: "small" }, { default: () => healthLabel(row.status) }) },
  { title: "最后心跳", key: "lastHeartbeatAt", minWidth: 170, render: (row) => formatDateTime(row.lastHeartbeatAt) },
  { title: "维护版本", key: "maintenanceVersion", width: 110, render: (row) => row.maintenanceVersion.toString() }
];

const dependencyColumns: DataTableColumns<AdminDependencyHealth> = [
  { title: "依赖", key: "kind", minWidth: 180, render: (row) => dependencyLabel(row.kind) },
  { title: "状态", key: "status", width: 110, render: (row) => h(NTag, { type: healthTagType(row.status), size: "small" }, { default: () => healthLabel(row.status) }) },
  { title: "采样时间", key: "sampledAt", minWidth: 170, render: (row) => formatDateTime(row.sampledAt) },
  { title: "有效至", key: "freshUntil", minWidth: 170, render: (row) => formatDateTime(row.freshUntil) }
];

const backlogColumns: DataTableColumns<AdminBacklogSummary> = [
  { title: "队列", key: "kind", minWidth: 180, render: (row) => backlogLabel(row.kind) },
  { title: "等待", key: "pending", width: 90, render: (row) => row.pending.toString() },
  { title: "执行中", key: "running", width: 90, render: (row) => row.running.toString() },
  { title: "失败", key: "failed", width: 90, render: (row) => row.failed.toString() },
  { title: "最早等待", key: "oldestPendingAt", minWidth: 170, render: (row) => row.oldestPendingAt ? formatDateTime(row.oldestPendingAt) : "无" }
];

const maintenanceStatus = computed(() => store.snapshot?.maintenance?.enabled ? "维护中" : "正常开放");

/** Opens maintenance control from the exact authority version displayed on the page. */
const openMaintenanceDialog = (): void => {
  const current = store.snapshot?.maintenance;
  if (!current) return;
  maintenanceDialogRef.value?.toggleDialog(true, { current, onApplied: () => void store.refresh(), onConflict: () => void store.refresh() });
};

const openCacheRefreshDialog = (): void => {
  cacheRefreshDialogRef.value?.toggleDialog(true, { onApplied: () => void store.refresh(), onConflict: () => void store.refresh() });
};

const openTaskRetryDialog = (): void => {
  taskRetryDialogRef.value?.toggleDialog(true, { onApplied: () => void store.refresh(), onConflict: () => void store.refresh() });
};

onMounted(() => void store.refresh());
onBeforeUnmount(() => store.dispose());
</script>

<template>
  <div class="admin-view operations-view">
    <header class="admin-view__header operations-view__header">
      <div>
        <h1 class="admin-view__title">系统运维</h1>
        <p class="admin-view__subtitle">服务实例、依赖健康和持久任务积压</p>
      </div>
      <NButton quaternary :loading="store.loading" title="刷新" @click="store.refresh">
        <template #icon><RefreshCw :size="17" /></template>
      </NButton>
    </header>

    <NAlert v-if="store.errorMessage" type="error" :show-icon="false">{{ store.errorMessage }}</NAlert>
    <NSpin :show="store.loading && !store.snapshot">
      <div v-if="store.snapshot" class="operations-view__sections">
        <section class="operations-status" aria-label="维护状态">
          <div>
            <span class="operations-status__label">用户写入</span>
            <strong>{{ maintenanceStatus }}</strong>
          </div>
          <NTag :type="store.snapshot.maintenance?.enabled ? 'warning' : 'success'" size="small">
            版本 {{ store.snapshot.maintenance?.version?.toString() ?? "-" }}
          </NTag>
          <span class="operations-status__sample">采样 {{ formatDateTime(store.snapshot.sampledAt) }}</span>
        </section>

        <PermissionGate :permission="AdminPermission.OPERATIONS_MAINTAIN">
          <section class="operations-section operations-command-bar" aria-label="受控运维命令">
            <div>
              <h2>受控命令</h2>
              <p>所有操作先生成短期预览，再经过运维提权和版本校验执行。</p>
            </div>
            <div class="operations-command-bar__actions">
              <NButton secondary @click="openMaintenanceDialog"><template #icon><Wrench :size="16" /></template>维护控制</NButton>
              <NButton secondary @click="openCacheRefreshDialog"><template #icon><RotateCcw :size="16" /></template>刷新投影</NButton>
              <NButton secondary @click="openTaskRetryDialog"><template #icon><Settings2 :size="16" /></template>重试任务</NButton>
            </div>
          </section>
        </PermissionGate>

        <section class="operations-section">
          <h2>服务实例</h2>
          <NDataTable :columns="serviceColumns" :data="store.snapshot.services" :bordered="false" :single-line="false" :scroll-x="940" />
        </section>

        <section class="operations-section">
          <h2>依赖健康</h2>
          <NDataTable :columns="dependencyColumns" :data="store.snapshot.dependencies" :bordered="false" :single-line="false" :scroll-x="680" />
        </section>

        <section class="operations-section">
          <h2>任务积压</h2>
          <NDataTable :columns="backlogColumns" :data="store.snapshot.backlogs" :bordered="false" :single-line="false" :scroll-x="700" />
        </section>
      </div>
      <NEmpty v-else-if="!store.loading" description="暂无运维快照" />
    </NSpin>

    <MaintenanceDialog ref="maintenanceDialogRef" />
    <CacheRefreshDialog ref="cacheRefreshDialogRef" />
    <TaskRetryDialog ref="taskRetryDialogRef" />
  </div>
</template>

<style scoped>
.operations-view { display: grid; gap: 16px; }
.operations-view__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.operations-view__sections { display: grid; gap: 22px; }
.operations-status { display: flex; align-items: center; gap: 14px; min-height: 58px; padding: 10px 14px; border: 1px solid var(--admin-line); border-radius: 6px; background: var(--admin-surface); }
.operations-status > div { display: grid; gap: 2px; }
.operations-status__label, .operations-status__sample { color: var(--admin-muted); font-size: 12px; }
.operations-status__sample { margin-left: auto; }
.operations-section { min-width: 0; padding-top: 18px; border-top: 1px solid var(--admin-line); }
.operations-section h2 { margin: 0 0 12px; font-size: 16px; letter-spacing: 0; }
.operations-command-bar { display: flex; align-items: center; justify-content: space-between; gap: 18px; }
.operations-command-bar h2 { margin-bottom: 3px; }
.operations-command-bar p { margin: 0; color: var(--admin-muted); font-size: 13px; }
.operations-command-bar__actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
@media (max-width: 640px) {
  .operations-status { align-items: flex-start; flex-wrap: wrap; }
  .operations-status__sample { width: 100%; margin-left: 0; }
  .operations-command-bar { align-items: stretch; flex-direction: column; }
  .operations-command-bar__actions { display: grid; grid-template-columns: 1fr; }
}
</style>
