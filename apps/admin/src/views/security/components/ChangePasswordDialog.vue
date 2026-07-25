<script setup lang="ts">
import { ref, shallowRef } from "vue";
import { NAlert, NButton, NForm, NFormItem, NInput } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";
import AppDialog from "../../../components/AppDialog.vue";
import { useAuthStore } from "../../../stores/auth";
import { changeAdminPassword } from "../../../api/admin-auth";
import { createRequestId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";

const auth = useAuthStore();
const emit = defineEmits<{ updated: [] }>();
const dialogRef = ref<{ toggleDialog: (open: boolean) => void } | null>(null);

// Dialog state
const submitting = ref(false);
const errorMessage = ref("");
const revokedSessionsCount = ref(0);
const showSuccess = ref(false);

// Form state
const formRef = ref<FormInst | null>(null);
const formData = ref({
  currentPassword: "",
  newPassword: "",
  confirmPassword: ""
});

// Abort controller for async operations
const abortController = shallowRef<AbortController | null>(null);

// Generation token for stale response guard
const generation = ref(0);

// Form validation rules
const rules: FormRules = {
  currentPassword: [{ required: true, message: "请输入当前密码", trigger: "blur" }],
  newPassword: [
    { required: true, message: "请输入新密码", trigger: "blur" },
    { min: 12, message: "密码至少12位", trigger: "blur" }
  ],
  confirmPassword: [
    { required: true, message: "请确认密码", trigger: "blur" },
    {
      validator: (_rule, value) => {
        if (value !== formData.value.newPassword) {
          return new Error("两次输入的密码不一致");
        }
        return true;
      },
      trigger: "blur"
    }
  ]
};

/**
 * Opens or closes the dialog.
 * When opening, resets form and initializes state.
 */
const toggleDialog = (open: boolean): void => {
  if (open) {
    errorMessage.value = "";
    revokedSessionsCount.value = 0;
    showSuccess.value = false;
    abortController.value?.abort();
    generation.value += 1;
    abortController.value = new AbortController();
  }
  dialogRef.value?.toggleDialog(open);
};

/**
 * Handles dialog close - cleans up resources.
 * Aborts pending requests, resets form and sensitive data.
 */
const handleClose = (): void => {
  generation.value += 1;
  const controller = abortController.value;
  controller?.abort();
  if (abortController.value === controller) {
    abortController.value = null;
  }
  formRef.value?.restoreValidation();
  formData.value = {
    currentPassword: "",
    newPassword: "",
    confirmPassword: ""
  };
  errorMessage.value = "";
  revokedSessionsCount.value = 0;
  showSuccess.value = false;
  submitting.value = false;
};

/**
 * Submits password change request.
 * Shows revoked session count on success.
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

  const requestToken = generation.value;
  const controller = abortController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    const response = await changeAdminPassword({
      operationId: createRequestId(),
      currentPassword: formData.value.currentPassword,
      newPassword: formData.value.newPassword,
      expectedPasswordVersion: auth.session?.passwordVersion ?? 0n,
      ...(controller ? { signal: controller.signal } : {})
    });

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    // Update session with new password version
    if (response.session) {
      auth.applySession(response.session);
    }

    revokedSessionsCount.value = response.revokedSessions;
    showSuccess.value = true;
    emit("updated");

  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "密码修改失败，请稍后重试。";
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
  <AppDialog ref="dialogRef" title="修改密码" @closed="handleClose">
    <template #default="{ close }">
      <div v-if="showSuccess" class="admin-grid">
        <NAlert type="success" title="密码修改成功">
          密码已更新，撤销了 {{ revokedSessionsCount }} 个其他会话。
        </NAlert>
        <div class="admin-dialog-footer">
          <NButton type="primary" @click="close">关闭</NButton>
        </div>
      </div>
      <div v-else class="admin-grid">
        <NAlert v-if="errorMessage" type="error" :title="errorMessage" closable @close="errorMessage = ''" />
        <NForm ref="formRef" :model="formData" :rules="rules">
          <NFormItem path="currentPassword" label="当前密码">
            <NInput
              v-model:value="formData.currentPassword"
              type="password"
              placeholder="请输入当前密码"
              show-password-on="click"
              :disabled="submitting"
            />
          </NFormItem>
          <NFormItem path="newPassword" label="新密码">
            <NInput
              v-model:value="formData.newPassword"
              type="password"
              placeholder="请输入新密码（至少12位）"
              show-password-on="click"
              :disabled="submitting"
            />
          </NFormItem>
          <NFormItem path="confirmPassword" label="确认密码">
            <NInput
              v-model:value="formData.confirmPassword"
              type="password"
              placeholder="请再次输入新密码"
              show-password-on="click"
              :disabled="submitting"
            />
          </NFormItem>
        </NForm>
        <div class="admin-dialog-footer">
          <NButton @click="close" :disabled="submitting">取消</NButton>
          <NButton type="primary" :loading="submitting" @click="handleSubmit(close)">确认修改</NButton>
        </div>
      </div>
    </template>
  </AppDialog>
</template>
