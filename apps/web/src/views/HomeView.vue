<script setup lang="ts">
import { ArrowRight, Crown, Dices, DoorOpen, Eye, Globe2, RefreshCw, ShieldCheck, Users, X } from "lucide-vue-next";
import { computed, nextTick, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import { ApiError, type MyRoomCardWire, type PublicRoomCardWire, type RoomSnapshot } from "../api/client";
import CreateRoomDialog, { type CreateRoomDialogHandle, type RoomVisibility } from "../components/CreateRoomDialog.vue";
import ProfileTrigger from "../components/ProfileTrigger.vue";
import UsernameDialog, { type UsernameChangedEvent, type UsernameDialogHandle } from "../components/UsernameDialog.vue";
import { gameById, gameCatalog, type GameId } from "../game-catalog";
import { useRoomStore } from "../stores/room";
import { USERNAME_RULE_MESSAGE, validateUsernameInput } from "../username";

type JoinIntent = "JOIN_INTENT_PARTICIPANT" | "JOIN_INTENT_SPECTATOR";
/** Stores only the original target and intent needed for one explicit post-rename retry. */
type PendingRoomEntry =
  | { kind: "code"; roomCode: string }
  | { kind: "public-existing"; roomId: string }
  | { kind: "public-join"; roomId: string; intent: JoinIntent };

const router = useRouter();
const room = useRoomStore();
const props = withDefaults(defineProps<{ inviteCode?: string }>(), { inviteCode: "" });
const displayName = ref(room.displayName);
const inviteCode = computed(() => props.inviteCode.trim().toUpperCase());
const roomCode = ref(inviteCode.value);
const error = ref("");
const listError = ref("");
const syncNotice = ref("");
// Pending entry state remains page-local so refreshing an invite can restart from the URL without persisting stale actions.
const pendingRoomEntry = ref<PendingRoomEntry | null>(null);
const roomUsernameConflict = ref(false);
const roomEntryPending = ref(false);
const usernameDialog = ref<UsernameDialogHandle | null>(null);
const createRoomDialog = ref<CreateRoomDialogHandle | null>(null);
const conflictAction = ref<HTMLButtonElement | null>(null);
const ready = computed(() => room.hasIdentity);
const usernameValidation = computed(() => validateUsernameInput(displayName.value));
const usernameInvalid = computed(() => displayName.value.length > 0 && !usernameValidation.value.isValid);
const canSubmitIdentity = computed(() => usernameValidation.value.isValid && !room.busy);
// The selected game is the target of the currently opened creation dialog, not a persistent card selection.
const selectedGameId = ref<GameId>("liars-dice");
const creatingRoom = ref(false);
const createRoomError = ref("");
const selectedGame = computed(() => gameById(selectedGameId.value) ?? gameCatalog[0]);

const saveIdentity = async (): Promise<boolean> => {
  if (!usernameValidation.value.isValid) {
    error.value = `用户名需要 ${USERNAME_RULE_MESSAGE}`;
    displayName.value = usernameValidation.value.normalized;
    return false;
  }
  try {
    await room.ensureIdentity(usernameValidation.value.normalized);
    displayName.value = room.displayName || usernameValidation.value.normalized;
    error.value = "";
    return true;
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : `用户名需要 ${USERNAME_RULE_MESSAGE}`;
    return false;
  }
};

const entryFallback = (entry: PendingRoomEntry): string => entry.kind === "code" ? "加入房间失败" : "公开房间进入失败";

const setEntryError = (entry: PendingRoomEntry, message: string): void => {
  if (entry.kind === "code") error.value = message;
  else listError.value = message;
};

/** Resolves every join surface through one data-only context so a rename can retry the exact original action once. */
const executeRoomEntry = async (entry: PendingRoomEntry): Promise<RoomSnapshot> => {
  if (entry.kind === "code") return room.joinRemote(entry.roomCode);
  if (entry.kind === "public-join") return room.joinPublicRemote(entry.roomId, entry.intent);
  const snapshot = await room.loadRoom(entry.roomId);
  if (!snapshot) throw new Error("房间响应缺少状态");
  return snapshot;
};

/** Attempts one entry without recursively opening the rename flow when an automatic retry also conflicts. */
const attemptRoomEntry = async (entry: PendingRoomEntry): Promise<void> => {
  if (roomEntryPending.value) return;
  let focusConflict = false;
  pendingRoomEntry.value = entry;
  roomUsernameConflict.value = false;
  setEntryError(entry, "");
  roomEntryPending.value = true;
  try {
    const joined = await executeRoomEntry(entry);
    const resolvedRoomId = joined.roomId;
    room.enterRoom(resolvedRoomId, joined.roomCode);
    room.setRemoteRoom(joined);
    pendingRoomEntry.value = null;
    await router.push({ name: "room", params: { roomId: resolvedRoomId } });
  } catch (reason) {
    if (reason instanceof ApiError && reason.businessKey === "room.username.taken") {
      roomUsernameConflict.value = true;
      focusConflict = true;
      return;
    }
    pendingRoomEntry.value = null;
    setEntryError(entry, reason instanceof Error ? reason.message : entryFallback(entry));
    if (entry.kind !== "code") await room.loadPublicRooms(true).catch(() => undefined);
  } finally {
    roomEntryPending.value = false;
    if (focusConflict) {
      await nextTick();
      conflictAction.value?.focus();
    }
  }
};

const enterRoom = async (code: string): Promise<void> => {
  if (!room.hasIdentity && !(await saveIdentity())) return;
  await attemptRoomEntry({ kind: "code", roomCode: code });
};

/** Refreshes both discovery lanes independently so one unavailable list does not hide the other. */
const loadRoomLists = async (): Promise<void> => {
  listError.value = "";
  const results = await Promise.allSettled([room.loadMyRooms(true), room.loadPublicRooms(true)]);
  if (results.every((result) => result.status === "rejected")) listError.value = "房间列表暂时无法加载";
};

const confirmIdentity = async (): Promise<void> => {
  if (!(await saveIdentity())) return;
  if (/^[A-Z0-9]{4,8}$/.test(inviteCode.value)) {
    await enterRoom(inviteCode.value);
    return;
  }
  await loadRoomLists();
};

const joinRoom = async (): Promise<void> => {
  const code = roomCode.value.trim().toUpperCase();
  if (!/^[A-Z0-9]{4,8}$/.test(code)) {
    error.value = "请输入 4 到 8 位房间码";
    return;
  }
  await enterRoom(code);
};

/** Binds room creation to the game whose local action the user selected. */
const openCreateRoomDialog = (gameId: GameId): void => {
  selectedGameId.value = gameId;
  createRoomError.value = "";
  createRoomDialog.value?.open();
};

const createRoom = async (visibility: RoomVisibility): Promise<void> => {
  if (creatingRoom.value) return;
  creatingRoom.value = true;
  createRoomError.value = "";
  try {
    if (!room.hasIdentity && !(await saveIdentity())) return;
    const created = await room.createRemoteRoom(visibility);
    // CreateRoom starts from the platform default; commit the card selected on
    // the discovery screen before any member can observe a different table.
    const selected = created.selectedGameId === selectedGameId.value
      ? created
      : await room.selectRemoteGame(selectedGameId.value);
    const roomId = selected.roomId;
    const code = selected.roomCode;
    room.enterRoom(roomId, code);
    room.setRemoteRoom(selected);
    await router.push({ name: "room", params: { roomId }, query: { game: selectedGameId.value } });
  } catch (reason) {
    createRoomError.value = reason instanceof Error ? reason.message : "创建房间失败";
  } finally {
    creatingRoom.value = false;
  }
};

/** Revalidates a member room before restoring it, so closed or removed memberships never become stale shortcuts. */
const openMyRoom = async (card: MyRoomCardWire): Promise<void> => {
  try {
    const snapshot = await room.loadRoom(card.roomId);
    if (snapshot?.status.includes("CLOSED")) {
      await room.loadMyRooms(true);
      return;
    }
    room.enterRoom(card.roomId, snapshot?.roomCode ?? card.roomCode);
    if (snapshot) room.setRemoteRoom(snapshot);
    await router.push({ name: "room", params: { roomId: card.roomId } });
  } catch (reason) {
    listError.value = reason instanceof Error ? reason.message : "房间恢复失败";
    await room.loadMyRooms(true).catch(() => undefined);
  }
};

const publicActionLabel = (action: string): string => {
  if (action.includes("ENTER_ROOM")) return "进入房间";
  if (action.includes("REQUEST_JOIN")) return "申请入座";
  if (action.includes("JOIN")) return "加入房间";
  if (action.includes("REQUEST_SPECTATE")) return "申请观战";
  if (action.includes("SPECTATE")) return "进入观战";
  if (action.includes("FULL")) return "座位已满";
  if (action.includes("IN_PROGRESS")) return "对局进行中";
  return "等待房主";
};

const publicActionEnabled = (action: string): boolean =>
  ["ENTER_ROOM", "REQUEST_JOIN", "JOIN", "REQUEST_SPECTATE", "SPECTATE"].some((value) => action.includes(value));

/** Executes the server-computed public-card action without duplicating admission policy in the browser. */
const openPublicRoom = async (card: PublicRoomCardWire): Promise<void> => {
  if (!publicActionEnabled(card.primaryAction)) return;
  const entry: PendingRoomEntry = card.primaryAction.includes("ENTER_ROOM")
    ? { kind: "public-existing", roomId: card.roomId }
    : {
        kind: "public-join",
        roomId: card.roomId,
        intent: card.primaryAction.includes("SPECTATE") ? "JOIN_INTENT_SPECTATOR" : "JOIN_INTENT_PARTICIPANT",
      };
  await attemptRoomEntry(entry);
};

const openProfileDialog = (): void => {
  // A normal profile edit is deliberately detached from any earlier failed room entry.
  pendingRoomEntry.value = null;
  roomUsernameConflict.value = false;
  usernameDialog.value?.open("profile");
};

const openConflictDialog = (): void => {
  if (pendingRoomEntry.value !== null) usernameDialog.value?.open("room-conflict");
};

/** Refresh failures affect only derived room cards; the committed identity remains authoritative in the store. */
const refreshListsAfterRename = async (): Promise<void> => {
  syncNotice.value = "";
  const results = await Promise.allSettled([room.loadMyRooms(true), room.loadPublicRooms(true)]);
  if (results.some((result) => result.status === "rejected")) {
    syncNotice.value = "用户名已更新，部分房间信息同步失败，可稍后刷新";
  }
};

const handleUsernameChanged = async (event: UsernameChangedEvent): Promise<void> => {
  const entry = event.mode === "room-conflict" ? pendingRoomEntry.value : null;
  const refresh = refreshListsAfterRename();
  // One successful form submission produces exactly one retry; a second conflict waits for another explicit rename.
  if (entry !== null) await attemptRoomEntry(entry);
  await refresh;
};

const roomStatusLabel = (status: string): string => {
  if (status.includes("PLAYING")) return "游戏中";
  if (status.includes("POST_GAME")) return "本局结束";
  return "等候中";
};

const roomGameName = (activeGameId: string, lastFinishedGameId = ""): string =>
  gameById(activeGameId || lastFinishedGameId)?.name ?? "等待选游戏";

onMounted(async () => {
  try {
    await room.recoverIdentity();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : "身份恢复失败";
  }
  // An authentication rejection clears the viewer-safe store, so the form must not retain its pre-recovery snapshot.
  displayName.value = room.hasIdentity ? room.displayName : "";
  if (room.hasIdentity && /^[A-Z0-9]{4,8}$/.test(inviteCode.value)) {
    await enterRoom(inviteCode.value);
    return;
  }
  if (room.hasIdentity) await loadRoomLists();
});
</script>

<template>
  <main class="screen-shell home-screen">
    <header class="topbar">
      <RouterLink class="brand" to="/" aria-label="Game Night 首页">
        <img :src="'/brand-mark.svg'" alt="" />
        <span>GAME NIGHT</span>
      </RouterLink>
      <ProfileTrigger v-if="ready" :username="room.displayName" @activate="openProfileDialog" />
      <span v-else class="device-badge"><ShieldCheck :size="15" aria-hidden="true" /> 设备登录</span>
    </header>

    <div v-if="room.notice" class="home-notice" role="status">
      <span>{{ room.notice }}</span>
      <button class="icon-button" type="button" title="关闭提示" @click="room.clearNotice"><X :size="18" aria-hidden="true" /></button>
    </div>

    <div v-if="roomUsernameConflict" class="username-conflict" role="alert">
      <div><strong>房间内已有同名玩家</strong><span>换一个用户名后，可以继续刚才的进房操作。</span></div>
      <button ref="conflictAction" class="button button--quiet" type="button" :disabled="roomEntryPending" @click="openConflictDialog">修改用户名后重试</button>
    </div>

    <p v-if="syncNotice" class="sync-notice" role="status">{{ syncNotice }}</p>

    <section class="home-intro" aria-labelledby="home-title">
      <p class="eyebrow">朋友在线，今晚开桌</p>
      <h1 id="home-title" class="display-title">不在同一张桌，也能一起玩。</h1>
      <p class="home-intro__copy">创建房间，发给朋友。每个人的手机都是自己的座位，公共进度始终留在桌面中央。</p>
    </section>

    <section v-if="!ready" class="join-panel panel" aria-labelledby="identity-title">
      <div><p class="eyebrow">第一次来</p><h2 id="identity-title" class="section-title">先设置你的用户名</h2></div>
      <form class="entry-form" @submit.prevent="confirmIdentity">
        <label for="display-name">用户名</label>
        <div class="field-row">
          <input
            id="display-name"
            v-model="displayName"
            autocomplete="nickname"
            maxlength="4"
            placeholder="朋友看到的名字"
            :aria-invalid="usernameInvalid ? 'true' : 'false'"
            aria-describedby="display-name-hint"
          />
          <button class="button" type="submit" :disabled="!canSubmitIdentity">继续 <ArrowRight :size="18" aria-hidden="true" /></button>
        </div>
        <p id="display-name-hint" class="field-hint" :class="{ 'field-hint--invalid': usernameInvalid }">{{ USERNAME_RULE_MESSAGE }}</p>
      </form>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </section>

    <template v-else>
      <section class="room-shelf" aria-labelledby="my-rooms-title">
        <div class="section-heading">
          <div><p class="eyebrow">你的桌</p><h2 id="my-rooms-title" class="section-title">我的房间</h2></div>
          <button class="icon-button" type="button" title="刷新我的房间" :disabled="room.myRoomsLoading" @click="room.loadMyRooms(true)"><RefreshCw :size="18" aria-hidden="true" /></button>
        </div>
        <p v-if="room.myRoomsLoading && room.myRooms.length === 0" class="list-state" role="status">正在同步房间…</p>
        <p v-else-if="room.myRooms.length === 0" class="list-state">还没有有效房间</p>
        <div v-else class="my-room-grid">
          <button v-for="(card, index) in room.myRooms" :key="card.roomId" class="my-room-card" :class="{ 'is-primary': index === 0 }" type="button" @click="openMyRoom(card)">
            <span class="room-card__icon"><Crown v-if="card.isHost" :size="19" aria-hidden="true" /><Users v-else :size="19" aria-hidden="true" /></span>
            <span class="room-card__body"><strong>{{ card.isHost ? "我的房间" : `${card.hostUsername} 的房间` }}</strong><small>{{ card.roomCode }} · {{ roomGameName(card.activeGameId, card.lastFinishedGameId) }}</small></span>
            <span class="room-card__meta"><em>{{ roomStatusLabel(card.status) }}</em><small>{{ card.participantCount }} / {{ card.participantCapacity }}</small></span>
          </button>
        </div>
        <button v-if="room.myRoomsNextPageToken" class="button button--quiet list-more" type="button" :disabled="room.myRoomsLoading" @click="room.loadMyRooms(false)">加载更多</button>
      </section>

      <section class="game-shelf" aria-labelledby="games-title">
        <div class="section-heading"><div><p class="eyebrow">游戏桌</p><h2 id="games-title" class="section-title">今晚玩什么</h2></div><span class="muted">{{ gameCatalog.length }} 款</span></div>
        <div class="game-list">
          <article v-for="game in gameCatalog" :key="game.gameId" class="game-card">
            <span class="game-card__accent">{{ game.accent }}</span>
            <span class="game-card__copy">
              <span class="game-card__name"><strong>{{ game.name }}</strong><em>至少 {{ game.minimumPlayers }} 人</em></span>
              <small>{{ game.summary }}</small>
            </span>
            <button
              class="button button--quiet game-card__create"
              type="button"
              :aria-label="`创建${game.name}房间`"
              @click="openCreateRoomDialog(game.gameId)"
            >
              <Dices :size="16" aria-hidden="true" /> 创建房间
            </button>
          </article>
        </div>
      </section>

      <section class="join-panel panel" aria-labelledby="join-title">
        <div><p class="eyebrow">晚上好，{{ room.displayName }}</p><h2 id="join-title" class="section-title">加入房间</h2></div>
        <form class="entry-form" @submit.prevent="joinRoom">
          <label for="room-code">房间码</label>
          <div class="field-row">
            <input id="room-code" v-model="roomCode" inputmode="text" maxlength="8" autocapitalize="characters" placeholder="例如 N789" />
            <button class="button" type="submit" :disabled="roomEntryPending"><DoorOpen :size="18" aria-hidden="true" /> 进房</button>
          </div>
        </form>
        <p v-if="error" class="form-error" role="alert">{{ error }}</p>
      </section>

      <section class="public-lobby" aria-labelledby="public-rooms-title">
        <div class="section-heading">
          <div><p class="eyebrow">在线大厅</p><h2 id="public-rooms-title" class="section-title">公开房间</h2></div>
          <button class="icon-button" type="button" title="刷新公开房间" :disabled="room.publicRoomsLoading" @click="room.loadPublicRooms(true)"><RefreshCw :size="18" aria-hidden="true" /></button>
        </div>
        <p v-if="room.publicRoomsLoading && room.publicRooms.length === 0" class="list-state" role="status">正在同步大厅…</p>
        <p v-else-if="room.publicRooms.length === 0" class="list-state">目前没有公开房间</p>
        <div v-else class="public-room-list">
          <article v-for="card in room.publicRooms" :key="card.roomId" class="public-room-card">
            <span class="room-card__icon"><Globe2 :size="19" aria-hidden="true" /></span>
            <div class="room-card__body"><strong>{{ card.hostUsername }} 的房间</strong><small>{{ roomGameName(card.activeGameId) }} · {{ roomStatusLabel(card.status) }}</small></div>
            <span class="public-room-card__count"><Users :size="15" aria-hidden="true" /> {{ card.participantCount }} / {{ card.participantCapacity }}</span>
            <button class="button button--quiet" type="button" :disabled="roomEntryPending || !publicActionEnabled(card.primaryAction)" @click="openPublicRoom(card)"><Eye v-if="card.primaryAction.includes('SPECTATE')" :size="16" aria-hidden="true" />{{ publicActionLabel(card.primaryAction) }}</button>
          </article>
        </div>
        <button v-if="room.publicRoomsNextPageToken" class="button button--quiet list-more" type="button" :disabled="room.publicRoomsLoading" @click="room.loadPublicRooms(false)">加载更多</button>
        <p v-if="listError" class="form-error" role="alert">{{ listError }}</p>
      </section>
    </template>

    <UsernameDialog ref="usernameDialog" @changed="handleUsernameChanged" />
    <CreateRoomDialog
      ref="createRoomDialog"
      :game-name="selectedGame.name"
      :pending="creatingRoom"
      :error="createRoomError"
      @choose="createRoom"
    />
  </main>
</template>

<style scoped>
.home-screen { display: grid; align-content: start; gap: 30px; }
.device-badge { display: inline-flex; align-items: center; gap: 6px; color: #99d8b1; font-size: 12px; }
.home-notice { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 14px; color: var(--platform-ink); background: rgb(255 138 126 / 10%); border: 1px solid rgb(255 138 126 / 38%); border-radius: 7px; font-size: 13px; }
.username-conflict { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 14px; background: rgb(230 181 102 / 10%); border: 1px solid rgb(230 181 102 / 42%); border-radius: 7px; }
.username-conflict > div { min-width: 0; display: grid; gap: 3px; }
.username-conflict strong { color: var(--platform-ink); font-size: 14px; }
.username-conflict span,
.sync-notice { color: var(--platform-muted); font-size: 12px; }
.username-conflict .button { min-height: 42px; flex: 0 0 auto; }
.sync-notice { margin: 0; padding: 10px 12px; border-left: 2px solid var(--platform-accent); }
.home-intro { padding: clamp(26px, 8vh, 72px) 0 4px; }
.home-intro__copy { max-width: 620px; margin: 20px 0 0; color: var(--platform-muted); font-size: 16px; line-height: 1.7; }
.join-panel { display: grid; gap: 18px; padding: 20px; border-left: 3px solid var(--platform-accent); }
.entry-form { display: grid; gap: 10px; }
.entry-form label { color: var(--platform-muted); font-size: 12px; font-weight: 700; }
.field-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px; }
.field-row input { min-width: 0; min-height: 50px; padding: 0 14px; color: var(--platform-ink); background: rgb(8 18 19 / 48%); border: 1px solid rgb(168 181 180 / 28%); border-radius: 7px; }
.field-hint { margin: -2px 0 0; color: var(--platform-muted); font-size: 12px; }
.field-hint--invalid { color: var(--platform-danger); }
.form-error { margin: 0; color: var(--platform-danger); font-size: 13px; }
.section-heading { display: flex; align-items: end; justify-content: space-between; gap: 12px; }
.room-shelf,
.game-shelf,
.public-lobby { display: grid; gap: 14px; }
.list-state { margin: 0; padding: 18px 0; color: var(--platform-muted); border-top: 1px solid rgb(168 181 180 / 14%); border-bottom: 1px solid rgb(168 181 180 / 14%); font-size: 13px; }
.my-room-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 9px; }
.my-room-card { min-height: 78px; display: grid; grid-template-columns: 40px minmax(0, 1fr) auto; align-items: center; gap: 11px; padding: 12px; color: var(--platform-ink); text-align: left; background: rgb(27 41 45 / 72%); border: 1px solid rgb(168 181 180 / 17%); border-radius: 8px; }
.my-room-card.is-primary { border-color: rgb(230 181 102 / 46%); }
.room-card__icon { width: 40px; height: 40px; display: grid; place-items: center; color: #171b1a; background: var(--platform-accent); border-radius: 50%; }
.room-card__body { min-width: 0; display: grid; gap: 4px; }
.room-card__body strong,
.room-card__body small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.room-card__body strong { font-size: 14px; }
.room-card__body small { color: var(--platform-muted); font-size: 11px; }
.room-card__meta { display: grid; justify-items: end; gap: 4px; }
.room-card__meta em { color: #99d8b1; font-size: 11px; font-style: normal; }
.room-card__meta small { color: var(--platform-muted); font-size: 11px; }
.game-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.game-card { min-height: 92px; display: grid; grid-template-columns: 40px minmax(0, 1fr) auto; align-items: center; gap: 12px; padding: 13px 14px; color: var(--platform-ink); background: rgb(27 41 45 / 66%); border: 1px solid rgb(168 181 180 / 16%); border-radius: 8px; }
.game-card__accent { width: 40px; height: 40px; display: grid; place-items: center; color: #171b1a; background: var(--platform-accent); border-radius: 50%; font-family: var(--font-display); font-weight: 900; }
.game-card__copy { min-width: 0; display: grid; gap: 6px; }
.game-card__name { min-width: 0; display: flex; align-items: baseline; gap: 8px; }
.game-card strong { font-size: 15px; }
.game-card small { overflow: hidden; color: var(--platform-muted); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.game-card em { flex: 0 0 auto; color: var(--platform-accent); font-size: 10px; font-style: normal; }
.game-card__create { min-width: 108px; min-height: 42px; padding: 0 12px; white-space: nowrap; }
.public-room-list { display: grid; gap: 8px; }
.public-room-card { min-height: 72px; display: grid; grid-template-columns: 40px minmax(0, 1fr) auto minmax(120px, auto); align-items: center; gap: 11px; padding: 11px 12px; background: rgb(27 41 45 / 64%); border: 1px solid rgb(168 181 180 / 16%); border-radius: 8px; }
.public-room-card__count { display: inline-flex; align-items: center; gap: 5px; color: var(--platform-muted); font-size: 11px; }
.public-room-card .button { min-height: 40px; padding: 0 12px; }
.list-more { justify-self: center; min-width: 150px; }

@media (max-width: 720px) {
  .my-room-grid,
  .game-list { grid-template-columns: 1fr; }
  .public-room-card { grid-template-columns: 40px minmax(0, 1fr) auto; }
  .public-room-card .button { grid-column: 2 / 4; width: 100%; }
}

@media (max-width: 390px) {
  .field-row { grid-template-columns: 1fr; }
  .field-row .button { width: 100%; }
  .username-conflict { align-items: stretch; flex-direction: column; }
  .username-conflict .button { width: 100%; }
  .my-room-card { grid-template-columns: 36px minmax(0, 1fr) auto; }
  .room-card__icon { width: 36px; height: 36px; }
  .game-card { grid-template-columns: 36px minmax(0, 1fr) auto; gap: 9px; padding: 11px; }
  .game-card__accent { width: 36px; height: 36px; font-size: 13px; }
  .game-card__name { display: grid; gap: 2px; }
  .game-card__create { min-width: 98px; padding: 0 9px; }
}
</style>
