<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

import {
  DICE_789_CONFIRM_ACTION,
  DICE_789_DROPPED_ACTION,
  DICE_789_ROLL_ACTION,
  Dice789ReplayTable,
  Dice789Table,
  Phase as Dice789Phase,
  applyDice789FixtureAction,
  createFinishAction,
  dice789FixtureContext,
  dice789FixtureView,
  dice789Reducer,
  dice789ReplayFixture,
  finishDice789Fixture,
  type Dice789ActionInput,
  type Dice789FixtureState,
  type Dice789TableContext,
  type Dice789View,
} from "@game-night/dice-789-client";
import { classicTheme, dice789SoundProfile, dice789Themes } from "@game-night/dice-789-themes";
import { ThemeRuntime, safeTheme } from "@game-night/theme-system";

import { useRoomStore } from "../stores/room";
import { useLiveGameTable } from "../composables/use-live-game-table";

const props = withDefaults(defineProps<{ roomId?: string; sessionId?: string; fixtureState?: Dice789FixtureState }>(), {
  roomId: "fixture-room",
  sessionId: "fixture-session",
  fixtureState: "active",
});
const router = useRouter();
const room = useRoomStore();
const themeRuntime = new ThemeRuntime();
const view = ref(dice789FixtureView(props.fixtureState));
const replay = dice789ReplayFixture();
const context = ref<Dice789TableContext>({
  ...dice789FixtureContext(room.displayName || "你", props.fixtureState),
  roomCode: room.roomCode ?? "D789",
});
const fixtureMode = computed(() => props.roomId === "fixture-room");
const liveTable = useLiveGameTable<Dice789View, Dice789TableContext>({
  roomId: props.roomId,
  sessionId: props.sessionId,
  fixtureMode,
  reducer: dice789Reducer,
  view,
  context,
  players: (current) => current.players,
  viewActions: (current) => current.allowedActions,
  finished: (current) => current.phase === Dice789Phase.FINISHED,
});
const allowedActions = liveTable.allowedActions;
const pendingAction = liveTable.pendingAction;
const muted = ref(false);
const themeIndex = ref(0);
let pendingTimer: number | undefined;
let audioContext: AudioContext | undefined;

const applyTheme = (): void => {
  const manifest = dice789Themes[themeIndex.value] ?? classicTheme;
  themeRuntime.apply({ manifest, assets: new Map(), usedFallback: false, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "false";
  document.documentElement.dataset.muted = String(muted.value);
};

const playSound = (cue: "roll" | "effect"): void => {
  if (muted.value || typeof window.AudioContext === "undefined") return;
  try {
    audioContext ??= new window.AudioContext();
    const profile = dice789SoundProfile(dice789Themes[themeIndex.value]?.themeId ?? classicTheme.themeId);
    const oscillator = audioContext.createOscillator();
    const gain = audioContext.createGain();
    const endsAt = audioContext.currentTime + profile.durationMs / 1000;
    oscillator.type = "triangle";
    oscillator.frequency.value = cue === "roll" ? profile.rollHz : profile.effectHz;
    gain.gain.setValueAtTime(0.035, audioContext.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.0001, endsAt);
    oscillator.connect(gain).connect(audioContext.destination);
    oscillator.start();
    oscillator.stop(endsAt);
  } catch {
    // Optional audio feedback must never block an authoritative action.
  }
};

onMounted(applyTheme);
onBeforeUnmount(() => {
  if (pendingTimer !== undefined) window.clearTimeout(pendingTimer);
  if (audioContext !== undefined) void audioContext.close();
  themeRuntime.apply({ manifest: safeTheme, assets: new Map(), usedFallback: true, errorCode: null }, document.documentElement);
  document.documentElement.dataset.themeFallback = "true";
});

const submitAction = async (input: Dice789ActionInput): Promise<void> => {
  if (pendingAction.value !== null || context.value.connection !== "online") return;
  playSound(input.action === DICE_789_ROLL_ACTION ? "roll" : "effect");
  if (await liveTable.submitLiveAction(input)) return;
  pendingAction.value = input.action;
  pendingTimer = window.setTimeout(() => {
    view.value = applyDice789FixtureAction(view.value, input);
    pendingAction.value = null;
    pendingTimer = undefined;
  }, input.action === DICE_789_CONFIRM_ACTION || input.action === DICE_789_DROPPED_ACTION ? 850 : 650);
};
const finishSession = async (): Promise<void> => {
  if (await liveTable.finishLiveSession(createFinishAction(room.userId).message)) return;
  view.value = finishDice789Fixture(view.value);
};
const cycleTheme = (): void => { themeIndex.value = (themeIndex.value + 1) % dice789Themes.length; applyTheme(); };
const toggleSound = (): void => { muted.value = !muted.value; document.documentElement.dataset.muted = String(muted.value); };
const leave = async (): Promise<void> => {
  await router.push(fixtureMode.value ? { name: "home" } : { name: "room", params: { roomId: props.roomId } });
};
</script>

<template>
  <Dice789ReplayTable
    v-if="fixtureState === 'replay'"
    :replay="replay"
    :context="context"
    :muted="muted"
    @leave="leave"
    @toggle-sound="toggleSound"
    @cycle-theme="cycleTheme"
  />
  <Dice789Table
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
