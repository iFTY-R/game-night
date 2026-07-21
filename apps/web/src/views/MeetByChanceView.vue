<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import {
  MEET_BY_CHANCE_REROLL_ACTION,
  MeetByChanceReplayTable,
  MeetByChanceTable,
  applyMeetByChanceFixtureAction,
  finishMeetByChanceFixture,
  meetByChanceFixtureContext,
  meetByChanceFixtureView,
  meetByChanceReplayFixture,
  type MeetByChanceActionInput,
  type MeetByChanceFixtureState,
  type MeetByChanceTableContext,
} from "@game-night/meet-by-chance-client";
import { classicTheme, meetByChanceSoundProfile, meetByChanceThemes } from "@game-night/meet-by-chance-themes";
import { ThemeRuntime, safeTheme } from "@game-night/theme-system";

import { useRoomStore } from "../stores/room";

const props = withDefaults(defineProps<{ fixtureState?: MeetByChanceFixtureState }>(), { fixtureState: "active" });
const router = useRouter();
const room = useRoomStore();
const themeRuntime = new ThemeRuntime();
const view = ref(meetByChanceFixtureView(props.fixtureState));
const replay = meetByChanceReplayFixture();
const context = ref<MeetByChanceTableContext>({
  ...meetByChanceFixtureContext(room.displayName || "你", props.fixtureState),
  roomCode: room.roomCode ?? "MEET",
});
const pendingAction = ref<string | null>(null);
const muted = ref(false);
const themeIndex = ref(0);
let pendingTimer: number | undefined;
let audioContext: AudioContext | undefined;

const applyTheme = (): void => {
  const manifest = meetByChanceThemes[themeIndex.value] ?? classicTheme;
  themeRuntime.apply({ manifest, assets: new Map(), usedFallback: false, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "false";
  document.documentElement.dataset.muted = String(muted.value);
};

const playSound = (cue: "reveal" | "target"): void => {
  if (muted.value || typeof window.AudioContext === "undefined") return;
  try {
    audioContext ??= new window.AudioContext();
    const profile = meetByChanceSoundProfile(meetByChanceThemes[themeIndex.value]?.themeId ?? classicTheme.themeId);
    const oscillator = audioContext.createOscillator();
    const gain = audioContext.createGain();
    const endsAt = audioContext.currentTime + profile.durationMs / 1000;
    oscillator.type = "triangle";
    oscillator.frequency.value = cue === "reveal" ? profile.revealHz : profile.targetHz;
    gain.gain.setValueAtTime(0.035, audioContext.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.0001, endsAt);
    oscillator.connect(gain).connect(audioContext.destination);
    oscillator.start();
    oscillator.stop(endsAt);
  } catch {
    // Optional sound feedback cannot block an authoritative target decision.
  }
};

onMounted(applyTheme);
onBeforeUnmount(() => {
  if (pendingTimer !== undefined) window.clearTimeout(pendingTimer);
  if (audioContext !== undefined) void audioContext.close();
  themeRuntime.apply({ manifest: safeTheme, assets: new Map(), usedFallback: true, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "true";
});

const submitAction = (input: MeetByChanceActionInput): void => {
  if (pendingAction.value !== null || context.value.connection !== "online") return;
  pendingAction.value = input.action;
  playSound(input.action === MEET_BY_CHANCE_REROLL_ACTION ? "reveal" : "target");
  pendingTimer = window.setTimeout(() => {
    view.value = applyMeetByChanceFixtureAction(view.value, input);
    pendingAction.value = null;
    pendingTimer = undefined;
  }, 700);
};
const retry = (): void => { context.value = { ...context.value, connection: "online" }; };
const cycleTheme = (): void => { themeIndex.value = (themeIndex.value + 1) % meetByChanceThemes.length; applyTheme(); };
const toggleSound = (): void => { muted.value = !muted.value; document.documentElement.dataset.muted = String(muted.value); };
const leave = async (): Promise<void> => { await router.push({ name: "home" }); };
</script>

<template>
  <MeetByChanceReplayTable
    v-if="fixtureState === 'replay'"
    :replay="replay"
    :context="context"
    :muted="muted"
    @leave="leave"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
  <MeetByChanceTable
    v-else
    :view="view"
    :context="context"
    :allowed-actions="view.allowedActions"
    :pending-action="pendingAction"
    :muted="muted"
    @submit="submitAction"
    @retry="retry"
    @finish="view = finishMeetByChanceFixture(view)"
    @leave="leave"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
</template>
