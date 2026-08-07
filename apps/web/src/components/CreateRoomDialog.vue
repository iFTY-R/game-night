<script lang="ts">
export type RoomVisibility = "ROOM_VISIBILITY_PRIVATE" | "ROOM_VISIBILITY_PUBLIC";

/** Public controls used by the discovery page to start or dismiss one room-creation choice. */
export interface CreateRoomDialogHandle {
  open: () => void;
  close: () => void;
}
</script>

<script setup lang="ts">
import { ArrowRight, Globe2, LockKeyhole, X } from "lucide-vue-next";
import { nextTick, onBeforeUnmount, ref } from "vue";

const props = withDefaults(defineProps<{ gameName: string; pending?: boolean; error?: string }>(), {
  pending: false,
  error: "",
});
const emit = defineEmits<{ choose: [visibility: RoomVisibility] }>();
const dialogRef = ref<HTMLDialogElement | null>(null);
const activeVisibility = ref<RoomVisibility | null>(null);
let returnFocus: HTMLElement | null = null;

/** Starts a fresh choice and records the trigger so every close path restores keyboard focus. */
const open = (): void => {
  if (props.pending) return;
  returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  activeVisibility.value = null;
  void nextTick(() => {
    const dialog = dialogRef.value;
    if (!dialog || dialog.hasAttribute("open")) return;
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
  });
};

const cleanup = (): void => {
  activeVisibility.value = null;
  const target = returnFocus;
  returnFocus = null;
  queueMicrotask(() => target?.focus());
};

const close = (): void => {
  const dialog = dialogRef.value;
  if (props.pending || !dialog?.hasAttribute("open")) return;
  if (typeof dialog.close === "function") dialog.close();
  else {
    dialog.removeAttribute("open");
    cleanup();
  }
};

const handleCancel = (event: Event): void => {
  event.preventDefault();
  close();
};

const handleBackdrop = (event: MouseEvent): void => {
  if (event.target === dialogRef.value) close();
};

