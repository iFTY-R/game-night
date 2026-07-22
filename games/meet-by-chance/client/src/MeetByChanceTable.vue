<script setup lang="ts">
import { ArrowLeft, CircleStop, Dices, Palette, RefreshCw, Target, Volume2, VolumeX } from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

import type { ActionInput } from "@game-night/game-client";
import { ActionTray, ConnectionBadge, DangerConfirm } from "@game-night/game-ui-kit";
import type { TrayState } from "@game-night/game-ui-kit";

import {
  MEET_BY_CHANCE_REROLL_ACTION,
  MEET_BY_CHANCE_STAND_ACTION,
} from "./constants";
import { batchSummary, enabledStraights, formatTicks, matchKindLabel, settlementLabel, special235Label } from "./controls";
import { Phase } from "./generated/game/meet_by_chance/v1/meet_by_chance_pb";
import MeetTable from "./MeetTable.vue";
import { createRerollAction, createStandAction } from "./protocol";
import type { MeetByChanceTableContext, MeetByChanceView } from "./types";

const props = withDefaults(defineProps<{
  view: MeetByChanceView;
  context: MeetByChanceTableContext;
  allowedActions: readonly string[];
  pendingAction?: string | null;
  muted?: boolean;
}>(), { pendingAction: null, muted: false });

const emit = defineEmits<{
  submit: [input: ActionInput];
  leave: [];
  retry: [];
  finish: [];
  "toggle-sound": [];
  "cycle-theme": [];
}>();

const trayState = ref<TrayState>("compact");
const rerollConfirm = ref(false);
const standConfirm = ref(false);
const finishConfirm = ref(false);
const clockNow = ref(Date.now());
let clockTimer: number | undefined;

onMounted(() => {
  clockTimer = window.setInterval(() => { clockNow.value = Date.now(); }, 250);
});
onBeforeUnmount(() => {
  if (clockTimer !== undefined) window.clearInterval(clockTimer);
});

const presentations = computed(() => new Map(props.context.players.map((player) => [player.userId, player])));
const displayName = (userId: string): string => presentations.value.get(userId)?.displayName ?? `玩家 ${userId.slice(-4)}`;
const selfSeatIndex = computed(() => props.view.publicPlayers.find((player) => player.userId === props.context.selfUserId)?.seatIndex ?? 0);
const targetName = computed(() => displayName(props.view.targetUserId));
const isSelfTarget = computed(() => props.context.viewerRole === "player" && props.view.targetUserId === props.context.selfUserId);
const actionLocked = computed(() => props.pendingAction !== null || props.context.connection !== "online");
const countdown = computed(() => {
  const deadline = Number(props.view.actionDeadlineUnixMillis);
  return deadline <= 0 ? null : Math.max(0, Math.ceil((deadline - clockNow.value) / 1000));
});
const can = (action: string): boolean => props.context.viewerRole === "player" && props.allowedActions.includes(action);
const rerollsRemaining = computed(() => Math.max(0, props.view.targetRerollLimit - props.view.targetRerollCount));
const pendingLabel = computed(() => {
  if (props.pendingAction === MEET_BY_CHANCE_REROLL_ACTION) return "正在提交重摇";
  if (props.pendingAction === MEET_BY_CHANCE_STAND_ACTION) return "正在结束本轮";
  return null;
});
const summaryLabel = computed(() => {
  if (props.view.phase === Phase.FINISHED) return "本局已结束";
  if (props.context.viewerRole === "spectator") return `观战 · ${targetName.value} 是靶子`;
  return isSelfTarget.value ? "轮到你决定" : `等待 ${targetName.value} 决定`;
});
const specialNotice = computed(() => {
  const special = props.view.publicPlayers.filter((player) => player.special235);
  if (special.length === 0) return null;
  return `${special.map((player) => displayName(player.userId)).join("、")} · ${special235Label(special[0]?.special235Outcome ?? 0) ?? "235 已判定"}`;
});
const wildNotice = computed(() => {
  const changed = props.view.publicPlayers.filter((player) => player.dice.join(",") !== player.normalizedDice.join(","));
  return changed.length === 0 ? null : `百搭：${changed.map((player) => `${displayName(player.userId)} → ${player.normalizedDice.join("")}`).join(" · ")}`;
});
const matchNotice = computed(() => batchSummary(props.view.lastMatchBatch));

const submit = (input: ActionInput): void => {
  if (!actionLocked.value && can(input.action)) emit("submit", input);
};
const confirmReroll = (): void => {
  rerollConfirm.value = false;
  submit(createRerollAction());
};
const confirmStand = (): void => {
  standConfirm.value = false;
  submit(createStandAction());
};
</script>

