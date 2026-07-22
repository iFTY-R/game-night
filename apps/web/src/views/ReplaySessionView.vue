<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import {
  Dice789ReplayTable,
  decodeDice789Replay,
  type Dice789Replay,
} from "@game-night/dice-789-client";
import { dice789Themes } from "@game-night/dice-789-themes";
import {
  LiarsDiceReplayTable,
  decodeLiarsDiceReplay,
  type LiarsDiceReplay,
} from "@game-night/liars-dice-client";
import { liarsDiceThemes } from "@game-night/liars-dice-themes";
import {
  MeetByChanceReplayTable,
  decodeMeetByChanceReplay,
  type MeetByChanceReplay,
} from "@game-night/meet-by-chance-client";
import { meetByChanceThemes } from "@game-night/meet-by-chance-themes";
import { ThemeRuntime, safeTheme } from "@game-night/theme-system";

import { ApiError, gameClient } from "../api/client";
import { gameProjectionFromConnect } from "../api/game-projection";
import { isGameId, type GameId } from "../game-catalog";
import { useRoomStore } from "../stores/room";

const props = defineProps<{ roomId: string; sessionId: string }>();
const router = useRouter();
const room = useRoomStore();
const themeRuntime = new ThemeRuntime();
const loading = ref(true);
const loadError = ref("");
const gameId = ref<GameId | null>(null);
const liarsReplay = ref<LiarsDiceReplay | null>(null);
const dice789Replay = ref<Dice789Replay | null>(null);
const meetReplay = ref<MeetByChanceReplay | null>(null);
const muted = ref(false);
const themeIndex = ref(0);
// Cancelling the immutable request prevents a departed replay page from applying stale theme or payload state.
const requestController = new AbortController();

interface ReplaySeat {
  readonly userId: string;
  readonly seatIndex: number;
}

const legacyLiarsSeats = (replay: LiarsDiceReplay): ReplaySeat[] => {
  const users = new Map<string, number>();
  const remember = (userId: string): void => {
    if (userId && !users.has(userId)) users.set(userId, users.size);
  };
  for (const round of replay.rounds) {
    remember(round.firstActorUserId);
    for (const bid of round.bids) remember(bid.userId);
    for (const roll of round.dice) remember(roll.userId);
    remember(round.loserUserId);
    remember(round.openerUserId);
  }
  for (const userId of replay.revokedUserIds) remember(userId);
  return [...users].map(([userId, seatIndex]) => ({ userId, seatIndex }));
};

const replaySeats = computed<readonly ReplaySeat[]>(() => {
  if (liarsReplay.value) {
    return liarsReplay.value.players.length > 0 ? liarsReplay.value.players : legacyLiarsSeats(liarsReplay.value);
  }
  if (dice789Replay.value) return dice789Replay.value.players;
  if (meetReplay.value) return meetReplay.value.players;
  return [];
});

const replayContext = computed(() => ({
  roomCode: room.remoteRoom?.roomCode ?? room.roomCode ?? props.roomId.slice(0, 6).toUpperCase(),
  players: replaySeats.value.map((player) => {
    const ownSeat = player.userId === room.userId;
    const displayName = ownSeat && room.displayName ? room.displayName : `玩家 ${player.userId.slice(-4)}`;
    return {
      userId: player.userId,
      seatIndex: player.seatIndex,
      displayName,
      avatarText: ownSeat && room.displayName ? room.displayName.slice(0, 1) : player.userId.slice(-2).toUpperCase(),
      connected: true,
      host: player.userId === room.remoteRoom?.hostUserId,
    };
  }),
}));

const themes = () => {
  if (gameId.value === "liars-dice") return liarsDiceThemes;
  if (gameId.value === "dice-789") return dice789Themes;
  if (gameId.value === "meet-by-chance") return meetByChanceThemes;
  return [];
};

const applyTheme = (): void => {
  const available = themes();
  const manifest = available[themeIndex.value] ?? safeTheme;
  themeRuntime.apply({ manifest, assets: new Map(), usedFallback: available.length === 0, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = String(available.length === 0);
  document.documentElement.dataset.muted = String(muted.value);
};

const cycleTheme = (): void => {
  const available = themes();
  if (available.length === 0) return;
  themeIndex.value = (themeIndex.value + 1) % available.length;
  applyTheme();
};

const toggleSound = (): void => {
  muted.value = !muted.value;
  document.documentElement.dataset.muted = String(muted.value);
};

/** Fetches the ACL-authorized projection and dispatches it to the exact versioned game decoder. */
const loadReplay = async (): Promise<void> => {
  // Current room membership is optional presentation context; replay ACL is the sole access decision.
  void room.loadRoom(props.roomId).catch(() => null);
  try {
    const response = await gameClient.getReplayProjection(props.roomId, props.sessionId, 0, requestController.signal);
    if (requestController.signal.aborted) return;
    const session = response.session;
    if (!response.complete || session?.sessionId !== props.sessionId || session.roomId !== props.roomId || !isGameId(session.gameId)) {
      throw new Error("复盘会话信息不完整");
    }
    const projection = gameProjectionFromConnect(response.projection);
    if (projection.sessionId !== props.sessionId || projection.view.gameId !== session.gameId || projection.viewerRole !== "replay" || projection.allowedActions.length !== 0) {
      throw new Error("复盘投影与会话不匹配");
    }
    gameId.value = session.gameId;
    if (session.gameId === "liars-dice") liarsReplay.value = decodeLiarsDiceReplay(projection);
    if (session.gameId === "dice-789") dice789Replay.value = decodeDice789Replay(projection);
    if (session.gameId === "meet-by-chance") meetReplay.value = decodeMeetByChanceReplay(projection);
    themeIndex.value = 0;
    applyTheme();
  } catch (error) {
    if (requestController.signal.aborted) return;
    if (error instanceof ApiError && error.status === 403) loadError.value = "你没有查看这局复盘的权限";
    else loadError.value = error instanceof Error ? error.message : "复盘加载失败";
  } finally {
    if (!requestController.signal.aborted) loading.value = false;
  }
};

const leave = async (): Promise<void> => {
  await router.push({ name: "room", params: { roomId: props.roomId } });
};

onMounted(() => { void loadReplay(); });
onBeforeUnmount(() => {
  requestController.abort();
  themeRuntime.apply({ manifest: safeTheme, assets: new Map(), usedFallback: true, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "true";
});
</script>

<template>
  <LiarsDiceReplayTable
    v-if="liarsReplay"
    :replay="liarsReplay"
    :context="replayContext"
    :muted="muted"
    @leave="leave"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
  <Dice789ReplayTable
    v-else-if="dice789Replay"
    :replay="dice789Replay"
    :context="replayContext"
    :muted="muted"
    @leave="leave"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
  <MeetByChanceReplayTable
    v-else-if="meetReplay"
    :replay="meetReplay"
    :context="replayContext"
    :muted="muted"
    @leave="leave"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
  <main v-else class="screen-shell replay-gate">
    <p class="eyebrow">{{ loading ? "正在读取复盘" : "无法打开复盘" }}</p>
    <h1 class="display-title">{{ loading ? "正在还原这一局。" : "这局复盘暂时不可用。" }}</h1>
    <p class="muted" role="status">{{ loading ? "只会加载规则允许公开的结算信息。" : loadError }}</p>
    <button v-if="!loading" class="button button--quiet" type="button" @click="leave">返回房间</button>
  </main>
</template>

<style scoped>
.replay-gate { display: grid; min-height: 100dvh; align-content: center; justify-items: start; gap: 14px; }
.replay-gate .display-title { max-width: 720px; }
</style>
