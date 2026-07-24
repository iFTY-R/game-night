<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NForm, NFormItem, NInput, NRadioButton, NRadioGroup } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";

const props = defineProps<{
  pending: boolean;
}>();

const emit = defineEmits<{
  totp: [code: string];
  recovery: [code: string];
}>();

const mode = ref<"totp" | "recovery">("totp");
const formRef = ref<FormInst | null>(null);
const form = ref({ code: "" });

const rules = computed<FormRules>(() => ({
  code: [{ required: true, message: mode.value === "totp" ? "请输入 6 位验证码" : "请输入恢复码", trigger: ["input", "blur"] }]
}));

const handleSubmit = async (): Promise<void> => {
  await formRef.value?.validate();
  if (mode.value === "totp") {
    emit("totp", form.value.code);
    return;
  }
  emit("recovery", form.value.code);
};
</script>

<template>
  <div class="admin-grid">
    <NRadioGroup v-model:value="mode" name="mfa-mode">
      <NRadioButton value="totp">验证器</NRadioButton>
      <NRadioButton value="recovery">恢复码</NRadioButton>
    </NRadioGroup>
    <NForm ref="formRef" :model="form" :rules="rules">
      <NFormItem :label="mode === 'totp' ? 'TOTP 验证码' : '恢复码'" path="code">
        <NInput v-model:value="form.code" :maxlength="mode === 'totp' ? 6 : 64" />
      </NFormItem>
      <NButton type="primary" block :loading="props.pending" @click="handleSubmit">
        {{ mode === "totp" ? "验证并进入后台" : "使用恢复码继续" }}
      </NButton>
    </NForm>
  </div>
</template>
