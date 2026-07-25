<script setup lang="ts">
import { ref } from "vue";
import { NAlert, NButton, NForm, NFormItem, NInput } from "naive-ui";
import type { FormInst } from "naive-ui";

// Component props
defineProps<{
  pending?: boolean;
  errorMessage?: string;
}>();

// Component emits
const emit = defineEmits<{
  submit: [password: string];
}>();

// Form state
const formRef = ref<FormInst | null>(null);
const password = ref("");

// Form validation rules
const rules = {
  password: [{ required: true, message: "请输入密码", trigger: "blur" }]
};

/**
 * Handles form submission.
 * Validates form first, then emits submit event with password.
 */
const handleSubmit = async (): Promise<void> => {
  try {
    await formRef.value?.validate();
    emit("submit", password.value);
  } catch {
    // Validation failed, do nothing (errors shown in form)
  }
};
</script>

<template>
  <div class="admin-grid">
    <p class="admin-form-instruction">请输入管理员密码登录。</p>
    <NAlert v-if="errorMessage" type="error" :title="errorMessage" />
    <NForm ref="formRef" :model="{ password }" :rules="rules" @keyup.enter="handleSubmit">
      <NFormItem path="password" label="密码">
        <NInput
          v-model:value="password"
          type="password"
          placeholder="请输入密码"
          show-password-on="click"
          :disabled="pending"
          autofocus
        />
      </NFormItem>
    </NForm>
    <NButton type="primary" :loading="pending" :disabled="!password" block @click="handleSubmit">登录</NButton>
  </div>
</template>
