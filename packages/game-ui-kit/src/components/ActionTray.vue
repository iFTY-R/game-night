<script setup lang="ts">
import { ChevronDown, ChevronUp, GripHorizontal } from "lucide-vue-next";
import { computed, onBeforeUnmount, ref } from "vue";

import type { TrayState } from "../types";

const props = withDefaults(defineProps<{ modelValue: TrayState; pending?: boolean; label?: string }>(), {
  pending: false,
  label: "游戏操作",
});
const emit = defineEmits<{ "update:modelValue": [state: TrayState] }>();

const states: readonly TrayState[] = ["collapsed", "compact", "expanded"];
const pointerStart = ref<number>();
const suppressClick = ref(false);
let clickResetTimer: number | undefined;
const trayId = `tray-${Math.random().toString(36).slice(2)}`;
const expanded = computed(() => props.modelValue === "expanded");

const changeBy = (offset: number): void => {
  const index = states.indexOf(props.modelValue);
  emit("update:modelValue", states[Math.min(Math.max(index + offset, 0), states.length - 1)] ?? props.modelValue);
};

const toggle = (): void => {
  // Browsers emit click after pointerup; suppress that click when the gesture already snapped the tray.
  if (suppressClick.value) {
    return;
  }
  changeBy(props.modelValue === "expanded" ? -1 : 1);
};

const startDrag = (event: PointerEvent): void => {
  pointerStart.value = event.clientY;
  (event.currentTarget as HTMLElement).setPointerCapture?.(event.pointerId);
};

const finishDrag = (event: PointerEvent): void => {
  if (pointerStart.value === undefined) {
    return;
  }
  const distance = pointerStart.value - event.clientY;
  pointerStart.value = undefined;
  if (Math.abs(distance) >= 28) {
    suppressClick.value = true;
    clickResetTimer = window.setTimeout(() => {
      suppressClick.value = false;
      clickResetTimer = undefined;
    }, 0);
    changeBy(distance > 0 ? 1 : -1);
  }
};

onBeforeUnmount(() => {
  if (clickResetTimer !== undefined) {
    window.clearTimeout(clickResetTimer);
  }
});
</script>

<template>
  <section
    class="gn-tray"
    :class="`is-${modelValue}`"
    :aria-label="label"
    :aria-busy="pending"
    :data-state="modelValue"
  >
    <button
      class="gn-tray__handle"
      type="button"
      :aria-expanded="expanded"
      :aria-controls="trayId"
      :title="expanded ? '收起操作区' : '展开操作区'"
      @click="toggle"
      @pointerdown="startDrag"
      @pointerup="finishDrag"
      @pointercancel="pointerStart = undefined"
    >
      <GripHorizontal :size="24" aria-hidden="true" />
      <ChevronDown v-if="expanded" :size="17" aria-hidden="true" />
      <ChevronUp v-else :size="17" aria-hidden="true" />
    </button>
    <div :id="trayId" class="gn-tray__content">
      <div class="gn-tray__summary"><slot name="summary" /></div>
      <div v-show="modelValue !== 'collapsed'" class="gn-tray__primary"><slot name="primary" /></div>
      <div v-show="modelValue === 'expanded'" class="gn-tray__details"><slot name="details" /></div>
    </div>
  </section>
</template>

<style scoped>
.gn-tray {
  --tray-height: min(11dvh, 96px);
  position: absolute;
  inset-inline: 0;
  bottom: 0;
  z-index: 20;
  height: var(--tray-height);
  padding: 15px max(12px, env(safe-area-inset-right)) max(8px, env(safe-area-inset-bottom)) max(12px, env(safe-area-inset-left));
  color: var(--platform-ink, #f5f1e8);
  background: color-mix(in srgb, var(--platform-surface, #121a1d) 96%, var(--game-table, #173b38));
  border-top: 1px solid color-mix(in srgb, var(--platform-accent, #e6b566) 34%, transparent);
  box-shadow: 0 -14px 34px rgb(0 0 0 / 28%);
  transition: height 220ms cubic-bezier(.2, .8, .2, 1);
}

.gn-tray.is-compact {
  --tray-height: min(23dvh, 194px);
}

.gn-tray.is-expanded {
  --tray-height: min(41dvh, 360px);
}

.gn-tray__handle {
  position: absolute;
  top: -17px;
  left: 50%;
  width: 58px;
  height: 34px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 2px;
  padding: 0;
  color: var(--platform-muted, #a8b5b4);
  background: var(--platform-surface, #121a1d);
  border: 1px solid color-mix(in srgb, var(--platform-accent, #e6b566) 34%, transparent);
  border-radius: 7px;
  transform: translateX(-50%);
  touch-action: none;
  cursor: ns-resize;
}

.gn-tray__handle:focus-visible {
  outline: 3px solid var(--platform-focus, #86d6ca);
  outline-offset: 2px;
}

.gn-tray__content {
  height: 100%;
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-width: thin;
}

.gn-tray__summary,
.gn-tray__primary,
.gn-tray__details {
  width: min(100%, 680px);
  margin-inline: auto;
}

.gn-tray__summary {
  min-height: 38px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.gn-tray__primary {
  padding-top: 8px;
}

.gn-tray__details {
  padding: 12px 0 4px;
}

@media (orientation: landscape) {
  .gn-tray {
    --tray-height: min(18dvh, 72px);
    padding-top: 12px;
  }

  .gn-tray.is-compact {
    --tray-height: min(31dvh, 122px);
  }

  .gn-tray.is-expanded {
    --tray-height: min(45dvh, 176px);
  }

  .gn-tray__handle {
    top: -15px;
    height: 30px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .gn-tray {
    transition: none;
  }
}
</style>
