<script setup lang="ts">
import {
  ArrowLeft,
  CircleAlert,
  Dices,
  Minus,
  Palette,
  Plus,
  RefreshCw,
  Volume2,
  VolumeX,
} from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";

import type { ActionInput } from "@game-night/game-client";
import { ActionTray, ConnectionBadge, DangerConfirm, GameTable, PrivateZone } from "@game-night/game-ui-kit";
import type { TableSeat, TrayState } from "@game-night/game-ui-kit";

import { LIARS_DICE_BID_ACTION, LIARS_DICE_OPEN_ACTION, SESSION_FINISH_ACTION } from "./constants";
import { validateBidDraft, suggestBid } from "./bid";
import DiceFace from "./DiceFace.vue";
import { BidMode, Phase } from "./generated/game/liars_dice/v1/liars_dice_pb";
import { createBidAction, createOpenAction } from "./protocol";
import type { BidDraft, LiarsDiceTableContext, LiarsDiceView } from "./types";

const props = withDefaults(
  defineProps<{
    view: LiarsDiceView;
    context: LiarsDiceTableContext;
    allowedActions: readonly string[];
    pendingAction?: string | null;
    muted?: boolean;
  }>(),
  { pendingAction: null, muted: false },
);
const emit = defineEmits<{
  submit: [input: ActionInput];
  leave: [];
  retry: [];
  finish: [];
  "toggle-sound": [];
  "cycle-theme": [];
}>();

const trayState = ref<TrayState>("compact");
const bidDraft = ref<BidDraft>(suggestBid(props.view));
const openConfirm = ref(false);
const finishConfirm = ref(false);
const clockNow = ref(Date.now());
let clockTimer: number | undefined;

// Only authoritative round/bid advancement resets the draft; reconnect and rotation preserve unsubmitted input.
watch(
  () => [
    props.view.round,
    props.view.currentActorUserId,
    props.view.hasCurrentBid,
    props.view.currentBid?.quantity,
    props.view.currentBid?.face,
    props.view.currentBid?.mode,
  ],
  () => {
    bidDraft.value = suggestBid(props.view);
  },
);

onMounted(() => {
  clockTimer = window.setInterval(() => {
    clockNow.value = Date.now();
  }, 250);
});

onBeforeUnmount(() => {
  if (clockTimer !== undefined) window.clearInterval(clockTimer);
});

const presentations = computed(() => new Map(props.context.players.map((player) => [player.userId, player])));
const displayName = (userId: string): string => presentations.value.get(userId)?.displayName ?? `玩家 ${userId.slice(-4)}`;
const activePlayers = computed(() => props.view.players.filter((player) => player.active));
const config = computed(() => props.view.config);
const totalDice = computed(() => activePlayers.value.length * (config.value?.dicePerPlayer ?? 0));
const countdown = computed(() => {
  const deadline = Number(props.view.actionDeadlineUnixMillis);
  if (deadline <= 0) return null;
  return Math.max(0, Math.ceil((deadline - clockNow.value) / 1000));
});
const currentActorName = computed(() => displayName(props.view.currentActorUserId));
const isCurrentPlayer = computed(
  () => props.context.viewerRole === "player" && props.view.currentActorUserId === props.context.selfUserId,
);
const actionLocked = computed(() => props.pendingAction !== null || props.context.connection !== "online");
const canBid = computed(() => isCurrentPlayer.value && props.allowedActions.includes(LIARS_DICE_BID_ACTION));
const canOpen = computed(() => isCurrentPlayer.value && props.allowedActions.includes(LIARS_DICE_OPEN_ACTION));
const canFinish = computed(() => props.allowedActions.includes(SESSION_FINISH_ACTION));
const bidValidation = computed(() => validateBidDraft(props.view, bidDraft.value));
const seats = computed<readonly TableSeat[]>(() =>
  props.view.players.map((player) => {
    const presentation = presentations.value.get(player.userId);
    const status = !player.active
      ? "已离桌"
      : player.userId === props.view.currentActorUserId
        ? "正在叫骰"
        : `${config.value?.dicePerPlayer ?? 0} 颗已摇 · ${player.penaltyTicks} 罚点`;
    return {
      seatIndex: player.seatIndex,
      userId: player.userId,
      displayName: presentation?.displayName ?? displayName(player.userId),
      connected: presentation?.connected ?? true,
      active: player.userId === props.view.currentActorUserId,
      status,
      ...(presentation?.avatarText === undefined ? {} : { avatarText: presentation.avatarText }),
      ...(presentation?.host === undefined ? {} : { host: presentation.host }),
    };
  }),
);
const selfSeatIndex = computed(() => props.view.players.find((player) => player.userId === props.context.selfUserId)?.seatIndex ?? 0);
const phaseLabel = computed(() => {
  if (props.view.phase === Phase.FINISHED) return "本局已结束";
  return props.view.hasCurrentBid ? "当前叫骰" : "等待首叫";
});
const lastResultLabel = computed(() => {
  const settlement = props.view.lastSettlement;
  if (settlement === undefined) return null;
  const loser = displayName(settlement.loserUserId);
  if (settlement.reason === "timeout") return `上轮 ${loser} 超时 · ${settlement.penaltyTicks} 罚点`;
  return `上轮 ${loser} 负 · 实际 ${settlement.actualQuantity} 个`;
});
const pendingLabel = computed(() => {
  if (props.pendingAction === LIARS_DICE_BID_ACTION) return "正在提交叫骰";
  if (props.pendingAction === LIARS_DICE_OPEN_ACTION) return "正在开骰";
  return null;
});

