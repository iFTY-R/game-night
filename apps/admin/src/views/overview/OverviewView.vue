<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from "vue";
import { useRouter } from "vue-router";
import { NAlert, NButton, NEmpty, NRadioButton, NRadioGroup, NSpin, NTag } from "naive-ui";
import { ArrowUpRight, RefreshCw } from "lucide-vue-next";
import {
	AdminAttentionKind,
  AdminOverviewGranularity,
  AdminOverviewMetric,
  AdminOverviewUnavailableReason
} from "../../../../../contracts/gen/ts/platform/admin/v1/admin_overview_pb";
import { AdminHealthStatus } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_operations_pb";
import { formatDateTime } from "../../utils/format";
import { routeName } from "../../constants/navigation";
import { useOverviewStore } from "./overview-store";

const store = useOverviewStore();
const router = useRouter();

const metricLabels: Partial<Record<AdminOverviewMetric, string>> = {
  [AdminOverviewMetric.ONLINE_USERS]: "在线用户",
  [AdminOverviewMetric.ACTIVE_ROOMS]: "活跃房间",
  [AdminOverviewMetric.RUNNING_GAMES]: "进行中牌局",
  [AdminOverviewMetric.NEW_USERS]: "新增用户",
  [AdminOverviewMetric.SUSPENDED_USERS]: "封禁用户",
  [AdminOverviewMetric.UNSUSPENDED_USERS]: "解封用户",
  [AdminOverviewMetric.ABNORMAL_TERMINATIONS]: "异常终止",
  [AdminOverviewMetric.EMERGENCY_REPAIRS]: "紧急修正"
};
const metricLabel = (metric: AdminOverviewMetric): string => metricLabels[metric] ?? "未知指标";

const metricValue = (value: { value: bigint; unavailableReason: AdminOverviewUnavailableReason }): string =>
  value.unavailableReason === AdminOverviewUnavailableReason.NONE ? value.value.toString() : "不可用";

const healthLabels: Partial<Record<AdminHealthStatus, string>> = {
  [AdminHealthStatus.HEALTHY]: "正常",
  [AdminHealthStatus.DEGRADED]: "降级",
  [AdminHealthStatus.UNAVAILABLE]: "不可用",
  [AdminHealthStatus.STALE]: "已过期"
};
const healthText = (status: AdminHealthStatus): string => healthLabels[status] ?? "未知";

const attentionReasonLabels: Record<string, string> = {
	"room.session_missing": "房间缺少牌局会话",
	"room.session_not_found": "房间关联会话不存在",
	"room.session_room_mismatch": "会话所属房间不一致",
	"room.game_mismatch": "房间与会话游戏不一致",
	"room.session_inactive": "房间关联会话已结束",
	"game.room_not_found": "牌局所属房间不存在",
	"game.room_not_playing": "牌局运行但房间未在游戏中",
	"game.session_link_mismatch": "房间未关联当前牌局",
	"game.catalog_link_mismatch": "房间游戏标识不一致"
};
const attentionReason = (codes: string[]): string => codes.map((code) => attentionReasonLabels[code] ?? code).join("；");

const riskActionLabels: Record<string, string> = {
	user_suspended: "停权用户",
	user_unsuspended: "解除停权",
	user_deleted: "删除用户",
	real_name_read: "读取实名信息",
	real_name_updated: "修改实名信息",
	admin_password_changed: "修改管理员密码",
	admin_totp_rebound: "重新绑定 TOTP",
	admin_sessions_revoked: "撤销管理员会话",
	admin_offline_reset: "离线重置管理员",
	key_rotation_started: "开始密钥轮换",
	key_rotation_completed: "完成密钥轮换",
	admin_mfa_enabled: "启用管理员 MFA",
	admin_mfa_disabled: "停用管理员 MFA",
	admin_recovery_codes_regenerated: "重置恢复码",
	admin_maintenance_changed: "切换维护状态",
	admin_cache_refreshed: "刷新缓存投影",
	admin_task_retried: "手动重试任务"
};
const riskActionText = (action: string): string => riskActionLabels[action] ?? action;

/** Opens the existing management module responsible for the selected anomaly. */
const openAttention = (kind: AdminAttentionKind): void => {
	void router.push({ name: routeName.rooms, query: { view: kind === AdminAttentionKind.GAME ? "games" : "rooms" } });
};

