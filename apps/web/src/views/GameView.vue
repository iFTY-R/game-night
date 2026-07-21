<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import {
  LiarsDiceTable,
  LiarsDiceReplayTable,
  LIARS_DICE_OPEN_ACTION,
  applyLiarsDiceFixtureAction,
  finishLiarsDiceFixture,
  liarsDiceFixtureContext,
  liarsDiceFixtureView,
  liarsDiceReplayFixture,
  liarsDiceRevealedFixture,
  liarsDiceSpectatorFixture,
  liarsDiceTimeoutFixture,
  type LiarsDiceActionInput,
  type LiarsDiceTableContext,
} from "@game-night/liars-dice-client";
import { classicTheme, liarsDiceSoundProfile, liarsDiceThemes } from "@game-night/liars-dice-themes";
import { ThemeRuntime, safeTheme } from "@game-night/theme-system";

import { useRoomStore } from "../stores/room";

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
    room.enterRoom(props.roomId);
    room.setSession(props.sessionId);
  }
});

onBeforeUnmount(() => {
  if (pendingTimer !== undefined) window.clearTimeout(pendingTimer);
  if (audioContext !== undefined) void audioContext.close();
  themeRuntime.apply({ manifest: safeTheme, assets: new Map(), usedFallback: true, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "true";
});

// The fixture applies only viewer-safe command effects; production receipts and projections replace this adapter in Task 13.
const submitAction = (input: LiarsDiceActionInput): void => {
  if (pendingAction.value !== null || context.value.connection !== "online") return;
  pendingAction.value = input.action;
  playSound(input.action === LIARS_DICE_OPEN_ACTION ? "reveal" : "bid");
  pendingTimer = window.setTimeout(() => {
    view.value = applyLiarsDiceFixtureAction(view.value, input, context.value.selfUserId);
    pendingAction.value = null;
    pendingTimer = undefined;
  }, 700);
};

const finishSession = (): void => {
  if (pendingAction.value !== null) return;
  view.value = finishLiarsDiceFixture(view.value);
};

const retry = (): void => {
  context.value = { ...context.value, connection: "online" };
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
