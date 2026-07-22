<script setup lang="ts">
import { ArrowLeft, Check, ChevronDown, Copy, History, LockKeyhole, Play, UserPlus, Users, X } from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import { useRoomStore } from "../stores/room";
import { gameById, gameCatalog, isGameId, type GameId } from "../game-catalog";

const props = defineProps<{ roomId: string }>();
const router = useRouter();
const room = useRoomStore();
const copied = ref(false);
const entryOpen = ref(true);
const loading = ref(true);
const actionError = ref("");
const selectedGameId = ref<GameId>("liars-dice");
let refreshTimer: number | undefined;
let refreshPending = false;
let gameSelectionInitialized = false;
const roomCode = computed(() => room.roomCode ?? props.roomId.toUpperCase().slice(0, 6));
const remoteRoom = computed(() => room.remoteRoom);
const isRemote = computed(() => remoteRoom.value !== null);
const roomStatus = computed(() => remoteRoom.value?.status ?? "ROOM_STATUS_LOBBY");
const isPlaying = computed(() => roomStatus.value.includes("PLAYING"));
const isPostGame = computed(() => roomStatus.value.includes("POST_GAME"));
const currentHost = computed(() => remoteRoom.value?.hostUserId === room.userId);
const members = computed(() => remoteRoom.value?.members ?? []);
const participantCount = computed(() => members.value.filter((member) => member.role.includes("PARTICIPANT")).length);
const currentMember = computed(() => members.value.find((member) => member.userId === room.userId));
const canEnterActiveGame = computed(() => currentMember.value?.role.includes("PARTICIPANT") || currentMember.value?.role.includes("SPECTATOR"));
const selectedGame = computed(() => gameById(selectedGameId.value) ?? gameCatalog[0]);
const activeGame = computed(() => gameById(remoteRoom.value?.activeGameId ?? ""));
const enoughPlayers = computed(() => participantCount.value >= selectedGame.value.minimumPlayers);
const displayMemberName = (userId: string): string => userId === room.userId ? room.displayName || "你" : `玩家 ${userId.slice(0, 6)}`;

/** Seeds the next game from room history once without overwriting a host choice during polling. */
const initializeGameSelection = (snapshot: NonNullable<typeof room.remoteRoom>): void => {
  if (gameSelectionInitialized) return;
  const rememberedGame = snapshot.status.includes("POST_GAME") ? snapshot.lastFinishedGameId : snapshot.activeGameId;
  if (isGameId(rememberedGame)) selectedGameId.value = rememberedGame;
  gameSelectionInitialized = true;
};

const selectGame = (gameId: GameId): void => {
  selectedGameId.value = gameId;
  gameSelectionInitialized = true;
};

if (room.roomId !== props.roomId) {
  room.enterRoom(props.roomId, roomCode.value);
}

/** Refreshes lobby state so remote starts and admission changes appear without reloading. */
const refreshRoom = async (): Promise<void> => {
  if (refreshPending || document.visibilityState === "hidden") return;
  refreshPending = true;
  try {
    const loaded = await room.loadRoom(props.roomId);
    if (loaded) {
      entryOpen.value = !loaded.participantAdmission.includes("CLOSED");
      initializeGameSelection(loaded);
      if (loaded.status.includes("PLAYING") && loaded.activeSessionId && canEnterActiveGame.value) void enterActiveGame();
    }
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "房间加载失败";
  } finally {
    refreshPending = false;
  }
};

onMounted(async () => {
  if (room.remoteRoom?.roomId === props.roomId) {
    entryOpen.value = !room.remoteRoom.participantAdmission.includes("CLOSED");
    initializeGameSelection(room.remoteRoom);
    if (room.remoteRoom.status.includes("PLAYING") && room.remoteRoom.activeSessionId && canEnterActiveGame.value) void enterActiveGame();
  } else {
    await refreshRoom();
  }
  loading.value = false;
  refreshTimer = window.setInterval(() => { void refreshRoom(); }, 2_500);
});

onBeforeUnmount(() => {
  if (refreshTimer !== undefined) window.clearInterval(refreshTimer);
});

const copyRoomCode = async (): Promise<void> => {
  await navigator.clipboard?.writeText(roomCode.value);
  copied.value = true;
  window.setTimeout(() => { copied.value = false; }, 1200);
};

