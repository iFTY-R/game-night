<script setup lang="ts">
import { ArrowRightLeft } from "lucide-vue-next";
import { computed, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";

import { DangerConfirm } from "@game-night/game-ui-kit";

import Dice789View from "./Dice789View.vue";
import GameView from "./GameView.vue";
import MeetByChanceView from "./MeetByChanceView.vue";
import ThreeRoundsView from "./ThreeRoundsView.vue";
import GameGovernanceControls from "../components/game/GameGovernanceControls.vue";
import RoomPauseOverlay from "../components/game/RoomPauseOverlay.vue";
import type { LiveSessionLifecycle } from "../composables/use-live-game-table";
import { gameById, isGameId } from "../game-catalog";
import { memberDisplayName } from "../member-display";
import { useRoomStore } from "../stores/room";

const props = defineProps<{ roomId: string; sessionId: string }>();
const router = useRouter();
const room = useRoomStore();
const loading = ref(true);
const loadError = ref("");
const transferTarget = ref<{ userId: string; displayName: string } | null>(null);
const transferSaving = ref(false);
const transferError = ref("");
const liveLifecycle = ref<LiveSessionLifecycle | null>(null);
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
  if (gameId.value === "three-rounds") return ThreeRoundsView;
  return null;
});
const activePause = computed(() => {
  const pause = room.remoteRoom?.activePause;
  return pause?.sessionId === props.sessionId ? pause : null;
});
const sessionPaused = computed(() => liveLifecycle.value?.known ? liveLifecycle.value.paused : activePause.value !== null);
const effectivePausedAt = computed(() => sessionPaused.value
  ? liveLifecycle.value?.suspendedAt ?? activePause.value?.pausedAt
  : undefined);
const pauseActorName = computed(() => {
  const userId = activePause.value?.pausedByUserId;
  if (!userId) return "房主";
  const member = room.remoteRoom?.members.find((candidate) => candidate.userId === userId);
  return memberDisplayName(userId, member?.username);
});
const pauseRequesterName = computed(() => {
  const userId = activePause.value?.requestedByUserId;
  if (!userId) return undefined;
  const member = room.remoteRoom?.members.find((candidate) => candidate.userId === userId);
  return memberDisplayName(userId, member?.username);
});

/** Limits transfer targets to other current participants while the viewer still owns the room. */
const canTransferHost = (userId: string): boolean => {
  const snapshot = room.remoteRoom;
  if (!snapshot || snapshot.hostUserId !== room.userId || userId === room.userId) return false;
  return snapshot.members.some((member) => member.userId === userId && member.role.includes("PARTICIPANT"));
};

const openTransferHost = (userId: string, displayName: string): void => {
  if (!canTransferHost(userId)) return;
  transferError.value = "";
  transferTarget.value = { userId, displayName };
};

/** Rechecks authority through the server command; a stale host keeps the dialog open with the canonical error. */
const confirmTransferHost = async (): Promise<void> => {
  const target = transferTarget.value;
  if (!target || transferSaving.value) return;
  transferSaving.value = true;
  transferError.value = "";
  try {
    await room.transferRemoteHost(target.userId);
    transferTarget.value = null;
  } catch (error) {
    transferError.value = error instanceof Error ? error.message : "房主转移失败";
  } finally {
    transferSaving.value = false;
  }
};

/** Accepts the latest room-or-session lifecycle observation emitted by any versioned game client. */
const handleLifecycleChange = (state: LiveSessionLifecycle): void => {
  liveLifecycle.value = state;
};

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
watch(() => props.sessionId, () => { liveLifecycle.value = null; });

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
  <main v-if="!loading && gameComponent" class="game-session-shell" :class="{ 'is-paused': sessionPaused }">
    <component
      :is="gameComponent"
      :room-id="roomId"
      :session-id="sessionId"
      :paused-at="effectivePausedAt"
      @lifecycle-change="handleLifecycleChange"
    >
      <template #governance>
        <GameGovernanceControls :session-id="sessionId" :ownership-epoch="liveLifecycle?.ownershipEpoch ?? null" />
      </template>
      <template #seat-details="{ userId, displayName }">
        <button
          v-if="canTransferHost(userId)"
          class="transfer-host-action"
          type="button"
          :title="`把房主转移给 ${displayName}`"
          @click.stop="openTransferHost(userId, displayName)"
        >
          <ArrowRightLeft :size="14" aria-hidden="true" />
          <span>转移房主</span>
        </button>
      </template>
    </component>

    <RoomPauseOverlay
      v-if="sessionPaused"
      :paused-by-name="pauseActorName"
      :requested-by-name="pauseRequesterName"
    />

    <DangerConfirm
      :open="transferTarget !== null"
      :title="`把房主转移给 ${transferTarget?.displayName ?? '这位玩家'}？`"
      confirm-label="确认转移"
      @confirm="confirmTransferHost"
      @cancel="transferTarget = null; transferError = ''"
    >
      转移后，对方会立即获得暂停、恢复、审批和房间管理权限。
      <p v-if="transferError" class="transfer-host-error" role="alert">{{ transferError }}</p>
    </DangerConfirm>
  </main>
  <main v-else class="screen-shell session-gate">
    <p class="eyebrow">{{ loading ? "正在连接游戏" : "无法进入游戏" }}</p>
    <h1 class="display-title">{{ loading ? "正在确认这一桌的玩法。" : "这局暂时打不开。" }}</h1>
    <p class="muted">{{ loading ? "会根据房间保存的游戏类型加载对应客户端。" : loadError }}</p>
    <p v-if="!loading && gameById(gameId)" class="muted">会话游戏：{{ gameById(gameId)?.name }}</p>
    <button v-if="!loading" class="button" type="button" @click="returnToRoom">返回房间</button>
  </main>
</template>

<style scoped>
.game-session-shell { position: relative; min-height: 100dvh; overflow: hidden; }
.game-session-shell.is-paused :deep(*) { animation-play-state: paused !important; }
.transfer-host-action { width: 100%; min-height: 40px; display: flex; align-items: center; justify-content: center; gap: 6px; margin-top: 7px; padding: 0 9px; color: #10201c; background: var(--platform-focus); border: 0; border-radius: 6px; font: inherit; font-size: 11px; font-weight: 800; }
.transfer-host-action:focus-visible { outline: 2px solid var(--platform-focus); outline-offset: 2px; }
.transfer-host-error { margin: 10px 0 0; color: var(--platform-danger); font-size: 12px; }
.session-gate { min-height: 100dvh; display: grid; align-content: center; justify-items: start; gap: 14px; }
.session-gate .display-title { max-width: 720px; }
</style>
