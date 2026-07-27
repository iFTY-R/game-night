<script setup lang="ts">
import { computed, ref, shallowRef } from "vue";
import { NAlert, NButton, NList, NListItem, NTag } from "naive-ui";
import AppDialog from "../../../components/AppDialog.vue";
import ElevationDialog, { type ElevationDialogPayload } from "./ElevationDialog.vue";
import { useAuthStore } from "../../../stores/auth";
import { previewRevokeOtherAdminSessions, revokeOtherAdminSessions } from "../../../api/admin-auth";
import { createOperationId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { AdminElevationScope, type AdminElevationSummary, type AdminSessionInfo } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";

const auth = useAuthStore();
const emit = defineEmits<{ revoked: [] }>();
const dialogRef = ref<{ toggleDialog: (open: boolean) => void } | null>(null);

// Dialog state
const currentStep = ref<"preview" | "elevating" | "revoking" | "success">("preview");
const submitting = ref(false);
const errorMessage = ref("");
const previewSessions = ref<AdminSessionInfo[]>([]);
const previewVersion = ref("");
const previewAdminVersion = ref(0n);
const previewSessionVersion = ref(0n);
const elevationSummary = ref<AdminElevationSummary | null>(null);
const revokedCount = ref(0);

// Refs for elevation dialog
const elevationDialogRef = ref<InstanceType<typeof ElevationDialog> | null>(null);

// Abort controller for request cancellation
const abortController = shallowRef<AbortController | null>(null);

// Generation token for stale response guard
const generation = ref(0);

// Computed: session count message
const sessionCountMessage = computed(() => {
  const count = previewSessions.value.length;
  return count === 0 ? "没有其他活动会话" : `将撤销 ${count} 个其他会话`;
});

/**
 * Opens or closes the dialog.
 * When opening, loads preview.
 */
const toggleDialog = (open: boolean): void => {
  if (open) {
    currentStep.value = "preview";
    errorMessage.value = "";
    previewSessions.value = [];
    previewVersion.value = "";
    previewAdminVersion.value = 0n;
    previewSessionVersion.value = 0n;
    elevationSummary.value = null;
    revokedCount.value = 0;
    abortController.value?.abort();
    generation.value += 1;
    abortController.value = new AbortController();

    // Load preview
    dialogRef.value?.toggleDialog(true);
    void loadPreview();
    return;
  }
  elevationDialogRef.value?.toggleDialog(false);
  dialogRef.value?.toggleDialog(false);
};

/**
 * Handles dialog close - cleans up resources.
 */
const handleClose = (): void => {
  generation.value += 1;
  const controller = abortController.value;
  controller?.abort();
  if (abortController.value === controller) {
    abortController.value = null;
  }
  currentStep.value = "preview";
  errorMessage.value = "";
  previewSessions.value = [];
  previewVersion.value = "";
  previewAdminVersion.value = 0n;
  previewSessionVersion.value = 0n;
  elevationSummary.value = null;
  revokedCount.value = 0;
  submitting.value = false;
};

/**
 * Step 1: Load preview of sessions to be revoked.
 */
const loadPreview = async (): Promise<void> => {
  const requestToken = generation.value;
  const controller = abortController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    const response = await previewRevokeOtherAdminSessions(
      controller ? { signal: controller.signal } : undefined
    );

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    previewSessions.value = response.sessions;
    previewVersion.value = response.previewVersion;
    previewAdminVersion.value = response.currentAdminVersion;
    previewSessionVersion.value = response.currentSessionVersion;
  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "预览失败，请稍后重试。";
  } finally {
    if (requestToken === generation.value) {
      submitting.value = false;
    }
  }
};

/**
 * Step 2: User confirms preview and triggers elevation.
 */
const handleProceedToElevation = (): void => {
  if (previewSessions.value.length === 0) {
    return;
  }

  currentStep.value = "elevating";

  const elevationPayload: ElevationDialogPayload = {
    scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
    allowRecoveryCode: false,
    onElevated: handleElevated,
    onCancelled: () => {
      if (currentStep.value === "elevating") {
        currentStep.value = "preview";
      }
    }
  };
  elevationDialogRef.value?.toggleDialog(true, elevationPayload);
};