const startGame = async (): Promise<void> => {
  actionError.value = "";
  try {
    const response = isRemote.value ? await room.startRemoteGame(selectedGameId.value) : { sessionId: `session-${props.roomId}` };
    const sessionId = response.sessionId || `session-${props.roomId}`;
    room.setSession(sessionId);
    await router.push({ name: "game", params: { roomId: room.roomId ?? props.roomId, sessionId } });
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "开局失败";
  }
};

/** Re-enters the current authoritative session without creating another game. */
const enterActiveGame = async (): Promise<void> => {
  const sessionId = remoteRoom.value?.activeSessionId;
  if (!sessionId || !canEnterActiveGame.value) return;
  room.setSession(sessionId);
  await router.push({ name: "game", params: { roomId: props.roomId, sessionId } });
};

/** Opens the immutable last-session projection; authorization remains enforced by the replay API. */
const openLastReplay = async (): Promise<void> => {
  const sessionId = remoteRoom.value?.lastFinishedSessionId;
  if (!sessionId) return;
  await router.push({ name: "replay", params: { roomId: props.roomId, sessionId } });
};

const toggleAdmission = async (): Promise<void> => {
  const nextOpen = !entryOpen.value;
  entryOpen.value = nextOpen;
  if (!isRemote.value) {
    return;
  }
  try {
    await room.setAdmissionRemote(nextOpen ? "ADMISSION_MODE_OPEN" : "ADMISSION_MODE_CLOSED", "ADMISSION_MODE_OPEN");
  } catch (error) {
    entryOpen.value = !nextOpen;
    actionError.value = error instanceof Error ? error.message : "更新进房许可失败";
  }
};

const approveMember = async (userId: string): Promise<void> => {
  try {
    await room.approveRemoteMember(userId);
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "候场晋升失败";
  }
};

const leave = async (): Promise<void> => {
  room.leaveRoom();
  await router.push({ name: "home" });
};
</script>

