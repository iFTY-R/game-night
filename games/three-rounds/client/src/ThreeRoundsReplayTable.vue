<script setup lang="ts">
import { ArrowLeft, Palette, Trophy, Volume2, VolumeX } from "lucide-vue-next";
import { computed, ref } from "vue";

import { GameTable, type TableSeat } from "@game-night/game-ui-kit";

import { finishReasonLabel, revealSummary, roundLabel, roundResultLabel, standingBadge } from "./controls";
import type { FinalStanding, RoundSummary } from "./generated/game/three_rounds/v1/three_rounds_pb";
import type { ThreeRoundsReplay, ThreeRoundsReplayContext } from "./types";

const props = withDefaults(defineProps<{ replay: ThreeRoundsReplay; context: ThreeRoundsReplayContext; muted?: boolean }>(), { muted: false });
const emit = defineEmits<{ leave: []; "toggle-sound": []; "cycle-theme": [] }>();

type ReplayTab = 1 | 2 | 3 | "final";
const activeTab = ref<ReplayTab>(1);
const players = computed(() => new Map(props.context.players.map((player) => [player.userId, player])));
const seats = computed<readonly TableSeat[]>(() =>
  props.replay.players.map((player) => ({
    seatIndex: player.seatIndex,
    userId: player.userId,
    displayName: players.value.get(player.userId)?.displayName ?? `玩家 ${player.userId.slice(-4)}`,
    avatarText: players.value.get(player.userId)?.avatarText ?? (players.value.get(player.userId)?.displayName ?? "?").slice(0, 1),
    connected: true,
    host: players.value.get(player.userId)?.host ?? false,
  })),
);
const roundSummary = computed<RoundSummary | undefined>(() => typeof activeTab.value === "number" ? props.replay.rounds.find((round) => round.round === activeTab.value) : undefined);
const standings = computed<readonly FinalStanding[]>(() => [...(props.replay.finalSummary?.standings ?? [])].sort((left, right) => left.rank - right.rank || right.totalPoints - left.totalPoints));
const finishBanner = computed(() => finishReasonLabel(props.replay.finishReason));

const displayName = (userId: string): string => players.value.get(userId)?.displayName ?? `玩家 ${userId.slice(-4)}`;
</script>

<template>
  <main class="replay-screen" data-testid="three-rounds-replay-screen">
    <header class="replay-bar">
      <button class="icon-button" type="button" title="返回房间" @click="emit('leave')"><ArrowLeft :size="20" aria-hidden="true" /></button>
      <div class="replay-title">
        <h1>三关复盘</h1>
        <span>{{ context.roomCode }} · {{ finishBanner }}</span>
      </div>
      <button class="icon-button" type="button" title="切换桌面主题" @click="emit('cycle-theme')"><Palette :size="19" aria-hidden="true" /></button>
      <button class="icon-button" type="button" :title="muted ? '开启声音' : '静音'" @click="emit('toggle-sound')">
        <VolumeX v-if="muted" :size="19" aria-hidden="true" />
        <Volume2 v-else :size="19" aria-hidden="true" />
      </button>
    </header>

    <section class="replay-stage">
      <GameTable :seats="seats" :self-seat-index="seats[0]?.seatIndex ?? 0" shape="rounded-table" label="三关复盘桌面">
        <template #center>
          <div v-if="roundSummary" class="replay-focus">
            <span>{{ roundLabel(roundSummary.round) }}</span>
            <strong>{{ roundResultLabel(roundSummary) }}</strong>
            <small>{{ roundSummary.winnerUserIds.map(displayName).join("、") || "无人获分" }}</small>
            <ul>
              <li v-for="reveal in roundSummary.reveals" :key="`${roundSummary.round}-${reveal.userId}`">
                {{ displayName(reveal.userId) }} · {{ revealSummary(reveal, roundSummary.round) }}
              </li>
            </ul>
          </div>
          <div v-else class="replay-focus replay-focus--final">
            <span>总结果</span>
            <strong>{{ finishBanner }}</strong>
            <small>取消局不会伪装成正常冠军</small>
            <ol>
              <li v-for="standing in standings" :key="standing.userId">
                {{ displayName(standing.userId) }} · {{ standing.totalPoints }} 分 · {{ standingBadge(standing) }}
              </li>
            </ol>
          </div>
        </template>
      </GameTable>
    </section>

    <section class="replay-dock" aria-label="复盘标签">
      <div class="tab-row">
        <button v-for="tab in [1, 2, 3] as const" :key="`round-${tab}`" type="button" :class="{ active: activeTab === tab }" @click="activeTab = tab">
          {{ tab }}
        </button>
        <button type="button" :class="{ active: activeTab === 'final' }" @click="activeTab = 'final'">
          <Trophy :size="16" aria-hidden="true" /> 总结果
        </button>
      </div>
      <div class="replay-detail">
        <template v-if="roundSummary">
          <article v-for="reveal in roundSummary.reveals" :key="`${activeTab}-${reveal.userId}`">
            <strong>{{ displayName(reveal.userId) }}</strong>
            <span>{{ revealSummary(reveal, roundSummary.round) }}</span>
          </article>
        </template>
        <template v-else>
          <article v-for="standing in standings" :key="`final-${standing.userId}`">
            <strong>{{ displayName(standing.userId) }}</strong>
            <span>{{ standing.totalPoints }} 分 · {{ standingBadge(standing) }}</span>
          </article>
        </template>
      </div>
    </section>
  </main>
