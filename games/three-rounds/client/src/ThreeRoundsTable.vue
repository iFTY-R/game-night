<script setup lang="ts">
import { ArrowLeft, CheckCircle2, CircleStop, Palette, RefreshCw, Volume2, VolumeX } from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";

import type { ActionInput } from "@game-night/game-client";
import { ActionTray, ConnectionBadge, DangerConfirm, GameTable, PrivateZone, type TableSeat, type TrayState } from "@game-night/game-ui-kit";

import { formatCardLabel, parseCard, sortCardIdsByOrdinal } from "./cards";
import { THREE_ROUNDS_SUBMIT_SELECTION_ACTION } from "./constants";
import { configSummary, finishReasonLabel, phaseLabel, playerRoundStatus, revealSummary, roundLabel, roundResultLabel, selectionLimitForPhase, standingBadge } from "./controls";
import { createSubmitSelectionAction } from "./protocol";
import { FinishReason, Phase, type FinalStanding, type PublicPlayer, type RoundSummary } from "./generated/game/three_rounds/v1/three_rounds_pb";
import type { ThreeRoundsTableContext, ThreeRoundsView } from "./types";

const props = withDefaults(defineProps<{
  view: ThreeRoundsView;
  context: ThreeRoundsTableContext;
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
const selectedCards = ref<string[]>([]);
const submitConfirm = ref(false);
const finishConfirm = ref(false);
const clockNow = ref(Date.now());
let clockTimer: number | undefined;

const selectionLimit = computed(() => selectionLimitForPhase(props.view.phase));
const canSubmitSelection = computed(() => props.context.viewerRole === "player" && props.allowedActions.includes(THREE_ROUNDS_SUBMIT_SELECTION_ACTION));
const actionLocked = computed(() => props.pendingAction !== null || props.context.connection !== "online");
const isSelecting = computed(() => selectionLimit.value > 0);
const playerMap = computed(() => new Map(props.context.players.map((player) => [player.userId, player])));
const publicPlayers = computed(() => [...props.view.publicPlayers].sort((left, right) => left.seatIndex - right.seatIndex));
const selfPlayer = computed(() => publicPlayers.value.find((player) => player.userId === props.context.selfUserId));
const seats = computed<readonly TableSeat[]>(() =>
  publicPlayers.value.map((player) => ({
    seatIndex: player.seatIndex,
    userId: player.userId,
    displayName: playerMap.value.get(player.userId)?.displayName ?? `玩家 ${player.userId.slice(-4)}`,
    avatarText: playerMap.value.get(player.userId)?.avatarText ?? (playerMap.value.get(player.userId)?.displayName ?? "?").slice(0, 1),
    connected: playerMap.value.get(player.userId)?.connected ?? true,
    host: playerMap.value.get(player.userId)?.host ?? false,
    active: player.submitted || player.finalWinner,
    status: playerRoundStatus(player, props.view.currentRound),
  })),
);
const selfSeatIndex = computed(() => selfPlayer.value?.seatIndex ?? publicPlayers.value[0]?.seatIndex ?? 0);
const deadlineSeconds = computed(() => {
  const deadline = Number(props.view.phaseDeadlineUnixMillis);
  return deadline <= 0 ? null : Math.max(0, Math.ceil((deadline - clockNow.value) / 1000));
});
const pendingLabel = computed(() => props.pendingAction === THREE_ROUNDS_SUBMIT_SELECTION_ACTION ? "正在确认选牌" : null);
const selectionHint = computed(() => {
  if (props.context.viewerRole === "spectator") return "观战中";
  if (selectionLimit.value === 1) return "第一关需确认 1 张";
  if (selectionLimit.value === 2) return "第二关需确认 2 张";
  return "第三关自动结算";
});
const selfHand = computed(() => sortCardIdsByOrdinal(props.view.self?.remainingHand ?? []));
const submittedSelection = computed(() => sortCardIdsByOrdinal(props.view.self?.pendingSelection?.cardIds ?? []));
const currentSummary = computed(() => props.view.roundHistory.at(-1));
const finishNotice = computed(() => props.view.finishReason === FinishReason.UNSPECIFIED ? null : finishReasonLabel(props.view.finishReason));
const finalStandings = computed(() => [...(props.view.finalSummary?.standings ?? [])].sort((left, right) => left.rank - right.rank || right.totalPoints - left.totalPoints || left.seatIndex - right.seatIndex));

watch(
  () => props.view.self?.pendingSelection,
  (pending) => {
    selectedCards.value = pending === undefined ? [] : sortCardIdsByOrdinal(pending.cardIds);
  },
  { immediate: true },
);

onMounted(() => {
  clockTimer = window.setInterval(() => { clockNow.value = Date.now(); }, 250);
});

onBeforeUnmount(() => {
  if (clockTimer !== undefined) window.clearInterval(clockTimer);
});

const toggleCard = (cardId: string): void => {
  if (!isSelecting.value || !canSubmitSelection.value || actionLocked.value) return;
  const next = new Set(selectedCards.value);
  if (next.has(cardId)) {
    next.delete(cardId);
    selectedCards.value = sortCardIdsByOrdinal([...next]);
    return;
  }
  if (next.size >= selectionLimit.value) return;
  next.add(cardId);
  selectedCards.value = sortCardIdsByOrdinal([...next]);
};

const cardDisabled = (cardId: string): boolean =>
  actionLocked.value || !isSelecting.value || !canSubmitSelection.value || (!selectedCards.value.includes(cardId) && selectedCards.value.length >= selectionLimit.value);

const canConfirmSelection = computed(() => canSubmitSelection.value && selectedCards.value.length === selectionLimit.value && !actionLocked.value);

const submitSelection = (): void => {
  if (!canConfirmSelection.value) return;
  submitConfirm.value = false;
  emit("submit", createSubmitSelectionAction(props.view.currentRound, selectedCards.value));
};

const submitFinish = (): void => {
  finishConfirm.value = false;
  emit("finish");
};

const seatSummary = (player: PublicPlayer): string => {
  if (props.view.phase === Phase.FINAL_RESULT || props.view.phase === Phase.FINISHED) {
    const standing = props.view.finalSummary?.standings.find((candidate) => candidate.userId === player.userId);
    return standing ? `${standing.totalPoints} 分 · ${standingBadge(standing)}` : playerRoundStatus(player, props.view.currentRound);
  }
  if (player.submitted && isSelecting.value) return "已确认";
  if (props.view.phase === Phase.ROUND_THREE_RESULT && player.wonRoundThree) return "第三关领先";
  return playerRoundStatus(player, props.view.currentRound);
};

const currentStanding = (standing: FinalStanding): string => {
  const player = playerMap.value.get(standing.userId);
  const displayName = player?.displayName ?? `玩家 ${standing.userId.slice(-4)}`;
  return `${displayName} · ${standing.totalPoints} 分 · ${standingBadge(standing)}`;
};

const revealPrompt = (summary: RoundSummary | undefined): string =>
  summary === undefined ? "等待公开" : `${roundLabel(summary.round)}已公开`;
const cardMeta = (cardId: string) => parseCard(cardId);
</script>

<template>
  <main
    class="three-screen"
    :class="[
      `tray-${trayState}`,
      {
        'is-terminal': view.phase === Phase.FINAL_RESULT || view.phase === Phase.FINISHED || view.phase === Phase.ROUND_THREE_RESULT,
      },
    ]"
    data-testid="three-rounds-screen"
  >
    <header class="three-bar">
      <button class="icon-button" type="button" title="返回房间" @click="emit('leave')"><ArrowLeft :size="20" aria-hidden="true" /></button>
      <div class="three-title">
        <h1>三关定胜负</h1>
        <span>{{ context.roomCode }} · {{ phaseLabel(view.phase) }}</span>
      </div>
      <ConnectionBadge :state="context.connection" />
      <button class="icon-button" type="button" title="切换桌面主题" @click="emit('cycle-theme')"><Palette :size="19" aria-hidden="true" /></button>
      <button class="icon-button" type="button" :title="muted ? '开启声音' : '静音'" @click="emit('toggle-sound')">
        <VolumeX v-if="muted" :size="19" aria-hidden="true" />
        <Volume2 v-else :size="19" aria-hidden="true" />
      </button>
    </header>

    <section class="three-stage" aria-label="三关定胜负游戏区域">
      <GameTable
        :seats="seats"
        :self-seat-index="selfSeatIndex"
        shape="adaptive"
        :bottom-inset="trayState === 'expanded' ? 324 : trayState === 'compact' ? 188 : 86"
        label="三关定胜负桌面"
      >
        <template #center>
          <div class="focus-card" :class="{ 'is-terminal': view.phase === Phase.FINAL_RESULT || view.phase === Phase.FINISHED }">
            <span class="focus-card__eyebrow">{{ roundLabel(Math.min(Math.max(view.currentRound, 1), 3)) }}</span>
            <strong>{{ phaseLabel(view.phase) }}</strong>
            <small v-if="deadlineSeconds !== null && view.phase !== Phase.FINISHED">{{ deadlineSeconds }} 秒后推进</small>
            <small v-else-if="finishNotice">{{ finishNotice }}</small>
            <p>{{ revealPrompt(currentSummary) }}</p>
            <ul v-if="currentSummary && (view.phase === Phase.ROUND_ONE_RESULT || view.phase === Phase.ROUND_TWO_RESULT || view.phase === Phase.ROUND_THREE_RESULT)" class="focus-list">
              <li v-for="reveal in currentSummary.reveals" :key="`${currentSummary.round}-${reveal.userId}`">
                {{ playerMap.get(reveal.userId)?.displayName ?? reveal.userId }} · {{ revealSummary(reveal, currentSummary.round) }}
              </li>
            </ul>
            <ol v-else-if="finalStandings.length > 0" class="focus-list">
              <li v-for="standing in finalStandings" :key="standing.userId">{{ currentStanding(standing) }}</li>
            </ol>
          </div>
        </template>

        <template #private>
          <PrivateZone label="你的手牌区">
            <div class="private-hand">
              <button
                v-for="cardId in selfHand"
                :key="cardId"
                class="hand-card"
                :class="{ 'is-selected': selectedCards.includes(cardId), 'is-disabled': cardDisabled(cardId) }"
                type="button"
                :disabled="cardDisabled(cardId)"
                :aria-pressed="selectedCards.includes(cardId)"
                @click="toggleCard(cardId)"
              >
                <template v-if="cardMeta(cardId)">
                  <span class="hand-card__rank">{{ cardMeta(cardId)?.rankLabel }}</span>
                  <span class="hand-card__glyph" :class="`tone-${cardMeta(cardId)?.suitTone}`">{{ cardMeta(cardId)?.suitGlyph }}</span>
                </template>
                <span class="hand-card__code">{{ formatCardLabel(cardId) }}</span>
              </button>
            </div>
          </PrivateZone>
        </template>
      </GameTable>
    </section>

    <ActionTray v-model="trayState" :pending="pendingAction !== null" label="三关操作区">
      <template #summary>
        <div class="tray-summary">
          <span :class="{ active: canSubmitSelection }" />
          <strong>{{ pendingLabel ?? selectionHint }}</strong>
        </div>
        <button v-if="context.connection !== 'online'" class="retry-button" type="button" title="立即重连" @click="emit('retry')"><RefreshCw :size="17" aria-hidden="true" /></button>
        <b v-else class="tray-clock">{{ deadlineSeconds === null ? "--" : `${deadlineSeconds}s` }}</b>
      </template>

      <template #primary>
        <div class="tray-primary">
          <div class="tray-card">
            <span>你的提交</span>
            <strong>{{ submittedSelection.length > 0 ? submittedSelection.map(formatCardLabel).join(" · ") : selectionLimit > 0 ? "尚未确认" : "第三关无提交" }}</strong>
            <small>{{ selectionLimit > 0 ? `需要 ${selectionLimit} 张，当前 ${selectedCards.length} 张` : "剩余三张由系统自动结算" }}</small>
          </div>
          <div class="tray-card">
            <span>桌面状态</span>
            <strong>{{ currentSummary ? `${roundLabel(currentSummary.round)}已公开` : "等待所有人确认" }}</strong>
            <small>{{ configSummary(view.config).slice(0, 2).join(" · ") }}</small>
          </div>
          <button
            v-if="selectionLimit > 0"
            data-testid="submit-selection-action"
            class="primary-action"
            type="button"
            :disabled="!canConfirmSelection"
            @click="submitConfirm = true"
          >
            <CheckCircle2 :size="20" aria-hidden="true" />
            <span>确认{{ roundLabel(view.currentRound) }}</span>
          </button>
          <div v-else class="readonly-state">第三关自动公开，当前不需要再选牌。</div>
        </div>
      </template>

      <template #details>
        <div class="tray-details">
          <section class="detail-panel">
            <header><strong>本桌确认进度</strong><small>{{ roundLabel(Math.min(Math.max(view.currentRound, 1), 3)) }}</small></header>
            <ul>
              <li v-for="player in publicPlayers" :key="`status-${player.userId}`">
                <span>{{ playerMap.get(player.userId)?.displayName ?? player.userId }}</span>
                <b>{{ seatSummary(player) }}</b>
              </li>
            </ul>
          </section>
          <section class="detail-panel">
            <header><strong>规则时长</strong><small>房主可调</small></header>
            <ul>
              <li v-for="item in configSummary(view.config)" :key="item"><span>{{ item }}</span><b>固定</b></li>
            </ul>
          </section>
          <section class="detail-panel">
            <header><strong>总分排行</strong><small>{{ view.finalSummary ? "竞赛排名" : "未结算" }}</small></header>
            <ul v-if="finalStandings.length > 0">
              <li v-for="standing in finalStandings" :key="`standing-${standing.userId}`">
                <span>{{ playerMap.get(standing.userId)?.displayName ?? standing.userId }}</span>
                <b>{{ standingBadge(standing) }}</b>
              </li>
            </ul>
            <p v-else class="detail-empty">总榜会在第三关公开后显示。</p>
          </section>
          <button v-if="view.viewerIsHost" class="finish-button" type="button" :disabled="actionLocked" @click="finishConfirm = true">
            <CircleStop :size="18" aria-hidden="true" /> 结束本局
          </button>
        </div>
      </template>
    </ActionTray>

    <DangerConfirm :open="submitConfirm" title="确认提交这手牌？" confirm-label="确认提交" @confirm="submitSelection" @cancel="submitConfirm = false">
      {{ selectionLimit === 1 ? "第一关比单张大小，确认后本关不能再换牌。" : "第二关比十点半，确认后这两张会立刻锁定并等待其他玩家。" }}
    </DangerConfirm>
    <DangerConfirm :open="finishConfirm" title="结束整局？" confirm-label="确认结束" @confirm="submitFinish" @cancel="finishConfirm = false">
      当前局面会立即终止；已公开进度保留，未公开手牌继续保密。
    </DangerConfirm>
  </main>
</template>

<style scoped>
.three-screen {
  position: relative;
  width: 100%;
  height: 100dvh;
  min-height: 580px;
  overflow: hidden;
  color: var(--platform-ink);
  background:
    radial-gradient(circle at top, color-mix(in srgb, var(--platform-accent) 18%, transparent), transparent 32%),
    repeating-linear-gradient(135deg, rgb(255 255 255 / 2%) 0 1px, transparent 1px 10px),
    var(--platform-surface);
}

.three-bar {
  position: absolute;
  inset: max(8px, env(safe-area-inset-top)) max(8px, env(safe-area-inset-right)) auto max(8px, env(safe-area-inset-left));
  z-index: 30;
  min-height: 48px;
  display: grid;
  grid-template-columns: 42px minmax(0, 1fr) auto 42px 42px;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  background: color-mix(in srgb, var(--platform-surface) 88%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-muted) 18%, transparent);
  border-radius: 8px;
  backdrop-filter: blur(12px);
}