const setQuantity = (offset: number): void => {
  bidDraft.value = { ...bidDraft.value, quantity: Math.max(1, bidDraft.value.quantity + offset) };
};

const setFace = (face: number): void => {
  bidDraft.value = {
    ...bidDraft.value,
    face,
    mode: face === 1 ? BidMode.STRICT : bidDraft.value.mode,
  };
};

const setMode = (mode: BidMode): void => {
  if (mode === BidMode.FLYING && bidDraft.value.face === 1) return;
  bidDraft.value = { ...bidDraft.value, mode };
};

const submitBid = (): void => {
  if (!canBid.value || actionLocked.value || !bidValidation.value.valid) return;
  emit("submit", createBidAction(bidDraft.value));
};

const confirmOpen = (): void => {
  openConfirm.value = false;
  if (canOpen.value && !actionLocked.value) emit("submit", createOpenAction());
};

const confirmFinish = (): void => {
  finishConfirm.value = false;
  if (canFinish.value && !actionLocked.value) emit("finish");
};

</script>

<template>
  <main class="liars-screen" :class="`tray-${trayState}`" data-testid="liars-dice-screen">
    <header class="liars-bar">
      <button class="icon-button" type="button" title="返回房间" @click="emit('leave')">
        <ArrowLeft :size="20" aria-hidden="true" />
      </button>
      <div class="liars-title">
        <h1>吹牛骰子</h1>
        <span>第 {{ view.round }} 轮 · {{ context.roomCode }}</span>
      </div>
      <ConnectionBadge :state="context.connection" />
      <button class="icon-button" type="button" title="切换桌面主题" @click="emit('cycle-theme')">
        <Palette :size="19" aria-hidden="true" />
      </button>
      <button class="icon-button" type="button" :title="muted ? '开启声音' : '静音'" @click="emit('toggle-sound')">
        <VolumeX v-if="muted" :size="19" aria-hidden="true" />
        <Volume2 v-else :size="19" aria-hidden="true" />
      </button>
    </header>

    <section class="liars-stage" aria-label="吹牛骰子共同桌面">
      <GameTable :seats="seats" :self-seat-index="selfSeatIndex" shape="compact-oval">
        <template #center>
          <div class="table-focus" aria-live="polite">
            <span class="table-focus__phase">{{ phaseLabel }}</span>
            <div v-if="view.hasCurrentBid && view.currentBid" :key="`${view.currentBid.quantity}-${view.currentBid.face}-${view.currentBid.mode}`" class="current-bid">
              <strong>{{ view.currentBid.quantity }}</strong>
              <span>个</span>
              <DiceFace :face="view.currentBid.face" variant="focus" />
              <em>{{ view.currentBid.mode === BidMode.STRICT ? "斋" : "飞" }}</em>
            </div>
            <strong v-else class="first-bid">{{ currentActorName }} 首叫</strong>
            <small v-if="countdown !== null">{{ currentActorName }} · {{ countdown }} 秒</small>
            <small v-else>{{ currentActorName }}</small>
            <div v-if="lastResultLabel" class="last-result">{{ lastResultLabel }}</div>
            <div v-if="view.revealedDice.length > 0" class="revealed-grid" role="region" aria-label="上一轮公开骰子">
              <div v-for="roll in view.revealedDice" :key="roll.userId" class="revealed-row">
                <span>{{ displayName(roll.userId) }}</span>
                <div>
                  <DiceFace v-for="(face, dieIndex) in roll.faces" :key="dieIndex" :face="face" variant="tiny" />
                </div>
              </div>
            </div>
          </div>
        </template>
        <template #private>
          <PrivateZone :label="view.ownDice.length > 0 ? '你的私密骰子' : '私密骰子状态'">
            <div v-if="view.ownDice.length > 0" class="own-dice">
              <DiceFace v-for="(face, index) in view.ownDice" :key="index" :face="face" variant="private" />
            </div>
            <div v-else class="private-empty">
              <Dices :size="18" aria-hidden="true" />
              <span>{{ context.viewerRole === "player" ? "等待下一轮摇骰" : "观战视角" }}</span>
            </div>
          </PrivateZone>
        </template>
      </GameTable>
    </section>

    <ActionTray v-model="trayState" :pending="pendingAction !== null" label="本轮操作">
      <template #summary>
        <div class="turn-summary">
          <span class="turn-light" :class="{ active: isCurrentPlayer }" />
          <strong>{{ pendingLabel ?? (isCurrentPlayer ? "轮到你叫骰" : `等待 ${currentActorName}`) }}</strong>
        </div>
        <button
          v-if="context.connection !== 'online'"
          class="retry-button"
          type="button"
          title="立即重连"
          @click="emit('retry')"
        >
          <RefreshCw :size="17" aria-hidden="true" />
        </button>
        <span v-else class="turn-clock">{{ countdown === null ? "--" : `${countdown}s` }}</span>
      </template>

      <template #primary>
        <div class="bid-controls" :aria-disabled="!canBid || actionLocked">
          <div class="quantity-stepper" aria-label="叫骰数量">
            <button type="button" title="减少数量" :disabled="!canBid || actionLocked" @click="setQuantity(-1)">
              <Minus :size="17" aria-hidden="true" />
            </button>
            <output aria-live="polite">{{ bidDraft.quantity }}</output>
            <button type="button" title="增加数量" :disabled="!canBid || actionLocked" @click="setQuantity(1)">
              <Plus :size="17" aria-hidden="true" />
            </button>
          </div>

          <div class="face-picker" role="group" aria-label="选择骰面">
            <button
              v-for="face in 6"
              :key="face"
              type="button"
              :class="{ selected: bidDraft.face === face }"
              :aria-pressed="bidDraft.face === face"
              :title="`选择 ${face} 点`"
              :disabled="!canBid || actionLocked"
              @click="setFace(face)"
            >
              <DiceFace :face="face" variant="picker" decorative />
            </button>
          </div>

          <div class="mode-switch" role="group" aria-label="叫骰模式">
            <button
              type="button"
              :class="{ selected: bidDraft.mode === BidMode.FLYING }"
              :aria-pressed="bidDraft.mode === BidMode.FLYING"
              :disabled="!canBid || actionLocked || bidDraft.face === 1"
              @click="setMode(BidMode.FLYING)"
            >飞</button>
            <button
              type="button"
              :class="{ selected: bidDraft.mode === BidMode.STRICT }"
              :aria-pressed="bidDraft.mode === BidMode.STRICT"
              :disabled="!canBid || actionLocked || !config?.strictEnabled"
              @click="setMode(BidMode.STRICT)"
            >斋</button>
          </div>
        </div>

        <div class="action-row">
          <button
            data-testid="bid-action"
            class="bid-submit"
            type="button"
            :disabled="!canBid || actionLocked || !bidValidation.valid"
            @click="submitBid"
          >
            <Dices :size="20" aria-hidden="true" />
            <span>叫 {{ bidDraft.quantity }} 个 {{ bidDraft.face }} · {{ bidDraft.mode === BidMode.STRICT ? "斋" : "飞" }}</span>
          </button>
          <button
            data-testid="open-action"
            class="open-submit"
            type="button"
            :disabled="!canOpen || actionLocked"
            @click="openConfirm = true"
          >
            <CircleAlert :size="20" aria-hidden="true" />
            <span>开骰</span>
          </button>
        </div>
        <p class="bid-feedback" :class="{ risky: bidValidation.risky, invalid: !bidValidation.valid }" role="status">
          {{ bidValidation.message }}<span v-if="bidValidation.risky"> · 场上共 {{ totalDice }} 颗</span>
        </p>
      </template>

      <template #details>
        <div class="round-details">
          <dl class="rule-facts">
            <div><dt>万能 1</dt><dd>{{ config?.onesWild ? "开启" : "关闭" }}</dd></div>
            <div><dt>斋</dt><dd>{{ config?.strictEnabled ? "开启" : "关闭" }}</dd></div>
            <div><dt>每人骰子</dt><dd>{{ config?.dicePerPlayer ?? 0 }} 颗</dd></div>
            <div><dt>场上骰子</dt><dd>{{ totalDice }} 颗</dd></div>
          </dl>
          <div v-if="view.lastSettlement" class="settlement">
            <span>上一轮</span>
            <strong>{{ displayName(view.lastSettlement.loserUserId) }} · {{ view.lastSettlement.penaltyTicks }} 罚点</strong>
            <small>实际 {{ view.lastSettlement.actualQuantity }} 个 · {{ view.lastSettlement.reason }}</small>
          </div>
          <button v-if="canFinish" class="finish-button" type="button" :disabled="actionLocked" @click="finishConfirm = true">
            结束本局
          </button>
        </div>
      </template>
    </ActionTray>

    <DangerConfirm :open="openConfirm" title="确认开骰？" confirm-label="确认开骰" @confirm="confirmOpen" @cancel="openConfirm = false">
      将立即公开本轮所有骰子并结算，提交后不能撤回。
    </DangerConfirm>
    <DangerConfirm :open="finishConfirm" title="结束整局？" confirm-label="确认结束" @confirm="confirmFinish" @cancel="finishConfirm = false">
      当前结果会被保存，房间回到赛后大厅并保持进房许可关闭。
    </DangerConfirm>
  </main>
