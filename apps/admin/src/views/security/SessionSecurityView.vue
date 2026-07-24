<script setup lang="ts">
import { NAlert, NButton, NTag } from "naive-ui";
import { ref } from "vue";
import LogoutConfirmDialog from "../../components/session/LogoutConfirmDialog.vue";
import SessionSummary from "../../components/session/SessionSummary.vue";
import { useAuthStore } from "../../stores/auth";

const auth = useAuthStore();
const dialogRef = ref<InstanceType<typeof LogoutConfirmDialog> | null>(null);
const pending = ref(false);
const resultMessage = ref("");

const handleConfirm = async (payload: { mode: "current" | "all"; summary: string }): Promise<void> => {
  pending.value = true;
  resultMessage.value = "";
  try {
    if (payload.mode === "current") {
      await auth.logoutCurrentSession();
      resultMessage.value = "当前会话已退出。";
      return;
    }
    const revoked = await auth.logoutEverySession();
    resultMessage.value = `已撤销 ${revoked} 个管理员会话。`;
  } finally {
    pending.value = false;
  }
};
</script>

<template>
  <div class="admin-pane__section">
    <div class="admin-page-title">
      <div class="admin-eyebrow">Session Security</div>
      <h2>会话安全</h2>
    </div>

    <div class="admin-grid admin-grid--two">
      <SessionSummary :session="auth.session" />
      <div class="admin-card-shell admin-section-card admin-grid">
        <div class="admin-list">
          <div class="admin-list__row"><span class="admin-muted">当前 next step</span><NTag>{{ auth.nextStep }}</NTag></div>
          <div class="admin-list__row"><span class="admin-muted">持久化范围</span><span>仅主题 / 侧栏 / 静态标签</span></div>
          <div class="admin-list__row"><span class="admin-muted">敏感数据</span><span>仅在内存中短暂存在</span></div>
        </div>
        <NAlert type="warning" title="危险操作">
          退出全部会话会使其他后台标签页在下一次请求时统一回到登录流程。
        </NAlert>
        <div class="admin-toolbar__cluster">
          <NButton type="warning" :loading="pending" @click="dialogRef?.toggleDialog(true, { mode: 'current', summary: '仅撤销当前浏览器会话。' })">
            退出当前会话
          </NButton>
          <NButton type="error" :loading="pending" @click="dialogRef?.toggleDialog(true, { mode: 'all', summary: '撤销当前管理员的全部会话，包括其他标签页与浏览器。' })">
            退出全部会话
          </NButton>
        </div>
        <p v-if="resultMessage" class="admin-muted">{{ resultMessage }}</p>
      </div>
    </div>

    <LogoutConfirmDialog ref="dialogRef" @confirm="handleConfirm" />
  </div>
</template>
