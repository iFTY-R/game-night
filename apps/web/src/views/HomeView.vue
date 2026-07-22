<script setup lang="ts">
import { ArrowRight, Dices, DoorOpen, ShieldCheck, Spade, X } from "lucide-vue-next";
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import { useRoomStore } from "../stores/room";

const router = useRouter();
const room = useRoomStore();
const props = withDefaults(defineProps<{ inviteCode?: string }>(), { inviteCode: "" });
const displayName = ref(room.displayName);
const inviteCode = computed(() => props.inviteCode.trim().toUpperCase());
const roomCode = ref(inviteCode.value);
const error = ref("");
const ready = computed(() => room.hasIdentity);

const saveIdentity = async (): Promise<boolean> => {
  try {
    await room.ensureIdentity(displayName.value);
    displayName.value = room.displayName || displayName.value.trim();
    error.value = "";
    return true;
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : "用户名需要 1 到 18 个字符";
    return false;
  }
};

const enterRoom = async (roomId: string, code: string): Promise<void> => {
  try {
    if (!room.hasIdentity && !(await saveIdentity())) {
      return;
    }
    const joined = await room.joinRemote(code);
    const resolvedRoomId = joined?.roomId ?? roomId;
    room.enterRoom(resolvedRoomId, joined?.roomCode ?? code);
    if (joined) {
      room.setRemoteRoom(joined);
    }
    await router.push({ name: "room", params: { roomId: resolvedRoomId } });
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : "加入房间失败";
  }
};

const confirmIdentity = async (): Promise<void> => {
  if (!(await saveIdentity())) {
    return;
  }
  // Completing onboarding from an invite returns directly to that invite's room.
  if (/^[A-Z0-9]{4,8}$/.test(inviteCode.value)) {
    await enterRoom(inviteCode.value.toLowerCase(), inviteCode.value);
  }
};

const joinRoom = async (): Promise<void> => {
  const code = roomCode.value.trim().toUpperCase();
  if (!/^[A-Z0-9]{4,8}$/.test(code)) {
    error.value = "请输入 4 到 8 位房间码";
    return;
  }
  await enterRoom(code.toLowerCase(), code);
};

const createRoom = async (): Promise<void> => {
  try {
    if (!room.hasIdentity && !(await saveIdentity())) {
      return;
    }
    const created = await room.createRemoteRoom();
    const roomId = created?.roomId ?? "night-789";
    const code = created?.roomCode ?? "N789";
    room.enterRoom(roomId, code);
    if (created) {
      room.setRemoteRoom(created);
    }
    await router.push({ name: "room", params: { roomId } });
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : "创建房间失败";
  }
};

onMounted(async () => {
  try {
    await room.recoverIdentity();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : "身份恢复失败";
  }
  displayName.value = room.displayName || displayName.value;
  // A recognized device can follow an invite without an intermediate form submit.
  if (room.hasIdentity && /^[A-Z0-9]{4,8}$/.test(inviteCode.value)) {
    void enterRoom(inviteCode.value.toLowerCase(), inviteCode.value).catch((reason: unknown) => {
      error.value = reason instanceof Error ? reason.message : "加入房间失败";
    });
  }
});
</script>