</template>

<style scoped>
.liars-screen {
  --liars-display: "STKaiti", "KaiTi", serif;
  position: relative;
  width: 100%;
  height: 100dvh;
  min-height: 560px;
  overflow: hidden;
  color: var(--platform-ink);
  background:
    repeating-linear-gradient(118deg, rgb(255 255 255 / 1.8%) 0 1px, transparent 1px 11px),
    var(--platform-surface);
}

.liars-screen::before {
  position: absolute;
  inset: 0;
  content: "";
  pointer-events: none;
  background: linear-gradient(180deg, rgb(255 255 255 / 3%), transparent 28%, rgb(0 0 0 / 16%));
}

.liars-bar {
  position: absolute;
  inset: max(8px, env(safe-area-inset-top)) max(8px, env(safe-area-inset-right)) auto max(8px, env(safe-area-inset-left));
  z-index: 30;
  min-height: 48px;
  display: grid;
  grid-template-columns: 42px minmax(0, 1fr) auto 42px 42px;
  align-items: center;
  gap: 5px;
  padding: 4px 5px;
  background: color-mix(in srgb, var(--platform-surface) 88%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-muted) 18%, transparent);
  border-radius: 7px;
  backdrop-filter: blur(12px);
}

.icon-button {
  width: 40px;
  height: 40px;
  display: grid;
  place-items: center;
  padding: 0;
  color: var(--platform-muted);
  background: transparent;
  border: 0;
  border-radius: 6px;
}

