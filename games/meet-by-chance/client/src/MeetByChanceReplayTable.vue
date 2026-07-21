<script setup lang="ts">
import { ArrowLeft, ChevronLeft, ChevronRight, Palette, Target, Volume2, VolumeX } from "lucide-vue-next";
import { computed, ref, watch } from "vue";

import { formatTicks, handClassLabel, matchKindLabel, roundOutcomeLabel, special235Label } from "./controls";
import type { ReplayEntry } from "./generated/game/meet_by_chance/v1/meet_by_chance_pb";
import MeetTable from "./MeetTable.vue";
import type { MeetByChanceReplay, MeetByChanceReplayContext } from "./types";

const props = withDefaults(defineProps<{ replay: MeetByChanceReplay; context: MeetByChanceReplayContext; muted?: boolean }>(), { muted: false });
const emit = defineEmits<{ leave: []; "toggle-sound": []; "cycle-theme": [] }>();

const roundIndex = ref(0);
watch(() => props.replay.rounds.length, (length) => {
  roundIndex.value = Math.min(roundIndex.value, Math.max(0, length - 1));
});

const selectedRound = computed(() => props.replay.rounds[roundIndex.value]?.summary);
const presentations = computed(() => new Map(props.context.players.map((player) => [player.userId, player])));
const displayName = (userId: string): string => presentations.value.get(userId)?.displayName ?? `玩家 ${userId.slice(-4)}`;
const eventRound = (entry: ReplayEntry): number => {
  const event = entry.event?.event;
  if (event === undefined || event.case === undefined) return 0;
  return event.case === "roundSettled" ? event.value.summary?.round ?? 0 : event.value.round;
};
const selectedEntries = computed(() => props.replay.entries.filter((entry) => eventRound(entry) === selectedRound.value?.round));
const selectedMatchBatch = computed(() => {
  for (let index = selectedEntries.value.length - 1; index >= 0; index--) {
    const event = selectedEntries.value[index]?.event?.event;
    if (event?.case === "matchResolved") return event.value.batch;
  }
  return undefined;
});
const anchorSeat = computed(() => selectedRound.value?.finalPlayers[0]?.seatIndex ?? 0);

const eventLabel = (entry: ReplayEntry): string => {
  const event = entry.event?.event;
  if (event === undefined || event.case === undefined) return "未知事件";
  if (event.case === "roundStarted") return "本轮开始";
  if (event.case === "diceRevealed") return `${event.value.rolledUserIds.map(displayName).join("、")} 亮骰`;
  if (event.case === "handClassified") {
    return `${displayName(event.value.userId)} · ${special235Label(event.value.special235Outcome) ?? handClassLabel(event.value.handClass)}`;
  }
  if (event.case === "matchResolved") {
    if (event.value.batch?.capped) return `同牌解析达到上限 ${event.value.batch.resolutionCount}`;
    return event.value.batch?.groups.map((group) => `${matchKindLabel(group.kind)} ${group.userIds.map(displayName).join("、")}`).join(" + ") ?? "自动解析同牌";
  }
  if (event.case === "targetSelected") {
    return event.value.previousUserId
      ? `靶子 ${displayName(event.value.previousUserId)} → ${displayName(event.value.userId)}`
      : `${displayName(event.value.userId)} 成为靶子`;
  }
  if (event.case === "targetRerolled") return `${displayName(event.value.userId)} 第 ${event.value.count} 次重摇`;
  if (event.case === "penaltyRecorded") return `${displayName(event.value.userId)} +${formatTicks(event.value.ticks)}`;
  if (event.case === "participantRevoked") return `${displayName(event.value.userId)} 离场`;
  if (event.case === "sessionFinished") return "整局结束";
  if (event.case === "special235Evaluated") return `${event.value.specialUserIds.map(displayName).join("、")} · ${special235Label(event.value.outcome) ?? "235 已判定"}`;
  return `本轮结算 · ${roundOutcomeLabel(event.value.summary?.outcome ?? 0)}`;
};

