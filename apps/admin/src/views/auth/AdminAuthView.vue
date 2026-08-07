<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NAlert, NButton, NCard } from "naive-ui";
import { useRouter } from "vue-router";
import { AdminAccountState, AdminSessionKind } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import AdminLoginForm from "./components/AdminLoginForm.vue";
import InitialPasswordForm from "./components/InitialPasswordForm.vue";
import MfaChallengeForm from "./components/MfaChallengeForm.vue";
import BootstrapPendingState from "../../components/auth/BootstrapPendingState.vue";
import { useAuthStore } from "../../stores/auth";

const auth = useAuthStore();
const router = useRouter();
const brandMarkURL = `${import.meta.env.BASE_URL}brand-mark.svg`;
const submitting = ref(false);

// Compute panel title based on session kind
const panelTitle = computed(() => {
  if (!auth.session) {
    return auth.setupState === AdminAccountState.BOOTSTRAP_PENDING ? "等待初始化" : "管理员登录";
  }
  switch (auth.session.kind) {
    case AdminSessionKind.SETUP_PASSWORD_PENDING:
      return "修改初始密码";
    case AdminSessionKind.MFA_PENDING:
      return "多因素验证";
    case AdminSessionKind.FULL:
      return "已通过认证";
    default:
      return "管理员登录";
  }
});

// Sync route based on session state
const syncRoute = async (): Promise<void> => {
  if (!auth.session) {
    if (auth.setupState === AdminAccountState.BOOTSTRAP_PENDING) {
      await router.replace({ name: "auth-bootstrap" });
    } else {
      await router.replace({ name: "auth-login" });
    }
    return;
  }

  switch (auth.session.kind) {
    case AdminSessionKind.FULL:
      await router.replace({ name: "security" });
      break;
    case AdminSessionKind.SETUP_PASSWORD_PENDING:
      await router.replace({ name: "auth-change-password" });
      break;
    case AdminSessionKind.MFA_PENDING:
      await router.replace({ name: "auth-verify-mfa" });
      break;
    default:
      await router.replace({ name: "auth-login" });
  }
};

watch(
  () => [auth.session?.kind, auth.setupState],
  () => {
    void syncRoute();
  },
  { immediate: true }
);

const run = async (work: () => Promise<void>): Promise<void> => {
  submitting.value = true;
  try {
    await work();
  } finally {
    submitting.value = false;
  }
};

// Start login flow if no challenge
if (!auth.session && !auth.challenge) {
  void auth.startLogin();
}
</script>

<template>
  <main class="auth-shell">
    <div class="auth-shell__workspace">
      <section class="auth-shell__intro">
        <div class="auth-brand">
          <img class="auth-brand__mark" :src="brandMarkURL" alt="" aria-hidden="true" />
          <div class="auth-brand__copy">
            <span class="admin-eyebrow">Game Night</span>
            <strong>运营管理</strong>
          </div>
        </div>
        <div class="auth-shell__headline">
          <h1>管理后台</h1>
          <p>Game Night 运营与安全控制</p>
        </div>
        <div class="auth-shell__status">管理员专用入口</div>
      </section>

      <NCard class="admin-card-shell auth-shell__card" :bordered="false" :title="panelTitle">
        <BootstrapPendingState
          v-if="!auth.session && auth.setupState === AdminAccountState.BOOTSTRAP_PENDING"
          :loading="auth.restoring"
          @retry="auth.restore"
        />
        <AdminLoginForm
          v-else-if="!auth.session"
          :pending="submitting"
          :error-message="auth.errorMessage"
          @submit="(password) => run(() => auth.submitPassword(password))"
        />
        <InitialPasswordForm
          v-else-if="auth.session.kind === AdminSessionKind.SETUP_PASSWORD_PENDING"
          :pending="submitting"
          :error-message="auth.errorMessage"
          @submit="(password) => run(() => auth.submitInitialPassword(password))"
        />
        <MfaChallengeForm
          v-else-if="auth.session.kind === AdminSessionKind.MFA_PENDING"
          :pending="submitting"
          :error-message="auth.errorMessage"
          @totp="(code) => run(() => auth.submitTotp(code))"
          @recovery="(code) => run(() => auth.submitRecoveryCode(code))"
        />
        <div v-else class="admin-grid">
          <NAlert type="success" title="认证完成">页面正在进入管理后台。</NAlert>
          <NButton type="primary" @click="router.replace({ name: 'security' })">进入后台</NButton>
        </div>
      </NCard>
    </div>
  </main>
</template>
