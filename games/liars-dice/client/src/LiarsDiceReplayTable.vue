<script setup lang="ts">
import { ArrowLeft, ChevronLeft, ChevronRight, Eye, Palette, Volume2, VolumeX } from "lucide-vue-next";
import { computed, ref, watch } from "vue";

import { GameTable, PrivateZone } from "@game-night/game-ui-kit";
import type { TableSeat } from "@game-night/game-ui-kit";

import DiceFace from "./DiceFace.vue";
import { BidMode, type Replay } from "./generated/game/liars_dice/v1/liars_dice_pb";
import type { LiarsDiceReplayContext } from "./types";

const props = withDefaults(
  defineProps<{
    replay: Replay;
    context: LiarsDiceReplayContext;
    muted?: boolean;
  }>(),
  { muted: false },
);
const emit = defineEmits<{ leave: []; "toggle-sound": []; "cycle-theme": [] }>();

const roundIndex = ref(0);
watch(
  () => props.replay.rounds.length,
  (length) => {
    roundIndex.value = Math.min(roundIndex.value, Math.max(0, length - 1));
  },
);

const selectedRound = computed(() => props.replay.rounds[roundIndex.value]);
const presentations = computed(() => new Map(props.context.players.map((player) => [player.userId, player])));
const displayName = (userId: string): string => presentations.value.get(userId)?.displayName ?? `玩家 ${userId.slice(-4)}`;
const seats = computed<readonly TableSeat[]>(() =>
  props.context.players.map((player, index) => ({
    seatIndex: player.seatIndex ?? index,
    userId: player.userId,
    displayName: player.displayName,
    connected: true,
    status: "复盘席位",
    ...(player.avatarText === undefined ? {} : { avatarText: player.avatarText }),
    ...(player.host === undefined ? {} : { host: player.host }),
  })),
);
const anchorSeat = computed(() => seats.value[0]?.seatIndex ?? 0);
const finalBid = computed(() => {
  const round = selectedRound.value;
  if (round === undefined) return undefined;
  return round.bid ?? round.bids.at(-1)?.bid;
});
const settlementLabel = computed(() => {
  const round = selectedRound.value;
  if (round === undefined) return "暂无已结算轮次";
  if (round.reason === "timeout") return `${displayName(round.loserUserId)} 超时 · ${round.penaltyTicks} 罚点`;
  return `${displayName(round.loserUserId)} 负 · 实际 ${round.actualQuantity} 个`;
});

const selectRound = (offset: number): void => {
  roundIndex.value = Math.min(Math.max(roundIndex.value + offset, 0), Math.max(0, props.replay.rounds.length - 1));
};
</script>

