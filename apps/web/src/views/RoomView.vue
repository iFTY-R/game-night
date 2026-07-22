<script setup lang="ts">
import { ArrowLeft, Check, ChevronDown, Copy, LockKeyhole, Play, UserPlus, Users, X } from "lucide-vue-next";
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import { useRoomStore } from "../stores/room";

const props = defineProps<{ roomId: string }>();
const router = useRouter();
const room = useRoomStore();
const copied = ref(false);
const entryOpen = ref(true);
const loading = ref(true);
const actionError = ref("");
const roomCode = computed(() => room.roomCode ?? props.roomId.toUpperCase().slice(0, 6));
const remoteRoom = computed(() => room.remoteRoom);
const isRemote = computed(() => remoteRoom.value !== null);
const roomStatus = computed(() => remoteRoom.value?.status ?? "ROOM_STATUS_LOBBY");
const isPlaying = computed(() => roomStatus.value.includes("PLAYING"));
const isPostGame = computed(() => roomStatus.value.includes("POST_GAME"));
const currentHost = computed(() => remoteRoom.value?.hostUserId === room.userId);
const members = computed(() => remoteRoom.value?.members ?? []);
const participantCount = computed(() => members.value.filter((member) => member.role.includes("PARTICIPANT")).length);
const displayMemberName = (userId: string): string => userId === room.userId ? room.displayName || "你" : `玩家 ${userId.slice(0, 6)}`;

if (room.roomId !== props.roomId) {
  room.enterRoom(props.roomId, roomCode.value);
}

onMounted(async () => {
  if (room.remoteRoom?.roomId === props.roomId) {
    entryOpen.value = !room.remoteRoom.participantAdmission.includes("CLOSED");
    loading.value = false;
    return;
  }
  try {
    const loaded = await room.loadRoom(props.roomId);
    if (loaded) {
      entryOpen.value = !loaded.participantAdmission.includes("CLOSED");
    }
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "房间加载失败";
  } finally {
    loading.value = false;
  }
});

const copyRoomCode = async (): Promise<void> => {
  await navigator.clipboard?.writeText(roomCode.value);
  copied.value = true;
  window.setTimeout(() => { copied.value = false; }, 1200);
};

const startGame = async (): Promise<void> => {
  actionError.value = "";
  try {
    const response = isRemote.value ? await room.startRemoteGame("liars-dice") : { sessionId: `session-${props.roomId}` };
    const sessionId = response.sessionId || `session-${props.roomId}`;
    room.setSession(sessionId);
    await router.push({ name: "game", params: { roomId: room.roomId ?? props.roomId, sessionId } });
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "开局失败";
  }
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
      <p class="eyebrow">{{ isPlaying ? "正在游戏" : isPostGame ? "上一局结束" : "吹牛骰子 · 等候区" }}</p>
      <h1 class="display-title">{{ isPlaying ? "这一局正在进行。" : isPostGame ? "要不要再开一局？" : "朋友到齐，再开骰盅。" }}</h1>
      <p class="muted">开局后新玩家会在本局结束前等候。每局结束，房主可以重新开放进房许可。</p>
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

    <section class="host-controls panel" aria-labelledby="host-title">
      <div><p class="eyebrow">房主管理</p><h2 id="host-title" class="section-title">本轮进房许可</h2></div>
      <button class="permission-toggle" type="button" :aria-pressed="entryOpen" :disabled="isPlaying || (isRemote && !currentHost)" @click="toggleAdmission">
        <span><component :is="entryOpen ? Check : X" :size="17" aria-hidden="true" /></span>
        {{ entryOpen ? (isPostGame ? "开放下一局加入" : "本局开始前允许加入") : "新玩家进入等候区" }}
      </button>
      <button class="button button--wide" type="button" :disabled="isPlaying || (isRemote && !currentHost)" @click="startGame"><Play :size="19" fill="currentColor" aria-hidden="true" /> {{ isPostGame ? "再开一局" : "开始游戏" }}</button>
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

@media (max-width: 720px) {
  .seat-list { grid-template-columns: 1fr; }
  .host-controls { grid-template-columns: 1fr; }
}
</style>
