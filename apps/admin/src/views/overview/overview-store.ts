import { ref, shallowRef } from "vue";
import { defineStore } from "pinia";
import {
  AdminOverviewGranularity,
  type GetOverviewResponse
} from "../../../../../contracts/gen/ts/platform/admin/v1/admin_overview_pb";
import { getOverview } from "../../api/admin-overview";
import { AdminApiError } from "../../api/errors";

const alignedWindow = (granularity: AdminOverviewGranularity): { start: Date; end: Date } => {
  const end = new Date();
  if (granularity === AdminOverviewGranularity.DAY) {
    end.setUTCHours(0, 0, 0, 0);
    const start = new Date(end);
    start.setUTCDate(start.getUTCDate() - 30);
    return { start, end };
  }
  end.setUTCMinutes(0, 0, 0);
  const start = new Date(end.getTime() - 24 * 60 * 60 * 1000);
  return { start, end };
};

export const useOverviewStore = defineStore("admin-overview", () => {
  const response = ref<GetOverviewResponse | null>(null);
  const granularity = ref(AdminOverviewGranularity.HOUR);
  const loading = ref(false);
  const errorMessage = ref("");
  const generation = ref(0);
  const activeController = shallowRef<AbortController | null>(null);

  /** Reads one aligned real-data window and rejects late responses after granularity changes. */
  const refresh = async (): Promise<void> => {
    generation.value += 1;
    const token = generation.value;
    activeController.value?.abort();
    const controller = new AbortController();
    activeController.value = controller;
    const window = alignedWindow(granularity.value);
    loading.value = true;
    errorMessage.value = "";
    try {
      const next = await getOverview({ windowStart: window.start, windowEnd: window.end, granularity: granularity.value, signal: controller.signal });
      if (!controller.signal.aborted && token === generation.value) {
        response.value = next;
      }
    } catch (error) {
      if (!controller.signal.aborted && token === generation.value) {
        errorMessage.value = error instanceof AdminApiError ? error.message : "无法读取运营概览。";
      }
    } finally {
      if (token === generation.value) {
        loading.value = false;
      }
    }
  };

  const setGranularity = (value: AdminOverviewGranularity): void => {
    granularity.value = value;
    void refresh();
  };

  const dispose = (): void => {
    generation.value += 1;
    activeController.value?.abort();
    activeController.value = null;
  };

  return { activeController, dispose, errorMessage, generation, granularity, loading, refresh, response, setGranularity };
});
