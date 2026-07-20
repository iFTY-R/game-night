<script setup lang="ts">
import { ArrowLeft, CircleHelp, Dices, EyeOff, Hand, MoreHorizontal, Volume2 } from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import { ActionTray, ConnectionBadge, DangerConfirm, GameTable, PrivateZone } from "@game-night/game-ui-kit";
import type { TableSeat, TrayState } from "@game-night/game-ui-kit";
import { safeTheme } from "@game-night/theme-system";

import { useRoomStore } from "../stores/room";

const props = defineProps<{ roomId: string; sessionId: string }>();
const router = useRouter();
const room = useRoomStore();
const trayState = ref<TrayState>("compact");
const pendingAction = ref<string | null>(null);
const lastAction = ref("等待你选择操作");
const dangerOpen = ref(false);
let pendingTimer: number | undefined;

const seats = computed<readonly TableSeat[]>(() => [
  { seatIndex: 0, userId: "user-qing", displayName: "阿青", avatarText: "青", connected: true, status: "5 颗骰子" },
  { seatIndex: 1, userId: "user-man", displayName: "小满", avatarText: "满", connected: true, active: true, host: true, status: "正在行动" },
  { seatIndex: 2, userId: "user-nan", displayName: "南风", avatarText: "南", connected: true, status: "5 颗骰子" },
  { seatIndex: 3, userId: "user-self", displayName: room.displayName || "你", avatarText: (room.displayName || "你").slice(0, 1), connected: true, status: "你的座位" },
]);
const fixtureMode = computed(() => props.roomId === "fixture-room");

onMounted(() => {
  document.documentElement.dataset.themeId = safeTheme.themeId;
  document.documentElement.dataset.themeFallback = "true";
  if (!fixtureMode.value) {
    room.enterRoom(props.roomId);
    room.setSession(props.sessionId);
  }
});

onBeforeUnmount(() => {
  if (pendingTimer !== undefined) {
    window.clearTimeout(pendingTimer);
  }
});

const dispatchAction = (action: string, label: string): void => {
  // A synchronous lock closes the double-tap window before the network action is dispatched.
  if (pendingAction.value !== null) {
    return;
  }
  pendingAction.value = action;
  lastAction.value = `正在提交：${label}`;
  pendingTimer = window.setTimeout(() => {
    lastAction.value = `${label}已提交`;
    pendingAction.value = null;
    pendingTimer = undefined;
  }, 650);
};

const confirmChallenge = (): void => {
  dangerOpen.value = false;
  dispatchAction("round.challenge", "质疑");
};

const leaveTable = async (): Promise<void> => {
  await router.push({ name: "room", params: { roomId: props.roomId } });
};
</script>

<template>
  <main class="game-screen" data-testid="game-screen">
    <header class="game-bar">
      <button class="game-bar__icon" type="button" title="返回房间" @click="leaveTable"><ArrowLeft :size="20" aria-hidden="true" /></button>
      <div class="game-bar__title"><h1>吹牛骰子</h1><span>第 3 轮 · 房间 {{ room.roomCode ?? "N789" }}</span></div>
      <ConnectionBadge state="online" />
      <button class="game-bar__icon" type="button" title="声音设置"><Volume2 :size="19" aria-hidden="true" /></button>
      <button class="game-bar__icon" type="button" title="更多设置"><MoreHorizontal :size="21" aria-hidden="true" /></button>
    </header>

    <section class="game-stage" aria-label="吹牛骰子共同桌面">
      <GameTable :seats="seats" :self-seat-index="3">
        <template #center>
          <div class="table-call">
            <span>当前叫骰</span>
            <strong>6 × 4</strong>
            <small>小满 · 还剩 18 秒</small>
          </div>
        </template>
        <template #private>
          <PrivateZone>
            <div class="private-dice" aria-label="你的骰子：2、4、4、5、6">
              <EyeOff :size="15" aria-hidden="true" /><span v-for="(value, index) in [2, 4, 4, 5, 6]" :key="index">{{ value }}</span>
            </div>
          </PrivateZone>
        </template>
      </GameTable>
    </section>

    <ActionTray v-model="trayState" :pending="pendingAction !== null" label="本轮操作">
      <template #summary>
        <div class="turn-summary"><span class="turn-dot" /><strong>{{ lastAction }}</strong></div>
        <span class="turn-time">18 秒</span>
      </template>
      <template #primary>
        <div class="action-grid">
          <button data-testid="roll-action" type="button" :disabled="pendingAction !== null" @click="dispatchAction('round.bid', '叫 7 个 4')">
            <Dices :size="21" aria-hidden="true" /><span>叫 7 个 4</span>
          </button>
          <button data-testid="challenge-action" class="is-danger" type="button" :disabled="pendingAction !== null" @click="dangerOpen = true">
            <CircleHelp :size="21" aria-hidden="true" /><span>质疑</span>
          </button>
          <button type="button" :disabled="pendingAction !== null" @click="dispatchAction('round.pass', '跳过')">
            <Hand :size="21" aria-hidden="true" /><span>跳过</span>
          </button>
        </div>
      </template>
      <template #details>
        <div class="tray-details">
          <div><span>上次叫骰</span><strong>6 个 4</strong></div>
          <div><span>你的可用操作</span><strong>加注 · 质疑 · 跳过</strong></div>
          <p>本轮记录会随桌面同步，重新连接后从最后确认的状态继续。</p>
        </div>
      </template>
    </ActionTray>

    <DangerConfirm :open="dangerOpen" title="确认质疑小满？" confirm-label="确认质疑" @confirm="confirmChallenge" @cancel="dangerOpen = false">
      质疑后会立即公开所有骰子并结束本轮，提交后不能撤回。
    </DangerConfirm>
  </main>
