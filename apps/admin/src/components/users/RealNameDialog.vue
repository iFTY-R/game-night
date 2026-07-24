<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NForm, NFormItem, NInput, NSpace } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";
import AppDialog from "../AppDialog.vue";

export type RealNameDialogPayload = {
  mode: "read" | "update";
  userId: string;
  username: string;
  realName?: string;
};

const emit = defineEmits<{
  submit: [payload: { mode: "read" | "update"; userId: string; reason: string; realName?: string }];
}>();

const dialogRef = ref<{ toggleDialog: (open: boolean, payload?: RealNameDialogPayload) => void } | null>(null);
const formRef = ref<FormInst | null>(null);
const payload = ref<RealNameDialogPayload | null>(null);
const form = ref({
  reason: "",
  realName: ""
});

const rules = computed<FormRules>(() => ({
  reason: [{ required: true, message: "reason 为必填审计字段", trigger: ["input", "blur"] }],
  realName:
    payload.value?.mode === "update"
      ? [{ required: true, message: "请输入新的实名", trigger: ["input", "blur"] }]
      : []
}));

const toggleDialog = (open: boolean, nextPayload?: RealNameDialogPayload): void => {
  if (open) {
    payload.value = nextPayload ?? null;
    form.value.reason = "";
    form.value.realName = nextPayload?.realName ?? "";
  }
  dialogRef.value?.toggleDialog(open, nextPayload);
};

const handleSubmit = async (): Promise<void> => {
  await formRef.value?.validate();
  if (!payload.value) {
    return;
  }
  emit("submit", {
    mode: payload.value.mode,
    userId: payload.value.userId,
    reason: form.value.reason,
    ...(payload.value.mode === "update" ? { realName: form.value.realName } : {})
  });
  toggleDialog(false);
};

defineExpose({
  toggleDialog
});
</script>

<template>
  <AppDialog ref="dialogRef" :title="payload?.mode === 'update' ? '修改实名' : '读取实名'" :width="560">
    <template #default>
      <NForm ref="formRef" :model="form" :rules="rules">
        <p class="admin-muted">目标：{{ payload?.username }} / {{ payload?.userId }}</p>
        <NFormItem label="审计原因" path="reason">
          <NInput v-model:value="form.reason" type="textarea" :autosize="{ minRows: 3, maxRows: 5 }" />
        </NFormItem>
        <NFormItem v-if="payload?.mode === 'update'" label="新的实名" path="realName">
          <NInput v-model:value="form.realName" />
        </NFormItem>
        <NSpace justify="end">
          <NButton secondary @click="toggleDialog(false)">取消</NButton>
          <NButton type="primary" @click="handleSubmit">{{ payload?.mode === "update" ? "提交修改" : "确认读取" }}</NButton>
        </NSpace>
      </NForm>
    </template>
  </AppDialog>
</template>