/** Opens the verified audit center where the complete redacted event can be inspected. */
const openAudit = (): void => {
	void router.push({ name: routeName.audit });
};

const unhealthyDependencies = computed(() => store.response?.dependencies.filter((item) => item.status !== AdminHealthStatus.HEALTHY) ?? []);
const trend = computed(() => store.response?.trends.find((series) => series.metric === AdminOverviewMetric.ACTIVE_ROOMS));
const trendMaximum = computed(() => trend.value?.points.reduce((maximum, point) => point.value > maximum ? point.value : maximum, 1n) ?? 1n);
const trendHeight = (value: bigint): string => `${Math.max(4, Number((value * 100n) / trendMaximum.value))}%`;

onMounted(() => void store.refresh());
onBeforeUnmount(() => store.dispose());
</script>

<template>
  <div class="admin-view overview-view">
    <header class="admin-view__header overview-view__header">
      <div>
        <h1 class="admin-view__title">运营概览</h1>
        <p class="admin-view__subtitle">当前状态、趋势和需要处理的后台任务</p>
      </div>
      <div class="overview-view__actions">
        <NRadioGroup :value="store.granularity" size="small" @update:value="store.setGranularity">
          <NRadioButton :value="AdminOverviewGranularity.HOUR">24 小时</NRadioButton>
          <NRadioButton :value="AdminOverviewGranularity.DAY">30 天</NRadioButton>
        </NRadioGroup>
        <NButton quaternary :loading="store.loading" title="刷新" @click="store.refresh">
          <template #icon><RefreshCw :size="17" /></template>
        </NButton>
      </div>
    </header>

    <NAlert v-if="store.errorMessage" type="error" :show-icon="false">{{ store.errorMessage }}</NAlert>
    <NSpin :show="store.loading && !store.response">
      <div v-if="store.response" class="overview-view__content">
        <section class="metric-strip" aria-label="核心指标">
          <div v-for="metric in store.response.metrics" :key="metric.metric" class="metric-strip__item">
            <span>{{ metricLabel(metric.metric) }}</span>
            <strong :class="{ 'is-unavailable': metric.unavailableReason !== AdminOverviewUnavailableReason.NONE }">{{ metricValue(metric) }}</strong>
            <small>{{ formatDateTime(metric.sampledAt) }}</small>
          </div>
        </section>

        <section class="overview-section overview-trend">
          <div class="overview-section__heading">
            <h2>活跃房间趋势</h2>
            <span>{{ formatDateTime(store.response.windowStart) }} 至 {{ formatDateTime(store.response.windowEnd) }}</span>
          </div>
          <div v-if="trend?.points.length" class="trend-bars" aria-label="活跃房间趋势图">
            <div v-for="point in trend.points" :key="point.bucketStart?.seconds?.toString()" class="trend-bars__column" :title="`${formatDateTime(point.bucketStart)} · ${point.value}`">
              <span :style="{ height: trendHeight(point.value) }" />
            </div>
          </div>
          <NEmpty v-else description="当前窗口暂无趋势数据" />
        </section>

        <div class="overview-view__grid">
          <section class="overview-section">
            <div class="overview-section__heading"><h2>资源异常</h2></div>
            <div v-if="store.response.attention.length" class="attention-list">
              <div v-for="item in store.response.attention" :key="`${item.kind}:${item.resourceId}`">
                <div class="attention-list__body">
                  <span>{{ attentionReason(item.reasonCodes) }}</span>
                  <small>{{ item.resourceId }} · {{ formatDateTime(item.observedAt) }}</small>
                </div>
                <NButton text title="打开房间与牌局" @click="openAttention(item.kind)">
                  <template #icon><ArrowUpRight :size="16" /></template>
                </NButton>
              </div>
            </div>
            <NEmpty v-else description="当前没有房间或牌局异常" />
          </section>

          <section class="overview-section">
            <div class="overview-section__heading"><h2>高风险操作</h2></div>
            <div v-if="store.response.highRiskOperations.length" class="attention-list">
              <div v-for="operation in store.response.highRiskOperations" :key="operation.auditEventId">
                <div class="attention-list__body">
                  <span>{{ riskActionText(operation.action) }}</span>
                  <small>{{ operation.actorAdminId }} · {{ formatDateTime(operation.occurredAt) }}</small>
                </div>
                <NButton text title="打开审计中心" @click="openAudit">
                  <template #icon><ArrowUpRight :size="16" /></template>
                </NButton>
              </div>
            </div>
            <NEmpty v-else description="当前窗口没有已验签高风险操作" />
          </section>

          <section class="overview-section">
            <div class="overview-section__heading"><h2>依赖状态</h2></div>
            <div v-if="unhealthyDependencies.length" class="attention-list">
              <div v-for="dependency in unhealthyDependencies" :key="dependency.kind">
                <span>依赖 {{ dependency.kind }}</span>
                <NTag type="warning" size="small">{{ healthText(dependency.status) }}</NTag>
              </div>
            </div>
            <NEmpty v-else description="依赖状态正常" />
          </section>

          <section class="overview-section">
            <div class="overview-section__heading"><h2>失败任务</h2></div>
            <div v-if="store.response.failedTasks.length" class="attention-list">
              <div v-for="task in store.response.failedTasks" :key="task.taskId">
                <span>{{ task.stableErrorCode || task.taskId }}</span>
                <NTag type="error" size="small">尝试 {{ task.attempts }}</NTag>
              </div>
            </div>
            <NEmpty v-else description="当前没有失败任务" />
          </section>
        </div>
        <p class="overview-view__freshness">采样 {{ formatDateTime(store.response.sampledAt) }} · 有效至 {{ formatDateTime(store.response.freshUntil) }}</p>
      </div>
      <NEmpty v-else-if="!store.loading" description="暂无概览数据" />
    </NSpin>
  </div>
