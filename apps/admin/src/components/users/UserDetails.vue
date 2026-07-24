<script setup lang="ts">
import { NButton, NCard, NTag } from "naive-ui";
import type { AdminUserView, RealNameProfile } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_identity_pb";
import { AdminPermission } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import PermissionGate from "../PermissionGate.vue";
import { formatDateTime } from "../../utils/format";

defineProps<{
  user: AdminUserView;
  realNameProfile: RealNameProfile | null;
  loadingRealName: boolean;
}>();

defineEmits<{
  readRealName: [];
  updateRealName: [];
  suspend: [];
  unsuspend: [];
}>();

const formatStatus = (status: number): string => {
  switch (status) {
    case 1:
      return "活跃";
    case 2:
      return "已封禁";
    case 3:
      return "已删除";
    default:
      return `未知状态 ${status}`;
  }
};
</script>

<template>
  <NCard class="admin-card-shell admin-section-card" title="用户详情">
    <div class="admin-grid">
      <div class="admin-list">
        <div class="admin-list__row"><span class="admin-muted">用户 ID</span><span class="admin-code">{{ user.userId }}</span></div>
        <div class="admin-list__row"><span class="admin-muted">用户名</span><span>{{ user.username }}</span></div>
        <div class="admin-list__row"><span class="admin-muted">状态</span><NTag>{{ formatStatus(user.status) }}</NTag></div>
        <div class="admin-list__row"><span class="admin-muted">创建时间</span><span>{{ formatDateTime(user.createdAt) }}</span></div>
        <div class="admin-list__row"><span class="admin-muted">最近更新时间</span><span>{{ formatDateTime(user.updatedAt) }}</span></div>
      </div>
      <section class="admin-inset-section">
        <div class="admin-page-title">
          <div class="admin-eyebrow">隐私信息</div>
          <h3>实名按需读取</h3>
        </div>
        <p v-if="realNameProfile" class="admin-code">{{ realNameProfile.realName }}</p>
        <p v-else class="admin-muted">当前未加载实名。离开页面、切换目标或会话失效时会立即清空。</p>
        <div class="admin-toolbar__cluster">
          <PermissionGate :permission="AdminPermission.GET_REAL_NAME">
            <NButton secondary :loading="loadingRealName" @click="$emit('readRealName')">读取实名</NButton>
          </PermissionGate>
          <PermissionGate :permission="AdminPermission.UPDATE_REAL_NAME">
            <NButton secondary @click="$emit('updateRealName')">修改实名</NButton>
          </PermissionGate>
        </div>
      </section>
      <div class="admin-toolbar__cluster">
        <PermissionGate :permission="AdminPermission.SUSPEND_USER">
          <NButton type="error" secondary @click="$emit('suspend')">封禁</NButton>
          <NButton type="primary" secondary @click="$emit('unsuspend')">解除封禁</NButton>
        </PermissionGate>
      </div>
    </div>
  </NCard>
</template>
