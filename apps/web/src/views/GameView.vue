<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import {
  GameClient as ViewerGameClient,
  SubscriptionFailure,
  SubscriptionRunner,
  type ConnectionPhase,
  type GameClientState,
  type SubscriptionCursor,
  type ViewerRole,
} from "@game-night/game-client";

import {
  LiarsDiceTable,
  LiarsDiceReplayTable,
  LIARS_DICE_OPEN_ACTION,
  applyLiarsDiceFixtureAction,
  createFinishAction,
  finishLiarsDiceFixture,
  liarsDiceFixtureContext,
  liarsDiceFixtureView,
  liarsDiceReplayFixture,
  liarsDiceRevealedFixture,
  liarsDiceReducer,
  liarsDiceSpectatorFixture,
  liarsDiceTimeoutFixture,
  type LiarsDiceActionInput,
  type LiarsDiceTableContext,
  type LiarsDiceView,
} from "@game-night/liars-dice-client";
import { classicTheme, liarsDiceSoundProfile, liarsDiceThemes } from "@game-night/liars-dice-themes";
import { ThemeRuntime, safeTheme } from "@game-night/theme-system";

import { useRoomStore } from "../stores/room";
import { BrowserRealtimeAdapter } from "../api/browser-realtime";
import { ApiError, gameClient } from "../api/client";
import { gameProjectionFromConnect } from "../api/game-projection";

type FixtureState = "active" | "revealed" | "spectator" | "reconnecting" | "timeout" | "replay";

const props = withDefaults(defineProps<{ roomId: string; sessionId: string; fixtureState?: FixtureState }>(), {
  fixtureState: "active",
});
const router = useRouter();
const room = useRoomStore();
const themeRuntime = new ThemeRuntime();
const fixtureView = () => {
  if (props.fixtureState === "revealed") return liarsDiceRevealedFixture();
  if (props.fixtureState === "timeout") return liarsDiceTimeoutFixture();
  return liarsDiceFixtureView();
};
const view = ref(fixtureView());
const replay = liarsDiceReplayFixture();
const pendingAction = ref<string | null>(null);
const liveFallback = ref(false);
const liveStateVersion = ref(0);
const muted = ref(false);
const themeIndex = ref(0);
let pendingTimer: number | undefined;
let audioContext: AudioContext | undefined;
let subscriptionController: AbortController | undefined;
let actionController: AbortController | undefined;
let stopLiveState: (() => void) | undefined;

// This client owns only viewer-safe projection state; mutations remain on the authenticated Connect API.
const liveClient = new ViewerGameClient<LiarsDiceView>({
  reducer: liarsDiceReducer,
  dispatch: async () => {
    throw new Error("live_dispatch_port_unused");
  },
});
const subscriptionRunner = new SubscriptionRunner<LiarsDiceView>();
const lifecycleController = new AbortController();

const fixtureMode = computed(() => props.roomId === "fixture-room");
const context = ref<LiarsDiceTableContext>({
  ...liarsDiceFixtureContext(room.displayName || "你"),
  roomCode: room.roomCode ?? "N789",
  viewerRole: props.fixtureState === "spectator" ? "spectator" : props.fixtureState === "replay" ? "replay" : "player",
  connection: props.fixtureState === "reconnecting" ? "reconnecting" : "online",
});
if (context.value.viewerRole === "spectator") {
  view.value = liarsDiceSpectatorFixture();
}

const connectionState = (phase: ConnectionPhase): LiarsDiceTableContext["connection"] => {
  if (phase === "online" || phase === "reconnecting" || phase === "draining") return phase;
  return "offline";
};

/** Applies one immutable SDK snapshot to the table without exposing transport frames to the game client. */
const applyLiveState = (state: GameClientState<LiarsDiceView>): void => {
  if (state.view === null) {
    context.value = { ...context.value, connection: connectionState(state.connection) };
    return;
  }
  view.value = state.view;
  liveStateVersion.value = state.stateVersion;
  context.value = {
    ...context.value,
    selfUserId: room.userId,
    roomCode: room.roomCode ?? context.value.roomCode,
    viewerRole: state.viewerRole ?? context.value.viewerRole,
    connection: connectionState(state.connection),
    players: state.view.players.map((player) => ({
      userId: player.userId,
      displayName: player.userId === room.userId ? room.displayName || "你" : `玩家 ${player.userId.slice(0, 6)}`,
      avatarText: (player.userId === room.userId ? room.displayName || "你" : player.userId).slice(0, 1),
      connected: true,
      host: player.userId === room.remoteRoom?.hostUserId,
      seatIndex: player.seatIndex,
    })),
  };
};

