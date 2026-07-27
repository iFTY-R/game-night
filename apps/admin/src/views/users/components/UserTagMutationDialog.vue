<script setup lang="ts">
import { computed, reactive, ref, shallowRef } from "vue";
import { NAlert, NButton, NForm, NFormItem, NInput, NSpace, useMessage, type FormInst, type FormRules } from "naive-ui";
import type { AdminUserTag } from "../../../../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import { deleteUserTag, isValidTagColorInput, normalizeTagColor, updateUserTag } from "../../../api/admin-user";
import { createOperationId } from "../../../api/connect";
import { AdminApiError } from "../../../api/errors";
import AppDialog from "../../../components/AppDialog.vue";

export type UserTagMutationMode = "edit" | "delete";

export type UserTagMutationDialogPayload = Readonly<{
  mode: UserTagMutationMode;
  tag: AdminUserTag;
}>;

const emit = defineEmits<{
  updated: [tag: AdminUserTag];
  deleted: [tagId: string];
}>();

const message = useMessage();
const dialogRef = ref<{ toggleDialog: (open: boolean) => void } | null>(null);
const formRef = ref<FormInst | null>(null);

// The target snapshot provides the exact optimistic-lock version the server must validate.
const target = ref<UserTagMutationDialogPayload | null>(null);
const submitting = ref(false);
const errorMessage = ref("");
const generation = ref(0);
const activeController = shallowRef<AbortController | null>(null);
const form = reactive({
  name: "",
  color: "",
  reason: ""
});

const editRules: FormRules = {
  name: { required: true, message: "请输入标签名称", trigger: ["input", "blur"] },
  // Mirrors the backend #RRGGBB contract so a malformed color fails inline instead of
  // round-tripping to a server-side invalid_argument.
  color: [
    { required: true, message: "请输入标签颜色", trigger: ["input", "blur"] },
    {
      validator: (_rule, value: string) => !value || isValidTagColorInput(value),
      message: "颜色格式应为 #RRGGBB，例如 #2563EB",
      trigger: ["input", "blur"]
    }
  ],
  reason: { required: true, message: "请输入操作原因", trigger: ["input", "blur"] }
};
const deleteRules: FormRules = {
  reason: { required: true, message: "请输入删除原因", trigger: ["input", "blur"] }
};

const isDeleting = computed(() => target.value?.mode === "delete");
const title = computed(() => isDeleting.value ? "删除标签" : "编辑标签");

/**
 * Initializes a dialog from the explicit caller payload. It never watches visibility because a
 * reused dialog must not carry the previous tag or its version into the next mutation.
 */
const toggleDialog = (open: boolean, payload?: UserTagMutationDialogPayload): void => {
  if (open) {
    if (!payload) {
      return;
    }
    generation.value += 1;
    activeController.value?.abort();
    activeController.value = new AbortController();
    target.value = payload;
    form.name = payload.tag.name;
    form.color = payload.tag.color;
    form.reason = "";
    errorMessage.value = "";
    submitting.value = false;
    formRef.value?.restoreValidation();
  }
  dialogRef.value?.toggleDialog(open);
};

/**
 * Invalidates in-flight responses and removes mutation context after every close path.
 */
const handleClose = (): void => {
  generation.value += 1;
  const controller = activeController.value;
  controller?.abort();
  if (activeController.value === controller) {
    activeController.value = null;
  }
  target.value = null;
  form.name = "";
  form.color = "";
  form.reason = "";
  errorMessage.value = "";
  submitting.value = false;
  formRef.value?.restoreValidation();
};

const handleCancel = (close: () => void): void => {
  if (!submitting.value) {
    close();
  }
};

/**
 * Sends exactly one version-bound catalog mutation. A late response cannot update a newly opened
 * dialog because the generation and controller are both checked before the parent receives it.
 */
const handleSubmit = async (close: () => void): Promise<void> => {
  if (submitting.value || !target.value) {
    return;
  }
  try {
    await formRef.value?.validate();
  } catch {
    return;
  }

  const requestGeneration = generation.value;
  const payload = target.value;
  const controller = activeController.value;
  submitting.value = true;
  errorMessage.value = "";

  try {
    if (payload.mode === "edit") {
      // Canonicalize before the request so the mocked API boundary and the real backend both
      // observe the uppercase form enforced by the tag color contract.
      form.color = normalizeTagColor(form.color);
      const response = await updateUserTag({
        operationId: createOperationId(),
        tagId: payload.tag.tagId,
        name: form.name,
        color: form.color,
        reason: form.reason,
        expectedVersion: payload.tag.version,
        ...(controller ? { signal: controller.signal } : {})
      });
      if (controller?.signal.aborted || requestGeneration !== generation.value || !response.tag) {
        return;
      }
      emit("updated", response.tag);
      message.success("标签已更新。");
    } else {
      const response = await deleteUserTag({
        operationId: createOperationId(),
        tagId: payload.tag.tagId,
        reason: form.reason,
        expectedVersion: payload.tag.version,
        ...(controller ? { signal: controller.signal } : {})
      });
      if (controller?.signal.aborted || requestGeneration !== generation.value) {
        return;
      }
      emit("deleted", response.tagId || payload.tag.tagId);
      message.success("标签已删除，相关用户的标签分配已同步移除。");
    }
    close();
  } catch (error) {
    if (controller?.signal.aborted || requestGeneration !== generation.value) {
      return;
    }
    errorMessage.value = error instanceof AdminApiError ? error.message : "标签操作失败，请稍后重试。";
  } finally {
    if (requestGeneration === generation.value) {
      submitting.value = false;
    }
  }
};

defineExpose({
  toggleDialog
});
</script>

<template>
  <AppDialog ref="dialogRef" :title="title" :width="480" @closed="handleClose">
    <template #default="{ close }">
      <div class="admin-grid">
        <NAlert v-if="isDeleting" type="warning" :bordered="false">
          删除“{{ target?.tag.name }}”会移除所有用户身上的此标签，且该操作会写入审计记录。
        </NAlert>
        <NAlert v-if="errorMessage" type="error" :title="errorMessage" closable @close="errorMessage = ''" />
        <NForm ref="formRef" :model="form" :rules="isDeleting ? deleteRules : editRules" label-placement="top">
          <template v-if="!isDeleting">
            <NFormItem label="名称" path="name">
              <NInput v-model:value="form.name" maxlength="24" :disabled="submitting" />
            </NFormItem>
            <NFormItem label="颜色" path="color">
              <NInput v-model:value="form.color" placeholder="#2563EB" :disabled="submitting">
                <template #suffix>
                  <span
                    v-if="isValidTagColorInput(form.color)"
                    :style="{ display: 'inline-block', width: '14px', height: '14px', borderRadius: '4px', background: normalizeTagColor(form.color) }"
                  />
                </template>
              </NInput>
            </NFormItem>
          </template>
          <NFormItem :label="isDeleting ? '删除原因' : '修改原因'" path="reason">
            <NInput v-model:value="form.reason" type="textarea" :disabled="submitting" />
          </NFormItem>
        </NForm>
        <NSpace justify="end">
          <NButton :disabled="submitting" @click="handleCancel(close)">取消</NButton>
          <NButton :type="isDeleting ? 'error' : 'primary'" :loading="submitting" @click="handleSubmit(close)">
            {{ isDeleting ? "确认删除" : "保存标签" }}
          </NButton>
        </NSpace>
      </div>
    </template>
  </AppDialog>
</template>
