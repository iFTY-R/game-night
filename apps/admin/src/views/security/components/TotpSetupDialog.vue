<script setup lang="ts">
import { ref, shallowRef } from "vue";
import { NAlert, NButton, NForm, NFormItem, NInput, NSteps, NStep, NCode, NQrCode, NTag } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";
import AppDialog from "../../../components/AppDialog.vue";
import { useAuthStore } from "../../../stores/auth";
import { beginTotpEnrollment, completeTotpEnrollment, confirmAdminSecretReceipt } from "../../../api/admin-auth";
import { createOperationId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { AdminSecretOperation } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";

const auth = useAuthStore();
const emit = defineEmits<{ updated: [] }>();
const dialogRef = ref<{ toggleDialog: (open: boolean) => void } | null>(null);

// Dialog state
const currentStep = ref(1);
const submitting = ref(false);
const errorMessage = ref("");

// Component-local secrets - never stored in global state
const password = ref("");
const manualEntryKey = ref("");
const otpauthUri = ref("");
const recoveryCodes = ref<string[]>([]);
const enrollmentOperationId = ref("");
const enrollmentResultId = ref("");
const recoveryCodesOperationId = ref("");
const recoveryCodesResultId = ref("");

// Form state for steps
const passwordFormRef = ref<FormInst | null>(null);
const totpFormRef = ref<FormInst | null>(null);
const totpCode = ref("");

// Abort controller for request cancellation
const abortController = shallowRef<AbortController | null>(null);

// Generation token for stale response guard
const generation = ref(0);

// Step titles
const stepTitles = ["验证密码", "扫描二维码", "验证 TOTP", "保存恢复码"];

// Form validation rules
const passwordRules: FormRules = {
  password: [{ required: true, message: "请输入密码", trigger: "blur" }]
};

const totpRules: FormRules = {
  totpCode: [
    { required: true, message: "请输入验证码", trigger: "blur" },
    { pattern: /^\d{6}$/, message: "验证码为6位数字", trigger: "blur" }
  ]
};

/**
 * Opens or closes the dialog.
 * When opening, resets all state.
 */
const toggleDialog = (open: boolean): void => {
  if (open) {
    currentStep.value = 1;
    errorMessage.value = "";
    password.value = "";
    manualEntryKey.value = "";
    otpauthUri.value = "";
    recoveryCodes.value = [];
    enrollmentOperationId.value = "";
    enrollmentResultId.value = "";
    recoveryCodesOperationId.value = "";
    recoveryCodesResultId.value = "";
    totpCode.value = "";
    abortController.value?.abort();
    generation.value += 1;
    abortController.value = new AbortController();
  }
  dialogRef.value?.toggleDialog(open);
};

/**
 * Handles dialog close - cleans up all sensitive data.
 */
const handleClose = (): void => {
  generation.value += 1;
  const controller = abortController.value;
  controller?.abort();
  if (abortController.value === controller) {
    abortController.value = null;
  }
  passwordFormRef.value?.restoreValidation();
  totpFormRef.value?.restoreValidation();

  // Clear all sensitive data
  password.value = "";
  manualEntryKey.value = "";
  otpauthUri.value = "";
  recoveryCodes.value = [];
  enrollmentOperationId.value = "";
  enrollmentResultId.value = "";
  recoveryCodesOperationId.value = "";
  recoveryCodesResultId.value = "";
  totpCode.value = "";

  currentStep.value = 1;
  errorMessage.value = "";
  submitting.value = false;
};

/**
 * Step 1: Submit password and begin TOTP enrollment.
 * Receives QR code URI and manual entry key.
 */
const handlePasswordSubmit = async (): Promise<void> => {
  if (submitting.value) {
    return;
  }
  try {
    await passwordFormRef.value?.validate();
  } catch {
    return;
  }

  const requestToken = generation.value;
  const controller = abortController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    const operationId = createOperationId();
    const response = await beginTotpEnrollment({
      operationId,
      currentPassword: password.value,
      ...(controller ? { signal: controller.signal } : {})
    });

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    if (!response.result) {
      throw new Error("missing enrollment result");
    }
    enrollmentOperationId.value = response.result.operationId || operationId;
    enrollmentResultId.value = response.result.resultId;
    manualEntryKey.value = response.manualEntryKey;
    otpauthUri.value = response.otpauthUri;

    currentStep.value = 2;
  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "密码验证失败，请稍后重试。";
  } finally {
    if (requestToken === generation.value) {
      submitting.value = false;
    }
  }
};

/**
 * Step 2 → 3: User scans QR code, proceeds to verification.
 */
const proceedToVerification = (): void => {
  currentStep.value = 3;
  errorMessage.value = "";
};

/**
 * Step 3: Submit TOTP code to complete enrollment.
 * Receives recovery codes.
 */
const handleTotpSubmit = async (): Promise<void> => {
  if (submitting.value) {
    return;
  }
  try {
    await totpFormRef.value?.validate();
  } catch {
    return;
  }

  const requestToken = generation.value;
  const controller = abortController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    const recoveryOperationId = createOperationId();
    const response = await completeTotpEnrollment({
      enrollmentOperationId: enrollmentOperationId.value,
      recoveryCodesOperationId: recoveryOperationId,
      totpCode: totpCode.value,
      ...(controller ? { signal: controller.signal } : {})
    });

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    // Update session with new enrollment state
    if (response.session) {
      auth.applySession(response.session);
    }

    // Store recovery codes in component-local memory
    if (!response.result || response.recoveryCodes.length === 0) {
      throw new Error("missing recovery-code result");
    }
    recoveryCodesOperationId.value = response.result.operationId || recoveryOperationId;
    recoveryCodes.value = [...response.recoveryCodes];
    recoveryCodesResultId.value = response.result.resultId;

    currentStep.value = 4;
    emit("updated");
  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "验证码错误，请重试。";
  } finally {
    if (requestToken === generation.value) {
      submitting.value = false;
    }
  }
};