const selectRound = (offset: number): void => {
  roundIndex.value = Math.min(Math.max(roundIndex.value + offset, 0), Math.max(0, props.replay.rounds.length - 1));
};
</script>

<template>
  <main class="replay-screen" data-testid="meet-by-chance-replay-screen">
    <header class="replay-bar">
      <button class="icon-button" type="button" title="返回房间" @click="emit('leave')"><ArrowLeft :size="20" aria-hidden="true" /></button>
      <div class="replay-title"><h1>喜相逢复盘</h1><span>{{ context.roomCode }} · {{ replay.rounds.length }} 轮已结算</span></div>
      <button class="icon-button" type="button" title="切换桌面主题" @click="emit('cycle-theme')"><Palette :size="19" aria-hidden="true" /></button>
      <button class="icon-button" type="button" :title="muted ? '开启声音' : '静音'" @click="emit('toggle-sound')"><VolumeX v-if="muted" :size="19" aria-hidden="true" /><Volume2 v-else :size="19" aria-hidden="true" /></button>
    </header>

    <section class="replay-stage" aria-label="喜相逢复盘区域">
      <MeetTable
        v-if="selectedRound"
        :players="selectedRound.finalPlayers"
        :presentations="presentations"
        :self-seat-index="anchorSeat"
        :target-user-id="selectedRound.targetUserId"
        :match-batch="selectedMatchBatch"
        label="喜相逢复盘桌面"
      >
        <div class="replay-focus">
          <span>第 {{ selectedRound.round }} 轮</span>
          <div><Target :size="18" aria-hidden="true" /><strong>{{ displayName(selectedRound.targetUserId) }}</strong></div>
          <b>{{ roundOutcomeLabel(selectedRound.outcome) }}</b>
          <small>重摇 {{ selectedRound.targetRerollCount }} 次 · 同牌 {{ selectedRound.matchResolutionCount }} 批</small>
          <em v-if="selectedRound.targetHistoryUserIds.length > 1">靶子经过 {{ selectedRound.targetHistoryUserIds.map(displayName).join(" → ") }}</em>
        </div>
      </MeetTable>
      <div v-else class="replay-empty">暂无已结算轮次</div>
    </section>

    <section class="replay-dock" aria-label="完整事件链">
      <div class="round-nav">
        <button type="button" title="上一轮" :disabled="roundIndex === 0" @click="selectRound(-1)"><ChevronLeft :size="20" aria-hidden="true" /></button>
        <strong>{{ selectedRound ? `第 ${selectedRound.round} 轮完整事件` : "暂无事件" }}</strong>
        <button type="button" title="下一轮" :disabled="roundIndex >= replay.rounds.length - 1" @click="selectRound(1)"><ChevronRight :size="20" aria-hidden="true" /></button>
      </div>
      <ol class="event-chain">
        <li v-for="entry in selectedEntries" :key="entry.sequence.toString()">
          <span>{{ entry.sequence }}</span><b>{{ eventLabel(entry) }}</b><small>已公开</small>
        </li>
      </ol>
    </section>
  </main>
</template>