.icon-button,
.retry-button {
  width: 40px;
  height: 40px;
  display: grid;
  place-items: center;
  padding: 0;
  color: var(--platform-muted);
  background: transparent;
  border: 0;
  border-radius: 8px;
}

.three-title {
  min-width: 0;
  display: grid;
  gap: 1px;
}

.three-title h1,
.three-title span {
  overflow: hidden;
  margin: 0;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.three-title h1 {
  font-size: 18px;
  letter-spacing: 0.02em;
}

.three-title span {
  color: var(--platform-muted);
  font-size: 10px;
}

.three-stage {
  position: absolute;
  inset: 60px 0 min(22dvh, 184px);
  transition: bottom var(--game-motion-fast, 170ms) ease;
}

.tray-collapsed .three-stage {
  bottom: min(11dvh, 94px);
}

.tray-expanded .three-stage {
  bottom: min(40dvh, 340px);
}

.focus-card {
  width: min(100%, 220px);
  display: grid;
  gap: 4px;
  padding: 12px 14px;
  background: color-mix(in srgb, var(--platform-surface-raised) 92%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-accent) 32%, transparent);
  border-radius: 14px;
  box-shadow: 0 12px 32px rgb(0 0 0 / 26%);
  text-align: left;
}

.three-screen.is-terminal .focus-card {
  width: min(100%, 208px);
  gap: 3px;
  padding-block: 10px;
}

