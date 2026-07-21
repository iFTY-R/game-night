<script setup lang="ts">
import { ArrowLeft, ChevronLeft, ChevronRight, Palette, RotateCcw, RotateCw, Volume2, VolumeX } from "lucide-vue-next";
import { computed, ref, watch } from "vue";

import { GameTable } from "@game-night/game-ui-kit";
import type { TableSeat } from "@game-night/game-ui-kit";

import { formatTicks } from "./controls";
import DiceFace from "./DiceFace.vue";
import { Effect, type TurnSummary } from "./generated/game/dice789/v1/dice_789_pb";
import PoolStack from "./PoolStack.vue";
import type { Dice789Replay, Dice789ReplayContext } from "./types";

const props = withDefaults(defineProps<{ replay: Dice789Replay; context: Dice789ReplayContext; muted?: boolean }>(), { muted: false });
const emit = defineEmits<{ leave: []; "toggle-sound": []; "cycle-theme": [] }>();
const turnIndex = ref(0);

watch(() => props.replay.turns.length, (length) => {
  turnIndex.value = Math.min(turnIndex.value, Math.max(0, length - 1));
});

const presentations = computed(() => new Map(props.context.players.map((player) => [player.userId, player])));
const displayName = (userId: string): string => presentations.value.get(userId)?.displayName ?? `玩家 ${userId.slice(-4)}`;
const selected = computed(() => props.replay.turns[turnIndex.value]);
const summary = computed(() => selected.value?.summary);
const seats = computed<readonly TableSeat[]>(() => props.replay.players.map((player) => {
  const presentation = presentations.value.get(player.userId);
  return {
    seatIndex: player.seatIndex,
    userId: player.userId,
    displayName: presentation?.displayName ?? displayName(player.userId),
    connected: true,
    active: player.userId === summary.value?.sourceUserId,
    status: "复盘席位",
    ...(presentation?.avatarText === undefined ? {} : { avatarText: presentation.avatarText }),
    ...(presentation?.host === undefined ? {} : { host: presentation.host }),
  };
}));
const effectLabels: Readonly<Record<number, string>> = {
  [Effect.PASS]: "直接过牌", [Effect.SUM_SEVEN_ADD]: "和为 7 · 加注", [Effect.SUM_EIGHT_HALF_POOL]: "和为 8 · 半池",
  [Effect.SUM_NINE_DRAIN_POOL]: "和为 9 · 全池", [Effect.ORDINARY_PAIR_REVERSE]: "普通对子 · 反转", [Effect.ORDINARY_PAIR_REROLL]: "两人对子 · 重摇",
  [Effect.DOUBLE_ONE_TARGET_DRAIN]: "双 1 · 指定全池", [Effect.DOUBLE_FOUR_HALF_POOL_REROLL]: "双 4 · 半池重摇",
  [Effect.DOUBLE_SIX_TARGET_ADD]: "双 6 · 指定加注", [Effect.DROPPED_DRAIN_POOL]: "掉骰 · 全池",
};
const effectLabel = (value: TurnSummary | undefined): string => value === undefined ? "暂无结算" : effectLabels[value.effect] ?? value.resolutionReason;
const selectTurn = (offset: number): void => {
  turnIndex.value = Math.min(Math.max(turnIndex.value + offset, 0), Math.max(0, props.replay.turns.length - 1));
};
</script>

