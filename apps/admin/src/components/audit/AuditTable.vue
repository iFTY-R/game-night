<script setup lang="ts">
import { NButton, NCard, NTag } from "naive-ui";
import type { SignedAuditEvent } from "../../../../../contracts/gen/ts/platform/audit/v1/audit_pb";
import { formatDateTime } from "../../utils/format";

defineProps<{
  events: SignedAuditEvent[];
  loading: boolean;
  canBack: boolean;
  canNext: boolean;
}>();

defineEmits<{
  select: [event: SignedAuditEvent];
  next: [];
  back: [];
}>();
</script>

<template>
  <NCard class="admin-card-shell admin-section-card" title="审计事件">
    <div class="admin-table-scroll">
      <table class="audit-table">
        <thead>
          <tr>
            <th>时间</th>
            <th>动作</th>
            <th>Actor</th>
            <th>Target</th>
            <th>状态</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="event in events" :key="event.event?.eventId">
            <td>{{ formatDateTime(event.event?.occurredAt) }}</td>
            <td>{{ event.event?.action }}</td>
            <td class="admin-code">{{ event.event?.actor?.actorId }}</td>
            <td class="admin-code">{{ event.event?.target?.targetId }}</td>
            <td><NTag type="success">服务端完整性校验通过</NTag></td>
            <td><NButton text @click="$emit('select', event)">详情</NButton></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="admin-table-pagination">
      <div class="admin-toolbar__cluster">
        <NButton secondary :disabled="!canBack || loading" @click="$emit('back')">上一页</NButton>
        <NButton secondary :disabled="!canNext || loading" @click="$emit('next')">下一页</NButton>
      </div>
    </div>
  </NCard>
</template>
