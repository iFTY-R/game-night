<script setup lang="ts">
import { ref } from "vue";
import { NButton, NForm, NFormItem, NInput, NSpace } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";
import AppDialog from "../AppDialog.vue";

type LogoutDialogPayload = {
  mode: "current" | "all";
  summary: string;
};

const emit = defineEmits<{
  confirm: [payload: LogoutDialogPayload];
}>();

const dialogRef = ref<{ toggleDialog: (open: boolean, payload?: LogoutDialogPayload) => void } | null>(null);
const formRef = ref<FormInst | null>(null);
const currentPayload = ref<LogoutDialogPayload | null>(null);
const form = ref({ phrase: "" });

const rules: FormRules = {
  phrase: [{ required: true, message: "请输入确认短语", trigger: ["input", "blur"] }]
};

const toggleDialog = (open: boolean, payload?: LogoutDialogPayload): void => {
  if (open) {
    currentPayload.value = payload ?? null;
    form.value.phrase = "";
  }
  dialogRef.value?.toggleDialog(open, payload);
};

const handleConfirm = async (): Promise<void> => {
  await formRef.value?.validate();
  if (!currentPayload.value) {
    return;
  }
  emit("confirm", currentPayload.value);
  toggleDialog(false);
};

defineExpose({
  toggleDialog
});
</script>

<template>
  <AppDialog ref="dialogRef" title="确认退出" :width="520">
    <template #default>
      <NForm ref="formRef" :model="form" :rules="rules">
        <p class="admin-muted">{{ currentPayload?.summary }}</p>
        <NFormItem label="确认短语" path="phrase">
          <NInput v-model:value="form.phrase" placeholder="输入 yes 继续" />
        </NFormItem>
        <NSpace justify="end">
          <NButton secondary @click="toggleDialog(false)">取消</NButton>
          <NButton type="error" @click="handleConfirm">确认退出</NButton>
        </NSpace>
      </NForm>
    </template>
  </AppDialog>
</template>
