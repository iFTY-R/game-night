<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NForm, NFormItem, NInput, NSpace } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";
import AppDialog from "../AppDialog.vue";

export type UserStatusDialogPayload = {
  mode: "suspend" | "unsuspend";
  userId: string;
  username: string;
  currentStatus: string;
};

const emit = defineEmits<{
  submit: [payload: { mode: "suspend" | "unsuspend"; userId: string; reason: string }];
}>();

const dialogRef = ref<{ toggleDialog: (open: boolean, payload?: UserStatusDialogPayload) => void } | null>(null);
const formRef = ref<FormInst | null>(null);
const payload = ref<UserStatusDialogPayload | null>(null);
const form = ref({ reason: "" });

const rules = computed<FormRules>(() => ({
  reason: [{ required: true, message: "reason 为必填审计字段", trigger: ["input", "blur"] }]
}));

const toggleDialog = (open: boolean, nextPayload?: UserStatusDialogPayload): void => {
  if (open) {
    payload.value = nextPayload ?? null;
    form.value.reason = "";
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
    reason: form.value.reason
  });
  toggleDialog(false);
};

defineExpose({
  toggleDialog
});
</script>

<template>
  <AppDialog ref="dialogRef" :title="payload?.mode === 'suspend' ? '封禁用户' : '解除封禁'" :width="560">
    <template #default>
      <NForm ref="formRef" :model="form" :rules="rules">
        <div class="admin-list">
          <div class="admin-list__row"><span class="admin-muted">目标</span><span>{{ payload?.username }}</span></div>
          <div class="admin-list__row"><span class="admin-muted">UUID</span><span class="admin-code">{{ payload?.userId }}</span></div>
          <div class="admin-list__row"><span class="admin-muted">当前状态</span><span>{{ payload?.currentStatus }}</span></div>
        </div>
        <NFormItem label="审计原因" path="reason">
          <NInput v-model:value="form.reason" type="textarea" :autosize="{ minRows: 3, maxRows: 5 }" />
        </NFormItem>
        <NSpace justify="end">
          <NButton secondary @click="toggleDialog(false)">取消</NButton>
          <NButton :type="payload?.mode === 'suspend' ? 'error' : 'primary'" @click="handleSubmit">
            {{ payload?.mode === "suspend" ? "确认封禁" : "确认解除" }}
          </NButton>
        </NSpace>
      </NForm>
    </template>
  </AppDialog>
</template>
