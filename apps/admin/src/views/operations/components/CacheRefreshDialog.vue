<script setup lang="ts">
import { reactive, ref, shallowRef } from "vue";
import { NAlert, NButton, NDescriptions, NDescriptionsItem, NForm, NFormItem, NInput, NSelect } from "naive-ui";
import type { FormInst, FormRules, SelectOption } from "naive-ui";
import AppDialog from "../../../components/AppDialog.vue";
import { applyCacheRefresh, previewCacheRefresh } from "../../../api/admin-operations";
import { createOperationId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { AdminElevationScope } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { AdminCacheNamespace, type PreviewCacheRefreshResponse } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_operations_pb";
import { formatDateTime } from "../../../utils/format";
import ElevationDialog from "../../security/components/ElevationDialog.vue";

export type CacheRefreshDialogPayload = { onApplied: () => void; onConflict: () => void };

const namespaceOptions: SelectOption[] = [
  { label: "运营概览投影", value: AdminCacheNamespace.OVERVIEW_PROJECTION },
  { label: "运维探针投影", value: AdminCacheNamespace.OPERATIONS_PROBES },
  { label: "实时在线投影", value: AdminCacheNamespace.REALTIME_PRESENCE_PROJECTION }
];

const dialogRef = ref<{ toggleDialog: (open: boolean, payload?: CacheRefreshDialogPayload) => void } | null>(null);
const elevationDialogRef = ref<InstanceType<typeof ElevationDialog> | null>(null);
const formRef = ref<FormInst | null>(null);
const currentPayload = ref<CacheRefreshDialogPayload | null>(null);
const controller = shallowRef<AbortController | null>(null);
const generation = ref(0);
const previewing = ref(false);
const applying = ref(false);
const errorMessage = ref("");
const preview = ref<PreviewCacheRefreshResponse | null>(null);
const form = reactive({ namespace: AdminCacheNamespace.OVERVIEW_PROJECTION, reason: "" });
const rules: FormRules = {
  namespace: { required: true, type: "number", message: "请选择缓存投影", trigger: "change" },
  reason: [
    { required: true, message: "请输入刷新原因", trigger: ["input", "blur"] },
    { min: 4, max: 512, message: "原因需为 4 至 512 个字符", trigger: ["input", "blur"] }
  ]
};

/** Opens a fresh fixed-namespace cache command flow. */
const toggleDialog = (open: boolean, payload?: CacheRefreshDialogPayload): void => {
  if (open && payload) {
    generation.value += 1;
    controller.value?.abort();
    controller.value = new AbortController();
    currentPayload.value = payload;
    form.namespace = AdminCacheNamespace.OVERVIEW_PROJECTION;
    form.reason = "";
    preview.value = null;
    errorMessage.value = "";
  }
  dialogRef.value?.toggleDialog(open, payload);
};

/** Clears the preview and aborts stale requests after every close path. */
const handleClose = (): void => {
  generation.value += 1;
  controller.value?.abort();
  controller.value = null;
  currentPayload.value = null;
  preview.value = null;
  form.namespace = AdminCacheNamespace.OVERVIEW_PROJECTION;
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

/** Reads generation and real estimated owned entries before execution is possible. */
const handlePreview = async (): Promise<void> => {
  if (!(await validateForm()) || previewing.value) return;
  const token = generation.value;
  const signal = controller.value?.signal;
  previewing.value = true;
  errorMessage.value = "";
  try {
    const response = await previewCacheRefresh({ namespace: form.namespace, reason: form.reason.trim(), ...(signal ? { signal } : {}) });
    if (token === generation.value && !signal?.aborted) preview.value = response;
  } catch (error) {
    if (token === generation.value && !signal?.aborted) errorMessage.value = error instanceof AdminApiError ? error.message : "缓存刷新预览失败，请稍后重试。";
  } finally {
    if (token === generation.value) previewing.value = false;
  }
};

const openElevation = (): void => {
  elevationDialogRef.value?.toggleDialog(true, { scope: AdminElevationScope.OPERATIONS_MAINTENANCE, allowRecoveryCode: true, onElevated: () => void executeApply() });
};

/** Advances only the reviewed PostgreSQL generation; no arbitrary Redis key reaches this flow. */
const executeApply = async (): Promise<void> => {
  const reviewed = preview.value;
  const payload = currentPayload.value;
  if (!reviewed || !payload || applying.value) return;
  const token = generation.value;
  const signal = controller.value?.signal;
  applying.value = true;
  errorMessage.value = "";
  try {
    await applyCacheRefresh({
      operationId: createOperationId(), namespace: form.namespace, reason: form.reason.trim(),
      expectedGeneration: reviewed.currentGeneration, previewDigest: reviewed.previewDigest, ...(signal ? { signal } : {})
    });
    if (token !== generation.value || signal?.aborted) return;
    payload.onApplied();
    dialogRef.value?.toggleDialog(false);
  } catch (error) {
    if (token !== generation.value || signal?.aborted) return;
    if (error instanceof AdminApiError && ["admin.elevation.required", "admin.elevation.expired"].includes(error.businessKey)) { openElevation(); return; }
    if (error instanceof AdminApiError && error.businessKey === "admin.version.conflict") { payload.onConflict(); preview.value = null; }
    errorMessage.value = error instanceof AdminApiError ? error.message : "缓存刷新失败，请稍后重试。";
  } finally {
    if (token === generation.value) applying.value = false;
  }
};

defineExpose({ toggleDialog });
</script>

<template>
  <AppDialog ref="dialogRef" title="刷新缓存投影" :width="600" @closed="handleClose">
    <template #default="{ close }">
      <div class="operations-command-dialog">
        <NAlert v-if="errorMessage" type="error" :show-icon="false">{{ errorMessage }}</NAlert>
        <NForm ref="formRef" :model="form" :rules="rules" label-placement="top">
          <NFormItem path="namespace" label="投影范围">
            <NSelect v-model:value="form.namespace" :options="namespaceOptions" @update:value="invalidatePreview" />
          </NFormItem>
          <NFormItem path="reason" label="操作原因">
            <NInput v-model:value="form.reason" type="textarea" :maxlength="512" show-count :autosize="{ minRows: 3, maxRows: 5 }" @update:value="invalidatePreview" />
          </NFormItem>
        </NForm>
        <NDescriptions v-if="preview" label-placement="left" :column="2" bordered size="small">
          <NDescriptionsItem label="当前代次">{{ preview.currentGeneration.toString() }}</NDescriptionsItem>
          <NDescriptionsItem label="估算条目">{{ preview.estimatedEntries.toString() }}</NDescriptionsItem>
          <NDescriptionsItem label="预览失效" :span="2">{{ formatDateTime(preview.expiresAt) }}</NDescriptionsItem>
        </NDescriptions>
        <NAlert v-if="preview" type="info" :show-icon="false">执行只推进持久化代次，投影消费者随后按代次幂等重建。</NAlert>
        <div class="admin-dialog-footer">
          <NButton :disabled="previewing || applying" @click="close">取消</NButton>
          <NButton :loading="previewing" :disabled="applying" @click="handlePreview">生成预览</NButton>
          <NButton type="primary" :loading="applying" :disabled="!preview || previewing" @click="executeApply">确认刷新</NButton>
        </div>
      </div>
    </template>
  </AppDialog>
  <ElevationDialog ref="elevationDialogRef" />
</template>

<style scoped>.operations-command-dialog { display: grid; gap: 14px; }</style>