.icon-button:hover { color: var(--platform-ink); background: rgb(255 255 255 / 5%); }
.liars-title { min-width: 0; display: grid; gap: 1px; }
.liars-title h1,
.liars-title span { overflow: hidden; margin: 0; text-overflow: ellipsis; white-space: nowrap; }
.liars-title h1 { font-family: var(--liars-display); font-size: 17px; letter-spacing: 0; }
.liars-title span { color: var(--platform-muted); font-size: 10px; }

.liars-stage {
  position: absolute;
  inset: 60px 0 min(23dvh, 194px);
  min-height: 0;
  transition: bottom 220ms cubic-bezier(.2, .8, .2, 1);
}

.tray-collapsed .liars-stage { bottom: min(11dvh, 96px); }
.tray-expanded .liars-stage { bottom: min(41dvh, 360px); }

.table-focus { width: 100%; display: grid; place-items: center; gap: 5px; }
.table-focus__phase { color: var(--platform-muted); font-size: 11px; }
.current-bid { display: flex; align-items: center; justify-content: center; gap: 5px; animation: bid-enter var(--game-motion-fast, 160ms) ease-out; }
.current-bid strong { font-family: var(--liars-display); font-size: 44px; line-height: .9; }
.current-bid > span:not(.liars-die) { color: var(--platform-muted); font-size: 12px; }
.current-bid em { min-width: 25px; color: var(--platform-accent); font-family: var(--liars-display); font-size: 16px; font-style: normal; }
.first-bid { font-family: var(--liars-display); font-size: 21px; }
.table-focus small { color: var(--platform-muted); font-size: 10px; }

