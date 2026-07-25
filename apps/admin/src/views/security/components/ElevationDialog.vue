<script setup lang="ts">
import { computed, ref, shallowRef } from "vue";
import { NAlert, NButton, NForm, NFormItem, NInput } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";
import AppDialog from "../../../components/AppDialog.vue";
import { elevateAdminSession } from "../../../api/admin-auth";
import { createRequestId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { useAuthStore } from "../../../stores/auth";
import type { AdminElevationScope, AdminElevationSummary } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";

/**
 * Payload for opening the elevation dialog.
 */
export type ElevationDialogPayload = {
  scope: AdminElevationScope;
  allowRecoveryCode: boolean;
  onElevated: (summary: AdminElevationSummary) => void;
  onCancelled?: () => void;
};

const auth = useAuthStore();
const dialogRef = ref<{ toggleDialog: (open: boolean, payload?: ElevationDialogPayload) => void } | null>(null);

// Dialog state
const submitting = ref(false);
const errorMessage = ref("");
const currentPayload = ref<ElevationDialogPayload | null>(null);
const completed = ref(false);

// Form state
const formRef = ref<FormInst | null>(null);
const formData = ref({
  password: "",
  totpCode: "",
  recoveryCode: ""
});

// Abort controller for request cancellation
const abortController = shallowRef<AbortController | null>(null);

// Generation token for stale response guard
const generation = ref(0);

// Computed: which second factor field to show
const useRecoveryCode = ref(false);
const requiresSecondFactor = computed(() => auth.session?.mfa?.enabled === true);
const verificationMessage = computed(() =>
  requiresSecondFactor.value
    ? "请输入当前密码和第二因素完成身份验证。"
    : "当前未启用多因素认证，请输入当前密码完成身份验证。"
);
// Form validation rules
const rules = computed<FormRules>(() => ({
  password: [{ required: true, message: "请输入密码", trigger: "blur" }],
  totpCode: [
    {
      required: requiresSecondFactor.value && !useRecoveryCode.value,
      message: "请输入 TOTP 验证码",
      trigger: "blur"
    },
    { pattern: /^\d{6}$/, message: "验证码为6位数字", trigger: "blur" }
  ],
  recoveryCode: [
    {
      required: requiresSecondFactor.value && useRecoveryCode.value,
      message: "请输入恢复码",
      trigger: "blur"
    }
  ]
}));

/**
 * Opens or closes the dialog.
 * When opening, initializes form and sets payload.
 */
const toggleDialog = (open: boolean, payload?: ElevationDialogPayload): void => {
  if (open && payload) {
    currentPayload.value = payload;
    errorMessage.value = "";
    useRecoveryCode.value = false;
    completed.value = false;
    abortController.value?.abort();
    generation.value += 1;
    abortController.value = new AbortController();
  }
  dialogRef.value?.toggleDialog(open, payload);
};

/**
 * Handles dialog close - cleans up resources.
 * Aborts pending requests, resets form and sensitive data.
 */
const handleClose = (): void => {
  const payload = currentPayload.value;
  const shouldNotifyCancellation = !completed.value;
  generation.value += 1;
  const controller = abortController.value;
  controller?.abort();
  if (abortController.value === controller) {
    abortController.value = null;
  }
  formRef.value?.restoreValidation();
  formData.value = {
    password: "",
    totpCode: "",
    recoveryCode: ""
  };
  errorMessage.value = "";
  submitting.value = false;
  currentPayload.value = null;
  useRecoveryCode.value = false;
  completed.value = false;
  if (shouldNotifyCancellation) {
    payload?.onCancelled?.();
  }
};

/**
 * Submits elevation request.
 * Calls the onElevated callback on success.
 */
const handleSubmit = async (close: () => void): Promise<void> => {
  if (submitting.value) {
    return;
  }
  try {
    await formRef.value?.validate();
  } catch {
    return;
  }

  if (!currentPayload.value) {
    return;
  }

  const requestToken = generation.value;
  const controller = abortController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    const response = await elevateAdminSession({
      operationId: createRequestId(),
      scope: currentPayload.value.scope,
      currentPassword: formData.value.password,
      ...(requiresSecondFactor.value
        ? useRecoveryCode.value
          ? { recoveryCode: formData.value.recoveryCode }
          : { totpCode: formData.value.totpCode }
        : {}),
      ...(controller ? { signal: controller.signal } : {})
    });

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    if (response.elevation && currentPayload.value) {
      completed.value = true;
      currentPayload.value.onElevated(response.elevation);
      close();
    }
  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "权限提升失败，请稍后重试。";
  } finally {
    if (requestToken === generation.value) {
      submitting.value = false;
    }
  }
};

/**
 * Toggles between TOTP and recovery code input.
 */
const toggleSecondFactorMode = (): void => {
  if (!requiresSecondFactor.value || !currentPayload.value?.allowRecoveryCode) {
    return;
  }
  useRecoveryCode.value = !useRecoveryCode.value;
  formData.value.totpCode = "";
  formData.value.recoveryCode = "";
  formRef.value?.restoreValidation();
};

// Expose toggleDialog for parent component
defineExpose({
  toggleDialog
});
</script>

<template>
  <AppDialog ref="dialogRef" title="权限提升" :width="480" @closed="handleClose">
    <template #default="{ close }">
      <div class="admin-grid">
        <NAlert v-if="errorMessage" type="error" :title="errorMessage" closable @close="errorMessage = ''" />
        <NAlert type="info" title="需要验证身份">
          {{ verificationMessage }}
        </NAlert>
        <NForm ref="formRef" :model="formData" :rules="rules">
          <NFormItem path="password" label="密码">
            <NInput
              v-model:value="formData.password"
              type="password"
              placeholder="请输入密码"
              show-password-on="click"
              :disabled="submitting"
            />
          </NFormItem>
          <NFormItem v-if="requiresSecondFactor && !useRecoveryCode" path="totpCode" label="TOTP 验证码">
            <NInput
              v-model:value="formData.totpCode"
              placeholder="请输入 6 位验证码"
              :maxlength="6"
              :disabled="submitting"
            />
          </NFormItem>
          <NFormItem v-else-if="requiresSecondFactor" path="recoveryCode" label="恢复码">
            <NInput
              v-model:value="formData.recoveryCode"
              placeholder="请输入恢复码"
              :disabled="submitting"
            />
          </NFormItem>
        </NForm>
        <div v-if="requiresSecondFactor && currentPayload?.allowRecoveryCode" style="text-align: center">
          <NButton text size="small" @click="toggleSecondFactorMode">
            {{ useRecoveryCode ? "使用 TOTP 验证码" : "使用恢复码" }}
          </NButton>
        </div>
        <div class="admin-dialog-footer">
          <NButton @click="close" :disabled="submitting">取消</NButton>
          <NButton type="primary" :loading="submitting" @click="handleSubmit(close)">提升权限</NButton>
        </div>
      </div>
    </template>
  </AppDialog>
</template>