const viewerRoleForRoom = (): Exclude<ViewerRole, "replay"> => {
  const member = room.remoteRoom?.members.find((candidate) => candidate.userId === room.userId);
  return member?.role.includes("SPECTATOR") ? "spectator" : "player";
};

const viewerKind = (role: Exclude<ViewerRole, "replay">): string =>
  role === "spectator" ? "VIEWER_KIND_SPECTATOR" : "VIEWER_KIND_PLAYER";

/** Uses an explicit deployment endpoint when provided, otherwise the exact same-origin public route. */
const realtimeWebSocketURL = (): string => {
  const configured = String(import.meta.env.VITE_REALTIME_URL ?? "").trim();
  const url = new URL(configured || "/realtime/game", window.location.href);
  if (url.protocol === "http:") url.protocol = "ws:";
  if (url.protocol === "https:") url.protocol = "wss:";
  return url.toString();
};

const loadLiveProjection = async (): Promise<void> => {
  try {
    const response = await gameClient.getProjection(
      props.roomId,
      props.sessionId,
      viewerKind(viewerRoleForRoom()),
      lifecycleController.signal,
    );
    liveClient.accept(gameProjectionFromConnect(response.projection));
  } catch (error) {
    if (import.meta.env.DEV && error instanceof ApiError && (error.status === 401 || error.status === 403 || error.status === 404)) {
      liveFallback.value = true;
    }
    liveClient.markReconnecting(error instanceof ApiError ? error.code : "projection_unavailable");
  }
};

const subscriptionFailure = (error: ApiError): SubscriptionFailure =>
  new SubscriptionFailure(error.code, error.message, ![401, 403, 404].includes(error.status), "reconnecting", null, { cause: error });

/** Refreshes room membership once when a reconnect reports that the viewer role changed. */
const openLiveSubscription = async (cursor: SubscriptionCursor | null, signal: AbortSignal) => {
  let role = viewerRoleForRoom();
  const request = async () => gameClient.openSubscription(
    props.roomId,
    props.sessionId,
    viewerKind(role),
    cursor?.stateVersion ?? 0,
    signal,
  );
  let response;
  try {
    response = await request();
  } catch (error) {
    if (!(error instanceof ApiError) || error.status !== 403) {
      if (error instanceof ApiError) throw subscriptionFailure(error);
      throw error;
    }
    try {
      await room.loadRoom(props.roomId);
    } catch (refreshError) {
      if (refreshError instanceof ApiError) throw subscriptionFailure(refreshError);
      throw refreshError;
    }
    const refreshedRole = viewerRoleForRoom();
    if (refreshedRole === role) throw subscriptionFailure(error);
    role = refreshedRole;
    try {
      response = await request();
    } catch (retryError) {
      if (retryError instanceof ApiError) throw subscriptionFailure(retryError);
      throw retryError;
    }
  }
  try {
    return {
      ticket: response.ticket,
      grant: response.grant,
      projection: gameProjectionFromConnect(response.projection),
    };
  } catch (error) {
    throw new SubscriptionFailure("invalid_subscription_projection", "订阅投影无效", false, "reconnecting", null, { cause: error });
  }
};

/** Replaces the current attempt so manual retry and component teardown cannot leave overlapping sockets. */
const startLiveSubscription = (): void => {
  subscriptionController?.abort();
  const controller = new AbortController();
  subscriptionController = controller;
  const adapter = new BrowserRealtimeAdapter({
    url: realtimeWebSocketURL,
    openSubscription: openLiveSubscription,
  });
  void subscriptionRunner.run(liveClient, adapter, controller.signal).catch((error: unknown) => {
    if (!controller.signal.aborted) {
      liveClient.fail(error instanceof Error ? error.name : "subscription_failed");
    }
  });
};

const initializeLiveTable = async (): Promise<void> => {
  await loadLiveProjection();
  if (!liveFallback.value && !lifecycleController.signal.aborted) startLiveSubscription();
};

