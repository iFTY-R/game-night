<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";

import Dice789View from "./Dice789View.vue";
import GameView from "./GameView.vue";
import MeetByChanceView from "./MeetByChanceView.vue";
import { gameById, isGameId } from "../game-catalog";
import { useRoomStore } from "../stores/room";

const props = defineProps<{ roomId: string; sessionId: string }>();
const router = useRouter();
const room = useRoomStore();
const loading = ref(true);
const loadError = ref("");
let terminalNavigationStarted = false;

const gameId = computed(() => {
  const snapshot = room.remoteRoom;
  if (snapshot?.activeSessionId === props.sessionId) return snapshot.activeGameId;
  if (snapshot?.lastFinishedSessionId === props.sessionId) return snapshot.lastFinishedGameId;
  return "";
});
const gameComponent = computed(() => {
  if (gameId.value === "liars-dice") return GameView;
  if (gameId.value === "dice-789") return Dice789View;
  if (gameId.value === "meet-by-chance") return MeetByChanceView;
  return null;
});

/** Owns terminal navigation above the game component, which unmounts as soon as active game metadata is cleared. */
const exitClosedRoom = async (): Promise<void> => {
  if (terminalNavigationStarted) return;
  terminalNavigationStarted = true;
  room.exitRoom("房主已解散房间，当前游戏已结束");
  await router.replace({ name: "home" });
};

watch(
  () => room.remoteRoom,
  (snapshot) => {
    if (snapshot?.roomId === props.roomId && snapshot.status.includes("CLOSED")) void exitClosedRoom();
  },
);

/** Loads authoritative room metadata before selecting a versioned game client. */
onMounted(async () => {
  try {
    const snapshot = await room.loadRoom(props.roomId);
    if (snapshot?.status.includes("CLOSED")) {
      await exitClosedRoom();
      return;
    }
    if (!isGameId(gameId.value)) loadError.value = "这个会话没有可用的游戏客户端";
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : "游戏会话加载失败";
  } finally {
    loading.value = false;
  }
});

const returnToRoom = async (): Promise<void> => {
  await router.push({ name: "room", params: { roomId: props.roomId } });
};
</script>

<template>
  <component
    :is="gameComponent"
    v-if="!loading && gameComponent"
    :room-id="roomId"
    :session-id="sessionId"
  />
  <main v-else class="screen-shell session-gate">
    <p class="eyebrow">{{ loading ? "正在连接游戏" : "无法进入游戏" }}</p>
    <h1 class="display-title">{{ loading ? "正在确认这一桌的玩法。" : "这局暂时打不开。" }}</h1>
    <p class="muted">{{ loading ? "会根据房间保存的游戏类型加载对应客户端。" : loadError }}</p>
    <p v-if="!loading && gameById(gameId)" class="muted">会话游戏：{{ gameById(gameId)?.name }}</p>
    <button v-if="!loading" class="button" type="button" @click="returnToRoom">返回房间</button>
  </main>
</template>

<style scoped>
.session-gate { min-height: 100dvh; display: grid; align-content: center; justify-items: start; gap: 14px; }
.session-gate .display-title { max-width: 720px; }
</style>
