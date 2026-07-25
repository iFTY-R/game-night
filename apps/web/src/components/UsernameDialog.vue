<script lang="ts">
/** Selects ordinary profile copy or the room-entry recovery copy without exposing form internals. */
export type UsernameDialogMode = "profile" | "room-conflict";
/** Reports the authoritative username together with the page flow that requested the change. */
export interface UsernameChangedEvent {
  username: string;
  mode: UsernameDialogMode;
}
/** Public dialog controls used by page-level profile triggers and conflict recovery actions. */
export interface UsernameDialogHandle {
  open: (mode?: UsernameDialogMode) => void;
  close: () => void;
}
</script>

<script setup lang="ts">
import { X } from "lucide-vue-next";
import { computed, nextTick, onBeforeUnmount, ref } from "vue";

import { useRoomStore } from "../stores/room";
import { USERNAME_RULE_MESSAGE, validateUsernameInput } from "../username";

const emit = defineEmits<{ changed: [event: UsernameChangedEvent] }>();
const room = useRoomStore();
const dialogRef = ref<HTMLDialogElement | null>(null);
const inputRef = ref<HTMLInputElement | null>(null);
const username = ref("");
const mode = ref<UsernameDialogMode>("profile");
const error = ref("");
const submitting = ref(false);
let returnFocus: HTMLElement | null = null;

const validation = computed(() => validateUsernameInput(username.value));
const currentValidation = computed(() => validateUsernameInput(room.displayName));
const inputInvalid = computed(() => username.value.length > 0 && !validation.value.isValid);
const unchanged = computed(() => validation.value.isValid && validation.value.normalized === currentValidation.value.normalized);
const canSubmit = computed(() => validation.value.isValid && !unchanged.value && !submitting.value);
const title = computed(() => mode.value === "room-conflict" ? "换个名字进入房间" : "修改用户名");
const description = computed(() => mode.value === "room-conflict" ? "房间内已有同名玩家" : `当前用户名：${room.displayName}`);
const submitLabel = computed(() => submitting.value ? "正在保存" : mode.value === "room-conflict" ? "改名并进房" : "保存用户名");

/** Opens one fresh form session and records the element that should regain focus after every close path. */
const open = (nextMode: UsernameDialogMode = "profile"): void => {
  if (submitting.value) return;
  returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  mode.value = nextMode;
  username.value = room.displayName;
  error.value = "";
  void nextTick(() => {
    const dialog = dialogRef.value;
    if (!dialog) return;
    if (!dialog.hasAttribute("open")) {
      if (typeof dialog.showModal === "function") dialog.showModal();
      else dialog.setAttribute("open", "");
    }
    inputRef.value?.focus();
    inputRef.value?.select();
  });
};

/** Clears form-only state and returns keyboard focus without changing the committed room identity. */
const cleanup = (): void => {
  username.value = "";
  error.value = "";
  mode.value = "profile";
  const target = returnFocus;
  returnFocus = null;
  queueMicrotask(() => target?.focus());
};

const close = (): void => {
  const dialog = dialogRef.value;
  if (!dialog?.hasAttribute("open")) return;
  if (typeof dialog.close === "function") {
    dialog.close();
    return;
  }
  dialog.removeAttribute("open");
  cleanup();
};

const handleCancel = (event: Event): void => {
  event.preventDefault();
  if (!submitting.value) close();
};

const handleBackdrop = (event: MouseEvent): void => {
  if (event.target === dialogRef.value && !submitting.value) close();
};

/** Commits through the device-authenticated store before notifying the page about any deferred room action. */
const submit = async (): Promise<void> => {
  if (!validation.value.isValid) {
    username.value = validation.value.normalized;
    error.value = `用户名需要 ${USERNAME_RULE_MESSAGE}`;
    return;
  }
  if (unchanged.value) {
    error.value = "新用户名需要与当前用户名不同";
    return;
  }
  submitting.value = true;
  error.value = "";
  try {
    const submittedMode = mode.value;
    await room.changeUsername(validation.value.normalized);
    const changed = room.displayName;
    submitting.value = false;
    emit("changed", { username: changed, mode: submittedMode });
    close();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : "修改用户名失败";
    submitting.value = false;
  }
};

onBeforeUnmount(() => {
  returnFocus?.focus();
  returnFocus = null;
});

defineExpose({ open, close });
</script>

