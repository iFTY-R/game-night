<script setup lang="ts">
import { Crown, Target, WifiOff } from "lucide-vue-next";
import { computed, ref } from "vue";

import type { PublicPlayer } from "./generated/game/meet_by_chance/v1/meet_by_chance_pb";
import { handClassLabel, special235Label } from "./controls";
import MeetDiceFace from "./MeetDiceFace.vue";
import type { MeetByChancePlayerPresentation } from "./types";

const props = withDefaults(defineProps<{
  player: PublicPlayer;
  presentation: MeetByChancePlayerPresentation;
  target?: boolean;
  matched?: boolean;
  density?: "normal" | "dense" | "micro";
}>(), { target: false, matched: false, density: "normal" });

const usedWild = computed(() => props.player.dice.join(",") !== props.player.normalizedDice.join(","));
const handLabel = computed(() => special235Label(props.player.special235Outcome) ?? handClassLabel(props.player.handClass));
const detailsOpen = ref(false);
const detailId = computed(() => `meet-seat-info-${props.player.userId.replace(/[^a-zA-Z0-9_-]/g, "-")}-${props.player.seatIndex}`);

/** Keeps projected governance actions usable while dismissing the detail popover after focus leaves the seat. */
const handleFocusOut = (event: FocusEvent): void => {
  const nextTarget = event.relatedTarget;
  if (nextTarget instanceof Node && (event.currentTarget as HTMLElement).contains(nextTarget)) return;
  detailsOpen.value = false;
};
</script>

<template>
  <article
    class="meet-seat"
    :class="[`is-${density}`, { 'is-target': target, 'is-matched': matched, 'is-offline': !presentation.connected, 'is-inactive': !player.active }]"
    :aria-label="`${presentation.displayName}，${handLabel}，骰子 ${player.dice.join('、')}，累计 ${player.penaltyTicks} 罚点${target ? '，当前靶子' : ''}${!target && player.targetedThisRound ? '，本轮曾为靶子' : ''}${usedWild ? `，百搭规范为 ${player.normalizedDice.join('、')}` : ''}`"
    @focusout="handleFocusOut"
  >
    <button
      class="meet-seat__card"
      type="button"
      :aria-expanded="detailsOpen"
      :aria-controls="detailId"
      @click="detailsOpen = !detailsOpen"
      @keydown.escape.stop="detailsOpen = false"
    >
      <header>
        <span class="seat-avatar" aria-hidden="true">{{ presentation.avatarText ?? presentation.displayName.slice(0, 1) }}</span>
        <strong>{{ presentation.displayName }}</strong>
        <Target v-if="target" class="seat-mark target-mark" :size="16" aria-label="当前靶子" />
        <Target v-else-if="player.targetedThisRound" class="seat-mark former-target-mark" :size="14" aria-label="本轮曾为靶子" />
        <Crown v-else-if="presentation.host" class="seat-mark" :size="14" aria-label="房主" />
        <WifiOff v-else-if="!presentation.connected" class="seat-mark" :size="14" aria-hidden="true" />
      </header>
      <span class="seat-dice" role="group" :aria-label="`${presentation.displayName} 的公开骰子`">
        <MeetDiceFace v-for="(face, index) in player.dice" :key="index" :face="face" :size="density === 'normal' ? 'seat' : density" decorative />
      </span>
      <span class="seat-footer">
        <b>{{ handLabel }}</b>
        <span>{{ player.penaltyTicks }} 罚点</span>
        <em v-if="usedWild">百搭 {{ player.normalizedDice.join('') }}</em>
      </span>
    </button>
    <div v-if="detailsOpen" :id="detailId" class="meet-seat__details" role="dialog" :aria-label="`${presentation.displayName} 的完整信息`">
      <strong>{{ presentation.displayName }}</strong>
      <span>{{ handLabel }} · 累计 {{ player.penaltyTicks }} 罚点</span>
      <slot name="details" />
    </div>
  </article>
</template>