.focus-card.is-terminal {
  background: color-mix(in srgb, var(--game-private-surface, var(--platform-surface-raised)) 88%, transparent);
}

.focus-card__eyebrow,
.focus-card small {
  color: var(--platform-muted);
  font-size: 10px;
}

.focus-card strong {
  font-size: 20px;
  line-height: 1.05;
}

.three-screen.is-terminal .focus-card strong {
  font-size: 18px;
}

.focus-card p {
  margin: 0;
  color: var(--platform-accent);
  font-size: 12px;
}

.focus-list {
  display: grid;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.three-screen.is-terminal .focus-list {
  gap: 3px;
}

.focus-list li {
  color: var(--platform-ink);
  font-size: 11px;
  line-height: 1.35;
}

.private-hand {
  width: min(72vw, 202px);
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 8px;
}

.hand-card {
  min-width: 62px;
  min-height: 88px;
  display: grid;
  align-content: space-between;
  gap: 4px;
  padding: 8px;
  color: var(--game-card-ink, var(--platform-surface));
  background: var(--game-card-face, var(--platform-ink));
  border: 1px solid var(--game-card-outline, var(--platform-accent));
  border-radius: 12px;
  box-shadow: 0 10px 20px rgb(0 0 0 / 18%);
}

.hand-card.is-selected {
  transform: translateY(-6px);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--game-seat-glow, var(--platform-focus)) 44%, transparent), 0 12px 24px rgb(0 0 0 / 24%);
}