<template>
  <main class="screen-shell room-screen">
    <header class="topbar">
      <button class="icon-button" type="button" title="离开房间" @click="leave"><ArrowLeft :size="21" aria-hidden="true" /></button>
      <div class="room-code">
        <span>房间码</span>
        <strong>{{ roomCode }}</strong>
        <button type="button" :title="copied ? '已复制' : '复制房间码'" @click="copyRoomCode">
          <Check v-if="copied" :size="17" aria-hidden="true" />
          <Copy v-else :size="17" aria-hidden="true" />
        </button>
      </div>
      <span class="room-count"><Users :size="16" aria-hidden="true" /> {{ isRemote ? `${participantCount} / ${remoteRoom?.participantCapacity ?? 0}` : "4 / 8" }}</span>
    </header>

    <section class="room-hero">
      <p class="eyebrow">{{ isPlaying ? (activeGame?.name ?? "正在游戏") : isPostGame ? "上一局结束" : selectedGame.name + " · 等候区" }}</p>
      <h1 class="display-title">{{ isPlaying ? "这一局正在进行。" : isPostGame ? "要不要再开一局？" : "朋友到齐，再开骰盅。" }}</h1>
      <p class="muted">开局后新玩家会在本局结束前等候。每局结束，房主可以重新开放进房许可。</p>
      <button v-if="isPlaying && canEnterActiveGame" class="button room-hero__enter" type="button" @click="enterActiveGame">
        <Play :size="18" fill="currentColor" aria-hidden="true" /> 进入{{ activeGame?.name ?? "当前游戏" }}
      </button>
      <button v-if="isPostGame && remoteRoom?.lastFinishedSessionId" class="button button--quiet room-hero__enter" type="button" @click="openLastReplay">
        <History :size="18" aria-hidden="true" /> 查看上一局复盘
      </button>
      <p v-if="loading" class="loading-note" role="status">正在同步房间状态…</p>
      <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
    </section>

    <section class="lobby-board" aria-labelledby="players-title">
      <div class="lobby-board__head">
        <div><p class="eyebrow">座位</p><h2 id="players-title" class="section-title">桌边的人</h2></div>
        <span class="entry-status" :class="{ 'is-closed': !entryOpen }">
          <component :is="entryOpen ? Users : LockKeyhole" :size="15" aria-hidden="true" />
          {{ entryOpen ? "允许进房" : "暂停进房" }}
        </span>
      </div>
      <div v-if="isRemote" class="seat-list">
        <article v-for="member in members" :key="member.userId" class="lobby-seat" :class="{ 'is-host': member.userId === remoteRoom?.hostUserId }">
          <span>{{ displayMemberName(member.userId).slice(0, 1) }}</span>
          <div><strong>{{ displayMemberName(member.userId) }}</strong><small>{{ member.role.includes("WAITING") ? "候场中" : member.userId === remoteRoom?.hostUserId ? "房主 · 已入座" : member.role.includes("SPECTATOR") ? "观战" : "已入座" }}</small></div>
          <Check v-if="!member.role.includes('WAITING')" :size="18" aria-label="已入座" />
          <button v-else-if="currentHost" class="mini-action" type="button" :title="`晋升 ${displayMemberName(member.userId)}`" @click="approveMember(member.userId)"><UserPlus :size="17" aria-hidden="true" /></button>
          <ChevronDown v-else :size="18" aria-label="候场中" />
        </article>
      </div>
      <div v-else class="seat-list">
        <article class="lobby-seat is-host"><span>满</span><div><strong>小满</strong><small>房主 · 已准备</small></div><Check :size="18" aria-label="已准备" /></article>
        <article class="lobby-seat"><span>青</span><div><strong>阿青</strong><small>已准备</small></div><Check :size="18" aria-label="已准备" /></article>
        <article class="lobby-seat"><span>南</span><div><strong>南风</strong><small>选游戏中</small></div><span class="pulse" aria-label="等待中" /></article>
        <article class="lobby-seat"><span>{{ room.displayName.slice(0, 1) || "你" }}</span><div><strong>{{ room.displayName || "你" }}</strong><small>本机</small></div><Check :size="18" aria-label="已准备" /></article>
      </div>
    </section>

    <section v-if="!isPlaying" class="game-picker" aria-labelledby="game-picker-title">
      <div class="game-picker__head">
        <div><p class="eyebrow">本局玩法</p><h2 id="game-picker-title" class="section-title">选一款上桌</h2></div>
        <span>{{ currentHost ? "由你开局" : "房主正在选择" }}</span>
      </div>
      <div class="game-options">
        <button
          v-for="game in gameCatalog"
          :key="game.gameId"
          class="game-option"
          :class="{ 'is-selected': selectedGameId === game.gameId }"
          type="button"
          :aria-pressed="selectedGameId === game.gameId"
          :disabled="isRemote && !currentHost"
          @click="selectGame(game.gameId)"
        >
          <span>{{ game.accent }}</span>
          <strong>{{ game.name }}</strong>
          <small>{{ game.summary }}</small>
          <em>至少 {{ game.minimumPlayers }} 人</em>
        </button>
      </div>
    </section>

    <section class="host-controls panel" aria-labelledby="host-title">
      <div><p class="eyebrow">房主管理</p><h2 id="host-title" class="section-title">本轮进房许可</h2></div>
      <button class="permission-toggle" type="button" :aria-pressed="entryOpen" :disabled="isPlaying || (isRemote && !currentHost)" @click="toggleAdmission">
        <span><component :is="entryOpen ? Check : X" :size="17" aria-hidden="true" /></span>
        {{ entryOpen ? (isPostGame ? "开放下一局加入" : "本局开始前允许加入") : "新玩家进入等候区" }}
      </button>
      <button class="button button--wide" type="button" :disabled="isPlaying || !enoughPlayers || (isRemote && !currentHost)" @click="startGame"><Play :size="19" fill="currentColor" aria-hidden="true" /> {{ enoughPlayers ? (isPostGame ? "再开一局" : "开始" + selectedGame.name) : "还需 " + (selectedGame.minimumPlayers - participantCount) + " 人" }}</button>
    </section>
  </main>
</template>

