<script setup lang="ts">
import { Check, CirclePause, Play, X } from "lucide-vue-next";
import { computed, ref } from "vue";

import { DangerConfirm } from "@game-night/game-ui-kit";

import { memberDisplayName } from "../../member-display";
import { useRoomStore } from "../../stores/room";

type ConfirmationKind = "pause" | "approve" | "resume";

const props = defineProps<{ sessionId: string; ownershipEpoch?: string | null }>();
const room = useRoomStore();
const confirmation = ref<ConfirmationKind | null>(null);
const busy = ref(false);
const actionError = ref("");

const snapshot = computed(() => room.remoteRoom);
const activePause = computed(() => snapshot.value?.activePause?.sessionId === props.sessionId ? snapshot.value.activePause : null);
const pendingRequest = computed(() => snapshot.value?.pendingPauseRequest?.sessionId === props.sessionId ? snapshot.value.pendingPauseRequest : null);
const isHost = computed(() => snapshot.value?.hostUserId === room.userId);
const selfMember = computed(() => snapshot.value?.members.find((member) => member.userId === room.userId));
const canRequestPause = computed(() => !isHost.value && selfMember.value?.role.includes("PARTICIPANT") === true);
const requestedByName = computed(() => {
  const userId = pendingRequest.value?.requestedByUserId;
  if (!userId) return "玩家";
  const member = snapshot.value?.members.find((candidate) => candidate.userId === userId);
  return memberDisplayName(userId, member?.username);
});
const requestedBySelf = computed(() => pendingRequest.value?.requestedByUserId === room.userId);
const hasSessionFence = computed(() => {
  if (!props.ownershipEpoch) return false;
  try {
    return BigInt(props.ownershipEpoch) > 0n;
  } catch {
    return false;
  }
});

const confirmationCopy = computed(() => {
  if (confirmation.value === "resume") return { title: "恢复游戏？", label: "确认恢复", body: "所有玩家会继续当前局，倒计时从暂停前的剩余时间继续。" };
  if (confirmation.value === "approve") return { title: `同意 ${requestedByName.value} 的暂停申请？`, label: "同意并暂停", body: "当前局会立即暂停，只有房主可以恢复。" };
  return { title: "暂停游戏？", label: "确认暂停", body: "当前桌面和回合会保留，倒计时及游戏操作立即冻结。" };
});

/** Runs one governance write at a time and keeps failures visible in the game context. */
const runAction = async (action: () => Promise<unknown>, fallback: string): Promise<void> => {
  if (busy.value) return;
  busy.value = true;
  actionError.value = "";
  try {
    await action();
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : fallback;
  } finally {
    busy.value = false;
  }
};

const requestPause = (): void => {
  void runAction(() => room.requestRemotePause(props.sessionId), "暂停申请提交失败");
};

const rejectRequest = (): void => {
  const requestId = pendingRequest.value?.requestId;
  if (!requestId) return;
  void runAction(() => room.rejectRemotePauseRequest(requestId), "暂停申请处理失败");
};

const confirmGovernance = (): void => {
  const kind = confirmation.value;
  confirmation.value = null;
  if (!kind || !hasSessionFence.value || !props.ownershipEpoch) return;
  if (kind === "resume") {
    void runAction(() => room.resumeRemoteGame(props.sessionId, props.ownershipEpoch!), "恢复游戏失败");
    return;
  }
  const requestId = kind === "approve" ? pendingRequest.value?.requestId ?? "" : "";
  void runAction(() => room.pauseRemoteGame(props.sessionId, requestId, props.ownershipEpoch!), "暂停游戏失败");
};
</script>

<template>
  <div class="game-governance" :aria-busy="busy">
    <template v-if="activePause">
      <span class="game-governance__state"><CirclePause :size="14" aria-hidden="true" />已暂停</span>
      <button v-if="isHost" type="button" title="恢复游戏" :disabled="busy || !hasSessionFence" @click="confirmation = 'resume'">
        <Play :size="16" aria-hidden="true" /><span>恢复</span>
      </button>
    </template>

    <template v-else-if="pendingRequest">
      <span class="game-governance__state" :title="`${requestedByName} 请求暂停`">
        <CirclePause :size="14" aria-hidden="true" />{{ requestedBySelf ? "已申请" : requestedByName }}
      </span>
      <template v-if="isHost">
        <button class="is-approve" type="button" title="同意暂停" :disabled="busy || !hasSessionFence" @click="confirmation = 'approve'">
          <Check :size="16" aria-hidden="true" />
        </button>
        <button type="button" title="拒绝暂停" :disabled="busy" @click="rejectRequest">
          <X :size="16" aria-hidden="true" />
        </button>
      </template>
    </template>

    <button v-else-if="isHost" type="button" title="暂停游戏" :disabled="busy || !hasSessionFence" @click="confirmation = 'pause'">
      <CirclePause :size="16" aria-hidden="true" /><span>暂停</span>
    </button>
    <button v-else-if="canRequestPause" type="button" title="向房主申请暂停" :disabled="busy" @click="requestPause">
      <CirclePause :size="16" aria-hidden="true" /><span>申请暂停</span>
    </button>

    <span v-if="actionError" class="game-governance__error" role="alert">{{ actionError }}</span>
  </div>

  <DangerConfirm
    :open="confirmation !== null"
    :title="confirmationCopy.title"
    :confirm-label="confirmationCopy.label"
    @confirm="confirmGovernance"
    @cancel="confirmation = null"
  >
    {{ confirmationCopy.body }}
  </DangerConfirm>
</template>

<style scoped>
.game-governance { position: relative; min-width: 0; display: flex; align-items: center; gap: 4px; }
.game-governance button,
.game-governance__state { min-height: 40px; display: inline-flex; align-items: center; justify-content: center; gap: 5px; padding: 0 9px; color: var(--platform-ink); background: color-mix(in srgb, var(--platform-surface-raised) 94%, transparent); border: 1px solid color-mix(in srgb, var(--platform-accent) 38%, transparent); border-radius: 7px; font: inherit; font-size: 11px; font-weight: 750; white-space: nowrap; box-shadow: 0 8px 20px rgb(0 0 0 / 24%); }
.game-governance button { cursor: pointer; }
.game-governance button:disabled { cursor: wait; opacity: .58; }
.game-governance button:focus-visible { outline: 2px solid var(--platform-focus); outline-offset: 2px; }
.game-governance button.is-approve { color: #10201c; background: var(--platform-focus); border-color: transparent; }
.game-governance__state { min-width: 0; max-width: 88px; overflow: hidden; color: var(--platform-accent); text-overflow: ellipsis; }
.game-governance__error { position: absolute; top: calc(100% + 5px); left: 0; width: max-content; max-width: min(260px, calc(100vw - 24px)); padding: 6px 8px; color: var(--platform-danger); background: var(--platform-surface-raised); border-left: 2px solid var(--platform-danger); font-size: 10px; line-height: 1.35; }
@media (max-width: 370px) {
  .game-governance button span { display: none; }
  .game-governance button { width: 40px; padding-inline: 0; }
}
</style>