<template>
  <main class="screen-shell home-screen">
    <header class="topbar">
      <RouterLink class="brand" to="/" aria-label="Game Night 首页">
        <img src="/brand-mark.svg" alt="" />
        <span>GAME NIGHT</span>
      </RouterLink>
      <span class="device-badge"><ShieldCheck :size="15" aria-hidden="true" /> 设备已识别</span>
    </header>

    <div v-if="room.notice" class="home-notice" role="status">
      <span>{{ room.notice }}</span>
      <button class="icon-button" type="button" title="关闭提示" @click="room.clearNotice"><X :size="18" aria-hidden="true" /></button>
    </div>

    <section class="home-intro" aria-labelledby="home-title">
      <p class="eyebrow">朋友在线，今晚开桌</p>
      <h1 id="home-title" class="display-title">不在同一张桌，也能一起玩。</h1>
      <p class="home-intro__copy">创建房间，发给朋友。每个人的手机都是自己的座位，公共进度始终留在桌面中央。</p>
    </section>

    <section class="join-panel panel" aria-labelledby="join-title">
      <div class="join-panel__heading">
        <div>
          <p class="eyebrow">{{ ready ? `晚上好，${room.displayName}` : "第一次来" }}</p>
          <h2 id="join-title" class="section-title">{{ ready ? "加入朋友的桌" : "先设置你的用户名" }}</h2>
        </div>
      </div>

      <form v-if="!ready" class="entry-form" @submit.prevent="confirmIdentity">
        <label for="display-name">用户名</label>
        <div class="field-row">
          <input id="display-name" v-model="displayName" autocomplete="nickname" maxlength="18" placeholder="朋友看到的名字" />
          <button class="button" type="submit">继续 <ArrowRight :size="18" aria-hidden="true" /></button>
        </div>
      </form>

      <form v-else class="entry-form" @submit.prevent="joinRoom">
        <label for="room-code">房间码</label>
        <div class="field-row">
          <input id="room-code" v-model="roomCode" inputmode="text" maxlength="8" autocapitalize="characters" placeholder="例如 N789" />
          <button class="button" type="submit"><DoorOpen :size="18" aria-hidden="true" /> 进房</button>
        </div>
        <button class="button button--quiet button--wide" type="button" @click="createRoom"><Dices :size="18" aria-hidden="true" /> 创建新房间</button>
      </form>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </section>

    <section class="game-shelf" aria-labelledby="games-title">
      <div class="game-shelf__heading">
        <div>
          <p class="eyebrow">游戏桌</p>
          <h2 id="games-title" class="section-title">今晚玩什么</h2>
        </div>
        <span class="muted">3 款</span>
      </div>
      <div class="game-list">
        <article class="game-card is-featured">
          <Dices :size="28" aria-hidden="true" />
          <div><strong>摇骰子</strong><span>多人轮流 · 快速开局</span></div>
          <span class="game-card__mark">推荐</span>
        </article>
        <article class="game-card">
          <span class="game-card__number" aria-hidden="true">3</span>
          <div><strong>三关定胜负</strong><span>三局节奏 · 连续对决</span></div>
        </article>
        <article class="game-card">
          <Spade :size="27" aria-hidden="true" />
          <div><strong>德州扑克</strong><span>完整牌桌 · 私密手牌</span></div>
        </article>
      </div>
    </section>
  </main>
</template>

<style scoped>
.home-screen { display: grid; align-content: start; gap: 30px; }
.device-badge { display: inline-flex; align-items: center; gap: 6px; color: #99d8b1; font-size: 12px; }
.home-intro { padding: clamp(26px, 8vh, 72px) 0 4px; }
.home-notice { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 14px; color: var(--platform-ink); background: rgb(255 138 126 / 10%); border: 1px solid rgb(255 138 126 / 38%); border-radius: 7px; font-size: 13px; }
.home-intro__copy { max-width: 620px; margin: 20px 0 0; color: var(--platform-muted); font-size: 16px; line-height: 1.7; }
.join-panel { display: grid; gap: 20px; padding: 20px; border-left: 3px solid var(--platform-accent); }
.entry-form { display: grid; gap: 10px; }
.entry-form label { color: var(--platform-muted); font-size: 12px; font-weight: 700; }
.field-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px; }
.field-row input { min-width: 0; min-height: 50px; padding: 0 14px; color: var(--platform-ink); background: rgb(8 18 19 / 48%); border: 1px solid rgb(168 181 180 / 28%); border-radius: 7px; }
.form-error { margin: -6px 0 0; color: var(--platform-danger); font-size: 13px; }
.game-shelf { display: grid; gap: 14px; padding-bottom: 24px; }
.game-shelf__heading { display: flex; align-items: end; justify-content: space-between; }
.game-list { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
.game-card { position: relative; min-height: 106px; display: flex; align-items: center; gap: 13px; padding: 16px; background: rgb(27 41 45 / 66%); border: 1px solid rgb(168 181 180 / 16%); border-radius: 8px; }
.game-card.is-featured { border-color: rgb(230 181 102 / 46%); }
.game-card svg,
.game-card__number { flex: 0 0 auto; color: var(--platform-accent); }
.game-card__number { width: 28px; font-family: Georgia, serif; font-size: 34px; font-weight: 800; text-align: center; }
.game-card div { min-width: 0; display: grid; gap: 5px; }
.game-card strong { font-size: 15px; }
.game-card span { color: var(--platform-muted); font-size: 11px; }
.game-card__mark { position: absolute; top: 8px; right: 8px; color: var(--platform-accent) !important; }

@media (max-width: 700px) {
  .game-list { grid-template-columns: 1fr; }
  .game-card { min-height: 76px; }
}

@media (max-width: 390px) {
  .field-row { grid-template-columns: 1fr; }
  .field-row .button { width: 100%; }
  .device-badge span { display: none; }
}
</style>
