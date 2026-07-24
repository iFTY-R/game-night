<script setup lang="ts">
import { computed, ref } from "vue";
import { NAlert, NButton, NForm, NFormItem, NInput, NQrCode, NSpace, NTag } from "naive-ui";
import type { FormInst, FormRules } from "naive-ui";
import type { OperationResult } from "../../../../../contracts/gen/ts/platform/common/v1/common_pb";
import { formatDateTime } from "../../utils/format";

const props = defineProps<{
  pending: boolean;
  operation: OperationResult | null;
  secret: string | null;
  otpauthUri: string | null;
  recoveryCodes: string[] | null;
  rebind?: boolean;
}>();

const emit = defineEmits<{
  begin: [];
  complete: [totpCode: string];
  acknowledge: [];
}>();

const formRef = ref<FormInst | null>(null);
const form = ref({ totpCode: "" });

const rules = computed<FormRules>(() => ({
  totpCode: [{ required: true, len: 6, message: "请输入 6 位验证码", trigger: ["input", "blur"] }]
}));

const handleComplete = async (): Promise<void> => {
  await formRef.value?.validate();
  emit("complete", form.value.totpCode);
};
</script>

<template>
  <div class="admin-grid">
    <NAlert type="warning" title="敏感信息仅显示一次">
      关闭或刷新页面后，验证器密钥和恢复码将无法再次查看。
    </NAlert>
    <NButton v-if="!props.secret && !props.recoveryCodes?.length" type="primary" :loading="pending" @click="emit('begin')">
      {{ props.rebind ? "显示重绑信息" : "显示注册信息" }}
    </NButton>
    <template v-else-if="props.recoveryCodes?.length">
      <section class="admin-inset-section">
        <h3>恢复码</h3>
        <p class="admin-muted">这些恢复码只展示一次，请先离线保存。</p>
        <NSpace>
          <NTag v-for="code in props.recoveryCodes" :key="code" round>{{ code }}</NTag>
        </NSpace>
        <p class="admin-muted">结果可用至 {{ formatDateTime(props.operation?.secretExpiresAt) }}</p>
        <NButton type="primary" block @click="emit('acknowledge')">我已离线保存并确认</NButton>
      </section>
    </template>
    <template v-else>
      <section class="admin-inset-section">
        <h3>验证器密钥</h3>
        <div class="admin-grid">
          <NQrCode v-if="props.otpauthUri" :value="props.otpauthUri" />
          <div class="admin-list">
            <div class="admin-list__row">
              <span class="admin-muted">手工密钥</span>
              <span class="admin-code">{{ props.secret }}</span>
            </div>
            <div class="admin-list__row">
              <span class="admin-muted">结果过期</span>
              <span>{{ formatDateTime(props.operation?.secretExpiresAt) }}</span>
            </div>
          </div>
        </div>
      </section>
      <NForm ref="formRef" :model="form" :rules="rules">
        <NFormItem label="6 位验证码" path="totpCode">
          <NInput v-model:value="form.totpCode" maxlength="6" inputmode="numeric" />
        </NFormItem>
        <NButton type="primary" block :loading="pending" @click="handleComplete">验证并继续</NButton>
      </NForm>
    </template>
  </div>
</template>
