<script setup lang="ts">
import { ref } from "vue";
import { NAlert, NButton, NForm, NFormItem, NInput, NTabs, NTabPane } from "naive-ui";
import type { FormInst } from "naive-ui";

// Component props
defineProps<{
  pending?: boolean;
  errorMessage?: string;
}>();

// Component emits
const emit = defineEmits<{
  totp: [code: string];
  recovery: [code: string];
}>();

// UI state
const activeTab = ref<"totp" | "recovery">("totp");

// Form refs
const totpFormRef = ref<FormInst | null>(null);
const recoveryFormRef = ref<FormInst | null>(null);

// Form data
const totpCode = ref("");
const recoveryCode = ref("");

// Validation rules
const totpRules = {
  code: [
    { required: true, message: "请输入验证码", trigger: "blur" },
    { pattern: /^\d{6}$/, message: "验证码为6位数字", trigger: "blur" }
  ]
};

const recoveryRules = {
  code: [{ required: true, message: "请输入恢复码", trigger: "blur" }]
};

/**
 * Handles TOTP code submission.
 */
const handleTotpSubmit = async (): Promise<void> => {
  try {
    await totpFormRef.value?.validate();
    emit("totp", totpCode.value);
  } catch {
    // Validation failed
  }
};

/**
 * Handles recovery code submission.
 */
const handleRecoverySubmit = async (): Promise<void> => {
  try {
    await recoveryFormRef.value?.validate();
    emit("recovery", recoveryCode.value);
  } catch {
    // Validation failed
  }
};
</script>

<template>
  <div class="admin-grid">
    <p class="admin-form-instruction">请完成多因素身份验证。</p>
    <NAlert v-if="errorMessage" type="error" :title="errorMessage" />
    <NTabs v-model:value="activeTab" type="segment">
      <NTabPane name="totp" tab="验证器">
        <div class="admin-grid" style="margin-top: 16px">
          <NForm ref="totpFormRef" :model="{ code: totpCode }" :rules="totpRules" @keyup.enter="handleTotpSubmit">
            <NFormItem path="code" label="验证码">
              <NInput
                v-model:value="totpCode"
                placeholder="请输入6位验证码"
                :disabled="pending"
                maxlength="6"
                autofocus
              />
            </NFormItem>
          </NForm>
          <NButton type="primary" :loading="pending" :disabled="!totpCode" block @click="handleTotpSubmit">
            验证
          </NButton>
        </div>
      </NTabPane>
      <NTabPane name="recovery" tab="恢复码">
        <div class="admin-grid" style="margin-top: 16px">
          <NAlert type="warning" title="使用恢复码" closable>
            恢复码仅可使用一次，使用后将失效。
          </NAlert>
          <NForm
            ref="recoveryFormRef"
            :model="{ code: recoveryCode }"
            :rules="recoveryRules"
            @keyup.enter="handleRecoverySubmit"
          >
            <NFormItem path="code" label="恢复码">
              <NInput
                v-model:value="recoveryCode"
                placeholder="请输入恢复码"
                :disabled="pending"
              />
            </NFormItem>
          </NForm>
          <NButton type="primary" :loading="pending" :disabled="!recoveryCode" block @click="handleRecoverySubmit">
            验证
          </NButton>
        </div>
      </NTabPane>
    </NTabs>
  </div>
</template>