</template>

<style scoped>
.game-screen { position: relative; width: 100%; height: 100dvh; min-height: 560px; overflow: hidden; background: #10191b; }
.game-bar { position: absolute; inset: max(8px, env(safe-area-inset-top)) max(8px, env(safe-area-inset-right)) auto max(8px, env(safe-area-inset-left)); z-index: 30; min-height: 46px; display: grid; grid-template-columns: 42px minmax(0, 1fr) auto 42px 42px; align-items: center; gap: 5px; padding: 4px 5px; background: rgb(18 26 29 / 84%); border: 1px solid rgb(168 181 180 / 17%); border-radius: 8px; backdrop-filter: blur(10px); }
.game-bar__icon { width: 40px; height: 40px; display: grid; place-items: center; padding: 0; color: var(--platform-muted); background: transparent; border: 0; border-radius: 6px; }
.game-bar__title { min-width: 0; display: grid; gap: 1px; }
.game-bar__title h1,
.game-bar__title span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.game-bar__title h1 { margin: 0; font-size: 14px; letter-spacing: 0; }
.game-bar__title span { color: var(--platform-muted); font-size: 10px; }
.game-stage { position: absolute; inset: 58px 0 min(23dvh, 194px); min-height: 0; transition: bottom 220ms ease; }
.table-call { display: grid; place-items: center; gap: 4px; }
.table-call span { color: var(--platform-muted); font-size: 11px; }
.table-call strong { color: var(--platform-accent); font-family: Georgia, "Times New Roman", serif; font-size: clamp(32px, 10vw, 54px); line-height: 1; }
.table-call small { color: var(--platform-ink); font-size: 11px; }
.private-dice { display: flex; align-items: center; gap: 5px; }
.private-dice > svg { margin-right: 2px; color: var(--platform-muted); }
.private-dice > span { width: 23px; height: 28px; display: grid; place-items: center; color: #161c1c; background: var(--platform-ink); border-radius: 5px; font-family: Georgia, serif; font-size: 14px; font-weight: 800; box-shadow: inset 0 -3px rgb(0 0 0 / 12%); }
.turn-summary { min-width: 0; display: flex; align-items: center; gap: 8px; }
.turn-summary strong { overflow: hidden; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.turn-dot { width: 8px; height: 8px; flex: 0 0 auto; background: #99d8b1; border-radius: 50%; box-shadow: 0 0 0 5px rgb(153 216 177 / 10%); }
.turn-time { flex: 0 0 auto; color: var(--platform-accent); font-family: Georgia, serif; font-size: 14px; font-weight: 700; }
.action-grid { display: grid; grid-template-columns: 1.5fr 1fr 1fr; gap: 8px; }
.action-grid button { min-height: 52px; display: flex; align-items: center; justify-content: center; gap: 7px; padding: 0 10px; color: #121a19; background: var(--platform-accent); border: 1px solid transparent; border-radius: 7px; font-weight: 800; }
.action-grid button.is-danger { background: var(--platform-danger); }
.action-grid button:last-child { color: var(--platform-ink); background: transparent; border-color: rgb(168 181 180 / 32%); }
.tray-details { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
.tray-details > div { display: grid; gap: 4px; padding: 10px; background: rgb(8 18 19 / 35%); border-radius: 7px; }
.tray-details span,
.tray-details p { color: var(--platform-muted); font-size: 11px; }
.tray-details strong { font-size: 13px; }
.tray-details p { grid-column: 1 / -1; margin: 2px 0; line-height: 1.5; }

@media (max-width: 370px) {
  .game-bar { grid-template-columns: 40px minmax(0, 1fr) auto 40px; }
  .game-bar > :last-child { display: none; }
  .action-grid button { gap: 4px; padding-inline: 6px; font-size: 12px; }
}

@media (orientation: landscape) {
  .game-screen { min-height: 360px; }
  .game-bar { inset-inline: 12px; grid-template-columns: 42px minmax(0, 1fr) auto 42px 42px; }
  .game-stage { inset: 55px 0 min(31dvh, 122px); }
  .private-dice > span { height: 24px; }
}

@media (prefers-reduced-motion: reduce) {
  .game-stage { transition: none; }
}
</style>
