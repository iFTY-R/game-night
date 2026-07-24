<script setup lang="ts">
import { ref } from "vue";
import { onBeforeRouteLeave } from "vue-router";
import AsyncState from "../../components/AsyncState.vue";
import RealNameDialog, { type RealNameDialogPayload } from "../../components/users/RealNameDialog.vue";
import UserDetails from "../../components/users/UserDetails.vue";
import UserLookupForm, { type UserLookupPayload } from "../../components/users/UserLookupForm.vue";
import UserStatusDialog from "../../components/users/UserStatusDialog.vue";
import { getRealName, lookupUser, suspendUser, unsuspendUser, updateRealName } from "../../api/admin-identity";
import type { AdminUserView, RealNameProfile } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_identity_pb";

const loading = ref(false);
const loadingRealName = ref(false);
const error = ref("");
const user = ref<AdminUserView | null>(null);
const realNameProfile = ref<RealNameProfile | null>(null);
const activeLookup = ref<UserLookupPayload | null>(null);
const realNameDialogRef = ref<{ toggleDialog: (open: boolean, payload?: RealNameDialogPayload) => void } | null>(null);
const userStatusDialogRef = ref<{ toggleDialog: (open: boolean, payload?: { mode: "suspend" | "unsuspend"; userId: string; username: string; currentStatus: string }) => void } | null>(null);

const clearSensitiveUserState = (): void => {
  realNameProfile.value = null;
};

const statusLabel = (status: number): string => {
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

const search = async (payload: UserLookupPayload): Promise<void> => {
  loading.value = true;
  error.value = "";
  clearSensitiveUserState();
  activeLookup.value = payload;
  try {
    const response = await lookupUser(
      payload.kind === "userId" ? { case: "userId", value: payload.value } : { case: "username", value: payload.value }
    );
    user.value = response.user ?? null;
  } catch {
    error.value = "查询失败，请确认 UUID 或规范化用户名后重试。";
    user.value = null;
  } finally {
    loading.value = false;
  }
};

const handleRealNameSubmit = async (payload: { mode: "read" | "update"; userId: string; reason: string; realName?: string }): Promise<void> => {
  loadingRealName.value = true;
  try {
    if (payload.mode === "read") {
      const response = await getRealName(payload.userId, payload.reason);
      realNameProfile.value = response.profile ?? null;
      return;
    }
    const response = await updateRealName({
      userId: payload.userId,
      realName: payload.realName ?? "",
      reason: payload.reason
    });
    realNameProfile.value = response.profile ?? null;
  } finally {
    loadingRealName.value = false;
  }
};

const handleStatusSubmit = async (payload: { mode: "suspend" | "unsuspend"; userId: string; reason: string }): Promise<void> => {
  if (!user.value) {
    return;
  }
  loading.value = true;
  try {
    const response =
      payload.mode === "suspend"
        ? await suspendUser({ userId: payload.userId, reason: payload.reason })
        : await unsuspendUser({ userId: payload.userId, reason: payload.reason });
    user.value = response.user ?? user.value;
    clearSensitiveUserState();
  } finally {
    loading.value = false;
  }
};

onBeforeRouteLeave(() => {
  clearSensitiveUserState();
});
</script>

<template>
  <div class="admin-pane__section">
    <div class="admin-page-title">
      <div class="admin-eyebrow">User Governance</div>
      <h2>精确用户治理</h2>
      <p class="admin-muted">按用户 ID 或用户名精确定位账号。</p>
    </div>

    <UserLookupForm :loading="loading" @lookup="search" />

    <AsyncState :loading="loading" :error="error" :empty="!user" empty-description="先输入 UUID 或规范化用户名进行精确查询。">
      <UserDetails
        v-if="user"
        :user="user"
        :real-name-profile="realNameProfile"
        :loading-real-name="loadingRealName"
        @read-real-name="realNameDialogRef?.toggleDialog(true, { mode: 'read', userId: user.userId, username: user.username })"
        @update-real-name="
          realNameDialogRef?.toggleDialog(true, {
            mode: 'update',
            userId: user.userId,
            username: user.username,
            ...(realNameProfile?.realName ? { realName: realNameProfile.realName } : {})
          })
        "
        @suspend="userStatusDialogRef?.toggleDialog(true, { mode: 'suspend', userId: user.userId, username: user.username, currentStatus: statusLabel(user.status) })"
        @unsuspend="userStatusDialogRef?.toggleDialog(true, { mode: 'unsuspend', userId: user.userId, username: user.username, currentStatus: statusLabel(user.status) })"
      />
    </AsyncState>

    <RealNameDialog ref="realNameDialogRef" @submit="handleRealNameSubmit" />
    <UserStatusDialog ref="userStatusDialogRef" @submit="handleStatusSubmit" />
  </div>
</template>
