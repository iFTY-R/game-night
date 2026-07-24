<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NForm, NFormItem, NInput, NRadioButton, NRadioGroup } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";

export type UserLookupPayload = {
  kind: "userId" | "username";
  value: string;
};

const props = defineProps<{
  loading: boolean;
}>();

const emit = defineEmits<{
  lookup: [payload: UserLookupPayload];
}>();

const formRef = ref<FormInst | null>(null);
const form = ref<UserLookupPayload>({
  kind: "userId",
  value: ""
});

const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const usernamePattern = /^[a-z0-9](?:[a-z0-9._-]{1,30}[a-z0-9])?$/;

const rules = computed<FormRules>(() => ({
  value: [
    { required: true, message: "请输入查询值", trigger: ["input", "blur"] },
    {
      validator: () => {
        if (form.value.kind === "userId") {
          return uuidPattern.test(form.value.value)
            ? Promise.resolve()
            : Promise.reject(new Error("请输入 canonical UUID"));
        }
        return usernamePattern.test(form.value.value.trim().toLowerCase())
          ? Promise.resolve()
          : Promise.reject(new Error("请输入规范化用户名"));
      },
      trigger: ["input", "blur"]
    }
  ]
}));

const handleSubmit = async (): Promise<void> => {
  await formRef.value?.validate();
  emit("lookup", {
    kind: form.value.kind,
    value: form.value.kind === "username" ? form.value.value.trim().toLowerCase() : form.value.value.trim()
  });
};
</script>

<template>
  <NForm ref="formRef" :model="form" :rules="rules" class="admin-card-shell admin-section-card">
    <div class="admin-grid">
      <NFormItem label="查询方式">
        <NRadioGroup v-model:value="form.kind" name="lookup-kind">
          <NRadioButton value="userId">UUID</NRadioButton>
          <NRadioButton value="username">用户名</NRadioButton>
        </NRadioGroup>
      </NFormItem>
      <NFormItem :label="form.kind === 'userId' ? '用户 UUID' : '用户名'" path="value">
        <NInput
          v-model:value="form.value"
          :placeholder="form.kind === 'userId' ? '00000000-0000-0000-0000-000000000000' : 'normalized_username'"
        />
      </NFormItem>
      <NButton type="primary" :loading="props.loading" @click="handleSubmit">精确查询</NButton>
    </div>
  </NForm>
</template>