<template>
  <main class="replay-screen" data-testid="liars-dice-replay-screen">
    <header class="replay-bar">
      <button class="icon-button" type="button" title="返回房间" @click="emit('leave')">
        <ArrowLeft :size="20" aria-hidden="true" />
      </button>
      <div class="replay-title">
        <h1>吹牛骰子复盘</h1>
        <span>{{ context.roomCode }} · {{ replay.rounds.length }} 轮已结算</span>
      </div>
      <button class="icon-button" type="button" title="切换桌面主题" @click="emit('cycle-theme')">
        <Palette :size="19" aria-hidden="true" />
      </button>
      <button class="icon-button" type="button" :title="muted ? '开启声音' : '静音'" @click="emit('toggle-sound')">
        <VolumeX v-if="muted" :size="19" aria-hidden="true" />
        <Volume2 v-else :size="19" aria-hidden="true" />
      </button>
    </header>

    <section class="replay-stage" aria-label="吹牛骰子复盘桌面">
      <GameTable :seats="seats" :self-seat-index="anchorSeat" shape="compact-oval">
        <template #center>
          <div v-if="selectedRound" :key="selectedRound.round" class="replay-focus">
            <span>第 {{ selectedRound.round }} 轮</span>
            <div v-if="finalBid" class="final-bid">
              <strong>{{ finalBid.quantity }}</strong>
              <small>个</small>
              <DiceFace :face="finalBid.face" variant="focus" />
              <em>{{ finalBid.mode === BidMode.STRICT ? "斋" : "飞" }}</em>
            </div>
            <b>{{ settlementLabel }}</b>
            <div v-if="selectedRound.diceRevealed" class="replay-dice" role="region" aria-label="本轮公开骰子">
              <div v-for="roll in selectedRound.dice" :key="roll.userId" class="replay-dice__row">
                <span>{{ displayName(roll.userId) }}</span>
                <div><DiceFace v-for="(face, index) in roll.faces" :key="index" :face="face" variant="tiny" /></div>
              </div>
            </div>
            <div v-else class="dice-private"><Eye :size="16" aria-hidden="true" />规则未公开本轮骰子</div>
          </div>
          <div v-else class="replay-empty">暂无已结算轮次</div>
        </template>
        <template #private>
          <PrivateZone label="复盘状态">
            <div class="replay-private"><Eye :size="18" aria-hidden="true" /><span>只读复盘 · 无可用操作</span></div>
          </PrivateZone>
        </template>
      </GameTable>
    </section>

    <section class="replay-dock" aria-label="完整叫数链">
      <div class="round-nav">
        <button type="button" title="上一轮" :disabled="roundIndex === 0" @click="selectRound(-1)">
          <ChevronLeft :size="20" aria-hidden="true" />
        </button>
        <strong>{{ selectedRound ? `第 ${selectedRound.round} 轮叫数链` : "暂无叫数链" }}</strong>
        <button type="button" title="下一轮" :disabled="roundIndex >= replay.rounds.length - 1" @click="selectRound(1)">
          <ChevronRight :size="20" aria-hidden="true" />
        </button>
      </div>
      <ol v-if="selectedRound?.bids.length" class="bid-chain">
        <li v-for="(entry, index) in selectedRound.bids" :key="`${entry.userId}-${index}`">
          <span>{{ index + 1 }}</span>
          <b>{{ displayName(entry.userId) }}</b>
          <strong>{{ entry.bid?.quantity ?? 0 }}</strong>
          <DiceFace v-if="entry.bid" :face="entry.bid.face" variant="tiny" />
          <em>{{ entry.bid?.mode === BidMode.STRICT ? "斋" : "飞" }}</em>
        </li>
      </ol>
      <p v-else class="chain-empty">本轮无人叫骰</p>
    </section>
  </main>
</template>

<style scoped>
.replay-screen {
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

.replay-bar {
  position: absolute;
  inset: max(8px, env(safe-area-inset-top)) max(8px, env(safe-area-inset-right)) auto max(8px, env(safe-area-inset-left));
  z-index: 30;
  min-height: 48px;
  display: grid;
  grid-template-columns: 42px minmax(0, 1fr) 42px 42px;
  align-items: center;
  gap: 5px;
  padding: 4px 5px;
  background: color-mix(in srgb, var(--platform-surface) 88%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-muted) 18%, transparent);
  border-radius: 7px;
  backdrop-filter: blur(12px);
}