<style scoped>
.room-screen { display: grid; align-content: start; gap: 28px; }
.room-code { display: inline-flex; align-items: center; gap: 8px; }
.room-code > span { color: var(--platform-muted); font-size: 11px; }
.room-code strong { color: var(--platform-accent); font-size: 19px; letter-spacing: .08em; }
.room-code button { width: 34px; height: 34px; display: grid; place-items: center; color: var(--platform-muted); background: transparent; border: 0; }
.room-count { display: inline-flex; align-items: center; gap: 5px; color: var(--platform-muted); font-size: 12px; }
.room-hero { padding: clamp(24px, 7vh, 58px) 0 0; }
.room-hero .display-title { max-width: 760px; }
.room-hero > .muted { max-width: 620px; line-height: 1.65; }
.room-hero__enter { width: fit-content; margin-top: 10px; }
.loading-note { color: var(--platform-muted); font-size: 13px; }
.form-error { margin: 0; color: var(--platform-danger); font-size: 13px; }
.lobby-board { display: grid; gap: 15px; }
.lobby-board__head { display: flex; align-items: end; justify-content: space-between; gap: 12px; }
.entry-status { display: inline-flex; align-items: center; gap: 6px; color: #99d8b1; font-size: 12px; }
.entry-status.is-closed { color: var(--platform-accent); }
.seat-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
.lobby-seat { min-height: 68px; display: grid; grid-template-columns: 40px minmax(0, 1fr) 20px; align-items: center; gap: 10px; padding: 10px 12px; background: rgb(27 41 45 / 72%); border: 1px solid rgb(168 181 180 / 17%); border-radius: 8px; }
.lobby-seat.is-host { border-color: rgb(230 181 102 / 40%); }
.lobby-seat > span:first-child { width: 40px; height: 40px; display: grid; place-items: center; color: #171b1a; background: var(--platform-accent); border-radius: 50%; font-weight: 800; }
.lobby-seat div { min-width: 0; display: grid; gap: 3px; }
.lobby-seat strong,
.lobby-seat small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lobby-seat small { color: var(--platform-muted); font-size: 11px; }
.lobby-seat > svg { color: #99d8b1; }
.pulse { width: 9px !important; height: 9px !important; background: var(--platform-accent) !important; box-shadow: 0 0 0 5px rgb(230 181 102 / 12%); }
.host-controls { display: grid; grid-template-columns: minmax(150px, 1fr) minmax(210px, auto) minmax(170px, auto); align-items: center; gap: 18px; padding: 18px; }
.permission-toggle { min-height: 48px; display: inline-flex; align-items: center; gap: 9px; padding: 0 12px; color: var(--platform-ink); background: rgb(8 18 19 / 35%); border: 1px solid rgb(168 181 180 / 22%); border-radius: 7px; }
.permission-toggle > span { width: 26px; height: 26px; display: grid; place-items: center; color: #13201d; background: #99d8b1; border-radius: 5px; }
.permission-toggle:disabled { cursor: not-allowed; opacity: .55; }
.mini-action { width: 34px; height: 34px; display: grid; place-items: center; color: var(--platform-accent); background: transparent; border: 1px solid rgb(230 181 102 / 42%); border-radius: 6px; }
.game-picker { display: grid; gap: 14px; }
.game-picker__head { display: flex; align-items: end; justify-content: space-between; gap: 12px; }
.game-picker__head > span { color: var(--platform-muted); font-size: 12px; }
.game-options { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
.game-option { min-height: 160px; display: grid; grid-template-columns: 48px 1fr; align-content: start; gap: 7px 12px; padding: 16px; color: var(--platform-ink); text-align: left; background: rgb(16 30 32 / 72%); border: 1px solid rgb(168 181 180 / 18%); border-radius: 10px; }
.game-option > span { grid-row: span 3; width: 48px; height: 48px; display: grid; place-items: center; color: #151b1a; background: var(--platform-accent); border-radius: 50%; font-family: var(--font-display); font-weight: 900; }
.game-option strong { align-self: center; font-size: 17px; }
.game-option small { grid-column: 2; min-height: 40px; color: var(--platform-muted); line-height: 1.5; }
.game-option em { grid-column: 2; color: var(--platform-accent); font-size: 11px; font-style: normal; }
.game-option.is-selected { background: rgb(48 52 42 / 86%); border-color: var(--platform-accent); box-shadow: inset 0 0 0 1px rgb(230 181 102 / 25%); }
.game-option:disabled { cursor: default; opacity: .72; }

@media (max-width: 720px) {
  .seat-list { grid-template-columns: 1fr; }
  .game-options { grid-template-columns: 1fr; }
  .game-option { min-height: 128px; }
  .host-controls { grid-template-columns: 1fr; }
}
</style>
