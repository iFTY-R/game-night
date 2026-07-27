<script setup lang="ts">
import { ref, shallowRef } from "vue";
import { NAlert, NButton } from "naive-ui";
import AppDialog from "../../../components/AppDialog.vue";
import ElevationDialog, { type ElevationDialogPayload } from "./ElevationDialog.vue";
import { useAuthStore } from "../../../stores/auth";
import { disableTotp } from "../../../api/admin-auth";
import { createOperationId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { AdminElevationScope, type AdminElevationSummary } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";

const auth = useAuthStore();
const emit = defineEmits<{ updated: [] }>();
const dialogRef = ref<{ toggleDialog: (open: boolean) => void } | null>(null);

// Dialog state
const submitting = ref(false);
const errorMessage = ref("");
const revokedSessionsCount = ref(0);
const showSuccess = ref(false);
const elevationSummary = ref<AdminElevationSummary | null>(null);

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
    revokedSessionsCount.value = 0;
    showSuccess.value = false;
    elevationSummary.value = null;
    abortController.value?.abort();
    generation.value += 1;
    abortController.value = new AbortController();

    dialogRef.value?.toggleDialog(true);
    // The mutation starts only after the separate elevation dialog succeeds.
    const elevationPayload: ElevationDialogPayload = {
      scope: AdminElevationScope.SECURITY_DISABLE_MFA,
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
 * Handles dialog close - cleans up resources.
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
  revokedSessionsCount.value = 0;
  showSuccess.value = false;
  elevationSummary.value = null;
  submitting.value = false;
};

/**
 * Callback when elevation succeeds.
 * Proceeds to disable TOTP.
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
    const response = await disableTotp({
      operationId: createOperationId(),
      reason: "用户主动停用",
      expectedEnrollmentVersion: auth.session?.mfa?.enrollmentVersion ?? 0n,
      ...(controller ? { signal: controller.signal } : {})
    });

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    // Update session with new MFA state
    if (response.session) {
      auth.applySession(response.session);
    }

    revokedSessionsCount.value = response.revokedSessions;
    showSuccess.value = true;
    emit("updated");
  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "停用 TOTP 失败，请稍后重试。";
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
  <AppDialog ref="dialogRef" title="停用 TOTP" @closed="handleClose">
    <template #default="{ close }">
      <div class="admin-grid">
        <NAlert v-if="errorMessage" type="error" :title="errorMessage" closable @close="errorMessage = ''" />

        <div v-if="showSuccess">
          <NAlert type="success" title="TOTP 已停用">
            多因素认证已停用，撤销了 {{ revokedSessionsCount }} 个会话。
          </NAlert>
          <div class="admin-dialog-footer">
            <NButton type="primary" @click="close">关闭</NButton>
          </div>
        </div>

        <div v-else-if="submitting">
          <NAlert type="info">
            正在停用 TOTP...
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