const applyTheme = (): void => {
  const manifest = liarsDiceThemes[themeIndex.value] ?? classicTheme;
  themeRuntime.apply({ manifest, assets: new Map(), usedFallback: false, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "false";
  document.documentElement.dataset.muted = String(muted.value);
};

// Theme-owned tone parameters provide replaceable feedback without exposing commands or state to theme code.
const playSound = (cue: "bid" | "reveal"): void => {
  if (muted.value || typeof window.AudioContext === "undefined") return;
  try {
    audioContext ??= new window.AudioContext();
    const profile = liarsDiceSoundProfile(liarsDiceThemes[themeIndex.value]?.themeId ?? classicTheme.themeId);
    const oscillator = audioContext.createOscillator();
    const gain = audioContext.createGain();
    const endsAt = audioContext.currentTime + profile.durationMs / 1000;
    oscillator.type = "triangle";
    oscillator.frequency.value = cue === "reveal" ? profile.revealHz : profile.bidHz;
    gain.gain.setValueAtTime(0.035, audioContext.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.0001, endsAt);
    oscillator.connect(gain).connect(audioContext.destination);
    oscillator.start();
    oscillator.stop(endsAt);
  } catch {
    // Audio is optional presentation feedback; unsupported or blocked contexts must not block a game action.
  }
};

onMounted(() => {
  applyTheme();
  if (!fixtureMode.value) {
    if (room.roomId !== props.roomId) {
      const roomCode = room.remoteRoom?.roomId === props.roomId ? room.remoteRoom.roomCode : props.roomId.toUpperCase().slice(0, 6);
      room.enterRoom(props.roomId, roomCode);
    }
    room.setSession(props.sessionId);
    context.value = { ...context.value, connection: "reconnecting", selfUserId: room.userId };
    stopLiveState = liveClient.subscribe(applyLiveState);
    void initializeLiveTable();
  }
});

onBeforeUnmount(() => {
  lifecycleController.abort();
  subscriptionController?.abort();
  actionController?.abort();
  stopLiveState?.();
  liveClient.dispose();
  if (pendingTimer !== undefined) window.clearTimeout(pendingTimer);
  if (audioContext !== undefined) void audioContext.close();
  themeRuntime.apply({ manifest: safeTheme, assets: new Map(), usedFallback: true, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "true";
});

/** Sends live commands through the authoritative API; fixture routes keep their deterministic preview adapter. */
const submitAction = async (input: LiarsDiceActionInput): Promise<void> => {
  if (pendingAction.value !== null || context.value.connection !== "online") return;
  pendingAction.value = input.action;
  playSound(input.action === LIARS_DICE_OPEN_ACTION ? "reveal" : "bid");
  if (!fixtureMode.value && !liveFallback.value) {
    const controller = new AbortController();
    actionController?.abort();
    actionController = controller;
    try {
      const actionId = crypto.randomUUID();
      const response = await gameClient.action(
        props.roomId,
        room.userId,
        props.sessionId,
        liveStateVersion.value,
        actionId,
        input.message,
        controller.signal,
      );
      liveClient.accept(gameProjectionFromConnect(response.projection));
    } catch (error) {
      if (!controller.signal.aborted) liveClient.markReconnecting(error instanceof ApiError ? error.code : "action_failed");
    } finally {
      if (actionController === controller) {
        actionController = undefined;
        pendingAction.value = null;
      }
    }
    return;
  }
  pendingTimer = window.setTimeout(() => {
    view.value = applyLiarsDiceFixtureAction(view.value, input, context.value.selfUserId);
    pendingAction.value = null;
    pendingTimer = undefined;
  }, 700);
};

const finishSession = async (): Promise<void> => {
  if (pendingAction.value !== null) return;
  if (!fixtureMode.value && !liveFallback.value && room.remoteRoom?.version) {
    pendingAction.value = "session.finish";
    try {
      await room.finishRemoteGame(props.sessionId, liveStateVersion.value, createFinishAction().message);
      await router.push({ name: "room", params: { roomId: props.roomId } });
    } catch {
      liveClient.markReconnecting("finish_failed");
    } finally {
      pendingAction.value = null;
    }
    return;
  }
  view.value = finishLiarsDiceFixture(view.value);
};

const retry = (): void => {
  if (fixtureMode.value || liveFallback.value) {
    context.value = { ...context.value, connection: "online" };
    return;
  }
  context.value = { ...context.value, connection: "reconnecting" };
  startLiveSubscription();
};

const cycleTheme = (): void => {
  themeIndex.value = (themeIndex.value + 1) % liarsDiceThemes.length;
  applyTheme();
};

const toggleSound = (): void => {
  muted.value = !muted.value;
  document.documentElement.dataset.muted = String(muted.value);
};

const leaveTable = async (): Promise<void> => {
  await router.push({ name: "room", params: { roomId: props.roomId } });
};
</script>

<template>
  <LiarsDiceReplayTable
    v-if="fixtureState === 'replay'"
    :replay="replay"
    :context="context"
    :muted="muted"
    @leave="leaveTable"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
  <LiarsDiceTable
    v-else
    :view="view"
    :context="context"
    :allowed-actions="view.allowedActions"
    :pending-action="pendingAction"
    :muted="muted"
    @submit="submitAction"
    @finish="finishSession"
    @retry="retry"
    @leave="leaveTable"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
</template>