</template>

<style scoped>
.overview-view { display: grid; gap: 16px; }
.overview-view__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.overview-view__actions { display: flex; align-items: center; gap: 8px; }
.overview-view__content { display: grid; gap: 22px; }
.metric-strip { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); border: 1px solid var(--admin-line); border-radius: 6px; background: var(--admin-surface); }
.metric-strip__item { display: grid; min-width: 0; gap: 5px; padding: 14px; border-right: 1px solid var(--admin-line); }
.metric-strip__item:last-child { border-right: 0; }
.metric-strip__item span, .metric-strip__item small { color: var(--admin-muted); font-size: 12px; }
.metric-strip__item strong { font-size: 24px; line-height: 1; letter-spacing: 0; }
.metric-strip__item strong.is-unavailable { color: var(--admin-muted); font-size: 15px; }
.overview-section { min-width: 0; padding-top: 18px; border-top: 1px solid var(--admin-line); }
.overview-section__heading { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.overview-section__heading h2 { margin: 0; font-size: 16px; letter-spacing: 0; }
.overview-section__heading span { color: var(--admin-muted); font-size: 12px; }
.trend-bars { display: flex; align-items: end; gap: 3px; height: 154px; padding: 12px 0 4px; border-bottom: 1px solid var(--admin-line); }
.trend-bars__column { display: flex; flex: 1 1 0; align-items: end; height: 100%; min-width: 3px; }
.trend-bars__column span { width: 100%; min-height: 4px; background: var(--admin-brand); border-radius: 2px 2px 0 0; }
.overview-view__grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 22px; }
.attention-list { display: grid; gap: 1px; border: 1px solid var(--admin-line); border-radius: 6px; overflow: hidden; }
.attention-list > div { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 12px; background: var(--admin-surface); }
.attention-list span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.attention-list__body { display: grid; min-width: 0; gap: 3px; }
.attention-list__body small { min-width: 0; overflow: hidden; color: var(--admin-muted); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.overview-view__freshness { margin: -8px 0 0; color: var(--admin-muted); font-size: 12px; text-align: right; }
@media (max-width: 980px) { .metric-strip { grid-template-columns: repeat(3, minmax(0, 1fr)); } .metric-strip__item:nth-child(3) { border-right: 0; } }
@media (max-width: 700px) {
  .overview-view__header { align-items: stretch; flex-direction: column; }
  .overview-view__actions { justify-content: space-between; }
  .metric-strip { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .metric-strip__item:nth-child(3) { border-right: 1px solid var(--admin-line); }
  .metric-strip__item:nth-child(even) { border-right: 0; }
  .overview-view__grid { grid-template-columns: 1fr; }
}
</style>
