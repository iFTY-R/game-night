<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NForm, NFormItem, NInput } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";

const props = defineProps<{
  pending: boolean;
}>();

const emit = defineEmits<{
  submit: [password: string];
}>();

const formRef = ref<FormInst | null>(null);
const form = ref({ password: "", confirmPassword: "" });

const rules = computed<FormRules>(() => ({
  password: [{ required: true, min: 12, message: "新密码至少 12 位", trigger: ["input", "blur"] }],
  confirmPassword: [
    { required: true, message: "请再次输入密码", trigger: ["input", "blur"] },
    {
      validator: () =>
        form.value.password === form.value.confirmPassword ? Promise.resolve() : Promise.reject(new Error("两次输入的密码不一致")),
      trigger: ["input", "blur"]
    }
  ]
}));

const handleSubmit = async (): Promise<void> => {
  await formRef.value?.validate();
  emit("submit", form.value.password);
};
</script>

<template>
  <NForm ref="formRef" :model="form" :rules="rules">
    <NFormItem label="新密码" path="password">
      <NInput v-model:value="form.password" type="password" show-password-on="mousedown" />
    </NFormItem>
    <NFormItem label="确认新密码" path="confirmPassword">
      <NInput v-model:value="form.confirmPassword" type="password" show-password-on="mousedown" />
    </NFormItem>
    <NButton type="primary" block :loading="pending" @click="handleSubmit">保存并继续</NButton>
  </NForm>
</template>
