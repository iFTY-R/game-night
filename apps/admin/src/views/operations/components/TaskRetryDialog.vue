<script setup lang="ts">
import { reactive, ref, shallowRef } from "vue";
import { NAlert, NButton, NDescriptions, NDescriptionsItem, NForm, NFormItem, NInput, NSelect, NTag } from "naive-ui";
import type { FormInst, FormRules, SelectOption } from "naive-ui";
import AppDialog from "../../../components/AppDialog.vue";
import { applyTaskRetry, previewTaskRetry } from "../../../api/admin-operations";
import { createOperationId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { AdminElevationScope } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { AdminRetryTaskKind, type PreviewTaskRetryResponse } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_operations_pb";
import { formatDateTime } from "../../../utils/format";
import ElevationDialog from "../../security/components/ElevationDialog.vue";

export type TaskRetryDialogPayload = {
  taskKind?: AdminRetryTaskKind;
  taskId?: string;
  onApplied: () => void;
  onConflict: () => void;
};

const taskKindOptions: SelectOption[] = [
  { label: "批量用户任务", value: AdminRetryTaskKind.USER_BATCH },
  { label: "用户擦除任务", value: AdminRetryTaskKind.USER_ERASURE }
];

const dialogRef = ref<{ toggleDialog: (open: boolean, payload?: TaskRetryDialogPayload) => void } | null>(null);
const elevationDialogRef = ref<InstanceType<typeof ElevationDialog> | null>(null);
const formRef = ref<FormInst | null>(null);
const currentPayload = ref<TaskRetryDialogPayload | null>(null);
const controller = shallowRef<AbortController | null>(null);
const generation = ref(0);
const previewing = ref(false);
const applying = ref(false);
const errorMessage = ref("");
const preview = ref<PreviewTaskRetryResponse | null>(null);
const form = reactive({ taskKind: AdminRetryTaskKind.USER_BATCH, taskId: "", reason: "" });
const rules: FormRules = {
  taskKind: { required: true, type: "number", message: "请选择任务类型", trigger: "change" },
  taskId: [
    { required: true, message: "请输入任务 ID", trigger: ["input", "blur"] },
    { pattern: /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u, message: "请输入规范 UUID", trigger: ["input", "blur"] }
  ],
  reason: [
    { required: true, message: "请输入重试原因", trigger: ["input", "blur"] },
    { min: 4, max: 512, message: "原因需为 4 至 512 个字符", trigger: ["input", "blur"] }
  ]
};

/** Opens a retry flow optionally prefilled from an overview failed-task row. */
const toggleDialog = (open: boolean, payload?: TaskRetryDialogPayload): void => {
  if (open && payload) {
    generation.value += 1;
    controller.value?.abort();
    controller.value = new AbortController();
    currentPayload.value = payload;
    form.taskKind = payload.taskKind ?? AdminRetryTaskKind.USER_BATCH;
    form.taskId = payload.taskId ?? "";
    form.reason = "";
    preview.value = null;
    errorMessage.value = "";
  }
  dialogRef.value?.toggleDialog(open, payload);
};

/** Aborts stale work and removes the task ID/reason when the dialog closes. */
const handleClose = (): void => {
  generation.value += 1;
  controller.value?.abort();
  controller.value = null;
  currentPayload.value = null;
  preview.value = null;
  form.taskKind = AdminRetryTaskKind.USER_BATCH;
  form.taskId = "";
  form.reason = "";
  previewing.value = false;
  applying.value = false;
  errorMessage.value = "";
  formRef.value?.restoreValidation();
};

const validateForm = async (): Promise<boolean> => {
  try { await formRef.value?.validate(); return true; } catch { return false; }
};

const invalidatePreview = (): void => { preview.value = null; };

/** Loads redacted task state and manual retry count before execution. */
const handlePreview = async (): Promise<void> => {
  if (!(await validateForm()) || previewing.value) return;
  const token = generation.value;
  const signal = controller.value?.signal;
  previewing.value = true;
  errorMessage.value = "";
  try {
    const response = await previewTaskRetry({ taskKind: form.taskKind, taskId: form.taskId.trim(), reason: form.reason.trim(), ...(signal ? { signal } : {}) });
    if (token === generation.value && !signal?.aborted) preview.value = response;
  } catch (error) {
    if (token === generation.value && !signal?.aborted) errorMessage.value = error instanceof AdminApiError ? error.message : "任务重试预览失败，请稍后重试。";
  } finally {
    if (token === generation.value) previewing.value = false;
  }
};

const openElevation = (): void => {
  elevationDialogRef.value?.toggleDialog(true, { scope: AdminElevationScope.OPERATIONS_MAINTENANCE, allowRecoveryCode: true, onElevated: () => void executeApply() });
};

/** Requeues only the reviewed failed version and preserves the original durable task identity. */
const executeApply = async (): Promise<void> => {
  const reviewed = preview.value;
  const payload = currentPayload.value;
  if (!reviewed || !payload || !reviewed.retryAllowed || applying.value) return;
  const token = generation.value;
  const signal = controller.value?.signal;
  applying.value = true;
  errorMessage.value = "";
  try {
    await applyTaskRetry({
      operationId: createOperationId(), taskKind: form.taskKind, taskId: form.taskId.trim(), reason: form.reason.trim(),
      expectedTaskVersion: reviewed.taskVersion, previewDigest: reviewed.previewDigest, ...(signal ? { signal } : {})
    });
    if (token !== generation.value || signal?.aborted) return;
    payload.onApplied();
    dialogRef.value?.toggleDialog(false);
  } catch (error) {
    if (token !== generation.value || signal?.aborted) return;
    if (error instanceof AdminApiError && ["admin.elevation.required", "admin.elevation.expired"].includes(error.businessKey)) { openElevation(); return; }
    if (error instanceof AdminApiError && error.businessKey === "admin.version.conflict") { payload.onConflict(); preview.value = null; }
    errorMessage.value = error instanceof AdminApiError ? error.message : "任务重试失败，请稍后重试。";
  } finally {
    if (token === generation.value) applying.value = false;
  }
};

defineExpose({ toggleDialog });
</script>

<template>
  <AppDialog ref="dialogRef" title="重试失败任务" :width="600" @closed="handleClose">
    <template #default="{ close }">
      <div class="operations-command-dialog">
        <NAlert v-if="errorMessage" type="error" :show-icon="false">{{ errorMessage }}</NAlert>
        <NForm ref="formRef" :model="form" :rules="rules" label-placement="top">
          <NFormItem path="taskKind" label="任务类型"><NSelect v-model:value="form.taskKind" :options="taskKindOptions" @update:value="invalidatePreview" /></NFormItem>
          <NFormItem path="taskId" label="任务 ID"><NInput v-model:value="form.taskId" placeholder="00000000-0000-0000-0000-000000000000" @update:value="invalidatePreview" /></NFormItem>
          <NFormItem path="reason" label="操作原因"><NInput v-model:value="form.reason" type="textarea" :maxlength="512" show-count :autosize="{ minRows: 3, maxRows: 5 }" @update:value="invalidatePreview" /></NFormItem>
        </NForm>
        <NDescriptions v-if="preview" label-placement="left" :column="2" bordered size="small">
          <NDescriptionsItem label="任务版本">{{ preview.taskVersion.toString() }}</NDescriptionsItem>
          <NDescriptionsItem label="人工重试">{{ preview.manualRetryCount }} / 3</NDescriptionsItem>
          <NDescriptionsItem label="错误代码">{{ preview.stableErrorCode || "-" }}</NDescriptionsItem>
          <NDescriptionsItem label="重试资格"><NTag :type="preview.retryAllowed ? 'success' : 'error'" size="small">{{ preview.retryAllowed ? "允许" : "拒绝" }}</NTag></NDescriptionsItem>
          <NDescriptionsItem label="预览失效" :span="2">{{ formatDateTime(preview.expiresAt) }}</NDescriptionsItem>
        </NDescriptions>
        <NAlert v-if="preview && !preview.retryAllowed" type="error" :show-icon="false">该任务不是可重试的失败状态，或已达到人工重试上限。</NAlert>
        <div class="admin-dialog-footer">
          <NButton :disabled="previewing || applying" @click="close">取消</NButton>
          <NButton :loading="previewing" :disabled="applying" @click="handlePreview">生成预览</NButton>
          <NButton type="primary" :loading="applying" :disabled="!preview?.retryAllowed || previewing" @click="executeApply">确认重试</NButton>
        </div>
      </div>
    </template>
  </AppDialog>
  <ElevationDialog ref="elevationDialogRef" />
</template>

<style scoped>.operations-command-dialog { display: grid; gap: 14px; }</style>
