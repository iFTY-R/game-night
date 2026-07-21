<script setup lang="ts">
import {
  ArrowLeft,
  Check,
  CircleAlert,
  Dices,
  Minus,
  Palette,
  Plus,
  RefreshCw,
  RotateCcw,
  RotateCw,
  SkipForward,
  Volume2,
  VolumeX,
} from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";

import type { ActionInput } from "@game-night/game-client";
import { ActionTray, ConnectionBadge, DangerConfirm, GameTable } from "@game-night/game-ui-kit";
import type { TableSeat, TrayState } from "@game-night/game-ui-kit";

import {
  DICE_789_ADD_ACTION,
  DICE_789_CONFIRM_ACTION,
  DICE_789_DROPPED_ACTION,
  DICE_789_PASS_ACTION,
  DICE_789_REROLL_ACTION,
  DICE_789_ROLL_ACTION,
  DICE_789_TARGET_ACTION,
} from "./constants";
import { formatTicks, legalAddValues } from "./controls";
import DiceFace from "./DiceFace.vue";
import { ContinueMode, Effect, Phase } from "./generated/game/dice789/v1/dice_789_pb";
import PoolStack from "./PoolStack.vue";
import {
  createAddAction,
  createConfirmAction,
  createDroppedAction,
  createPassAction,
  createRerollAction,
  createRollAction,
  createTargetAction,
} from "./protocol";
import type { Dice789TableContext, Dice789View } from "./types";

