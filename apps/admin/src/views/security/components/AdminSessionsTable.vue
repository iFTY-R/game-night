<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref, shallowRef } from "vue";
import { NAlert, NDataTable, NSpin, NButton, NPopconfirm } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { listAdminSessions, revokeAdminSession } from "../../../api/admin-auth";
import { createRequestId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import { useAuthStore } from "../../../stores/auth";
import { formatDateTime, formatSessionKind } from "../../../utils/format";
import { AdminPermission, type AdminSessionInfo } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";

const auth = useAuthStore();
// Table state
const loading = ref(false);
const sessions = ref<AdminSessionInfo[]>([]);
const revoking = ref<string | null>(null);
const errorMessage = ref("");
const abortController = shallowRef<AbortController | null>(null);
const revokeController = shallowRef<AbortController | null>(null);
const canManageSessions = computed(() => auth.permissions.includes(AdminPermission.SECURITY_MANAGE_SESSIONS));

/**
 * Loads admin sessions list from backend.
 */
const loadSessions = async (): Promise<void> => {
  loading.value = true;
  errorMessage.value = "";
  abortController.value?.abort();
  const controller = new AbortController();
  abortController.value = controller;
  try {
    const response = await listAdminSessions({ signal: controller.signal });
    sessions.value = response.sessions;
  } catch (error) {
    if (!controller.signal.aborted) {
      errorMessage.value = error instanceof AdminApiError ? error.message : "会话列表加载失败，请稍后重试。";
    }
  } finally {
    // An older aborted request must not clear the loading state of its replacement.
    if (abortController.value === controller) {
      abortController.value = null;
      loading.value = false;
    }
  }
};

/**
 * Revokes a single session by ID.
 */
const handleRevokeSession = async (session: AdminSessionInfo): Promise<void> => {
  if (!session.sessionId || session.current || !canManageSessions.value) {
    return;
  }

  revoking.value = session.sessionId;
  revokeController.value?.abort();
  const controller = new AbortController();
  revokeController.value = controller;

  try {
    await revokeAdminSession({
      operationId: createRequestId(),
      sessionId: session.sessionId,
      expectedSessionVersion: session.sessionVersion,
      signal: controller.signal
    });

    await loadSessions();
  } catch (error) {
    if (!controller.signal.aborted) {
      errorMessage.value = error instanceof AdminApiError ? error.message : "撤销失败，请稍后重试。";
    }
  } finally {
    if (revokeController.value === controller) {
      revokeController.value = null;
      revoking.value = null;
    }
  }
};

// Table columns definition
const columns: DataTableColumns<AdminSessionInfo> = [
  {
    title: "会话ID",
    key: "sessionId",
    ellipsis: { tooltip: true },
    width: 200,
    render: (row) => (row.current ? `${row.sessionId} (当前)` : row.sessionId)
  },
  {
    title: "状态",
    key: "kind",
    width: 130,
    render: (row) => formatSessionKind(row.kind)
  },
  {
    title: "创建时间",
    key: "createdAt",
    width: 180,
    render: (row) => formatDateTime(row.createdAt)
  },
  {
    title: "最后活动",
    key: "lastActivityAt",
    width: 180,
    render: (row) => formatDateTime(row.lastActivityAt)
  },
  {
    title: "客户端",
    key: "client",
    width: 220,
    ellipsis: { tooltip: true },
    render: (row) => [row.clientIp, row.userAgent].filter(Boolean).join(" / ") || "-"
  },
  {
    title: "有效提权",
    key: "elevations",
    width: 90,
    render: (row) => (row.activeElevationScopes.length > 0 ? `${row.activeElevationScopes.length} 项` : "-")
  },
  {
    title: "操作",
    key: "actions",
    width: 120,
    render: (row) => {
      if (row.current || !canManageSessions.value) {
        return null;
      }
      return h(
        NPopconfirm,
        {
          onPositiveClick: () => handleRevokeSession(row)
        },
        {
          trigger: () =>
            h(
              NButton,
              {
                size: "small",
                type: "error",
                loading: revoking.value === row.sessionId,
                disabled: revoking.value !== null
              },
              { default: () => "撤销" }
            ),
          default: () => "确定要撤销此会话吗？"
        }
      );
    }
  }
];

// Load sessions on mount
onMounted(() => {
  void loadSessions();
});

onBeforeUnmount(() => {
  abortController.value?.abort();
  revokeController.value?.abort();
});

defineExpose({ refresh: loadSessions });
</script>

<template>
  <div>
    <NAlert v-if="errorMessage" type="error" closable style="margin-bottom: 16px" @close="errorMessage = ''">
      {{ errorMessage }}
    </NAlert>
    <NAlert v-if="sessions.length === 0 && !loading" type="info" style="margin-bottom: 16px">
      暂无活动会话记录
    </NAlert>
    <NSpin :show="loading">
      <NDataTable :columns="columns" :data="sessions" :bordered="false" :scroll-x="1120" size="small" />
    </NSpin>
  </div>
</template>