<template>
  <main class="replay-screen" data-testid="dice-789-replay-screen">
    <header class="replay-bar">
      <button class="icon-button" type="button" title="返回房间" @click="emit('leave')"><ArrowLeft :size="20" aria-hidden="true" /></button>
      <div class="replay-title"><h1>789 复盘</h1><span>{{ context.roomCode }} · {{ replay.turns.length }} 回合</span></div>
      <button class="icon-button" type="button" title="切换桌面主题" @click="emit('cycle-theme')"><Palette :size="19" aria-hidden="true" /></button>
      <button class="icon-button" type="button" :title="muted ? '开启声音' : '静音'" @click="emit('toggle-sound')"><VolumeX v-if="muted" :size="19" aria-hidden="true" /><Volume2 v-else :size="19" aria-hidden="true" /></button>
    </header>

    <section class="replay-stage" aria-label="789 复盘桌面">
      <GameTable :seats="seats" :self-seat-index="seats[0]?.seatIndex ?? 0" shape="compact-oval">
        <template #center>
          <div v-if="summary" :key="summary.turn" class="replay-focus">
            <span>第 {{ summary.turn }} 回合 · {{ displayName(summary.sourceUserId) }}</span>
            <div class="replay-dice"><DiceFace :face="summary.dieOne" /><DiceFace :face="summary.dieTwo" /></div>
            <div class="replay-result"><strong>{{ summary.sum }}</strong><b>{{ effectLabel(summary) }}</b><RotateCw v-if="summary.directionAfter === 1" :size="18" aria-label="顺时针" /><RotateCcw v-else :size="18" aria-label="逆时针" /></div>
            <small>{{ formatTicks(summary.poolBeforeTicks) }} → {{ formatTicks(summary.poolAfterTicks) }}<template v-if="summary.penaltyTicks"> · {{ displayName(summary.penaltyUserId) }} +{{ formatTicks(summary.penaltyTicks) }}</template></small>
          </div>
          <div v-else class="replay-empty">暂无已结算回合</div>
        </template>
        <template #private>
          <PoolStack
            :layers="summary?.poolAfterLayers ?? []"
            :total-ticks="summary?.poolAfterTicks ?? 0"
            :layer-capacity-ticks="replay.config?.layerCapacityTicks ?? 0"
          />
        </template>
      </GameTable>
    </section>

    <section class="replay-dock" aria-label="回合复盘时间线">
      <div class="turn-nav">
        <button type="button" title="上一回合" :disabled="turnIndex === 0" @click="selectTurn(-1)"><ChevronLeft :size="20" aria-hidden="true" /></button>
        <strong>{{ summary ? `第 ${summary.turn} 回合` : "暂无回合" }}</strong>
        <button type="button" title="下一回合" :disabled="turnIndex >= replay.turns.length - 1" @click="selectTurn(1)"><ChevronRight :size="20" aria-hidden="true" /></button>
      </div>
      <ol class="turn-timeline">
        <li v-for="(turn, index) in replay.turns" :key="turn.summary?.turn ?? index" :class="{ selected: index === turnIndex }" @click="turnIndex = index">
          <span>{{ turn.summary?.turn ?? "-" }}</span><b>{{ effectLabel(turn.summary) }}</b><small>{{ turn.settled ? "已结算" : "未结算" }}</small>
        </li>
      </ol>
    </section>
  </main>
</template>