.hand-card.is-disabled {
  opacity: 0.58;
}

.hand-card__rank {
  font-size: 18px;
  font-weight: 900;
}

.hand-card__glyph {
  font-size: 18px;
  line-height: 1;
}

.tone-warm {
  color: var(--game-card-accent, var(--platform-danger));
}

.tone-cool {
  color: color-mix(in srgb, var(--game-card-ink, var(--platform-surface)) 78%, var(--platform-focus));
}

.tone-joker {
  color: var(--platform-accent);
}

.hand-card__code {
  font-size: 10px;
}

.tray-summary {
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
}

.tray-summary > span {
  width: 8px;
  height: 8px;
  flex: 0 0 auto;
  background: var(--platform-muted);
  border-radius: 50%;
}

.tray-summary > span.active {
  background: var(--game-success);
  box-shadow: 0 0 0 5px color-mix(in srgb, var(--game-success) 14%, transparent);
}

.tray-summary strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
}

.tray-clock {
  color: var(--platform-accent);
  font-size: 16px;
}

.tray-primary {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr)) minmax(0, 1.1fr);
  gap: 8px;
}

.tray-card,
.detail-panel {
  display: grid;
  gap: 4px;
  padding: 10px;
  background: rgb(0 0 0 / 16%);
  border: 1px solid color-mix(in srgb, var(--platform-muted) 18%, transparent);
  border-radius: 10px;
}

