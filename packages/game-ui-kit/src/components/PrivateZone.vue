<script setup lang="ts">
withDefaults(defineProps<{ label?: string }>(), { label: "你的私密区域" });
</script>

<template>
  <section class="gn-private" :aria-label="label">
    <slot />
  </section>
</template>

<style scoped>
.gn-private {
  min-width: 152px;
  min-height: 54px;
  display: grid;
  place-items: center;
  padding: 7px 12px;
  color: var(--platform-ink, #f5f1e8);
  background: color-mix(in srgb, var(--platform-surface, #121a1d) 86%, transparent);
  border: 1px solid color-mix(in srgb, var(--platform-accent, #e6b566) 38%, transparent);
  border-radius: 7px;
  box-shadow: 0 10px 26px rgb(0 0 0 / 26%);
  backdrop-filter: blur(8px);
}

@media (orientation: landscape) {
  .gn-private {
    min-height: 42px;
    padding-block: 4px;
  }
}
</style>