<template>
  <main class="meet-screen" :class="`tray-${trayState}`" data-testid="meet-by-chance-screen">
    <header class="meet-bar">
      <button class="icon-button" type="button" title="返回房间" @click="emit('leave')"><ArrowLeft :size="20" aria-hidden="true" /></button>
      <div class="meet-title"><h1>喜相逢</h1><span>第 {{ view.round }} 轮 · {{ context.roomCode }}</span></div>
      <ConnectionBadge :state="context.connection" />
      <button class="icon-button" type="button" title="切换桌面主题" @click="emit('cycle-theme')"><Palette :size="19" aria-hidden="true" /></button>
      <button class="icon-button" type="button" :title="muted ? '开启声音' : '静音'" @click="emit('toggle-sound')">
        <VolumeX v-if="muted" :size="19" aria-hidden="true" /><Volume2 v-else :size="19" aria-hidden="true" />
      </button>
    </header>

    <section class="meet-stage" aria-label="喜相逢游戏区域">
      <MeetTable
        :players="view.publicPlayers"
        :presentations="presentations"
        :self-seat-index="selfSeatIndex"
        :target-user-id="view.targetUserId"
        :match-batch="view.lastMatchBatch"
      >
        <div class="table-focus" aria-live="polite">
          <span>本轮靶子</span>
          <div class="target-focus"><Target :size="20" aria-hidden="true" /><strong>{{ targetName }}</strong></div>
          <small>重摇 {{ view.targetRerollCount }} / {{ view.targetRerollLimit }} · 连续 {{ view.targetStreak }}</small>
          <div v-if="matchNotice" class="resolution-note" :class="{ capped: view.lastMatchBatch?.capped }">
            <b>{{ matchNotice }}</b>
            <template v-if="!view.lastMatchBatch?.capped">
              <span v-for="(group, index) in view.lastMatchBatch?.groups" :key="index">
                {{ matchKindLabel(group.kind) }} · {{ group.userIds.map(displayName).join('、') }}
                <template v-if="group.weakestUserId"> · {{ displayName(group.weakestUserId) }} 额外 {{ formatTicks(group.weakExtraPenaltyTicks) }}</template>
              </span>
            </template>
          </div>
          <p v-if="specialNotice" class="special-note">{{ specialNotice }}</p>
          <p v-else-if="wildNotice" class="wild-note">{{ wildNotice }}</p>
        </div>
      </MeetTable>
    </section>

    <ActionTray v-model="trayState" :pending="pendingAction !== null" label="靶子操作">
      <template #summary>
        <div class="turn-summary"><span :class="{ active: isSelfTarget }" /><strong>{{ pendingLabel ?? summaryLabel }}</strong></div>
        <button v-if="context.connection !== 'online'" class="retry-button" type="button" title="立即重连" @click="emit('retry')"><RefreshCw :size="17" aria-hidden="true" /></button>
        <b v-else class="turn-clock">{{ countdown === null ? "--" : `${countdown}s` }}</b>
      </template>

      <template #primary>
        <div v-if="can(MEET_BY_CHANCE_REROLL_ACTION) || can(MEET_BY_CHANCE_STAND_ACTION)" class="target-actions">
          <button v-if="can(MEET_BY_CHANCE_REROLL_ACTION)" data-testid="reroll-action" class="primary-action" type="button" :disabled="actionLocked" @click="rerollConfirm = true">
            <Dices :size="21" aria-hidden="true" /><span>重摇 · +{{ formatTicks(view.config?.rerollPenaltyTicks ?? 0) }}</span>
          </button>
          <button v-if="can(MEET_BY_CHANCE_STAND_ACTION)" data-testid="stand-action" class="secondary-action" type="button" :disabled="actionLocked" @click="standConfirm = true">
            <CircleStop :size="20" aria-hidden="true" /><span>结束本轮</span>
          </button>
        </div>
        <div v-else class="waiting-action">{{ context.viewerRole === "spectator" ? "观战中" : view.phase === Phase.FINISHED ? "本局已结束" : `等待 ${targetName}` }}</div>
      </template>

      <template #details>
        <div class="round-details">
          <dl class="rule-facts">
            <div><dt>顺子</dt><dd>{{ enabledStraights(view.config) }}</dd></div>
            <div><dt>235</dt><dd>{{ view.config?.special235Enabled ? "克豹子" : "关闭" }}</dd></div>
            <div><dt>1 百搭</dt><dd>{{ view.config?.onesWild ? "开启" : "关闭" }}</dd></div>
            <div><dt>同牌解析</dt><dd>{{ view.matchResolutionCount }} / {{ view.matchResolutionLimit }}</dd></div>
            <div><dt>剩余重摇</dt><dd>{{ rerollsRemaining }} 次</dd></div>
          </dl>
          <div v-if="view.lastSettlement" class="last-settlement">
            <span>上一轮</span><strong>{{ displayName(view.lastSettlement.targetUserId) }} · {{ settlementLabel(view.lastSettlement.outcome, view.lastSettlement.cause) }}</strong>
            <small>重摇 {{ view.lastSettlement.targetRerollCount }} 次 · 同牌 {{ view.lastSettlement.matchResolutionCount }} 批</small>
          </div>
          <button v-if="view.viewerIsHost" class="finish-button" type="button" :disabled="actionLocked" @click="finishConfirm = true">结束本局</button>
        </div>
      </template>
    </ActionTray>

    <DangerConfirm :open="rerollConfirm" title="确认重摇？" confirm-label="确认重摇" @confirm="confirmReroll" @cancel="rerollConfirm = false">
      本次会增加 {{ formatTicks(view.config?.rerollPenaltyTicks ?? 0) }}，全轮还可重摇 {{ rerollsRemaining }} 次。新牌面将立即公开，提交后不能撤回。
    </DangerConfirm>
    <DangerConfirm :open="standConfirm" title="确认结束本轮？" confirm-label="确认结束" @confirm="confirmStand" @cancel="standConfirm = false">
      将以 {{ targetName }} 为本轮靶子立即结算，提交后不能撤回。
    </DangerConfirm>
    <DangerConfirm :open="finishConfirm" title="结束整局？" confirm-label="确认结束" @confirm="finishConfirm = false; emit('finish')" @cancel="finishConfirm = false">
      当前结果会保存，房间回到赛后大厅并保持进房许可关闭。
    </DangerConfirm>
  </main>
