<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NAlert, NButton, NCard } from "naive-ui";
import { useRouter } from "vue-router";
import BootstrapPendingState from "../../components/auth/BootstrapPendingState.vue";
import ChangePasswordStep from "../../components/auth/ChangePasswordStep.vue";
import LoginPasswordStep from "../../components/auth/LoginPasswordStep.vue";
import MfaVerificationStep from "../../components/auth/MfaVerificationStep.vue";
import TotpEnrollmentStep from "../../components/auth/TotpEnrollmentStep.vue";
import { useAuthStore } from "../../stores/auth";

const auth = useAuthStore();
const router = useRouter();
const submitting = ref(false);

const panelTitle = computed(() => {
  switch (auth.currentStep) {
    case "bootstrap":
      return "等待初始化";
    case "changePassword":
      return "修改初始密码";
    case "enrollTotp":
      return "绑定身份验证器";
    case "verifyMfa":
      return "多因素验证";
    case "rebindTotp":
      return "重绑验证器";
    case "authenticated":
      return "已通过认证";
    default:
      return "管理员登录";
  }
});

const syncRoute = async (): Promise<void> => {
  switch (auth.currentStep) {
    case "authenticated":
      await router.replace({ name: "overview" });
      break;
    case "bootstrap":
      await router.replace({ name: "auth-bootstrap" });
      break;
    case "changePassword":
      await router.replace({ name: "auth-change-password" });
      break;
    case "enrollTotp":
      await router.replace({ name: "auth-enroll-totp" });
      break;
    case "verifyMfa":
      await router.replace({ name: "auth-verify-mfa" });
      break;
    case "rebindTotp":
      await router.replace({ name: "auth-rebind-totp" });
      break;
    default:
      await router.replace({ name: "auth-login" });
  }
};

watch(
  () => auth.currentStep,
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

if (auth.currentStep === "login" && !auth.challenge) {
  void auth.startLogin();
}
</script>

<template>
  <main class="auth-shell">
    <div class="auth-shell__workspace">
      <section class="auth-shell__intro">
        <div class="auth-brand">
          <img class="auth-brand__mark" :src="'/brand-mark.svg'" alt="" aria-hidden="true" />
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
          v-if="auth.currentStep === 'bootstrap'"
          :loading="auth.restoring"
          @retry="auth.restore"
        />
        <LoginPasswordStep
          v-else-if="auth.currentStep === 'login'"
          :pending="submitting"
          :error-message="auth.errorMessage"
          @submit="(password) => run(() => auth.submitPassword(password))"
        />
        <ChangePasswordStep
          v-else-if="auth.currentStep === 'changePassword'"
          :pending="submitting"
          @submit="(password) => run(() => auth.submitInitialPassword(password))"
        />
        <MfaVerificationStep
          v-else-if="auth.currentStep === 'verifyMfa'"
          :pending="submitting"
          @totp="(code) => run(() => auth.submitTotp(code))"
          @recovery="(code) => run(() => auth.submitRecoveryCode(code))"
        />
        <TotpEnrollmentStep
          v-else-if="auth.currentStep === 'enrollTotp'"
          :pending="submitting"
          :operation="auth.secretEnvelope?.result ?? null"
          :secret="auth.secretEnvelope?.totpSecret ?? null"
          :otpauth-uri="auth.secretEnvelope?.otpauthUri ?? null"
          :recovery-codes="auth.secretEnvelope?.recoveryCodes ?? null"
          @begin="() => run(() => auth.openTotpEnrollment())"
          @complete="(code) => run(() => auth.finishTotpEnrollment(code))"
          @acknowledge="() => run(() => auth.acknowledgeSecretReceipt())"
        />
        <TotpEnrollmentStep
          v-else-if="auth.currentStep === 'rebindTotp'"
          rebind
          :pending="submitting"
          :operation="auth.secretEnvelope?.result ?? null"
          :secret="auth.secretEnvelope?.totpSecret ?? null"
          :otpauth-uri="auth.secretEnvelope?.otpauthUri ?? null"
          :recovery-codes="auth.secretEnvelope?.recoveryCodes ?? null"
          @begin="() => run(() => auth.openTotpRebind())"
          @complete="(code) => run(() => auth.finishTotpRebind(code))"
          @acknowledge="() => run(() => auth.acknowledgeSecretReceipt())"
        />
        <div v-else class="admin-grid">
          <NAlert type="success" title="认证完成">页面正在进入管理后台。</NAlert>
          <NButton type="primary" @click="router.replace({ name: 'overview' })">进入后台</NButton>
        </div>
      </NCard>
    </div>
  </main>
</template>
