<script setup lang="ts">
import { computed, reactive, ref, shallowRef } from "vue";
import { NAlert, NButton, NDatePicker, NDescriptions, NDescriptionsItem, NForm, NFormItem, NInput, NSwitch } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";
import AppDialog from "../../../components/AppDialog.vue";
import { applyMaintenanceChange, previewMaintenanceChange } from "../../../api/admin-operations";
import { createOperationId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { AdminElevationScope } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { AdminMaintenanceScope, type AdminMaintenanceState, type PreviewMaintenanceChangeResponse } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_operations_pb";
import { formatDateTime } from "../../../utils/format";
import ElevationDialog from "../../security/components/ElevationDialog.vue";

export type MaintenanceDialogPayload = {
  current: AdminMaintenanceState;
  onApplied: () => void;
  onConflict: () => void;
};

const dialogRef = ref<{ toggleDialog: (open: boolean, payload?: MaintenanceDialogPayload) => void } | null>(null);
const elevationDialogRef = ref<InstanceType<typeof ElevationDialog> | null>(null);
const formRef = ref<FormInst | null>(null);
const currentPayload = ref<MaintenanceDialogPayload | null>(null);
const controller = shallowRef<AbortController | null>(null);
const requestGeneration = ref(0);
const previewing = ref(false);
const applying = ref(false);
const errorMessage = ref("");
const preview = ref<PreviewMaintenanceChangeResponse | null>(null);
const form = reactive({ enabled: false, reason: "", plannedEndAt: null as number | null });

const rules: FormRules = {
  reason: [
    { required: true, message: "请输入维护原因", trigger: ["input", "blur"] },
    { min: 4, max: 512, message: "原因需为 4 至 512 个字符", trigger: ["input", "blur"] }
  ]
};

const targetLabel = computed(() => form.enabled ? "开启维护" : "恢复开放");

/** Initializes one transition from the latest authoritative maintenance state. */
const toggleDialog = (open: boolean, payload?: MaintenanceDialogPayload): void => {
  if (open && payload) {
    requestGeneration.value += 1;
    controller.value?.abort();
    controller.value = new AbortController();
    currentPayload.value = payload;
    form.enabled = !payload.current.enabled;
    form.reason = "";
    form.plannedEndAt = null;
    preview.value = null;
    errorMessage.value = "";
  }
  dialogRef.value?.toggleDialog(open, payload);
};

/** Cancels in-flight work and erases preview-bound form state after every close path. */
const handleClose = (): void => {
  requestGeneration.value += 1;
  controller.value?.abort();
  controller.value = null;
  currentPayload.value = null;
  preview.value = null;
  form.enabled = false;
  form.reason = "";
  form.plannedEndAt = null;
  previewing.value = false;
  applying.value = false;
  errorMessage.value = "";
  formRef.value?.restoreValidation();
};

const validateForm = async (): Promise<boolean> => {
  try {
    await formRef.value?.validate();
    return true;
  } catch {
    return false;
  }
};

/** Creates a short-lived server preview; changing any form field invalidates it locally. */
const handlePreview = async (): Promise<void> => {
  if (!(await validateForm()) || previewing.value) return;
  const generation = requestGeneration.value;
  const signal = controller.value?.signal;
  previewing.value = true;
  errorMessage.value = "";
  try {
    const response = await previewMaintenanceChange({
      enabled: form.enabled,
      scope: AdminMaintenanceScope.USER_MUTATIONS,
      reason: form.reason.trim(),
      plannedEndAt: form.plannedEndAt ? new Date(form.plannedEndAt) : null,
      ...(signal ? { signal } : {})
    });
    if (generation === requestGeneration.value && !signal?.aborted) preview.value = response;
  } catch (error) {
    if (generation === requestGeneration.value && !signal?.aborted) errorMessage.value = error instanceof AdminApiError ? error.message : "维护预览失败，请稍后重试。";
  } finally {
    if (generation === requestGeneration.value) previewing.value = false;
  }
};

const openElevation = (): void => {
  elevationDialogRef.value?.toggleDialog(true, {
    scope: AdminElevationScope.OPERATIONS_MAINTENANCE,
    allowRecoveryCode: true,
    onElevated: () => void executeApply()
  });
};

/** Applies the exact preview; version conflicts force the parent to reload authority before another attempt. */
const executeApply = async (): Promise<void> => {
  const reviewed = preview.value;
  const payload = currentPayload.value;
  if (!reviewed || !payload || applying.value) return;
  const generation = requestGeneration.value;
  const signal = controller.value?.signal;
  applying.value = true;
  errorMessage.value = "";
  try {
    await applyMaintenanceChange({
      operationId: createOperationId(),
      enabled: form.enabled,
      scope: AdminMaintenanceScope.USER_MUTATIONS,
      reason: form.reason.trim(),
      plannedEndAt: form.plannedEndAt ? new Date(form.plannedEndAt) : null,
      expectedVersion: reviewed.current?.version ?? 0n,
      previewDigest: reviewed.previewDigest,
      ...(signal ? { signal } : {})
    });
    if (generation !== requestGeneration.value || signal?.aborted) return;
    payload.onApplied();
    dialogRef.value?.toggleDialog(false);
  } catch (error) {
    if (generation !== requestGeneration.value || signal?.aborted) return;
    if (error instanceof AdminApiError && ["admin.elevation.required", "admin.elevation.expired"].includes(error.businessKey)) {
      openElevation();
      return;
    }
    if (error instanceof AdminApiError && error.businessKey === "admin.version.conflict") {
      payload.onConflict();
      preview.value = null;
    }
    errorMessage.value = error instanceof AdminApiError ? error.message : "维护状态更新失败，请稍后重试。";
  } finally {
    if (generation === requestGeneration.value) applying.value = false;
  }
};

const invalidatePreview = (): void => { preview.value = null; };

defineExpose({ toggleDialog });
</script>

<template>
  <AppDialog ref="dialogRef" title="维护控制" :width="620" @closed="handleClose">
    <template #default="{ close }">
      <div class="operations-command-dialog">
        <NAlert v-if="errorMessage" type="error" :show-icon="false">{{ errorMessage }}</NAlert>
        <NForm ref="formRef" :model="form" :rules="rules" label-placement="top">
          <NFormItem label="目标状态">
            <div class="maintenance-toggle">
              <NSwitch v-model:value="form.enabled" :disabled="previewing || applying" @update:value="invalidatePreview" />
              <strong>{{ targetLabel }}</strong>
            </div>
          </NFormItem>
          <NFormItem path="reason" label="操作原因">
            <NInput v-model:value="form.reason" type="textarea" :maxlength="512" show-count :autosize="{ minRows: 3, maxRows: 5 }" @update:value="invalidatePreview" />
          </NFormItem>
          <NFormItem label="计划结束时间">
            <NDatePicker v-model:value="form.plannedEndAt" type="datetime" clearable :disabled="!form.enabled" style="width: 100%" @update:value="invalidatePreview" />
          </NFormItem>
        </NForm>

        <NDescriptions v-if="preview" label-placement="left" :column="2" bordered size="small">
          <NDescriptionsItem label="当前版本">{{ preview.current?.version?.toString() ?? "-" }}</NDescriptionsItem>
          <NDescriptionsItem label="预览失效">{{ formatDateTime(preview.expiresAt) }}</NDescriptionsItem>
          <NDescriptionsItem label="活跃房间">{{ preview.activeRooms.toString() }}</NDescriptionsItem>
          <NDescriptionsItem label="进行中牌局">{{ preview.activeGames.toString() }}</NDescriptionsItem>
        </NDescriptions>
        <NAlert v-if="preview" type="warning" :show-icon="false">执行后将拒绝用户、房间和牌局的变更请求，管理读取与认证恢复继续可用。</NAlert>

        <div class="admin-dialog-footer">
          <NButton :disabled="previewing || applying" @click="close">取消</NButton>
          <NButton :loading="previewing" :disabled="applying" @click="handlePreview">生成预览</NButton>
          <NButton type="primary" :loading="applying" :disabled="!preview || previewing" @click="executeApply">确认执行</NButton>
        </div>
      </div>
    </template>
  </AppDialog>
  <ElevationDialog ref="elevationDialogRef" />
</template>

<style scoped>
.operations-command-dialog { display: grid; gap: 14px; }
.maintenance-toggle { display: flex; align-items: center; gap: 10px; min-height: 34px; }
</style>
