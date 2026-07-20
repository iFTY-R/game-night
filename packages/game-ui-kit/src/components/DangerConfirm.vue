<script setup lang="ts">
import { AlertTriangle, X } from "lucide-vue-next";
import { nextTick, ref, watch } from "vue";

const props = withDefaults(
  defineProps<{ open: boolean; title: string; confirmLabel?: string; cancelLabel?: string }>(),
  { confirmLabel: "确认", cancelLabel: "取消" },
);
const emit = defineEmits<{ confirm: []; cancel: [] }>();
const confirmButton = ref<HTMLButtonElement>();

watch(
  () => props.open,
  async (open) => {
    if (open) {
      await nextTick();
      confirmButton.value?.focus();
    }
  },
);
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="gn-confirm" role="presentation" @keydown.esc="emit('cancel')">
      <section class="gn-confirm__dialog" role="alertdialog" aria-modal="true" :aria-label="title">
        <button class="gn-confirm__close" type="button" title="关闭" @click="emit('cancel')">
          <X :size="20" aria-hidden="true" />
        </button>
        <AlertTriangle class="gn-confirm__icon" :size="26" aria-hidden="true" />
        <h2>{{ title }}</h2>
        <div class="gn-confirm__body"><slot /></div>
        <div class="gn-confirm__actions">
          <button type="button" class="is-quiet" @click="emit('cancel')">{{ cancelLabel }}</button>
          <button ref="confirmButton" type="button" class="is-danger" @click="emit('confirm')">{{ confirmLabel }}</button>
        </div>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.gn-confirm {
  position: fixed;
  inset: 0;
  z-index: 100;
  display: grid;
  place-items: center;
  padding: 20px;
  background: rgb(5 9 10 / 74%);
}

.gn-confirm__dialog {
  position: relative;
  width: min(100%, 360px);
  padding: 22px;
  color: var(--platform-ink, #f5f1e8);
  background: var(--platform-surface-raised, #1b292d);
  border: 1px solid color-mix(in srgb, var(--platform-danger, #e77c65) 48%, transparent);
  border-radius: 8px;
  box-shadow: 0 26px 70px rgb(0 0 0 / 46%);
}

.gn-confirm__dialog h2 {
  margin: 9px 0 8px;
  font-size: 18px;
}

.gn-confirm__body {
  color: var(--platform-muted, #a8b5b4);
  line-height: 1.55;
}

.gn-confirm__icon {
  color: var(--platform-danger, #e77c65);
}

.gn-confirm__close {
  position: absolute;
  top: 10px;
  right: 10px;
  width: 40px;
  height: 40px;
  display: grid;
  place-items: center;
  padding: 0;
  color: var(--platform-muted, #a8b5b4);
  background: transparent;
  border: 0;
}

.gn-confirm__actions {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-top: 20px;
}

.gn-confirm__actions button {
  min-height: var(--platform-touch-min, 48px);
  border-radius: 7px;
  font: inherit;
  font-weight: 700;
}

.is-quiet {
  color: var(--platform-ink, #f5f1e8);
  background: transparent;
  border: 1px solid color-mix(in srgb, var(--platform-muted, #a8b5b4) 38%, transparent);
}

.is-danger {
  color: #1c1110;
  background: var(--platform-danger, #e77c65);
  border: 1px solid transparent;
}

button:focus-visible {
  outline: 3px solid var(--platform-focus, #86d6ca);
  outline-offset: 2px;
}
</style>