.own-dice { display: flex; align-items: center; gap: 5px; }
.private-empty { display: flex; align-items: center; gap: 7px; color: var(--platform-muted); font-size: 11px; }

.last-result { max-width: 210px; overflow: hidden; color: var(--platform-accent); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }

.revealed-grid {
  max-height: 104px;
  display: grid;
  gap: 3px;
  overflow: auto;
  padding: 5px 7px;
  background: rgb(0 0 0 / 18%);
  border-radius: 6px;
  animation: reveal-enter var(--game-motion-reveal, 260ms) ease-out;
}
.revealed-row { display: grid; grid-template-columns: 52px auto; align-items: center; gap: 5px; }
.revealed-row > span { overflow: hidden; color: var(--platform-muted); font-size: 9px; text-overflow: ellipsis; white-space: nowrap; }

/* Keep revealed dice inside the center lane reserved between the two side seats. */
@media (max-width: 480px) {
  .revealed-grid { width: 136px; padding: 5px; }
  .revealed-row { grid-template-columns: 32px minmax(0, 1fr); gap: 4px; }
  :deep(.liars-die--tiny) { --die-size: 17px; margin-left: 0; }
}

.turn-summary { min-width: 0; display: flex; align-items: center; gap: 8px; }
.turn-summary strong { overflow: hidden; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.turn-light { width: 8px; height: 8px; flex: 0 0 auto; background: var(--platform-muted); border-radius: 50%; }
.turn-light.active { background: var(--game-success, #91cda6); box-shadow: 0 0 0 5px color-mix(in srgb, var(--game-success, #91cda6) 12%, transparent); }
.turn-clock { color: var(--platform-accent); font-family: var(--liars-display); font-size: 16px; font-weight: 700; }
.retry-button { width: 38px; height: 34px; display: grid; place-items: center; color: var(--platform-accent); background: transparent; border: 1px solid currentColor; border-radius: 6px; }

.bid-controls { display: grid; grid-template-columns: 84px minmax(0, 1fr) 66px; align-items: center; gap: 7px; }
.quantity-stepper,
.mode-switch { height: 36px; display: grid; align-items: stretch; overflow: hidden; border: 1px solid color-mix(in srgb, var(--platform-muted) 28%, transparent); border-radius: 6px; }
.quantity-stepper { grid-template-columns: 26px minmax(28px, 1fr) 26px; }
.quantity-stepper button,
.mode-switch button,
.face-picker button { min-width: 0; padding: 0; color: var(--platform-muted); background: transparent; border: 0; }
.quantity-stepper output { display: grid; place-items: center; color: var(--platform-ink); font-family: var(--liars-display); font-size: 18px; font-weight: 800; }
.mode-switch { grid-template-columns: 1fr 1fr; }
.mode-switch button { font-family: var(--liars-display); font-size: 14px; font-weight: 800; }
.mode-switch button.selected { color: #161815; background: var(--platform-accent); }
.face-picker { min-width: 0; display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 2px; }
.face-picker button { min-height: 36px; display: grid; place-items: center; border-radius: 5px; }
.face-picker button.selected { background: color-mix(in srgb, var(--platform-accent) 18%, transparent); box-shadow: inset 0 0 0 1px var(--platform-accent); }

.action-row { display: grid; grid-template-columns: minmax(0, 1fr) 94px; gap: 7px; margin-top: 7px; }
.action-row button { min-height: 48px; display: flex; align-items: center; justify-content: center; gap: 7px; border-radius: 7px; font-weight: 800; }
.bid-submit { color: #171814; background: var(--platform-accent); border: 1px solid transparent; }
.open-submit { color: #211310; background: var(--platform-danger); border: 1px solid transparent; }
.bid-feedback { min-height: 15px; margin: 3px 2px 0; color: var(--game-success, #91cda6); font-size: 10px; }
.bid-feedback.risky { color: var(--platform-accent); }
.bid-feedback.invalid { color: var(--platform-danger); }

.round-details { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr) auto; align-items: stretch; gap: 8px; }
.rule-facts { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 5px; margin: 0; }
.rule-facts > div,
.settlement { padding: 8px; background: rgb(0 0 0 / 15%); border: 1px solid color-mix(in srgb, var(--platform-muted) 14%, transparent); border-radius: 6px; }
.rule-facts dt,
.settlement span,
.settlement small { color: var(--platform-muted); font-size: 9px; }
.rule-facts dd { margin: 3px 0 0; font-size: 12px; font-weight: 800; }
.settlement { display: grid; gap: 2px; }
.settlement strong { font-size: 11px; }
.finish-button { min-width: 92px; color: var(--platform-danger); background: transparent; border: 1px solid currentColor; border-radius: 6px; font-weight: 800; }

@media (max-width: 370px) {
  .liars-bar { grid-template-columns: 40px minmax(0, 1fr) auto 40px; }
  .liars-bar > :last-child { display: none; }
  .bid-controls { grid-template-columns: 76px minmax(0, 1fr) 60px; gap: 4px; }
  .face-picker { gap: 0; }
  :deep(.liars-die--picker) { --die-size: 23px; }
  .action-row { grid-template-columns: minmax(0, 1fr) 82px; }
  .action-row button { font-size: 12px; }
  .own-dice { gap: 3px; }
  :deep(.liars-die--private) { --die-size: 31px; }
  .revealed-grid { width: 120px; padding: 4px; }
  .revealed-row { grid-template-columns: 24px minmax(0, 1fr); gap: 3px; }
  .action-row { margin-top: 4px; }
  .bid-feedback { min-height: 12px; margin-top: 1px; line-height: 12px; }
}

@media (orientation: landscape) {
  .liars-screen { min-height: 360px; }
  .liars-bar { inset-inline: 12px; }
  .liars-stage { inset: 56px 0 min(31dvh, 122px); }
  .tray-collapsed .liars-stage { bottom: min(18dvh, 72px); }
  .tray-expanded .liars-stage { bottom: min(45dvh, 176px); }
  .current-bid strong { font-size: 34px; }
  :deep(.liars-die--focus) { --die-size: 34px; }
  :deep(.liars-die--private) { --die-size: 28px; }
  .bid-controls { grid-template-columns: 90px minmax(0, 1fr) 76px; }
  .action-row { position: absolute; right: max(12px, env(safe-area-inset-right)); top: 48px; width: min(38%, 330px); }
  .bid-feedback { padding-right: min(40%, 350px); }
  .round-details { grid-template-columns: 1.3fr 1fr auto; }
}

@media (prefers-reduced-motion: reduce) {
  .liars-stage { transition: none; }
  .current-bid,
  .revealed-grid { animation: none; }
}

@keyframes bid-enter {
  from { opacity: 0; transform: scale(.94); }
  to { opacity: 1; transform: scale(1); }
}

@keyframes reveal-enter {
  from { opacity: 0; transform: translateY(8px); }
  to { opacity: 1; transform: translateY(0); }
}

:global([data-motion="reduced"]) .current-bid,
:global([data-motion="reduced"]) .revealed-grid { animation: none; }
</style>
