<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(
  defineProps<{
    face: number;
    variant?: "focus" | "private" | "picker" | "tiny";
    decorative?: boolean;
  }>(),
  { variant: "focus", decorative: false },
);

const visiblePips = computed<ReadonlySet<number>>(() => {
  const patterns: Readonly<Record<number, readonly number[]>> = {
    1: [4],
    2: [0, 8],
    3: [0, 4, 8],
    4: [0, 2, 6, 8],
    5: [0, 2, 4, 6, 8],
    6: [0, 2, 3, 5, 6, 8],
  };
  return new Set(patterns[props.face] ?? []);
});
</script>

<template>
  <span
    class="liars-die"
    :class="`liars-die--${variant}`"
    :role="decorative ? undefined : 'img'"
    :aria-hidden="decorative ? 'true' : undefined"
    :aria-label="decorative ? undefined : `${face} 点`"
  >
    <i v-for="index in 9" :key="index" :class="{ visible: visiblePips.has(index - 1) }" />
  </span>
</template>

<style scoped>
.liars-die {
  --die-size: 32px;
  width: var(--die-size);
  height: var(--die-size);
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  grid-template-rows: repeat(3, 1fr);
  flex: 0 0 auto;
  padding: calc(var(--die-size) * .15);
  background: var(--game-dice, #f0eadc);
  border: 1px solid var(--game-dice-edge, #c9bda8);
  border-radius: 6px;
  box-shadow: inset 0 -3px rgb(0 0 0 / 12%), 0 3px 8px rgb(0 0 0 / 20%);
}

.liars-die i { width: 64%; aspect-ratio: 1; place-self: center; background: transparent; border-radius: 50%; }
.liars-die i.visible { background: var(--game-pip, #231f1b); box-shadow: inset 0 1px rgb(255 255 255 / 12%); }
.liars-die--focus { --die-size: 42px; margin-inline: 2px; }
.liars-die--private { --die-size: 34px; }
.liars-die--picker { --die-size: 26px; border-radius: 5px; box-shadow: inset 0 -2px rgb(0 0 0 / 12%); }
.liars-die--tiny { --die-size: 20px; display: inline-grid; margin-left: 2px; padding: 3px; border-radius: 4px; box-shadow: none; }
</style>