/**
 * Step 4: User confirms they have saved recovery codes.
 * Calls confirmAdminSecretReceipt and closes dialog.
 */
const handleConfirmReceipt = async (close: () => void): Promise<void> => {
  if (submitting.value) {
    return;
  }
  const requestToken = generation.value;
  const controller = abortController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    await confirmAdminSecretReceipt({
      operation: AdminSecretOperation.TOTP_ENROLLMENT,
      operationId: enrollmentOperationId.value,
      resultId: enrollmentResultId.value,
      ...(controller ? { signal: controller.signal } : {})
    });
    await confirmAdminSecretReceipt({
      operation: AdminSecretOperation.INITIAL_RECOVERY_CODES,
      operationId: recoveryCodesOperationId.value,
      resultId: recoveryCodesResultId.value,
      ...(controller ? { signal: controller.signal } : {})
    });

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    close();
  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "确认失败，请稍后重试。";
  } finally {
    if (requestToken === generation.value) {
      submitting.value = false;
    }
  }
};

// Expose toggleDialog for parent component
defineExpose({
  toggleDialog
});
</script>

<template>
  <AppDialog ref="dialogRef" title="启用 TOTP 多因素认证" :width="600" @closed="handleClose">
    <template #default="{ close }">
      <div class="admin-grid">
        <NAlert v-if="errorMessage" type="error" :title="errorMessage" closable @close="errorMessage = ''" />

        <NSteps :current="currentStep" :status="errorMessage ? 'error' : 'process'">
          <NStep v-for="(title, index) in stepTitles" :key="index" :title="title" />
        </NSteps>

        <!-- Step 1: Password verification -->
        <div v-if="currentStep === 1">
          <NAlert type="info" style="margin-bottom: 16px">
            启用 TOTP 需要验证您的密码。
          </NAlert>
          <NForm ref="passwordFormRef" :model="{ password }" :rules="passwordRules">
            <NFormItem path="password" label="密码">
              <NInput
                v-model:value="password"
                type="password"
                placeholder="请输入密码"
                show-password-on="click"
                :disabled="submitting"
                @keyup.enter="handlePasswordSubmit"
              />
            </NFormItem>
          </NForm>
          <div class="admin-dialog-footer">
            <NButton @click="close" :disabled="submitting">取消</NButton>
            <NButton type="primary" :loading="submitting" @click="handlePasswordSubmit">下一步</NButton>
          </div>
        </div>

        <!-- Step 2: QR code display -->
        <div v-if="currentStep === 2">
          <NAlert type="info" style="margin-bottom: 16px">
            使用身份验证器应用（如 Google Authenticator、Authy）扫描二维码，或手动输入密钥。
          </NAlert>
          <div style="display: flex; justify-content: center; margin: 24px 0">
            <NQrCode v-if="otpauthUri" :value="otpauthUri" :size="200" />
          </div>
          <div style="margin: 16px 0">
            <div style="margin-bottom: 8px"><strong>手动输入密钥：</strong></div>
            <NCode :code="manualEntryKey" language="text" />
          </div>
          <div class="admin-dialog-footer">
            <NButton @click="close">取消</NButton>
            <NButton type="primary" @click="proceedToVerification">下一步</NButton>
          </div>
        </div>

        <!-- Step 3: TOTP verification -->
        <div v-if="currentStep === 3">
          <NAlert type="info" style="margin-bottom: 16px">
            请输入身份验证器应用中显示的 6 位验证码。
          </NAlert>
          <NForm ref="totpFormRef" :model="{ totpCode }" :rules="totpRules">
            <NFormItem path="totpCode" label="验证码">
              <NInput
                v-model:value="totpCode"
                placeholder="请输入 6 位验证码"
                :maxlength="6"
                :disabled="submitting"
                @keyup.enter="handleTotpSubmit"
              />
            </NFormItem>
          </NForm>
          <div class="admin-dialog-footer">
            <NButton @click="close" :disabled="submitting">取消</NButton>
            <NButton type="primary" :loading="submitting" @click="handleTotpSubmit">验证</NButton>
          </div>
        </div>

        <!-- Step 4: Recovery codes display -->
        <div v-if="currentStep === 4">
          <NAlert type="warning" style="margin-bottom: 16px">
            请保存这些恢复码。如果您无法访问身份验证器应用，可以使用恢复码登录。每个恢复码只能使用一次。
          </NAlert>
          <div style="display: grid; grid-template-columns: repeat(2, 1fr); gap: 8px; margin: 16px 0">
            <NTag v-for="(code, index) in recoveryCodes" :key="index" type="info">
              {{ code }}
            </NTag>
          </div>
          <div class="admin-dialog-footer">
            <NButton type="primary" :loading="submitting" @click="handleConfirmReceipt(close)">
              我已保存恢复码
            </NButton>
          </div>
        </div>
      </div>
    </template>
  </AppDialog>
</template>
