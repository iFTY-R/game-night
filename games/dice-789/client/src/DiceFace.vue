<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(defineProps<{ face: number; size?: "large" | "small"; decorative?: boolean }>(), {
  size: "large",
  decorative: false,
});

const pips = computed<ReadonlySet<number>>(() => new Set(({
  1: [4], 2: [0, 8], 3: [0, 4, 8], 4: [0, 2, 6, 8], 5: [0, 2, 4, 6, 8], 6: [0, 2, 3, 5, 6, 8],
} as Readonly<Record<number, readonly number[]>>)[props.face] ?? []));
</script>

<template>
  <span
    class="d789-die"
    :class="`d789-die--${size}`"
    :role="decorative ? undefined : 'img'"
    :aria-hidden="decorative ? 'true' : undefined"
    :aria-label="decorative ? undefined : `${face} 点`"
  >
    <i v-for="index in 9" :key="index" :class="{ visible: pips.has(index - 1) }" />
  </span>
</template>

<style scoped>
.d789-die {
  --die-size: 54px;
  width: var(--die-size);
  height: var(--die-size);
  display: grid;
  grid-template: repeat(3, 1fr) / repeat(3, 1fr);
  flex: 0 0 auto;
  padding: calc(var(--die-size) * .15);
  background: var(--game-dice, #f3eee3);
  border: 1px solid var(--game-dice-edge, #c8bca7);
  border-radius: 7px;
  box-shadow: inset 0 -4px rgb(0 0 0 / 12%), 0 5px 12px rgb(0 0 0 / 24%);
}
.d789-die i { width: 64%; aspect-ratio: 1; place-self: center; background: transparent; border-radius: 50%; }
.d789-die i.visible { background: var(--game-pip, #221e1a); }
.d789-die--small { --die-size: 24px; padding: 3px; border-radius: 4px; box-shadow: inset 0 -2px rgb(0 0 0 / 12%); }
</style>