<style scoped>
.meet-seat { position: relative; width: var(--meet-seat-width, 126px); min-height: var(--meet-seat-height, 78px); color: var(--platform-ink); transform: translate(-50%, -50%); }
.meet-seat__card { width: 100%; min-height: inherit; display: grid; grid-template-rows: auto auto auto; gap: 4px; padding: 6px 7px; color: inherit; font: inherit; text-align: left; background: color-mix(in srgb, var(--game-seat, var(--platform-surface-raised)) 92%, transparent); border: 1px solid color-mix(in srgb, var(--platform-muted) 34%, transparent); border-radius: 7px; box-shadow: 0 8px 20px rgb(0 0 0 / 22%); transition: border-color var(--game-motion-fast, 170ms) ease, box-shadow var(--game-motion-fast, 170ms) ease; }
.meet-seat__card:focus-visible { outline: 2px solid var(--platform-focus); outline-offset: 3px; }
.meet-seat header { min-width: 0; display: grid; grid-template-columns: 24px minmax(0, 1fr) 17px; align-items: center; gap: 5px; }
.seat-avatar { width: 24px; height: 24px; display: grid; place-items: center; color: #171916; background: var(--platform-accent); border-radius: 50%; font-size: 11px; font-weight: 900; }
.meet-seat header strong { overflow: hidden; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.seat-mark { color: var(--platform-accent); }
.target-mark { color: var(--game-target, var(--platform-accent)); }
.former-target-mark { color: var(--platform-muted); opacity: .72; }
.seat-dice { display: flex; justify-content: center; gap: 4px; }
.seat-footer { min-width: 0; display: flex; align-items: center; justify-content: space-between; gap: 4px; }
.seat-footer b { overflow: hidden; color: var(--platform-accent); font-size: 9px; text-overflow: ellipsis; white-space: nowrap; }
.seat-footer span { flex: 0 0 auto; color: var(--platform-muted); font-size: 8px; }
.seat-footer em { display: none; color: var(--game-match); font-size: 8px; font-style: normal; }
.meet-seat.is-target .meet-seat__card { border-color: var(--game-target, var(--platform-accent)); box-shadow: 0 0 0 2px color-mix(in srgb, var(--game-target, var(--platform-accent)) 28%, transparent), 0 10px 24px rgb(0 0 0 / 28%); }
.meet-seat.is-matched:not(.is-target) .meet-seat__card { border-color: var(--game-match, var(--platform-focus)); }
.meet-seat.is-offline .meet-seat__card { opacity: .74; }
.meet-seat.is-inactive .meet-seat__card { filter: grayscale(.65); opacity: .55; }
.meet-seat.is-dense .meet-seat__card { gap: 3px; padding: 5px 6px; }
.meet-seat.is-dense header { grid-template-columns: 21px minmax(0, 1fr) 15px; gap: 4px; }
.meet-seat.is-dense .seat-avatar { width: 21px; height: 21px; font-size: 10px; }
.meet-seat.is-dense .seat-dice { gap: 3px; }
.meet-seat.is-micro .meet-seat__card { gap: 2px; padding: 4px; }
.meet-seat.is-micro header { grid-template-columns: 18px minmax(0, 1fr) 13px; gap: 3px; }
.meet-seat.is-micro .seat-avatar { width: 18px; height: 18px; font-size: 8px; }
.meet-seat.is-micro header strong { font-size: 9px; }
.meet-seat.is-micro .seat-dice { gap: 2px; }
.meet-seat.is-micro .seat-footer b { font-size: 8px; }
.meet-seat.is-micro .seat-footer span { display: none; }
.meet-seat__details { position: absolute; z-index: 5; bottom: calc(100% + 7px); left: 50%; width: max-content; max-width: min(220px, calc(100vw - 24px)); display: grid; gap: 5px; padding: 9px; color: var(--platform-muted); background: var(--platform-surface-raised); border: 1px solid color-mix(in srgb, var(--platform-accent) 42%, transparent); border-radius: 8px; box-shadow: 0 14px 34px rgb(0 0 0 / 36%); font-size: 10px; transform: translateX(-50%); }
.meet-seat__details > strong { color: var(--platform-accent); font-size: 12px; }
@media (orientation: landscape) {
  .meet-seat__card { gap: 2px; padding: 4px 6px; }
  .meet-seat header { grid-template-columns: 20px minmax(0, 1fr) 15px; gap: 4px; }
  .seat-avatar { width: 20px; height: 20px; font-size: 9px; }
  .seat-footer b { font-size: 8px; }
}
@media (prefers-reduced-motion: reduce) { .meet-seat__card { transition: none; } }
</style>