.icon-button,
.round-nav button {
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
.icon-button:hover,
.round-nav button:hover:not(:disabled) { color: var(--platform-ink); background: rgb(255 255 255 / 5%); }
.replay-title { min-width: 0; display: grid; gap: 1px; }
.replay-title h1,
.replay-title span { overflow: hidden; margin: 0; text-overflow: ellipsis; white-space: nowrap; }
.replay-title h1 { font-family: var(--liars-display); font-size: 17px; }
.replay-title span { color: var(--platform-muted); font-size: 10px; }

.replay-stage { position: absolute; inset: 60px 0 min(28dvh, 236px); min-height: 0; }
.replay-focus { width: 100%; display: grid; place-items: center; gap: 5px; animation: replay-enter var(--game-motion-reveal, 260ms) ease-out; }
.replay-focus > span { color: var(--platform-muted); font-size: 11px; }
.replay-focus > b { max-width: 210px; overflow: hidden; color: var(--platform-accent); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.final-bid { display: flex; align-items: center; justify-content: center; gap: 5px; }
.final-bid strong { font-family: var(--liars-display); font-size: 40px; line-height: .9; }
.final-bid small { color: var(--platform-muted); }
.final-bid em { color: var(--platform-accent); font-family: var(--liars-display); font-size: 16px; font-style: normal; }
.replay-dice { width: 136px; max-height: 104px; display: grid; gap: 3px; overflow: auto; padding: 5px; background: rgb(0 0 0 / 18%); border-radius: 6px; }
.replay-dice__row { display: grid; grid-template-columns: 32px minmax(0, 1fr); align-items: center; gap: 4px; }
.replay-dice__row > span { overflow: hidden; color: var(--platform-muted); font-size: 9px; text-overflow: ellipsis; white-space: nowrap; }
.replay-dice__row :deep(.liars-die--tiny) { --die-size: 17px; margin-left: 0; }
.dice-private,
.replay-private { display: flex; align-items: center; gap: 7px; color: var(--platform-muted); font-size: 11px; }
.replay-empty { color: var(--platform-muted); font-size: 12px; }

.replay-dock {
  position: absolute;
  inset-inline: 0;
  bottom: 0;
  z-index: 20;
  height: min(28dvh, 236px);
  padding: 12px max(12px, env(safe-area-inset-right)) max(8px, env(safe-area-inset-bottom)) max(12px, env(safe-area-inset-left));
  background: color-mix(in srgb, var(--platform-surface) 96%, var(--game-table));
  border-top: 1px solid color-mix(in srgb, var(--platform-accent) 34%, transparent);
  box-shadow: 0 -14px 34px rgb(0 0 0 / 28%);
}
.round-nav { display: grid; grid-template-columns: 40px minmax(0, 1fr) 40px; align-items: center; gap: 6px; }
.round-nav strong { overflow: hidden; text-align: center; text-overflow: ellipsis; white-space: nowrap; }
.round-nav button:disabled { opacity: .28; }
.bid-chain { display: flex; gap: 0; overflow-x: auto; margin: 10px 0 0; padding: 0 0 8px; list-style: none; scroll-snap-type: x proximity; }
.bid-chain li { min-width: 116px; display: grid; grid-template-columns: 22px minmax(0, 1fr) auto auto auto; align-items: center; gap: 4px; padding: 8px 10px; border-top: 1px solid color-mix(in srgb, var(--platform-accent) 45%, transparent); scroll-snap-align: start; }
.bid-chain li > span { width: 20px; height: 20px; display: grid; place-items: center; color: var(--platform-surface); background: var(--platform-accent); border-radius: 50%; font-size: 10px; font-weight: 900; }
.bid-chain b { overflow: hidden; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.bid-chain strong { font-family: var(--liars-display); font-size: 18px; }
.bid-chain em { color: var(--platform-accent); font-family: var(--liars-display); font-size: 12px; font-style: normal; }
.chain-empty { margin: 18px 0; color: var(--platform-muted); text-align: center; font-size: 11px; }

@media (max-width: 370px) {
  .replay-dice { width: 120px; padding: 4px; }
  .replay-dice__row { grid-template-columns: 24px minmax(0, 1fr); gap: 3px; }
}

@media (orientation: landscape) {
  .replay-screen { min-height: 360px; }
  .replay-bar { inset-inline: 12px; }
  .replay-stage { inset: 56px 0 min(34dvh, 132px); }
  .replay-dock { height: min(34dvh, 132px); padding-top: 7px; }
  .round-nav button { height: 32px; }
  .bid-chain { margin-top: 4px; }
  .bid-chain li { padding-block: 5px; }
  :deep(.gn-table__center) { inset: 23% 25% 27%; }
  .replay-focus {
    width: min(100%, 420px);
    grid-template-columns: 130px minmax(0, 280px);
    grid-template-rows: auto auto auto;
    column-gap: 10px;
    row-gap: 2px;
  }
  .replay-focus > span { grid-column: 1; grid-row: 1; }
  .final-bid { grid-column: 1; grid-row: 2; }
  .replay-focus > b { grid-column: 1; grid-row: 3; }
  .replay-dice,
  .dice-private { grid-column: 2; grid-row: 1 / span 3; align-self: center; }
  .replay-dice {
    width: 280px;
    max-height: none;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    column-gap: 8px;
  }
  .final-bid strong { font-size: 32px; }
  .final-bid :deep(.liars-die--focus) { --die-size: 32px; }
}

@keyframes replay-enter {
  from { opacity: 0; transform: translateY(8px); }
  to { opacity: 1; transform: translateY(0); }
}

@media (prefers-reduced-motion: reduce) {
  .replay-focus { animation: none; }
}

:global([data-motion="reduced"]) .replay-focus { animation: none; }
</style>
