<script setup lang="ts">
import { NCard, NTag } from "naive-ui";
import type { SignedAuditEvent } from "../../../../../contracts/gen/ts/platform/audit/v1/audit_pb";
import { formatDateTime } from "../../utils/format";

defineProps<{
  event: SignedAuditEvent | null;
}>();
</script>

<template>
  <NCard v-if="event?.event" class="admin-card-shell admin-section-card" title="审计详情">
    <div class="admin-list">
      <div class="admin-list__row"><span class="admin-muted">时间</span><span>{{ formatDateTime(event.event.occurredAt) }}</span></div>
      <div class="admin-list__row"><span class="admin-muted">动作</span><NTag>{{ event.event.action }}</NTag></div>
      <div class="admin-list__row"><span class="admin-muted">Actor</span><span class="admin-code">{{ event.event.actor?.actorId }}</span></div>
      <div class="admin-list__row"><span class="admin-muted">Target</span><span class="admin-code">{{ event.event.target?.targetId }}</span></div>
      <div class="admin-list__row"><span class="admin-muted">Request ID</span><span class="admin-code">{{ event.event.requestId }}</span></div>
      <div class="admin-list__row"><span class="admin-muted">完整性</span><span>服务端完整性校验通过</span></div>
    </div>
  </NCard>
</template>
