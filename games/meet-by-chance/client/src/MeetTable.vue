<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

import { computeSeatLayout } from "@game-night/game-ui-kit";

import type { MatchBatch, PublicPlayer } from "./generated/game/meet_by_chance/v1/meet_by_chance_pb";
import MeetPlayerSeat from "./MeetPlayerSeat.vue";
import type { MeetByChancePlayerPresentation } from "./types";

const props = defineProps<{
  players: readonly PublicPlayer[];
  presentations: ReadonlyMap<string, MeetByChancePlayerPresentation>;
  selfSeatIndex: number;
  targetUserId?: string | undefined;
  matchBatch?: MatchBatch | undefined;
  label?: string;
}>();

const root = ref<HTMLElement>();
const size = ref({ width: 390, height: 560 });
let observer: ResizeObserver | undefined;

const density = computed<"normal" | "dense" | "micro">(() => props.players.length <= 4 ? "normal" : props.players.length <= 8 ? "dense" : "micro");
const seatMetrics = computed(() => {
  const narrow = size.value.width <= 370;
  const landscape = size.value.width > size.value.height;
  if (density.value === "micro") return { width: narrow ? 78 : 84, height: landscape ? 46 : 56 };
  if (density.value === "dense") return { width: narrow ? 94 : 104, height: landscape ? 52 : 66 };
  return { width: narrow ? 116 : 126, height: landscape ? 60 : 78 };
});
const matchUsers = computed(() => new Set(props.matchBatch?.groups.flatMap((group) => group.userIds) ?? []));
const positions = computed(() => computeSeatLayout({
  seatIndexes: props.players.map((player) => player.seatIndex),
  selfSeatIndex: props.selfSeatIndex,
  width: size.value.width,
  height: size.value.height,
  seatWidth: seatMetrics.value.width,
  seatHeight: seatMetrics.value.height,
  shape: props.players.length > 8 ? "rounded-table" : props.players.length > 4 ? "elongated-oval" : "compact-oval",
}));
const positioned = computed(() => positions.value.map((position) => ({
  position,
  player: props.players.find((player) => player.seatIndex === position.seatIndex),
})));

onMounted(() => {
  // Seat density follows the actual stage, so tray expansion and rotation never reuse stale geometry.
  observer = new ResizeObserver(([entry]) => {
    if (entry !== undefined && entry.contentRect.width > 0 && entry.contentRect.height > 0) {
      size.value = { width: entry.contentRect.width, height: entry.contentRect.height };
    }
  });
  if (root.value !== undefined) observer.observe(root.value);
});

onBeforeUnmount(() => observer?.disconnect());
</script>

<template>
  <section
    ref="root"
    class="meet-table"
    :class="`density-${density}`"
    :style="{ '--meet-seat-width': `${seatMetrics.width}px`, '--meet-seat-height': `${seatMetrics.height}px` }"
    :aria-label="label ?? '喜相逢共同桌面'"
  >
    <div class="meet-table__rail" aria-hidden="true" />
    <div class="meet-table__center"><slot /></div>
    <div v-for="item in positioned" :key="item.position.seatIndex" class="meet-table__seat" :style="{ left: `${item.position.x}px`, top: `${item.position.y}px` }">
      <MeetPlayerSeat
        v-if="item.player"
        :player="item.player"
        :presentation="presentations.get(item.player.userId) ?? { userId: item.player.userId, displayName: `玩家 ${item.player.userId.slice(-4)}`, connected: true }"
        :target="item.player.userId === targetUserId"
        :matched="matchUsers.has(item.player.userId)"
        :density="density"
      />
    </div>
  </section>
</template>

<style scoped>
.meet-table { position: relative; width: 100%; height: 100%; min-height: 360px; overflow: hidden; isolation: isolate; }
.meet-table__rail { position: absolute; inset: 9% 7% 11%; background: repeating-linear-gradient(115deg, rgb(255 255 255 / 2%) 0 1px, transparent 1px 7px), var(--game-table); border: 2px solid color-mix(in srgb, var(--platform-accent) 42%, transparent); border-radius: 46% / 40%; box-shadow: inset 0 0 0 7px rgb(0 0 0 / 14%), inset 0 18px 36px rgb(255 255 255 / 3%), 0 22px 46px rgb(0 0 0 / 24%); }
.meet-table__center { position: absolute; inset: 27% 26% 31%; z-index: 1; display: grid; place-items: center; text-align: center; }
.meet-table__seat { position: absolute; z-index: 2; }
.density-micro .meet-table__center { inset-inline: 30%; }
@media (orientation: landscape) {
  .meet-table { min-height: 0; }
  .meet-table__rail { inset: 8% 9% 9%; border-radius: 42% / 46%; }
  .meet-table__center { inset: 22% 27% 25%; }
  .density-micro .meet-table__center { inset-inline: 31%; }
}
</style>