</template>

<style scoped>
.replay-screen {
  position: relative;
  width: 100%;
  height: 100dvh;
  min-height: 560px;
  overflow: hidden;
  color: var(--platform-ink);
  background:
    radial-gradient(circle at top, color-mix(in srgb, var(--platform-accent) 16%, transparent), transparent 32%),
    repeating-linear-gradient(135deg, rgb(255 255 255 / 2%) 0 1px, transparent 1px 10px),
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
  gap: 6px;
  padding: 4px 6px;
  background: color-mix(in srgb, var(--platform-surface) 88%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-muted) 18%, transparent);
  border-radius: 8px;
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
  border-radius: 8px;
}

.replay-title {
  min-width: 0;
  display: grid;
  gap: 1px;
}

.replay-title h1,
.replay-title span {
  overflow: hidden;
  margin: 0;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.replay-title h1 {
  font-size: 18px;
}

.replay-title span {
  color: var(--platform-muted);
  font-size: 10px;
}

.replay-stage {
  position: absolute;
  inset: 60px 0 min(28dvh, 228px);
}

.replay-focus {
  width: min(100%, 240px);
  display: grid;
  gap: 4px;
  padding: 12px;
  background: color-mix(in srgb, var(--platform-surface-raised) 92%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-accent) 32%, transparent);
  border-radius: 14px;
}

.replay-focus span,
.replay-focus small {
  color: var(--platform-muted);
  font-size: 10px;
}

.replay-focus strong {
  font-size: 20px;
}

.replay-focus ul,
.replay-focus ol {
  display: grid;
  gap: 4px;
  margin: 0;
  padding-left: 16px;
}

.replay-dock {
  position: absolute;
  inset-inline: 0;
  bottom: 0;
  z-index: 20;
  height: min(28dvh, 228px);
  padding: 10px max(12px, env(safe-area-inset-right)) max(8px, env(safe-area-inset-bottom)) max(12px, env(safe-area-inset-left));
  background: color-mix(in srgb, var(--platform-surface) 96%, var(--game-table));
  border-top: 1px solid color-mix(in srgb, var(--platform-accent) 34%, transparent);
}

.tab-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.tab-row button {
  min-height: 42px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 0 14px;
  color: var(--platform-muted);
  background: transparent;
  border: 1px solid color-mix(in srgb, var(--platform-muted) 24%, transparent);
  border-radius: 999px;
}

.tab-row button.active {
  color: var(--game-card-ink, var(--platform-surface));
  background: var(--platform-accent);
  border-color: var(--platform-accent);
}

.replay-detail {
  display: grid;
  gap: 8px;
  margin-top: 10px;
}

.replay-detail article {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 8px 10px;
  background: rgb(0 0 0 / 16%);
  border: 1px solid color-mix(in srgb, var(--platform-muted) 16%, transparent);
  border-radius: 10px;
}

.replay-detail span {
  color: var(--platform-muted);
  font-size: 11px;
}

@media (orientation: landscape) {
  .replay-screen {
    min-height: 360px;
  }

  .replay-stage {
    inset: 56px 0 min(34dvh, 138px);
  }

  .replay-dock {
    height: min(34dvh, 138px);
    padding-top: 6px;
  }
}
</style>
