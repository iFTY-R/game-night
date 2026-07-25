<script setup lang="ts">
import { computed, ref } from "vue";
import { NAlert, NButton, NCard, NDescriptions, NDescriptionsItem, NDivider, NSpin, NTag } from "naive-ui";
import { KeyRound, Shield, Smartphone, UserRoundX } from "lucide-vue-next";
import { AdminPermission } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import PermissionGate from "../../components/PermissionGate.vue";
import { useAuthStore } from "../../stores/auth";
import ChangePasswordDialog from "./components/ChangePasswordDialog.vue";
import TotpSetupDialog from "./components/TotpSetupDialog.vue";
import DisableTotpDialog from "./components/DisableTotpDialog.vue";
import RecoveryCodesDialog from "./components/RecoveryCodesDialog.vue";
import AdminSessionsTable from "./components/AdminSessionsTable.vue";
import RevokeOtherSessionsDialog from "./components/RevokeOtherSessionsDialog.vue";

const auth = useAuthStore();

// Dialog refs
const changePasswordDialogRef = ref<InstanceType<typeof ChangePasswordDialog> | null>(null);
const totpSetupDialogRef = ref<InstanceType<typeof TotpSetupDialog> | null>(null);
const disableTotpDialogRef = ref<InstanceType<typeof DisableTotpDialog> | null>(null);
const recoveryCodesDialogRef = ref<InstanceType<typeof RecoveryCodesDialog> | null>(null);
const revokeOtherSessionsDialogRef = ref<InstanceType<typeof RevokeOtherSessionsDialog> | null>(null);
const sessionsTableRef = ref<InstanceType<typeof AdminSessionsTable> | null>(null);

// Computed session info
const mfaEnabled = computed(() => auth.session?.mfa?.enabled ?? false);
const recoveryCodesRemaining = computed(() => auth.session?.mfa?.recoveryCodesRemaining ?? 0);

// Action handlers
const handleChangePassword = (): void => {
  changePasswordDialogRef.value?.toggleDialog(true);
};

const handleSetupTotp = (): void => {
  totpSetupDialogRef.value?.toggleDialog(true);
};

const handleDisableTotp = (): void => {
  disableTotpDialogRef.value?.toggleDialog(true);
};

const handleRegenerateRecoveryCodes = (): void => {
  recoveryCodesDialogRef.value?.toggleDialog(true);
};

const handleRevokeOtherSessions = (): void => {
  revokeOtherSessionsDialogRef.value?.toggleDialog(true);
};

// Security mutations may revoke sessions or advance their versions, so the list must be re-read.
const handleSecurityStateChanged = (): void => {
  void sessionsTableRef.value?.refresh();
};
</script>

<template>
  <div class="admin-view">
    <header class="admin-view__header">
      <h1 class="admin-view__title">安全设置</h1>
      <p class="admin-view__subtitle">管理密码、多因素认证和会话</p>
    </header>

    <NSpin :show="false">
      <div class="admin-view__content">
        <!-- Password Section -->
        <NCard title="密码" :bordered="false" size="small">
          <template #header-extra>
            <PermissionGate :permission="AdminPermission.SECURITY_MANAGE_PASSWORD">
              <NButton size="small" @click="handleChangePassword">
                <template #icon>
                  <KeyRound :size="16" />
                </template>
                修改密码
              </NButton>
            </PermissionGate>
          </template>
          <NDescriptions :column="1" label-placement="left" :label-style="{ width: '120px' }">
            <NDescriptionsItem label="密码版本">
              {{ auth.session?.passwordVersion ?? "-" }}
            </NDescriptionsItem>
          </NDescriptions>
        </NCard>

        <NDivider />

        <!-- MFA Section -->
        <NCard title="多因素认证" :bordered="false" size="small">
          <template #header-extra>
            <PermissionGate :permission="AdminPermission.SECURITY_MANAGE_MFA">
              <NButton v-if="!mfaEnabled" size="small" type="primary" @click="handleSetupTotp">
                <template #icon>
                  <Smartphone :size="16" />
                </template>
                启用 MFA
              </NButton>
              <NButton v-else size="small" type="error" @click="handleDisableTotp">
                <template #icon>
                  <Shield :size="16" />
                </template>
                停用 MFA
              </NButton>
            </PermissionGate>
          </template>
          <NDescriptions :column="1" label-placement="left" :label-style="{ width: '120px' }">
            <NDescriptionsItem label="状态">
              <NTag v-if="mfaEnabled" type="success">已启用</NTag>
              <NTag v-else type="default">未启用</NTag>
            </NDescriptionsItem>
            <NDescriptionsItem v-if="mfaEnabled" label="恢复码剩余">
              <span>{{ recoveryCodesRemaining }} / 10</span>
              <PermissionGate :permission="AdminPermission.SECURITY_MANAGE_MFA">
                <NButton
                  size="tiny"
                  :type="recoveryCodesRemaining < 3 ? 'warning' : 'default'"
                  style="margin-left: 8px"
                  @click="handleRegenerateRecoveryCodes"
                >
                  重新生成
                </NButton>
              </PermissionGate>
            </NDescriptionsItem>
            <NDescriptionsItem v-if="mfaEnabled" label="绑定版本">
              {{ auth.session?.mfa?.enrollmentVersion ?? "-" }}
            </NDescriptionsItem>
          </NDescriptions>
          <NAlert v-if="!mfaEnabled" type="warning" style="margin-top: 16px">
            启用多因素认证可以显著提升账户安全性。建议立即启用。
          </NAlert>
        </NCard>

        <NDivider />

        <!-- Sessions Section -->
        <NCard title="活动会话" :bordered="false" size="small">
          <template #header-extra>
            <PermissionGate :permission="AdminPermission.SECURITY_MANAGE_SESSIONS">
              <NButton size="small" type="error" @click="handleRevokeOtherSessions">
                <template #icon>
                  <UserRoundX :size="16" />
                </template>
                撤销其他会话
              </NButton>
            </PermissionGate>
          </template>
          <AdminSessionsTable ref="sessionsTableRef" />
        </NCard>
      </div>
    </NSpin>

    <!-- Dialogs -->
    <ChangePasswordDialog ref="changePasswordDialogRef" @updated="handleSecurityStateChanged" />
    <TotpSetupDialog ref="totpSetupDialogRef" @updated="handleSecurityStateChanged" />
    <DisableTotpDialog ref="disableTotpDialogRef" @updated="handleSecurityStateChanged" />
    <RecoveryCodesDialog ref="recoveryCodesDialogRef" />
    <RevokeOtherSessionsDialog ref="revokeOtherSessionsDialogRef" @revoked="handleSecurityStateChanged" />
  </div>
</template>