</template>

<style scoped>
.meet-screen { --meet-display: "STKaiti", "KaiTi", serif; position: relative; width: 100%; height: 100dvh; min-height: 560px; overflow: hidden; color: var(--platform-ink); background: repeating-linear-gradient(118deg, rgb(255 255 255 / 2%) 0 1px, transparent 1px 11px), var(--platform-surface); }
.meet-screen::before { position: absolute; inset: 0; content: ""; pointer-events: none; background: linear-gradient(180deg, rgb(255 255 255 / 3%), transparent 30%, rgb(0 0 0 / 15%)); }
.meet-bar { position: absolute; inset: max(8px, env(safe-area-inset-top)) max(8px, env(safe-area-inset-right)) auto max(8px, env(safe-area-inset-left)); z-index: 30; min-height: 48px; display: grid; grid-template-columns: 42px minmax(0, 1fr) auto 42px 42px; align-items: center; gap: 5px; padding: 4px 5px; background: color-mix(in srgb, var(--platform-surface) 88%, transparent); border: 1px solid color-mix(in srgb, var(--platform-muted) 18%, transparent); border-radius: 7px; backdrop-filter: blur(12px); }
.icon-button { width: 40px; height: 40px; display: grid; place-items: center; padding: 0; color: var(--platform-muted); background: transparent; border: 0; border-radius: 6px; }
.meet-title { min-width: 0; display: grid; gap: 1px; }
.meet-title h1,
.meet-title span { overflow: hidden; margin: 0; text-overflow: ellipsis; white-space: nowrap; }
.meet-title h1 { font-family: var(--meet-display); font-size: 18px; }
.meet-title span { color: var(--platform-muted); font-size: 10px; }
.meet-stage { position: absolute; inset: 60px 0 min(23dvh, 194px); min-height: 0; transition: bottom var(--game-motion-fast, 170ms) ease; }
.tray-collapsed .meet-stage { bottom: min(11dvh, 96px); }
.tray-expanded .meet-stage { bottom: min(41dvh, 360px); }
.table-focus { width: 100%; display: grid; place-items: center; gap: 3px; animation: focus-enter var(--game-motion-reveal, 300ms) ease-out; }
.table-focus > span { color: var(--platform-muted); font-size: 9px; }
.target-focus { display: flex; align-items: center; gap: 6px; color: var(--game-target, var(--platform-accent)); }
.target-focus strong { font-family: var(--meet-display); font-size: 20px; }
.table-focus > small { color: var(--platform-muted); font-size: 9px; }
.resolution-note { max-width: 174px; display: grid; gap: 2px; padding: 5px 7px; background: color-mix(in srgb, var(--game-match, var(--platform-focus)) 12%, transparent); border: 1px solid color-mix(in srgb, var(--game-match, var(--platform-focus)) 32%, transparent); border-radius: 6px; }
.resolution-note.capped { color: var(--platform-danger); border-color: currentColor; }
.resolution-note b { color: var(--game-match, var(--platform-focus)); font-size: 10px; }
.resolution-note span { overflow: hidden; color: var(--platform-muted); font-size: 9px; text-overflow: ellipsis; white-space: nowrap; }
.special-note,
.wild-note { max-width: 180px; overflow: hidden; margin: 0; color: var(--platform-accent); font-size: 8px; text-overflow: ellipsis; white-space: nowrap; }
.turn-summary { min-width: 0; display: flex; align-items: center; gap: 8px; }
.turn-summary > span { width: 8px; height: 8px; flex: 0 0 auto; background: var(--platform-muted); border-radius: 50%; }
.turn-summary > span.active { background: var(--game-success); box-shadow: 0 0 0 5px color-mix(in srgb, var(--game-success) 12%, transparent); }
.turn-summary strong { overflow: hidden; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.turn-clock { color: var(--platform-accent); font-family: var(--meet-display); font-size: 16px; }
.retry-button { width: 38px; height: 34px; display: grid; place-items: center; color: var(--platform-accent); background: transparent; border: 1px solid currentColor; border-radius: 6px; }
.target-actions { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(0, 1fr); gap: 7px; }
.target-actions > :only-child { grid-column: 1 / -1; }
.primary-action,
.secondary-action { min-height: 48px; display: flex; align-items: center; justify-content: center; gap: 7px; border-radius: 7px; font-weight: 850; }
.primary-action { color: #171814; background: var(--platform-accent); border: 0; }
.secondary-action { color: var(--platform-ink); background: transparent; border: 1px solid color-mix(in srgb, var(--platform-muted) 36%, transparent); }
.waiting-action { min-height: 48px; display: grid; place-items: center; color: var(--platform-muted); border: 1px dashed color-mix(in srgb, var(--platform-muted) 24%, transparent); border-radius: 7px; font-size: 12px; }
.round-details { display: grid; grid-template-columns: minmax(0, 1.5fr) minmax(0, 1fr) auto; gap: 8px; }
.rule-facts { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 5px; margin: 0; }
.rule-facts > div,
.last-settlement { padding: 8px; background: rgb(0 0 0 / 15%); border: 1px solid color-mix(in srgb, var(--platform-muted) 14%, transparent); border-radius: 6px; }
.rule-facts dt,
.last-settlement span,
.last-settlement small { color: var(--platform-muted); font-size: 9px; }
.rule-facts dd { margin: 3px 0 0; font-size: 10px; font-weight: 800; }
.last-settlement { display: grid; gap: 2px; }
.last-settlement strong { font-size: 10px; }
.finish-button { min-width: 92px; color: var(--platform-danger); background: transparent; border: 1px solid currentColor; border-radius: 6px; font-weight: 800; }
@media (max-width: 370px) {
  .meet-bar { grid-template-columns: 40px minmax(0, 1fr) auto 40px; }
  .meet-bar > :last-child { display: none; }
  .target-actions { grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr); gap: 5px; }
  .target-actions button { font-size: 11px; }
  .resolution-note { max-width: 150px; }
}
@media (orientation: landscape) {
  .meet-screen { min-height: 360px; }
  .meet-bar { inset-inline: 12px; }
  /* Rotation changes both geometries at once; disabling those transitions prevents stale seats crossing the tray. */
  .meet-stage { inset: 56px 0 min(36dvh, 142px); transition: none; }
  .meet-screen :deep(.gn-tray) { transition: none; }
  .tray-collapsed .meet-stage { bottom: min(22dvh, 88px); }
  .tray-expanded .meet-stage { bottom: min(50dvh, 196px); }
  .table-focus { width: 250px; grid-template-columns: 1fr 1fr; column-gap: 8px; }
  .table-focus > span,
  .table-focus > small { grid-column: 1; }
  .target-focus { grid-column: 1; }
  .resolution-note,
  .special-note,
  .wild-note { grid-column: 2; grid-row: 1 / span 3; align-self: center; }
  .target-actions { max-width: 680px; margin-inline: auto; }
}
@keyframes focus-enter { from { opacity: 0; transform: scale(.96); } to { opacity: 1; transform: scale(1); } }
@media (prefers-reduced-motion: reduce) { .meet-stage { transition: none; } .table-focus { animation: none; } }
:global([data-motion="reduced"]) .table-focus { animation: none; }
</style>
