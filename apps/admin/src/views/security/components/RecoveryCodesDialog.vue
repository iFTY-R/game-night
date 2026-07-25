<script setup lang="ts">
import { ref, shallowRef } from "vue";
import { NAlert, NButton, NTag } from "naive-ui";
import AppDialog from "../../../components/AppDialog.vue";
import ElevationDialog, { type ElevationDialogPayload } from "./ElevationDialog.vue";
import { useAuthStore } from "../../../stores/auth";
import { regenerateAdminRecoveryCodes, confirmAdminSecretReceipt } from "../../../api/admin-auth";
import { createRequestId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { AdminElevationScope, type AdminElevationSummary } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { AdminSecretOperation } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";

const auth = useAuthStore();
const dialogRef = ref<{ toggleDialog: (open: boolean) => void } | null>(null);

// Dialog state
const submitting = ref(false);
const errorMessage = ref("");
const showCodes = ref(false);
const elevationSummary = ref<AdminElevationSummary | null>(null);

// Component-local secrets - recovery codes
const recoveryCodes = ref<string[]>([]);
const recoveryCodesOperationId = ref("");
const recoveryCodesResultId = ref("");

// Refs for elevation dialog
const elevationDialogRef = ref<InstanceType<typeof ElevationDialog> | null>(null);

// Abort controller for request cancellation
const abortController = shallowRef<AbortController | null>(null);

// Generation token for stale response guard
const generation = ref(0);

/**
 * Opens or closes the dialog.
 * When opening, resets state and triggers elevation.
 */
const toggleDialog = (open: boolean): void => {
  if (open) {
    errorMessage.value = "";
    showCodes.value = false;
    elevationSummary.value = null;
    recoveryCodes.value = [];
    recoveryCodesOperationId.value = "";
    recoveryCodesResultId.value = "";
    abortController.value?.abort();
    generation.value += 1;
    abortController.value = new AbortController();

    dialogRef.value?.toggleDialog(true);
    // Recovery-code rotation starts only after step-up succeeds.
    const elevationPayload: ElevationDialogPayload = {
      scope: AdminElevationScope.SECURITY_REGENERATE_RECOVERY_CODES,
      allowRecoveryCode: true,
      onElevated: handleElevated,
      onCancelled: () => dialogRef.value?.toggleDialog(false)
    };
    elevationDialogRef.value?.toggleDialog(true, elevationPayload);
    return;
  }
  elevationDialogRef.value?.toggleDialog(false);
  dialogRef.value?.toggleDialog(false);
};

/**
 * Handles dialog close - cleans up all sensitive data.
 */
const handleClose = (): void => {
  elevationDialogRef.value?.toggleDialog(false);
  generation.value += 1;
  const controller = abortController.value;
  controller?.abort();
  if (abortController.value === controller) {
    abortController.value = null;
  }
  errorMessage.value = "";
  showCodes.value = false;
  elevationSummary.value = null;
  recoveryCodes.value = [];
  recoveryCodesOperationId.value = "";
  recoveryCodesResultId.value = "";
  submitting.value = false;
};

/**
 * Callback when elevation succeeds.
 * Proceeds to regenerate recovery codes.
 */
const handleElevated = async (summary: AdminElevationSummary): Promise<void> => {
  if (submitting.value) {
    return;
  }
  elevationSummary.value = summary;

  const requestToken = generation.value;
  const controller = abortController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    const operationId = createRequestId();
    recoveryCodesOperationId.value = operationId;

    const response = await regenerateAdminRecoveryCodes({
      operationId,
      expectedRecoveryCodesVersion: auth.session?.mfa?.recoveryCodesVersion ?? 0n,
      ...(controller ? { signal: controller.signal } : {})
    });

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    // Update session with new recovery codes state
    if (response.session) {
      auth.applySession(response.session);
    }

    // Store recovery codes in component-local memory
    if (response.result && response.recoveryCodes.length > 0) {
      recoveryCodesOperationId.value = response.result.operationId;
      recoveryCodes.value = [...response.recoveryCodes];
      recoveryCodesResultId.value = response.result.resultId;
      showCodes.value = true;
    }
  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "恢复码重新生成失败，请稍后重试。";
  } finally {
    if (requestToken === generation.value) {
      submitting.value = false;
    }
  }
};

/**
 * User confirms they have saved recovery codes.
 * Calls confirmAdminSecretReceipt and closes dialog.
 */
const handleConfirmReceipt = async (close: () => void): Promise<void> => {
  if (submitting.value) {
    return;
  }
  const requestToken = generation.value;
  const controller = abortController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    await confirmAdminSecretReceipt({
      operation: AdminSecretOperation.REGENERATED_RECOVERY_CODES,
      operationId: recoveryCodesOperationId.value,
      resultId: recoveryCodesResultId.value,
      ...(controller ? { signal: controller.signal } : {})
    });

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    close();
  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "确认失败，请稍后重试。";
  } finally {
    if (requestToken === generation.value) {
      submitting.value = false;
    }
  }
};

// Expose toggleDialog for parent component
defineExpose({
  toggleDialog
});
</script>

<template>
  <AppDialog ref="dialogRef" title="重新生成恢复码" :width="560" @closed="handleClose">
    <template #default="{ close }">
      <div class="admin-grid">
        <NAlert v-if="errorMessage" type="error" :title="errorMessage" closable @close="errorMessage = ''" />

        <div v-if="showCodes">
          <NAlert type="warning" style="margin-bottom: 16px">
            请保存这些新恢复码。旧的恢复码已失效。每个恢复码只能使用一次。
          </NAlert>
          <div style="display: grid; grid-template-columns: repeat(2, 1fr); gap: 8px; margin: 16px 0">
            <NTag v-for="(code, index) in recoveryCodes" :key="index" type="info">
              {{ code }}
            </NTag>
          </div>
          <div class="admin-dialog-footer">
            <NButton type="primary" :loading="submitting" @click="handleConfirmReceipt(close)">
              我已保存恢复码
            </NButton>
          </div>
        </div>

        <div v-else-if="submitting">
          <NAlert type="info">
            正在生成新恢复码...
          </NAlert>
        </div>

        <div v-else>
          <NAlert type="warning">
            等待权限提升...
          </NAlert>
        </div>
      </div>
    </template>
  </AppDialog>

  <ElevationDialog ref="elevationDialogRef" />
</template>
