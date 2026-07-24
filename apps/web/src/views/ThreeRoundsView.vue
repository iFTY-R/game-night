<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import {
  Phase as ThreeRoundsPhase,
  ThreeRoundsReplayTable,
  ThreeRoundsTable,
  applyThreeRoundsFixtureAction,
  createFinishAction,
  finishThreeRoundsFixture,
  resolveSelectionConflict,
  threeRoundsFixtureContext,
  threeRoundsFixtureView,
  threeRoundsReplayFixture,
  threeRoundsReducer,
  type ThreeRoundsActionInput,
  type ThreeRoundsFixtureState,
  type ThreeRoundsTableContext,
  type ThreeRoundsView,
} from "@game-night/three-rounds-client";
import { classicTheme, threeRoundsSoundProfile, threeRoundsThemes } from "@game-night/three-rounds-themes";
import { ThemeRuntime, safeTheme } from "@game-night/theme-system";

import { useLiveGameTable } from "../composables/use-live-game-table";
import { useRoomStore } from "../stores/room";

const props = withDefaults(defineProps<{ roomId?: string; sessionId?: string; fixtureState?: ThreeRoundsFixtureState }>(), {
  roomId: "fixture-room",
  sessionId: "fixture-session",
  fixtureState: "active",
});

const router = useRouter();
const room = useRoomStore();
const themeRuntime = new ThemeRuntime();
const view = ref(threeRoundsFixtureView(props.fixtureState));
const replay = threeRoundsReplayFixture();
const context = ref<ThreeRoundsTableContext>({
  ...threeRoundsFixtureContext(room.displayName || "你", props.fixtureState),
  roomCode: room.roomCode ?? "3RND",
});
const fixtureMode = computed(() => props.roomId === "fixture-room");
const liveTable = useLiveGameTable<ThreeRoundsView, ThreeRoundsTableContext>({
  roomId: props.roomId,
  sessionId: props.sessionId,
  fixtureMode,
  reducer: threeRoundsReducer,
  view,
  context,
  players: (current) => current.publicPlayers,
  viewActions: (current) => current.allowedActions,
  finished: (current) => current.phase === ThreeRoundsPhase.FINISHED,
  resolveSimultaneousConflict: ({ attemptedAction, latestView, latestAllowedActions }) =>
    resolveSelectionConflict(latestView, latestAllowedActions, attemptedAction),
});
const allowedActions = liveTable.allowedActions;
const pendingAction = liveTable.pendingAction;
const muted = ref(false);
const themeIndex = ref(0);
let pendingTimer: number | undefined;
let audioContext: AudioContext | undefined;

const applyTheme = (): void => {
  const manifest = threeRoundsThemes[themeIndex.value] ?? classicTheme;
  themeRuntime.apply({ manifest, assets: new Map(), usedFallback: false, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "false";
  document.documentElement.dataset.muted = String(muted.value);
};

const playSound = (cue: "select" | "confirm" | "reveal"): void => {
  if (muted.value || typeof window.AudioContext === "undefined") return;
  try {
    audioContext ??= new window.AudioContext();
    const profile = threeRoundsSoundProfile(threeRoundsThemes[themeIndex.value]?.themeId ?? classicTheme.themeId);
    const oscillator = audioContext.createOscillator();
    const gain = audioContext.createGain();
    const endsAt = audioContext.currentTime + profile.durationMs / 1000;
    oscillator.type = "triangle";
    oscillator.frequency.value = cue === "select" ? profile.selectHz : cue === "confirm" ? profile.confirmHz : profile.revealHz;
    gain.gain.setValueAtTime(0.035, audioContext.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.0001, endsAt);
    oscillator.connect(gain).connect(audioContext.destination);
    oscillator.start();
    oscillator.stop(endsAt);
  } catch {
    // Theme audio is optional presentation feedback and cannot block room-scoped actions.
  }
};

onMounted(applyTheme);

onBeforeUnmount(() => {
  if (pendingTimer !== undefined) window.clearTimeout(pendingTimer);
  if (audioContext !== undefined) void audioContext.close();
  themeRuntime.apply({ manifest: safeTheme, assets: new Map(), usedFallback: true, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "true";
});

const submitAction = async (input: ThreeRoundsActionInput): Promise<void> => {
  if (pendingAction.value !== null || context.value.connection !== "online") return;
  playSound("confirm");
  if (await liveTable.submitLiveAction(input)) return;
  pendingAction.value = input.action;
  pendingTimer = window.setTimeout(() => {
    view.value = applyThreeRoundsFixtureAction(view.value, input);
    pendingAction.value = null;
    pendingTimer = undefined;
  }, 500);
};

const finishSession = async (): Promise<void> => {
  playSound("reveal");
  if (await liveTable.finishLiveSession(createFinishAction().message)) return;
  view.value = finishThreeRoundsFixture(view.value);
};

const cycleTheme = (): void => {
  themeIndex.value = (themeIndex.value + 1) % threeRoundsThemes.length;
  applyTheme();
};

const toggleSound = (): void => {
  muted.value = !muted.value;
  document.documentElement.dataset.muted = String(muted.value);
};

const leave = async (): Promise<void> => {
  await router.push(fixtureMode.value ? { name: "home" } : { name: "room", params: { roomId: props.roomId }, query: { manage: "1" } });
};
</script>

<template>
  <ThreeRoundsReplayTable
    v-if="fixtureState === 'replay'"
    :replay="replay"
    :context="context"
    :muted="muted"
    @leave="leave"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
  <ThreeRoundsTable
    v-else
    :view="view"
    :context="context"
    :allowed-actions="allowedActions"
    :pending-action="pendingAction"
    :muted="muted"
    @submit="submitAction"
    @retry="liveTable.retry"
    @finish="finishSession"
    @leave="leave"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
</template>
