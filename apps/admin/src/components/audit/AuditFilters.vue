<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NForm, NFormItem, NSelect } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";
import { AuditAction } from "../../../../../contracts/gen/ts/platform/audit/v1/audit_pb";

export type AuditFilterPayload = {
  actorAdminId: string;
  targetUserId: string;
  actions: AuditAction[];
  startedAt: string;
  endedAt: string;
};

const props = defineProps<{
  loading: boolean;
}>();

const emit = defineEmits<{
  search: [payload: AuditFilterPayload];
}>();

const formRef = ref<FormInst | null>(null);
const form = ref<AuditFilterPayload>({
  actorAdminId: "",
  targetUserId: "",
  actions: [],
  startedAt: "",
  endedAt: ""
});

const actionOptions = [
  { label: "实名读取", value: AuditAction.REAL_NAME_READ },
  { label: "实名修改", value: AuditAction.REAL_NAME_UPDATED },
  { label: "用户封禁", value: AuditAction.USER_SUSPENDED },
  { label: "解除封禁", value: AuditAction.USER_UNSUSPENDED },
  { label: "审计读取", value: AuditAction.AUDIT_EVENTS_READ }
];

const rules = computed<FormRules>(() => ({
  endedAt: [
    {
      validator: () => {
        if (!form.value.startedAt || !form.value.endedAt) {
          return Promise.resolve();
        }
        return Date.parse(form.value.endedAt) >= Date.parse(form.value.startedAt)
          ? Promise.resolve()
          : Promise.reject(new Error("结束时间不能早于开始时间"));
      },
      trigger: ["change", "blur"]
    }
  ]
}));

const handleSubmit = async (): Promise<void> => {
  await formRef.value?.validate();
  emit("search", { ...form.value });
};
</script>

<template>
  <NForm ref="formRef" :model="form" :rules="rules" class="admin-card-shell admin-section-card">
    <div class="admin-grid admin-grid--two">
      <NFormItem label="Actor admin UUID">
        <NInput v-model:value="form.actorAdminId" placeholder="可选" />
      </NFormItem>
      <NFormItem label="Target user UUID">
        <NInput v-model:value="form.targetUserId" placeholder="可选" />
      </NFormItem>
      <NFormItem label="Actions">
        <NSelect v-model:value="form.actions" multiple :options="actionOptions" />
      </NFormItem>
      <NFormItem label="开始时间">
        <input v-model="form.startedAt" class="admin-native-input" type="datetime-local" />
      </NFormItem>
      <NFormItem label="结束时间" path="endedAt">
        <input v-model="form.endedAt" class="admin-native-input" type="datetime-local" />
      </NFormItem>
      <NButton type="primary" :loading="props.loading" @click="handleSubmit">应用筛选</NButton>
    </div>
  </NForm>
</template>