/**
 * Step 3: Callback when elevation succeeds.
 * Proceeds to revoke other sessions.
 */
const handleElevated = async (summary: AdminElevationSummary): Promise<void> => {
  if (submitting.value) {
    return;
  }
  elevationSummary.value = summary;
  currentStep.value = "revoking";

  const requestToken = generation.value;
  const controller = abortController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    const response = await revokeOtherAdminSessions({
      operationId: createOperationId(),
      previewVersion: previewVersion.value,
      expectedAdminVersion: previewAdminVersion.value,
      expectedCurrentSessionVersion: previewSessionVersion.value,
      ...(controller ? { signal: controller.signal } : {})
    });

    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    // Update session
    if (response.session) {
      auth.applySession(response.session);
    }

    revokedCount.value = response.revokedSessions;
    currentStep.value = "success";
    emit("revoked");
  } catch (error) {
    if (controller?.signal.aborted) {
      return;
    }

    // Guard against stale responses
    if (requestToken !== generation.value) {
      return;
    }

    errorMessage.value = error instanceof AdminApiError ? error.message : "撤销失败，请稍后重试。";
    currentStep.value = "preview";
  } finally {
    if (requestToken === generation.value) {
      submitting.value = false;
    }
  }
};

/**
 * Formats timestamp for display.
 */
const formatTimestamp = (timestamp: { seconds: bigint } | undefined): string => {
  if (!timestamp) return "-";
  return new Date(Number(timestamp.seconds) * 1000).toLocaleString("zh-CN");
};

// Expose toggleDialog for parent component
defineExpose({
  toggleDialog
});
</script>

<template>
  <AppDialog ref="dialogRef" title="撤销其他会话" :width="600" @closed="handleClose">
    <template #default="{ close }">
      <div class="admin-grid">
        <NAlert v-if="errorMessage" type="error" :title="errorMessage" closable @close="errorMessage = ''" />

        <!-- Step 1: Preview -->
        <div v-if="currentStep === 'preview'">
          <NAlert type="warning" style="margin-bottom: 16px">
            {{ sessionCountMessage }}
          </NAlert>

          <NList v-if="previewSessions.length > 0" bordered style="margin: 16px 0">
            <NListItem v-for="session in previewSessions" :key="session.sessionId">
              <div style="display: flex; justify-content: space-between; align-items: center; width: 100%">
                <div>
                  <div><strong>会话 ID:</strong> {{ session.sessionId }}</div>
                  <div style="font-size: 12px; color: #666; margin-top: 4px">
                    <span>创建: {{ formatTimestamp(session.createdAt) }}</span>
                    <span style="margin-left: 16px">活动: {{ formatTimestamp(session.lastActivityAt) }}</span>
                  </div>
                  <div v-if="session.clientIp" style="font-size: 12px; color: #666">
                    IP: {{ session.clientIp }}
                  </div>
                </div>
                <NTag type="warning">待撤销</NTag>
              </div>
            </NListItem>
          </NList>

          <div class="admin-dialog-footer">
            <NButton @click="close" :disabled="submitting">取消</NButton>
            <NButton
              type="error"
              :loading="submitting"
              :disabled="previewSessions.length === 0"
              @click="handleProceedToElevation"
            >
              继续撤销
            </NButton>
          </div>
        </div>

        <!-- Step 2: Elevating -->
        <div v-if="currentStep === 'elevating'">
          <NAlert type="info">
            等待权限提升...
          </NAlert>
        </div>

        <!-- Step 3: Revoking -->
        <div v-if="currentStep === 'revoking'">
          <NAlert type="info">
            正在撤销会话...
          </NAlert>
        </div>

        <!-- Step 4: Success -->
        <div v-if="currentStep === 'success'">
          <NAlert type="success" title="撤销成功">
            已撤销 {{ revokedCount }} 个其他会话。
          </NAlert>
          <div class="admin-dialog-footer">
            <NButton type="primary" @click="close">关闭</NButton>
          </div>
        </div>
      </div>
    </template>
  </AppDialog>

  <ElevationDialog ref="elevationDialogRef" />
</template>
