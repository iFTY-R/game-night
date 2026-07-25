<script setup lang="ts">
import { ref } from "vue";
import { NAlert, NButton, NForm, NFormItem, NInput } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";

// Component props
defineProps<{
  pending?: boolean;
  errorMessage?: string;
}>();

// Component emits
const emit = defineEmits<{
  submit: [newPassword: string];
}>();

// Form state
const formRef = ref<FormInst | null>(null);
const formData = ref({
  newPassword: "",
  confirmPassword: ""
});

// Form validation rules
const rules: FormRules = {
  newPassword: [
    { required: true, message: "请输入新密码", trigger: "blur" },
    { min: 12, message: "密码至少12位", trigger: "blur" }
  ],
  confirmPassword: [
    { required: true, message: "请确认密码", trigger: "blur" },
    {
      validator: (rule, value) => {
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
 * Handles form submission.
 * Validates form first, then emits submit event with new password.
 */
const handleSubmit = async (): Promise<void> => {
  try {
    await formRef.value?.validate();
    emit("submit", formData.value.newPassword);
  } catch {
    // Validation failed, errors shown in form
  }
};
</script>

<template>
  <div class="admin-grid">
    <p class="admin-form-instruction">首次登录需要修改初始密码。</p>
    <NAlert v-if="errorMessage" type="error" :title="errorMessage" />
    <NForm ref="formRef" :model="formData" :rules="rules" @keyup.enter="handleSubmit">
      <NFormItem path="newPassword" label="新密码">
        <NInput
          v-model:value="formData.newPassword"
          type="password"
          placeholder="请输入新密码（至少12位）"
          show-password-on="click"
          :disabled="pending"
          autofocus
        />
      </NFormItem>
      <NFormItem path="confirmPassword" label="确认密码">
        <NInput
          v-model:value="formData.confirmPassword"
          type="password"
          placeholder="请再次输入新密码"
          show-password-on="click"
          :disabled="pending"
        />
      </NFormItem>
    </NForm>
    <NButton
      type="primary"
      :loading="pending"
      :disabled="!formData.newPassword || !formData.confirmPassword"
      block
      @click="handleSubmit"
    >
      确认修改
    </NButton>
  </div>
</template>