<template>
  <Teleport to="body">
    <dialog
      ref="dialogRef"
      class="username-dialog"
      aria-labelledby="username-dialog-title"
      aria-describedby="username-dialog-description"
      @cancel="handleCancel"
      @close="cleanup"
      @click="handleBackdrop"
    >
      <div class="username-dialog__surface">
        <header class="username-dialog__header">
          <div>
            <p class="username-dialog__eyebrow">个人资料</p>
            <h2 id="username-dialog-title">{{ title }}</h2>
          </div>
          <button class="username-dialog__close" type="button" title="关闭" :disabled="submitting" @click="close">
            <X :size="19" aria-hidden="true" />
          </button>
        </header>

        <p id="username-dialog-description" class="username-dialog__description">{{ description }}</p>

        <form class="username-dialog__form" :aria-busy="submitting" @submit.prevent="submit">
          <label for="username-dialog-input">用户名</label>
          <input
            id="username-dialog-input"
            ref="inputRef"
            v-model="username"
            autocomplete="nickname"
            maxlength="8"
            :aria-invalid="inputInvalid ? 'true' : 'false'"
            aria-describedby="username-dialog-hint"
          />
          <p id="username-dialog-hint" class="username-dialog__hint" :class="{ 'is-invalid': inputInvalid }">
            {{ USERNAME_RULE_MESSAGE }}
          </p>
          <p v-if="error" class="username-dialog__error" role="alert">{{ error }}</p>
          <div class="username-dialog__actions">
            <button class="button button--quiet" type="button" :disabled="submitting" @click="close">取消</button>
            <button class="button username-dialog__submit" type="submit" :disabled="!canSubmit">{{ submitLabel }}</button>
          </div>
        </form>
      </div>
    </dialog>
  </Teleport>
</template>

<style scoped>
.username-dialog {
  width: min(420px, calc(100% - 32px));
  max-height: min(640px, calc(100dvh - 32px));
  margin: auto;
  padding: 0;
  color: var(--platform-ink);
  background: transparent;
  border: 0;
  overflow: visible;
}

.username-dialog::backdrop { background: rgb(3 9 10 / 72%); backdrop-filter: blur(5px); }

.username-dialog__surface {
  max-height: min(640px, calc(100dvh - 32px));
  display: grid;
  gap: 18px;
  padding: 22px;
  overflow: auto;
  background: color-mix(in srgb, var(--platform-surface-raised) 94%, black);
  border: 1px solid rgb(168 181 180 / 24%);
  border-radius: 8px;
  box-shadow: 0 24px 80px rgb(0 0 0 / 44%);
  animation: username-dialog-enter 180ms ease-out both;
}

.username-dialog__header { display: flex; align-items: start; justify-content: space-between; gap: 18px; }
.username-dialog__header h2 { margin: 0; font-size: 24px; line-height: 1.1; }
.username-dialog__eyebrow { margin: 0 0 6px; color: var(--platform-accent); font-size: 11px; font-weight: 800; }
.username-dialog__close { width: 40px; height: 40px; flex: 0 0 40px; display: grid; place-items: center; padding: 0; color: var(--platform-muted); background: transparent; border: 1px solid rgb(168 181 180 / 22%); border-radius: 50%; }
.username-dialog__description { margin: 0; color: var(--platform-muted); font-size: 13px; }
.username-dialog__form { display: grid; gap: 10px; }
.username-dialog__form label { color: var(--platform-muted); font-size: 12px; font-weight: 800; }
.username-dialog__form input { width: 100%; min-height: 52px; padding: 0 14px; color: var(--platform-ink); background: rgb(8 18 19 / 64%); border: 1px solid rgb(168 181 180 / 30%); border-radius: 7px; }
.username-dialog__form input[aria-invalid="true"] { border-color: var(--platform-danger); }
.username-dialog__hint { margin: -2px 0 0; color: var(--platform-muted); font-size: 12px; }
.username-dialog__hint.is-invalid,
.username-dialog__error { color: var(--platform-danger); }
.username-dialog__error { margin: 2px 0 0; font-size: 13px; }
.username-dialog__actions { display: grid; grid-template-columns: minmax(0, .7fr) minmax(0, 1.3fr); gap: 10px; margin-top: 8px; }

@keyframes username-dialog-enter {
  from { opacity: 0; transform: translateY(10px); }
  to { opacity: 1; transform: translateY(0); }
}

@media (max-width: 640px) {
  .username-dialog {
    width: 100%;
    max-width: none;
    max-height: calc(100dvh - max(12px, env(safe-area-inset-top)));
    margin: auto 0 0;
  }

  .username-dialog__surface {
    max-height: calc(100dvh - max(12px, env(safe-area-inset-top)));
    padding: 20px max(18px, env(safe-area-inset-right)) max(20px, env(safe-area-inset-bottom)) max(18px, env(safe-area-inset-left));
    border-right: 0;
    border-bottom: 0;
    border-left: 0;
    border-radius: 8px 8px 0 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .username-dialog__surface { animation: none; }
}
</style>
