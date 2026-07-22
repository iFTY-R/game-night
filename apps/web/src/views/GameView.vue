<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

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
  type GameProjection,
  type LiarsDiceActionInput,
  type LiarsDiceTableContext,
} from "@game-night/liars-dice-client";
import { classicTheme, liarsDiceSoundProfile, liarsDiceThemes } from "@game-night/liars-dice-themes";
import { ThemeRuntime, safeTheme } from "@game-night/theme-system";

import { useRoomStore } from "../stores/room";
import { ApiError, gameClient } from "../api/client";

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

type WireProjection = NonNullable<Awaited<ReturnType<typeof gameClient.getProjection>>["projection"]>;

const fromBase64 = (encoded: string): Uint8Array => {
  const binary = atob(encoded);
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
};

const safeStateVersion = (wire: string): number => {
  if (!/^[1-9]\d*$/.test(wire)) {
    throw new Error("game_state_version_invalid");
  }
  const version = Number(wire);
  if (!Number.isSafeInteger(version)) {
    throw new Error("game_state_version_unsupported");
  }
  return version;
};

/** Converts the Connect JSON shape into the versioned game-client contract. */
const toProjection = (wire: WireProjection): GameProjection => {
  if (!wire.view) {
    throw new Error("game_projection_missing");
  }
  const viewerRole = wire.viewerKind.includes("SPECTATOR") ? "spectator" : wire.viewerKind.includes("REPLAY") ? "replay" : "player";
  return {
    kind: "projection",
    sessionId: wire.sessionId,
    stateVersion: safeStateVersion(wire.stateVersion),
    viewerRole,
    view: {
      gameId: wire.view.gameId,
      version: wire.view.version ?? { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
      schemaVersion: wire.view.schemaVersion,
      messageType: wire.view.messageType,
      payload: fromBase64(wire.view.payload),
    },
    allowedActions: wire.allowedActions ?? [],
  };
};

const applyLiveProjection = (wire: WireProjection | undefined): void => {
  if (!wire) {
    throw new Error("game_projection_missing");
  }
  const projection = toProjection(wire);
  const nextView = liarsDiceReducer.fromProjection(projection);
  view.value = nextView;
  liveStateVersion.value = projection.stateVersion;
  context.value = {
    ...context.value,
    selfUserId: room.userId,
    roomCode: room.roomCode ?? context.value.roomCode,
    viewerRole: projection.viewerRole,
    connection: "online",
    players: nextView.players.map((player) => ({
      userId: player.userId,
      displayName: player.userId === room.userId ? room.displayName || "你" : `玩家 ${player.userId.slice(0, 6)}`,
      avatarText: (player.userId === room.userId ? room.displayName || "你" : player.userId).slice(0, 1),
      connected: true,
      host: player.userId === room.remoteRoom?.hostUserId,
      seatIndex: player.seatIndex,
    })),
  };
};

const loadLiveProjection = async (): Promise<void> => {
  try {
    const response = await gameClient.getProjection(props.roomId, props.sessionId);
    applyLiveProjection(response.projection);
  } catch (error) {
    if (import.meta.env.DEV && error instanceof ApiError && (error.status === 401 || error.status === 403 || error.status === 404)) {
      liveFallback.value = true;
    }
    context.value = { ...context.value, connection: "offline" };
  }
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
    void loadLiveProjection();
  }
});

onBeforeUnmount(() => {
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
    try {
      const actionId = crypto.randomUUID();
      const response = await gameClient.action(props.roomId, props.sessionId, liveStateVersion.value, actionId, input.message);
      applyLiveProjection(response.projection);
    } catch {
      context.value = { ...context.value, connection: "reconnecting" };
    } finally {
      pendingAction.value = null;
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
      context.value = { ...context.value, connection: "reconnecting" };
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
  void loadLiveProjection();
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
