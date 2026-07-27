import { ref, shallowRef } from "vue";
import { defineStore } from "pinia";
import type { GetOperationsSnapshotResponse } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_operations_pb";
import { getOperationsSnapshot } from "../../api/admin-operations";
import { AdminApiError } from "../../api/errors";

export const useOperationsStore = defineStore("admin-operations", () => {
  const snapshot = ref<GetOperationsSnapshotResponse | null>(null);
  const loading = ref(false);
  const errorMessage = ref("");
  const generation = ref(0);
  const activeController = shallowRef<AbortController | null>(null);

  /** Replaces in-flight reads so fast manual refreshes cannot commit an older snapshot. */
  const refresh = async (): Promise<void> => {
    generation.value += 1;
    const token = generation.value;
    activeController.value?.abort();
    const controller = new AbortController();
    activeController.value = controller;
    loading.value = true;
    errorMessage.value = "";
    try {
      const response = await getOperationsSnapshot(controller.signal);
      if (!controller.signal.aborted && token === generation.value) {
        snapshot.value = response;
      }
    } catch (error) {
      if (!controller.signal.aborted && token === generation.value) {
        errorMessage.value = error instanceof AdminApiError ? error.message : "无法读取系统运维状态。";
      }
    } finally {
      if (token === generation.value) {
        loading.value = false;
      }
    }
  };

  const dispose = (): void => {
    generation.value += 1;
    activeController.value?.abort();
    activeController.value = null;
  };

  return { activeController, dispose, errorMessage, generation, loading, refresh, snapshot };
});
