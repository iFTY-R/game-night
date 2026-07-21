<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(defineProps<{ face: number; size?: "focus" | "seat" | "dense" | "micro"; decorative?: boolean }>(), {
  size: "seat",
  decorative: false,
});

const pips = computed<ReadonlySet<number>>(() => new Set(({
  1: [4], 2: [0, 8], 3: [0, 4, 8], 4: [0, 2, 6, 8], 5: [0, 2, 4, 6, 8], 6: [0, 2, 3, 5, 6, 8],
} as Readonly<Record<number, readonly number[]>>)[props.face] ?? []));
</script>

<template>
  <span
    class="meet-die"
    :class="`meet-die--${size}`"
    :role="decorative ? undefined : 'img'"
    :aria-hidden="decorative ? 'true' : undefined"
    :aria-label="decorative ? undefined : `${face} 点`"
  >
    <i v-for="index in 9" :key="index" :class="{ visible: pips.has(index - 1) }" />
  </span>
</template>

<style scoped>
.meet-die { --die-size: 22px; width: var(--die-size); height: var(--die-size); display: grid; grid-template: repeat(3, 1fr) / repeat(3, 1fr); flex: 0 0 auto; padding: calc(var(--die-size) * .14); background: var(--game-dice, #f2ecdf); border: 1px solid var(--game-dice-edge, #c6b9a3); border-radius: 5px; box-shadow: inset 0 -2px rgb(0 0 0 / 12%), 0 3px 7px rgb(0 0 0 / 18%); }
.meet-die i { width: 62%; aspect-ratio: 1; place-self: center; background: transparent; border-radius: 50%; }
.meet-die i.visible { background: var(--game-pip, #241e1a); }
.meet-die--focus { --die-size: 34px; border-radius: 6px; }
.meet-die--dense { --die-size: 18px; border-radius: 4px; box-shadow: inset 0 -1px rgb(0 0 0 / 12%); }
.meet-die--micro { --die-size: 14px; padding: 2px; border-radius: 3px; box-shadow: none; }
@media (orientation: landscape) { .meet-die--seat { --die-size: 18px; } }
</style>
