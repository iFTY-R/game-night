<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NForm, NFormItem, NInput } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";

const props = defineProps<{
  pending: boolean;
  errorMessage?: string;
}>();

const emit = defineEmits<{
  submit: [password: string];
}>();

const formRef = ref<FormInst | null>(null);
const form = ref({
  password: ""
});

const rules = computed<FormRules>(() => ({
  password: [{ required: true, message: "请输入管理员密码", trigger: ["input", "blur"] }]
}));

const handleSubmit = async (): Promise<void> => {
  await formRef.value?.validate();
  emit("submit", form.value.password);
};
</script>

<template>
  <NForm ref="formRef" :model="form" :rules="rules">
    <NFormItem label="管理员密码" path="password">
      <NInput v-model:value="form.password" type="password" show-password-on="mousedown" placeholder="输入当前管理员密码" />
    </NFormItem>
    <NButton type="primary" block :loading="pending" @click="handleSubmit">继续</NButton>
    <p v-if="props.errorMessage" class="auth-form__error" role="alert">{{ props.errorMessage }}</p>
  </NForm>
</template>