.tray-card span,
.tray-card small,
.detail-panel header small,
.detail-panel li span,
.detail-empty {
  color: var(--platform-muted);
  font-size: 10px;
}

.tray-card strong {
  font-size: 13px;
}

.primary-action,
.finish-button {
  min-height: 56px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  color: var(--game-card-ink, var(--platform-surface));
  background: var(--platform-accent);
  border: 0;
  border-radius: 10px;
  font-weight: 900;
}

.primary-action:disabled {
  opacity: 0.5;
}

.readonly-state {
  min-height: 56px;
  display: grid;
  place-items: center;
  padding: 0 12px;
  color: var(--platform-muted);
  border: 1px dashed color-mix(in srgb, var(--platform-muted) 24%, transparent);
  border-radius: 10px;
  font-size: 12px;
}

.tray-details {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr)) auto;
  gap: 8px;
}

.detail-panel header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.detail-panel ul {
  display: grid;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.detail-panel li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.detail-panel li b {
  font-size: 11px;
}

.finish-button {
  min-width: 110px;
  color: var(--platform-danger);
  background: transparent;
  border: 1px solid currentColor;
}

@media (max-width: 420px) {
  .three-screen :deep(.gn-table__center) {
    transform: none;
  }

  .three-screen :deep(.gn-table__private) {
    bottom: calc(2% + min(var(--gn-safe-bottom), 108px));
  }

  .focus-card {
    gap: 2px;
    padding: 9px 12px;
  }

  .focus-list {
    display: none;
  }

  .tray-expanded .focus-card {
    display: none;
  }

  .tray-primary,
  .tray-details {
    grid-template-columns: 1fr;
  }
}

@media (orientation: landscape) {
  .three-screen {
    min-height: 360px;
  }

  .three-stage {
    inset: 56px 0 min(34dvh, 148px);
    transition: none;
  }

  .three-screen :deep(.gn-tray) {
    transition: none;
  }

  .tray-collapsed .three-stage {
    bottom: min(20dvh, 92px);
  }

  .tray-expanded .three-stage {
    bottom: min(48dvh, 196px);
  }

  .focus-card {
    width: min(100%, 280px);
  }
}

@media (orientation: portrait) {
  .three-screen.is-terminal .three-stage :deep(.gn-table__center) {
    inset: 28% 19% calc(27% + min(var(--gn-safe-bottom), 108px)) 19%;
  }

  .three-screen.is-terminal .focus-card {
    transform: none;
  }
}

@media (orientation: landscape) and (max-height: 480px) {
  .focus-card {
    gap: 2px;
    padding: 8px 12px;
  }

  .focus-card strong {
    font-size: 18px;
  }

  .focus-list {
    display: none;
  }
}
</style>