<style scoped>
.replay-screen { --meet-display: "STKaiti", "KaiTi", serif; position: relative; width: 100%; height: 100dvh; min-height: 560px; overflow: hidden; color: var(--platform-ink); background: repeating-linear-gradient(118deg, rgb(255 255 255 / 2%) 0 1px, transparent 1px 11px), var(--platform-surface); }
.replay-bar { position: absolute; inset: max(8px, env(safe-area-inset-top)) max(8px, env(safe-area-inset-right)) auto max(8px, env(safe-area-inset-left)); z-index: 30; min-height: 48px; display: grid; grid-template-columns: 42px minmax(0, 1fr) 42px 42px; align-items: center; gap: 5px; padding: 4px 5px; background: color-mix(in srgb, var(--platform-surface) 88%, transparent); border: 1px solid color-mix(in srgb, var(--platform-muted) 18%, transparent); border-radius: 7px; backdrop-filter: blur(12px); }
.icon-button,
.round-nav button { width: 40px; height: 40px; display: grid; place-items: center; padding: 0; color: var(--platform-muted); background: transparent; border: 0; border-radius: 6px; }
.replay-title { min-width: 0; display: grid; gap: 1px; }
.replay-title h1,
.replay-title span { overflow: hidden; margin: 0; text-overflow: ellipsis; white-space: nowrap; }
.replay-title h1 { font-family: var(--meet-display); font-size: 17px; }
.replay-title span { color: var(--platform-muted); font-size: 10px; }
.replay-stage { position: absolute; inset: 60px 0 min(29dvh, 244px); }
.replay-focus { width: 100%; display: grid; place-items: center; gap: 3px; animation: replay-enter var(--game-motion-reveal, 300ms) ease-out; }
.replay-focus > span,
.replay-focus small,
.replay-focus em { color: var(--platform-muted); font-size: 9px; font-style: normal; }
.replay-focus > div { display: flex; align-items: center; gap: 5px; color: var(--game-target, var(--platform-accent)); }
.replay-focus strong { font-family: var(--meet-display); font-size: 19px; }
.replay-focus b { color: var(--platform-accent); font-size: 10px; }
.replay-focus em { max-width: 170px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.replay-empty { height: 100%; display: grid; place-items: center; color: var(--platform-muted); }
.replay-dock { position: absolute; inset-inline: 0; bottom: 0; z-index: 20; height: min(29dvh, 244px); padding: 10px max(12px, env(safe-area-inset-right)) max(8px, env(safe-area-inset-bottom)) max(12px, env(safe-area-inset-left)); background: color-mix(in srgb, var(--platform-surface) 96%, var(--game-table)); border-top: 1px solid color-mix(in srgb, var(--platform-accent) 34%, transparent); box-shadow: 0 -14px 34px rgb(0 0 0 / 28%); }
.round-nav { display: grid; grid-template-columns: 40px minmax(0, 1fr) 40px; align-items: center; }
.round-nav strong { overflow: hidden; text-align: center; text-overflow: ellipsis; white-space: nowrap; }
.round-nav button:disabled { opacity: .28; }
.event-chain { display: flex; overflow-x: auto; margin: 8px 0 0; padding: 0 0 8px; list-style: none; scroll-snap-type: x proximity; }
.event-chain li { min-width: 142px; display: grid; grid-template-columns: 24px minmax(0, 1fr); gap: 3px 6px; padding: 8px 10px; border-top: 1px solid color-mix(in srgb, var(--platform-accent) 42%, transparent); scroll-snap-align: start; }
.event-chain li > span { grid-row: 1 / span 2; width: 22px; height: 22px; display: grid; place-items: center; color: var(--platform-surface); background: var(--platform-accent); border-radius: 50%; font-size: 9px; font-weight: 900; }
.event-chain b { overflow: hidden; font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.event-chain small { color: var(--platform-muted); font-size: 8px; }
@media (orientation: landscape) {
  .replay-screen { min-height: 360px; }
  .replay-bar { inset-inline: 12px; }
  .replay-stage { inset: 56px 0 min(34dvh, 132px); }
  .replay-dock { height: min(34dvh, 132px); padding-top: 6px; }
  .round-nav button { height: 30px; }
  .event-chain { margin-top: 3px; }
  .event-chain li { padding-block: 5px; }
  .replay-focus { width: 300px; grid-template-columns: auto minmax(0, 1fr); gap: 2px 9px; }
  .replay-focus > span,
  .replay-focus > div { grid-column: 1; }
  .replay-focus > b,
  .replay-focus > small,
  .replay-focus > em { grid-column: 2; justify-self: start; }
  .replay-focus > b { grid-row: 1; }
  .replay-focus > small { grid-row: 2; }
  .replay-focus > em { grid-row: 3; }
}
@keyframes replay-enter { from { opacity: 0; transform: translateY(7px); } to { opacity: 1; transform: translateY(0); } }
@media (prefers-reduced-motion: reduce) { .replay-focus { animation: none; } }
:global([data-motion="reduced"]) .replay-focus { animation: none; }
</style>