/** The selected visibility is the final creation command, avoiding a redundant confirmation step. */
const choose = (visibility: RoomVisibility): void => {
  if (props.pending) return;
  activeVisibility.value = visibility;
  emit("choose", visibility);
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
      class="create-room-dialog"
      aria-labelledby="create-room-dialog-title"
      aria-describedby="create-room-dialog-description"
      @cancel="handleCancel"
      @close="cleanup"
      @click="handleBackdrop"
    >
      <div class="create-room-dialog__surface">
        <header class="create-room-dialog__header">
          <div>
            <p>创建房间</p>
            <h2 id="create-room-dialog-title">{{ gameName }}</h2>
          </div>
          <button type="button" title="关闭" :disabled="pending" @click="close"><X :size="19" aria-hidden="true" /></button>
        </header>

        <p id="create-room-dialog-description" class="create-room-dialog__description">选择这个房间的加入方式</p>

        <div class="create-room-dialog__choices" role="group" aria-label="房间类型">
          <button
            type="button"
            aria-label="仅邀请"
            :aria-busy="pending && activeVisibility === 'ROOM_VISIBILITY_PRIVATE'"
            :disabled="pending"
            @click="choose('ROOM_VISIBILITY_PRIVATE')"
          >
            <span class="create-room-dialog__icon"><LockKeyhole :size="22" aria-hidden="true" /></span>
            <span class="create-room-dialog__copy">
              <strong>{{ pending && activeVisibility === "ROOM_VISIBILITY_PRIVATE" ? "正在创建" : "仅邀请" }}</strong>
              <small>持有房间码或邀请链接的玩家可加入</small>
            </span>
            <ArrowRight :size="18" aria-hidden="true" />
          </button>
          <button
            type="button"
            aria-label="公开房间"
            :aria-busy="pending && activeVisibility === 'ROOM_VISIBILITY_PUBLIC'"
            :disabled="pending"
            @click="choose('ROOM_VISIBILITY_PUBLIC')"
          >
            <span class="create-room-dialog__icon"><Globe2 :size="22" aria-hidden="true" /></span>
            <span class="create-room-dialog__copy">
              <strong>{{ pending && activeVisibility === "ROOM_VISIBILITY_PUBLIC" ? "正在创建" : "公开房间" }}</strong>
              <small>显示在公开大厅，其他玩家可直接加入</small>
            </span>
            <ArrowRight :size="18" aria-hidden="true" />
          </button>
        </div>

        <p v-if="error" class="create-room-dialog__error" role="alert">{{ error }}</p>
        <button class="button button--quiet create-room-dialog__cancel" type="button" :disabled="pending" @click="close">取消</button>
      </div>
    </dialog>
  </Teleport>
</template>

<style scoped>
.create-room-dialog {
  width: min(560px, calc(100% - 32px));
  max-height: min(680px, calc(100dvh - 32px));
  margin: auto;
  padding: 0;
  color: var(--platform-ink);
  background: transparent;
  border: 0;
  overflow: visible;
}

.create-room-dialog::backdrop { background: rgb(3 9 10 / 74%); backdrop-filter: blur(5px); }

.create-room-dialog__surface {
  display: grid;
  gap: 18px;
  padding: 22px;
  background: color-mix(in srgb, var(--platform-surface-raised) 95%, black);
  border: 1px solid rgb(168 181 180 / 24%);
  border-radius: 8px;
  box-shadow: 0 24px 80px rgb(0 0 0 / 44%);
  animation: create-room-dialog-enter 180ms ease-out both;
}

.create-room-dialog__header { display: flex; align-items: start; justify-content: space-between; gap: 18px; }
.create-room-dialog__header p { margin: 0 0 6px; color: var(--platform-accent); font-size: 11px; font-weight: 800; }
.create-room-dialog__header h2 { margin: 0; font-size: 26px; line-height: 1.1; }
.create-room-dialog__header > button { width: 40px; height: 40px; flex: 0 0 40px; display: grid; place-items: center; padding: 0; color: var(--platform-muted); background: transparent; border: 1px solid rgb(168 181 180 / 22%); border-radius: 50%; }
.create-room-dialog__description { margin: 0; color: var(--platform-muted); font-size: 13px; }
.create-room-dialog__choices { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.create-room-dialog__choices > button {
  min-height: 146px;
  display: grid;
  grid-template-columns: 46px minmax(0, 1fr) auto;
  align-content: center;
  align-items: center;
  gap: 12px;
  padding: 16px;
  color: var(--platform-ink);
  text-align: left;
  background: rgb(8 18 19 / 64%);
  border: 1px solid rgb(168 181 180 / 26%);
  border-radius: 8px;
}
.create-room-dialog__choices > button:hover,
.create-room-dialog__choices > button:focus-visible { border-color: var(--platform-accent); }
.create-room-dialog__choices > button:disabled { cursor: wait; opacity: .62; }
.create-room-dialog__icon { width: 46px; height: 46px; display: grid; place-items: center; color: #171b1a; background: var(--platform-accent); border-radius: 50%; }
.create-room-dialog__copy { min-width: 0; display: grid; gap: 6px; }
.create-room-dialog__copy strong { font-size: 17px; }
.create-room-dialog__copy small { color: var(--platform-muted); font-size: 11px; line-height: 1.5; }
.create-room-dialog__choices > button > svg { color: var(--platform-accent); }
.create-room-dialog__error { margin: 0; color: var(--platform-danger); font-size: 13px; }
.create-room-dialog__cancel { justify-self: end; min-width: 112px; }

@keyframes create-room-dialog-enter {
  from { opacity: 0; transform: translateY(10px); }
  to { opacity: 1; transform: translateY(0); }
}

@media (max-width: 640px) {
  .create-room-dialog { width: 100%; max-width: none; max-height: calc(100dvh - max(12px, env(safe-area-inset-top))); margin: auto 0 0; }
  .create-room-dialog__surface {
    max-height: calc(100dvh - max(12px, env(safe-area-inset-top)));
    padding: 20px max(18px, env(safe-area-inset-right)) max(20px, env(safe-area-inset-bottom)) max(18px, env(safe-area-inset-left));
    overflow: auto;
    border-right: 0;
    border-bottom: 0;
    border-left: 0;
    border-radius: 8px 8px 0 0;
  }
  .create-room-dialog__choices { grid-template-columns: 1fr; }
  .create-room-dialog__choices > button { min-height: 92px; padding: 14px; }
  .create-room-dialog__cancel { width: 100%; }
}

@media (prefers-reduced-motion: reduce) {
  .create-room-dialog__surface { animation: none; }
}
</style>