const props = withDefaults(defineProps<{
  view: Dice789View;
  context: Dice789TableContext;
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
const addIndex = ref(0);
const targetUserId = ref("");
const effectConfirm = ref(false);
const droppedConfirm = ref(false);
const finishConfirm = ref(false);
const dropReason = ref("left_cup");
const now = ref(Date.now());
let clockTimer: number | undefined;

onMounted(() => {
  clockTimer = window.setInterval(() => { now.value = Date.now(); }, 250);
});
onBeforeUnmount(() => {
  if (clockTimer !== undefined) window.clearInterval(clockTimer);
});

const presentations = computed(() => new Map(props.context.players.map((player) => [player.userId, player])));
const displayName = (userId: string): string => presentations.value.get(userId)?.displayName ?? `玩家 ${userId.slice(-4)}`;
const isOnline = computed(() => props.context.connection === "online");
const actionLocked = computed(() => props.pendingAction !== null || !isOnline.value);
const can = (action: string): boolean => props.allowedActions.includes(action);
const isCurrentPlayer = computed(() => props.context.viewerRole === "player" && props.view.currentUserId === props.context.selfUserId);
const countdown = computed(() => {
  const deadline = Number(props.view.actionDeadlineUnixMillis);
  return deadline <= 0 ? null : Math.max(0, Math.ceil((deadline - now.value) / 1000));
});
const addValues = computed(() => legalAddValues(props.view.actionConstraints));
const addTicks = computed(() => addValues.value[addIndex.value] ?? addValues.value[0] ?? 0);
const targets = computed(() => props.view.actionConstraints?.targetUserIds ?? []);

watch(
  () => [props.view.turn, props.view.phase, props.view.actionConstraints?.maximumAddTicks, props.view.actionConstraints?.targetUserIds.join("\0")],
  () => {
    addIndex.value = 0;
    targetUserId.value = targets.value[0] ?? "";
  },
  { immediate: true },
);

const seats = computed<readonly TableSeat[]>(() => props.view.players.map((player) => {
  const presentation = presentations.value.get(player.userId);
  return {
    seatIndex: player.seatIndex,
    userId: player.userId,
    displayName: presentation?.displayName ?? displayName(player.userId),
    connected: presentation?.connected ?? true,
    active: player.userId === props.view.currentUserId,
    status: player.active ? (player.userId === props.view.currentUserId ? "正在操作" : `累计 ${formatTicks(player.penaltyTicks)}`) : "已离桌",
    ...(presentation?.avatarText === undefined ? {} : { avatarText: presentation.avatarText }),
    ...(presentation?.host === undefined ? {} : { host: presentation.host }),
  };
}));
const selfSeat = computed(() => props.view.players.find((player) => player.userId === props.context.selfUserId)?.seatIndex ?? 0);

const effectLabels: Readonly<Record<number, string>> = {
  [Effect.PASS]: "直接过牌",
  [Effect.SUM_SEVEN_ADD]: "和为 7 · 加注",
  [Effect.SUM_EIGHT_HALF_POOL]: "和为 8 · 半池",
  [Effect.SUM_NINE_DRAIN_POOL]: "和为 9 · 全池",
  [Effect.ORDINARY_PAIR_REVERSE]: "普通对子 · 反转",
  [Effect.ORDINARY_PAIR_REROLL]: "两人对子 · 重摇",
  [Effect.DOUBLE_ONE_TARGET_DRAIN]: "双 1 · 指定全池",
  [Effect.DOUBLE_FOUR_HALF_POOL_REROLL]: "双 4 · 半池重摇",
  [Effect.DOUBLE_SIX_TARGET_ADD]: "双 6 · 指定加注",
  [Effect.DROPPED_DRAIN_POOL]: "掉骰 · 全池",
};
const effectLabel = computed(() => effectLabels[props.view.effect] ?? "等待摇骰");
const phaseLabel = computed(() => {
  if (props.view.phase === Phase.FINISHED) return "本局已结束";
  if (props.view.phase === Phase.RESULT_PENDING) return "等待房主确认";
  if (props.view.phase === Phase.AWAITING_ADD) return "选择加注量";
  if (props.view.phase === Phase.AWAITING_TARGET) return "选择目标玩家";
  if (props.view.phase === Phase.AWAITING_CONTINUE) return "选择重摇或过牌";
  return isCurrentPlayer.value ? "轮到你摇骰" : `等待 ${displayName(props.view.currentUserId)}`;
});
const pendingLabel = computed(() => props.pendingAction === null ? null : ({
  [DICE_789_ROLL_ACTION]: "正在摇骰",
  [DICE_789_CONFIRM_ACTION]: "正在应用效果",
  [DICE_789_ADD_ACTION]: "正在加注",
  [DICE_789_TARGET_ACTION]: "正在选择目标",
  [DICE_789_REROLL_ACTION]: "正在重摇",
  [DICE_789_PASS_ACTION]: "正在过牌",
  [DICE_789_DROPPED_ACTION]: "正在确认掉骰",
} as Readonly<Record<string, string>>)[props.pendingAction] ?? "正在提交");
const irreversibleEffect = computed(() => [
  Effect.SUM_EIGHT_HALF_POOL,
  Effect.SUM_NINE_DRAIN_POOL,
  Effect.DOUBLE_FOUR_HALF_POOL_REROLL,
].includes(props.view.effect));
const effectConfirmText = computed(() => {
  if (props.view.effect === Effect.SUM_NINE_DRAIN_POOL) return `将由 ${displayName(props.view.sourceUserId)} 承担公共池全部 ${formatTicks(props.view.totalPoolTicks)}。`;
  const ticks = Math.ceil(props.view.totalPoolTicks / 2);
  return `将由 ${displayName(props.view.sourceUserId)} 承担 ${formatTicks(ticks)}，公共池保留 ${formatTicks(props.view.totalPoolTicks - ticks)}。`;
});

const setAddIndex = (offset: number): void => {
  addIndex.value = Math.min(Math.max(addIndex.value + offset, 0), addValues.value.length - 1);
};
const submit = (input: ActionInput): void => {
  if (!actionLocked.value && can(input.action)) emit("submit", input);
};
const requestConfirm = (): void => {
  if (!can(DICE_789_CONFIRM_ACTION) || actionLocked.value) return;
  if (irreversibleEffect.value) effectConfirm.value = true;
  else submit(createConfirmAction());
};
const confirmEffect = (): void => {
  effectConfirm.value = false;
  submit(createConfirmAction());
};
const confirmDropped = (): void => {
  droppedConfirm.value = false;
  submit(createDroppedAction(dropReason.value));
};
</script>

<template>
  <main class="d789-screen" :class="`tray-${trayState}`" data-testid="dice-789-screen">
    <header class="d789-bar">
      <button class="icon-button" type="button" title="返回房间" @click="emit('leave')"><ArrowLeft :size="20" aria-hidden="true" /></button>
      <div class="d789-title"><h1>789</h1><span>第 {{ view.turn }} 回合 · {{ context.roomCode }}</span></div>
      <ConnectionBadge :state="context.connection" />
      <button class="icon-button" type="button" title="切换桌面主题" @click="emit('cycle-theme')"><Palette :size="19" aria-hidden="true" /></button>
      <button class="icon-button" type="button" :title="muted ? '开启声音' : '静音'" @click="emit('toggle-sound')">
        <VolumeX v-if="muted" :size="19" aria-hidden="true" /><Volume2 v-else :size="19" aria-hidden="true" />
      </button>
    </header>

    <section class="d789-stage" aria-label="789 共同桌面">
      <GameTable :seats="seats" :self-seat-index="selfSeat" shape="compact-oval">
        <template #center>
          <div class="table-focus" aria-live="polite">
            <span>{{ phaseLabel }}</span>
            <div v-if="view.dieOne > 0 && view.dieTwo > 0" :key="`${view.turn}-${view.dieOne}-${view.dieTwo}`" class="rolled-dice">
              <DiceFace :face="view.dieOne" /><DiceFace :face="view.dieTwo" />
            </div>
            <Dices v-else class="waiting-dice" :size="54" aria-hidden="true" />
            <div class="result-line">
              <strong>{{ view.sum > 0 ? view.sum : "--" }}</strong>
              <span>{{ effectLabel }}</span>
              <RotateCw v-if="view.direction === 1" :size="18" aria-label="顺时针" />
              <RotateCcw v-else :size="18" aria-label="逆时针" />
            </div>
            <small>{{ displayName(view.currentUserId) }} · {{ countdown === null ? "不限时" : `${countdown} 秒` }}</small>
          </div>
        </template>
        <template #private>
          <PoolStack :layers="view.pool" :total-ticks="view.totalPoolTicks" :layer-capacity-ticks="view.config?.layerCapacityTicks ?? 0" />
        </template>
      </GameTable>
    </section>

    <ActionTray v-model="trayState" :pending="pendingAction !== null" label="本回合操作">
      <template #summary>
        <div class="turn-summary"><span :class="{ active: isCurrentPlayer }" /><strong>{{ pendingLabel ?? phaseLabel }}</strong></div>
        <button v-if="!isOnline" class="retry-button" type="button" title="立即重连" @click="emit('retry')"><RefreshCw :size="17" aria-hidden="true" /></button>
        <b v-else class="turn-clock">{{ countdown === null ? "--" : `${countdown}s` }}</b>
      </template>

      <template #primary>
        <button v-if="view.phase === Phase.AWAITING_ROLL && can(DICE_789_ROLL_ACTION)" data-testid="roll-action" class="primary-action" type="button" :disabled="actionLocked" @click="submit(createRollAction())">
          <Dices :size="22" aria-hidden="true" /><span>摇骰</span>
        </button>

        <div v-else-if="view.phase === Phase.RESULT_PENDING && (can(DICE_789_CONFIRM_ACTION) || can(DICE_789_DROPPED_ACTION))" class="pending-controls">
          <select v-model="dropReason" aria-label="掉骰原因" :disabled="actionLocked || !can(DICE_789_DROPPED_ACTION)">
            <option value="left_cup">骰子掉出骰盅</option><option value="left_table">骰子掉落桌面</option><option value="unreadable">骰点无法确认</option>
          </select>
          <button data-testid="confirm-action" class="confirm-action" type="button" :disabled="actionLocked || !can(DICE_789_CONFIRM_ACTION)" @click="requestConfirm"><Check :size="19" aria-hidden="true" />确认落定</button>
          <button data-testid="dropped-action" class="danger-action" type="button" :disabled="actionLocked || !can(DICE_789_DROPPED_ACTION)" @click="droppedConfirm = true"><CircleAlert :size="19" aria-hidden="true" />掉骰</button>
        </div>

        <div v-else-if="view.phase === Phase.AWAITING_ADD && can(DICE_789_ADD_ACTION)" class="add-controls">
          <div class="add-stepper" aria-label="加注刻度">
            <button type="button" title="减少加注" :disabled="actionLocked || addIndex === 0" @click="setAddIndex(-1)"><Minus :size="18" aria-hidden="true" /></button>
            <output>{{ formatTicks(addTicks) }}</output>
            <button type="button" title="增加加注" :disabled="actionLocked || addIndex >= addValues.length - 1" @click="setAddIndex(1)"><Plus :size="18" aria-hidden="true" /></button>
          </div>
          <button data-testid="add-action" class="primary-action" type="button" :disabled="actionLocked" @click="submit(createAddAction(addTicks))"><Plus :size="20" aria-hidden="true" />加入公共池</button>
          <small v-if="view.actionConstraints?.allowCapacityRemainder">可直接补满最后余量</small>
        </div>

        <div v-else-if="view.phase === Phase.AWAITING_TARGET && can(DICE_789_TARGET_ACTION)" class="target-controls">
          <select v-model="targetUserId" aria-label="目标玩家" :disabled="actionLocked">
            <option v-for="userId in targets" :key="userId" :value="userId">{{ displayName(userId) }}</option>
          </select>
          <button data-testid="target-action" class="primary-action" type="button" :disabled="actionLocked || !targetUserId" @click="submit(createTargetAction(targetUserId))">确认目标</button>
        </div>

        <div v-else-if="view.phase === Phase.AWAITING_CONTINUE" class="continue-controls">
          <button v-if="can(DICE_789_REROLL_ACTION)" data-testid="reroll-action" class="primary-action" type="button" :disabled="actionLocked" @click="submit(createRerollAction())"><Dices :size="20" aria-hidden="true" />重摇</button>
          <button v-if="can(DICE_789_PASS_ACTION)" data-testid="pass-action" class="secondary-action" type="button" :disabled="actionLocked" @click="submit(createPassAction())"><SkipForward :size="20" aria-hidden="true" />过牌</button>
        </div>

        <div v-else class="waiting-action">{{ context.viewerRole === "spectator" ? "观战中" : `等待 ${displayName(view.currentUserId)}` }}</div>
      </template>

      <template #details>
        <div class="round-details">
          <dl class="rule-facts">
            <div><dt>普通对子</dt><dd>{{ view.config?.ordinaryPairsReverse ? "反转" : "重摇" }}</dd></div>
            <div><dt>789 后续</dt><dd>{{ view.config?.continueMode === ContinueMode.FORCED_REROLL ? "强制重摇" : view.config?.continueMode === ContinueMode.FORCED_PASS ? "强制过牌" : "自由选择" }}</dd></div>
            <div><dt>叠杯</dt><dd>{{ view.config?.stackedPool ? `${view.config.maxLayers} 层` : "关闭" }}</dd></div>
          </dl>
          <div v-if="view.lastSettlement" class="last-settlement"><span>上一回合</span><strong>{{ effectLabels[view.lastSettlement.effect] ?? view.lastSettlement.resolutionReason }}</strong><small>{{ formatTicks(view.lastSettlement.poolBeforeTicks) }} → {{ formatTicks(view.lastSettlement.poolAfterTicks) }}</small></div>
          <button v-if="view.viewerIsHost" class="finish-button" type="button" :disabled="actionLocked" @click="finishConfirm = true">结束本局</button>
        </div>
      </template>
    </ActionTray>

    <DangerConfirm :open="effectConfirm" title="确认应用本次效果？" confirm-label="确认应用" @confirm="confirmEffect" @cancel="effectConfirm = false">{{ effectConfirmText }} 提交后不能撤回。</DangerConfirm>
    <DangerConfirm :open="droppedConfirm" title="确认本次掉骰？" confirm-label="确认掉骰" @confirm="confirmDropped" @cancel="droppedConfirm = false">将清空公共池并记录房主审计，提交后不能撤回。</DangerConfirm>
    <DangerConfirm :open="finishConfirm" title="结束整局？" confirm-label="确认结束" @confirm="finishConfirm = false; emit('finish')" @cancel="finishConfirm = false">当前结果会保存，房间回到赛后大厅并保持进房许可关闭。</DangerConfirm>
  </main>
</template>

<style scoped>
.d789-screen {
  --d789-display: "STKaiti", "KaiTi", serif;
  position: relative;
  width: 100%;
  height: 100dvh;
  min-height: 560px;
  overflow: hidden;
  color: var(--platform-ink);
  background: repeating-linear-gradient(120deg, rgb(255 255 255 / 2%) 0 1px, transparent 1px 12px), var(--platform-surface);
}
.d789-bar { position: absolute; inset: max(8px, env(safe-area-inset-top)) max(8px, env(safe-area-inset-right)) auto max(8px, env(safe-area-inset-left)); z-index: 30; min-height: 48px; display: grid; grid-template-columns: 42px minmax(0, 1fr) auto 42px 42px; align-items: center; gap: 5px; padding: 4px 5px; background: color-mix(in srgb, var(--platform-surface) 88%, transparent); border: 1px solid color-mix(in srgb, var(--platform-muted) 18%, transparent); border-radius: 7px; backdrop-filter: blur(12px); }
.icon-button { width: 40px; height: 40px; display: grid; place-items: center; padding: 0; color: var(--platform-muted); background: transparent; border: 0; border-radius: 6px; }
.d789-title { min-width: 0; display: grid; gap: 1px; }
.d789-title h1,
.d789-title span { overflow: hidden; margin: 0; text-overflow: ellipsis; white-space: nowrap; }
.d789-title h1 { font-family: var(--d789-display); font-size: 20px; }
.d789-title span { color: var(--platform-muted); font-size: 10px; }
.d789-stage { position: absolute; inset: 60px 0 min(23dvh, 194px); min-height: 0; transition: bottom var(--game-motion-fast, 180ms) ease; }
.tray-collapsed .d789-stage { bottom: min(11dvh, 96px); }
.tray-expanded .d789-stage { bottom: min(41dvh, 360px); }
.table-focus { width: 100%; display: grid; place-items: center; gap: 5px; }
.table-focus > span { color: var(--platform-muted); font-size: 11px; }
.rolled-dice { display: flex; gap: 9px; animation: dice-enter var(--game-motion-roll, 280ms) cubic-bezier(.2,.8,.2,1); }
.waiting-dice { color: var(--platform-muted); opacity: .48; }
.result-line { display: flex; align-items: center; gap: 7px; }
.result-line strong { font-family: var(--d789-display); font-size: 29px; line-height: 1; }
.result-line span { max-width: 140px; overflow: hidden; color: var(--platform-accent); font-size: 11px; font-weight: 800; text-overflow: ellipsis; white-space: nowrap; }
.result-line svg { color: var(--game-direction, #80c7b5); }
.table-focus small { color: var(--platform-muted); font-size: 10px; }
.turn-summary { min-width: 0; display: flex; align-items: center; gap: 8px; }
.turn-summary > span { width: 8px; height: 8px; flex: 0 0 auto; background: var(--platform-muted); border-radius: 50%; }
.turn-summary > span.active { background: var(--game-success, #87c99a); box-shadow: 0 0 0 5px color-mix(in srgb, var(--game-success) 12%, transparent); }
.turn-summary strong { overflow: hidden; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.turn-clock { color: var(--platform-accent); font-family: var(--d789-display); font-size: 16px; }
.retry-button { width: 38px; height: 34px; display: grid; place-items: center; color: var(--platform-accent); background: transparent; border: 1px solid currentColor; border-radius: 6px; }
.primary-action,
.secondary-action,
.confirm-action,
.danger-action { min-height: 48px; display: flex; align-items: center; justify-content: center; gap: 7px; border-radius: 7px; font-weight: 850; }
.primary-action,
.confirm-action { color: #171814; background: var(--platform-accent); border: 0; }
.secondary-action { color: var(--platform-ink); background: transparent; border: 1px solid color-mix(in srgb, var(--platform-muted) 34%, transparent); }
.danger-action { color: #211310; background: var(--platform-danger); border: 0; }
.primary-action:only-child { width: 100%; }
.pending-controls { display: grid; grid-template-columns: minmax(0, 1fr) 1fr 88px; gap: 7px; }
.pending-controls select,
.target-controls select { min-width: 0; min-height: 44px; padding: 0 10px; color: var(--platform-ink); background: var(--platform-surface-raised); border: 1px solid color-mix(in srgb, var(--platform-muted) 32%, transparent); border-radius: 6px; }
.add-controls { display: grid; grid-template-columns: 148px minmax(0, 1fr); gap: 7px; }
.add-controls small { grid-column: 1 / -1; color: var(--platform-accent); font-size: 10px; }
.add-stepper { height: 48px; display: grid; grid-template-columns: 36px minmax(0, 1fr) 36px; overflow: hidden; border: 1px solid color-mix(in srgb, var(--platform-muted) 30%, transparent); border-radius: 7px; }
.add-stepper button { display: grid; place-items: center; padding: 0; color: var(--platform-muted); background: transparent; border: 0; }
.add-stepper output { display: grid; place-items: center; font-family: var(--d789-display); font-size: 16px; font-weight: 850; }
.target-controls { display: grid; grid-template-columns: minmax(0, 1fr) 146px; gap: 7px; }
.continue-controls { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 7px; }
.continue-controls > :only-child { grid-column: 1 / -1; }
.waiting-action { min-height: 48px; display: grid; place-items: center; color: var(--platform-muted); border: 1px dashed color-mix(in srgb, var(--platform-muted) 24%, transparent); border-radius: 7px; font-size: 12px; }
.round-details { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr) auto; gap: 8px; }
.rule-facts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 5px; margin: 0; }
.rule-facts > div,
.last-settlement { padding: 8px; background: rgb(0 0 0 / 15%); border: 1px solid color-mix(in srgb, var(--platform-muted) 14%, transparent); border-radius: 6px; }
.rule-facts dt,
.last-settlement span,
.last-settlement small { color: var(--platform-muted); font-size: 9px; }
.rule-facts dd { margin: 3px 0 0; font-size: 11px; font-weight: 800; }
.last-settlement { display: grid; gap: 2px; }
.last-settlement strong { font-size: 11px; }
.finish-button { min-width: 92px; color: var(--platform-danger); background: transparent; border: 1px solid currentColor; border-radius: 6px; font-weight: 800; }
@media (max-width: 370px) {
  .d789-bar { grid-template-columns: 40px minmax(0, 1fr) auto 40px; }
  .d789-bar > :last-child { display: none; }
  .pending-controls { grid-template-columns: minmax(0, 1fr) 1fr 76px; gap: 4px; }
  .pending-controls button { font-size: 11px; }
  .add-controls { grid-template-columns: 136px minmax(0, 1fr); }
  .target-controls { grid-template-columns: minmax(0, 1fr) 126px; }
}
@media (orientation: landscape) {
  .d789-screen { min-height: 360px; }
  .d789-bar { inset-inline: 12px; }
  .d789-stage { inset: 56px 0 min(31dvh, 122px); }
  .tray-collapsed .d789-stage { bottom: min(18dvh, 72px); }
  .tray-expanded .d789-stage { bottom: min(45dvh, 176px); }
  .table-focus { grid-template-columns: auto auto; width: 280px; column-gap: 12px; }
  .table-focus > span,
  .table-focus > small { grid-column: 1 / -1; }
  .rolled-dice,
  .waiting-dice { grid-column: 1; grid-row: 2 / span 2; }
  .result-line { grid-column: 2; grid-row: 2; }
  .pending-controls,
  .add-controls,
  .target-controls,
  .continue-controls { max-width: 680px; margin-inline: auto; }
}
@keyframes dice-enter { from { opacity: 0; transform: translateY(-8px) rotate(-3deg); } to { opacity: 1; transform: translateY(0) rotate(0); } }
@media (prefers-reduced-motion: reduce) { .d789-stage { transition: none; } .rolled-dice { animation: none; } }
:global([data-motion="reduced"]) .rolled-dice { animation: none; }
</style>
