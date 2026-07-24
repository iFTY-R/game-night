<script setup lang="ts">
import { NCard, NTag } from "naive-ui";
import type { AdminSessionSummary } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { formatDateTime, formatPermission, formatSessionKind } from "../../utils/format";

defineProps<{
  session: AdminSessionSummary | null;
}>();
</script>

<template>
  <NCard class="admin-card-shell admin-section-card" title="当前会话">
    <div v-if="session" class="admin-grid">
      <div class="admin-list">
        <div class="admin-list__row"><span class="admin-muted">管理员 ID</span><span class="admin-code">{{ session.adminId }}</span></div>
        <div class="admin-list__row"><span class="admin-muted">会话类型</span><span>{{ formatSessionKind(session.kind) }}</span></div>
        <div class="admin-list__row"><span class="admin-muted">空闲过期</span><span>{{ formatDateTime(session.idleExpiresAt) }}</span></div>
        <div class="admin-list__row"><span class="admin-muted">最长有效期</span><span>{{ formatDateTime(session.absoluteExpiresAt) }}</span></div>
      </div>
      <div class="admin-toolbar__cluster">
        <NTag v-for="permission in session.permissions" :key="permission" round>{{ formatPermission(permission) }}</NTag>
      </div>
    </div>
    <p v-else class="admin-muted">当前没有活跃管理员会话。</p>
  </NCard>
</template>