<style scoped>
.replay-screen { --d789-display: "STKaiti", "KaiTi", serif; position: relative; width: 100%; height: 100dvh; min-height: 560px; overflow: hidden; color: var(--platform-ink); background: repeating-linear-gradient(120deg, rgb(255 255 255 / 2%) 0 1px, transparent 1px 12px), var(--platform-surface); }
.replay-bar { position: absolute; inset: max(8px, env(safe-area-inset-top)) max(8px, env(safe-area-inset-right)) auto max(8px, env(safe-area-inset-left)); z-index: 30; min-height: 48px; display: grid; grid-template-columns: 42px minmax(0, 1fr) 42px 42px; align-items: center; gap: 5px; padding: 4px 5px; background: color-mix(in srgb, var(--platform-surface) 88%, transparent); border: 1px solid color-mix(in srgb, var(--platform-muted) 18%, transparent); border-radius: 7px; backdrop-filter: blur(12px); }
.icon-button,
.turn-nav button { width: 40px; height: 40px; display: grid; place-items: center; padding: 0; color: var(--platform-muted); background: transparent; border: 0; border-radius: 6px; }
.replay-title { min-width: 0; display: grid; gap: 1px; }
.replay-title h1,
.replay-title span { overflow: hidden; margin: 0; text-overflow: ellipsis; white-space: nowrap; }
.replay-title h1 { font-family: var(--d789-display); font-size: 18px; }
.replay-title span { color: var(--platform-muted); font-size: 10px; }
.replay-stage { position: absolute; inset: 60px 0 min(27dvh, 228px); }
.replay-focus { width: 100%; display: grid; place-items: center; gap: 5px; animation: replay-enter var(--game-motion-roll, 280ms) ease-out; }
.replay-focus > span,
.replay-focus small { color: var(--platform-muted); font-size: 10px; }
.replay-dice { display: flex; gap: 8px; }
.replay-result { display: flex; align-items: center; gap: 7px; }
.replay-result strong { font-family: var(--d789-display); font-size: 28px; }
.replay-result b { color: var(--platform-accent); font-size: 11px; }
.replay-result svg { color: var(--game-direction, #80c7b5); }
.replay-empty { color: var(--platform-muted); font-size: 12px; }
.replay-dock { position: absolute; inset-inline: 0; bottom: 0; z-index: 20; height: min(27dvh, 228px); padding: 10px max(12px, env(safe-area-inset-right)) max(8px, env(safe-area-inset-bottom)) max(12px, env(safe-area-inset-left)); background: color-mix(in srgb, var(--platform-surface) 96%, var(--game-table)); border-top: 1px solid color-mix(in srgb, var(--platform-accent) 34%, transparent); box-shadow: 0 -14px 34px rgb(0 0 0 / 28%); }
.turn-nav { display: grid; grid-template-columns: 40px minmax(0, 1fr) 40px; align-items: center; }
.turn-nav strong { text-align: center; }
.turn-nav button:disabled { opacity: .3; }
.turn-timeline { display: flex; gap: 0; overflow-x: auto; margin: 9px 0 0; padding: 0 0 8px; list-style: none; scroll-snap-type: x proximity; }
.turn-timeline li { min-width: 132px; display: grid; grid-template-columns: 24px minmax(0, 1fr); gap: 3px 6px; padding: 9px 10px; border-top: 1px solid color-mix(in srgb, var(--platform-muted) 22%, transparent); scroll-snap-align: start; cursor: pointer; }
.turn-timeline li.selected { border-color: var(--platform-accent); background: color-mix(in srgb, var(--platform-accent) 8%, transparent); }
.turn-timeline span { grid-row: 1 / span 2; width: 22px; height: 22px; display: grid; place-items: center; color: var(--platform-surface); background: var(--platform-accent); border-radius: 50%; font-size: 10px; font-weight: 900; }
.turn-timeline b { overflow: hidden; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.turn-timeline small { color: var(--platform-muted); font-size: 9px; }
@media (orientation: landscape) {
  .replay-screen { min-height: 360px; }
  .replay-bar { inset-inline: 12px; }
  .replay-stage { inset: 56px 0 min(34dvh, 132px); }
  .replay-dock { height: min(34dvh, 132px); padding-top: 6px; }
  .turn-nav button { height: 30px; }
  .turn-timeline { margin-top: 3px; }
  .turn-timeline li { padding-block: 5px; }
  .replay-stage :deep(.gn-table__center) { inset-inline: 20%; }
  .replay-focus { width: min(460px, 100%); grid-template-columns: auto auto minmax(0, 1fr); grid-template-rows: auto auto; gap: 2px 10px; }
  .replay-focus > span { grid-column: 2 / -1; grid-row: 1; justify-self: start; }
  .replay-focus > small { grid-column: 3; grid-row: 2; justify-self: start; white-space: nowrap; }
  .replay-dice { grid-column: 1; grid-row: 1 / -1; }
  .replay-result { grid-column: 2; grid-row: 2; justify-self: start; }
}
@keyframes replay-enter { from { opacity: 0; transform: translateY(8px); } to { opacity: 1; transform: translateY(0); } }
@media (prefers-reduced-motion: reduce) { .replay-focus { animation: none; } }
:global([data-motion="reduced"]) .replay-focus { animation: none; }
</style>
