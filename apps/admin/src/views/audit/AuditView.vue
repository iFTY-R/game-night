<script setup lang="ts">
import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { computed, ref } from "vue";
import AsyncState from "../../components/AsyncState.vue";
import AuditDetails from "../../components/audit/AuditDetails.vue";
import AuditFilters, { type AuditFilterPayload } from "../../components/audit/AuditFilters.vue";
import AuditTable from "../../components/audit/AuditTable.vue";
import { listAuditEvents } from "../../api/admin-identity";
import type { SignedAuditEvent } from "../../../../../contracts/gen/ts/platform/audit/v1/audit_pb";

const loading = ref(false);
const error = ref("");
const events = ref<SignedAuditEvent[]>([]);
const selected = ref<SignedAuditEvent | null>(null);
const filters = ref<AuditFilterPayload | null>(null);
const tokenStack = ref<string[]>([]);
const nextToken = ref("");

const parseTimestamp = (value: string) => {
  if (!value) {
    return undefined;
  }
  const date = new Date(value);
  return create(TimestampSchema, { seconds: BigInt(Math.floor(date.valueOf() / 1000)) });
};

const runQuery = async (pageToken = ""): Promise<void> => {
  if (!filters.value) {
    return;
  }
  loading.value = true;
  error.value = "";
  try {
    const request: Parameters<typeof listAuditEvents>[0] = {
      actions: filters.value.actions,
      pageSize: 20
    };
    if (filters.value.actorAdminId) {
      request.actorAdminId = filters.value.actorAdminId;
    }
    if (filters.value.targetUserId) {
      request.targetUserId = filters.value.targetUserId;
    }
    const startedAt = parseTimestamp(filters.value.startedAt);
    if (startedAt) {
      request.startedAt = startedAt;
    }
    const endedAt = parseTimestamp(filters.value.endedAt);
    if (endedAt) {
      request.endedAt = endedAt;
    }
    if (pageToken) {
      request.pageToken = pageToken;
    }
    const response = await listAuditEvents(request);
    events.value = response.events;
    nextToken.value = response.page?.nextPageToken ?? "";
    selected.value = response.events[0] ?? null;
  } catch {
    error.value = "审计查询失败。若后端报告完整性错误，页面会整体失败关闭。";
  } finally {
    loading.value = false;
  }
};

const handleSearch = async (payload: AuditFilterPayload): Promise<void> => {
  filters.value = payload;
  tokenStack.value = [];
  nextToken.value = "";
  await runQuery();
};

const canBack = computed(() => tokenStack.value.length > 0);
const canNext = computed(() => nextToken.value.length > 0);

const handleNext = async (): Promise<void> => {
  if (!nextToken.value) {
    return;
  }
  tokenStack.value.push(nextToken.value);
  await runQuery(nextToken.value);
};

const handleBack = async (): Promise<void> => {
  if (!tokenStack.value.length) {
    return;
  }
  tokenStack.value.pop();
  await runQuery(tokenStack.value[tokenStack.value.length - 1] ?? "");
};
</script>

<template>
  <div class="admin-pane__section">
    <div class="admin-page-title">
      <div class="admin-eyebrow">Audit</div>
      <h2>审计查询</h2>
      <p class="admin-muted">查询结果由服务端完成完整性校验。</p>
    </div>

    <AuditFilters :loading="loading" @search="handleSearch" />

    <AsyncState :loading="loading" :error="error" :empty="!events.length" empty-description="先应用筛选条件。" @retry="() => runQuery(tokenStack[tokenStack.length - 1] ?? '')">
      <div class="admin-grid admin-grid--two">
        <AuditTable
          :events="events"
          :loading="loading"
          :can-back="canBack"
          :can-next="canNext"
          @select="selected = $event"
          @next="handleNext"
          @back="handleBack"
        />
        <AuditDetails :event="selected" />
      </div>
    </AsyncState>
  </div>
</template>
