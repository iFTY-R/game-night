<script setup lang="ts">
import { RefreshCcw } from "lucide-vue-next";
import { NButton } from "naive-ui";
import { ref } from "vue";
import AsyncState from "../../components/AsyncState.vue";
import ReadinessStatus from "../../components/session/ReadinessStatus.vue";
import SessionSummary from "../../components/session/SessionSummary.vue";
import { fetchRuntimeReadiness, type ReadinessState } from "../../api/readiness";
import { useAuthStore } from "../../stores/auth";

const auth = useAuthStore();
const loading = ref(false);
const error = ref("");
const ordinary = ref<ReadinessState | null>(null);
const sensitive = ref<ReadinessState | null>(null);

const refresh = async (): Promise<void> => {
  loading.value = true;
  error.value = "";
  try {
    const readiness = await fetchRuntimeReadiness();
    ordinary.value = readiness.ordinary;
    sensitive.value = readiness.sensitive;
  } catch {
    error.value = "服务状态暂不可用，请稍后重试。";
  } finally {
    loading.value = false;
  }
};

void refresh();
</script>

<template>
  <div class="admin-pane__section">
    <div class="admin-toolbar">
      <div class="admin-page-title">
        <div class="admin-eyebrow">运行概览</div>
        <h2>会话概览与部署准备度</h2>
      </div>
      <NButton secondary :loading="loading" @click="refresh">
        <template #icon>
          <RefreshCcw :size="16" />
        </template>
        手动刷新
      </NButton>
    </div>

    <div class="admin-grid admin-grid--three">
      <div class="admin-card-shell admin-section-card admin-stat">
        <span class="admin-muted">当前状态</span>
        <span class="admin-stat__value">{{ auth.session ? "在线" : "未认证" }}</span>
      </div>
      <div class="admin-card-shell admin-section-card admin-stat">
        <span class="admin-muted">权限条目</span>
        <span class="admin-stat__value">{{ auth.permissions.length }}</span>
      </div>
      <div class="admin-card-shell admin-section-card admin-stat">
        <span class="admin-muted">会话范围</span>
        <span class="admin-stat__value">{{ auth.session ? (auth.isRestricted ? "受限" : "完整") : "无" }}</span>
      </div>
    </div>

    <AsyncState :loading="loading" :error="error" @retry="refresh">
      <div class="admin-grid admin-grid--two">
        <SessionSummary :session="auth.session" />
        <ReadinessStatus title="普通服务状态" :readiness="ordinary" />
        <ReadinessStatus title="敏感写入状态" :readiness="sensitive" />
      </div>
    </AsyncState>
  </div>
</template>
