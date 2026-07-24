<script setup lang="ts" generic="TPayload">
import { computed, ref } from "vue";
import { NModal } from "naive-ui";

const props = defineProps<{
  title: string;
  width?: number;
  maskClosable?: boolean;
}>();

const emit = defineEmits<{
  opened: [payload: TPayload | undefined];
  closed: [];
}>();

const visible = ref(false);
const payload = ref<TPayload | undefined>(undefined);

const style = computed(() => ({
  width: `${props.width ?? 560}px`,
  maxWidth: "calc(100vw - 32px)"
}));

// Dialog state must initialize from the explicit open call instead of watchers.
const toggleDialog = (open: boolean, nextPayload?: TPayload): void => {
  if (open) {
    payload.value = nextPayload;
    visible.value = true;
    emit("opened", nextPayload);
    return;
  }
  visible.value = false;
};

const handleAfterLeave = (): void => {
  payload.value = undefined;
  emit("closed");
};

defineExpose({
  toggleDialog
});
</script>

<template>
  <NModal v-model:show="visible" preset="card" :title="title" :mask-closable="maskClosable ?? false" :style="style" @after-leave="handleAfterLeave">
    <slot :payload="payload" :close="() => toggleDialog(false)" />
  </NModal>
</template>
