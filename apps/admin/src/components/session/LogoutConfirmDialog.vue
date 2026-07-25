<script setup lang="ts">
import { ref } from "vue";
import { NAlert, NButton } from "naive-ui";
import AppDialog from "../AppDialog.vue";

defineProps<{ pending?: boolean }>();

const emit = defineEmits<{
  confirm: [];
}>();

const dialogRef = ref<{ toggleDialog: (open: boolean) => void } | null>(null);

const toggleDialog = (open: boolean): void => {
  dialogRef.value?.toggleDialog(open);
};

const handleCancel = (): void => {
  toggleDialog(false);
};

defineExpose({
  toggleDialog
});
</script>

<template>
  <AppDialog ref="dialogRef" title="退出管理后台" :width="480">
    <template #default>
      <div class="admin-grid">
        <NAlert type="warning" title="确认退出当前会话">
          退出后需要重新验证管理员身份。其他设备上的会话不会受到影响。
        </NAlert>
        <div class="admin-dialog-footer">
          <NButton :disabled="pending" @click="handleCancel">取消</NButton>
          <NButton type="error" :loading="pending" @click="emit('confirm')">退出</NButton>
        </div>
      </div>
    </template>
  </AppDialog>
</template>
