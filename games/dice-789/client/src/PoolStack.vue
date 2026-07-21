<script setup lang="ts">
import { computed } from "vue";

import type { PoolLayer } from "./generated/game/dice789/v1/dice_789_pb";
import { formatTicks } from "./controls";

const props = defineProps<{ layers: readonly PoolLayer[]; totalTicks: number; layerCapacityTicks: number }>();
const label = computed(() => `公共池 ${formatTicks(props.totalTicks)}，${props.layers.length} 层`);
</script>

<template>
  <div class="pool-stack" role="img" :aria-label="label">
    <div class="pool-stack__layers">
      <div v-for="layer in layers" :key="layer.index" class="pool-cup">
        <span :style="{ height: `${Math.min(100, layerCapacityTicks === 0 ? 0 : layer.ticks / layerCapacityTicks * 100)}%` }" />
        <b>{{ layer.ticks }}</b>
      </div>
    </div>
    <div class="pool-stack__total"><small>公共池</small><strong>{{ formatTicks(totalTicks) }}</strong></div>
  </div>
</template>

<style scoped>
.pool-stack { display: flex; align-items: center; justify-content: center; gap: 9px; color: var(--platform-ink); }
.pool-stack__layers { display: flex; align-items: end; min-width: 44px; }
.pool-cup {
  position: relative;
  width: 32px;
  height: 38px;
  margin-left: -10px;
  overflow: hidden;
  background: color-mix(in srgb, var(--game-pool, #b64532) 12%, transparent);
  border: 2px solid var(--game-cup-edge, #d7c9ad);
  border-top-width: 4px;
  border-radius: 4px 4px 10px 10px;
  box-shadow: 0 5px 10px rgb(0 0 0 / 24%);
}
.pool-cup:first-child { margin-left: 0; }
.pool-cup span { position: absolute; inset: auto 0 0; background: var(--game-pool, #b64532); opacity: .86; }
.pool-cup b { position: relative; z-index: 1; display: grid; height: 100%; place-items: center; color: #fff8ec; font-size: 10px; text-shadow: 0 1px 2px #000; }
.pool-stack__total { display: grid; gap: 1px; text-align: left; }
.pool-stack__total small { color: var(--platform-muted); font-size: 9px; }
.pool-stack__total strong { font-size: 12px; white-space: nowrap; }
</style>
