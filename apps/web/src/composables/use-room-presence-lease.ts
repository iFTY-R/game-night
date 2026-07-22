import { onBeforeUnmount, onMounted, toValue, watch, type MaybeRefOrGetter } from "vue";

import { roomClient } from "../api/client";

// The interval stays well below the server idle timeout without adding foreground polling noise.
export const roomPresenceHeartbeatInterval = 30_000;

interface RoomPresenceLeaseOptions {
  readonly enabled?: MaybeRefOrGetter<boolean>;
}

/** Keeps a current member's room lease alive without coupling transport failures to lobby or game UI state. */
export const useRoomPresenceLease = (
  roomId: MaybeRefOrGetter<string>,
  options: RoomPresenceLeaseOptions = {},
): void => {
  let timer: number | undefined;
  let pending = false;
  let stopRoomWatch: (() => void) | undefined;

  const heartbeat = async (): Promise<void> => {
    const currentRoomId = toValue(roomId).trim();
    const enabled = options.enabled === undefined || toValue(options.enabled);
    if (!enabled || currentRoomId === "" || pending || document.visibilityState === "hidden") return;
    pending = true;
    try {
      await roomClient.heartbeatRoom(currentRoomId);
    } catch {
      // Room polling and realtime reconciliation own visible errors; lease retries remain independent.
    } finally {
      pending = false;
    }
  };

  const handleVisibilityChange = (): void => {
    if (document.visibilityState !== "hidden") void heartbeat();
  };

  onMounted(() => {
    stopRoomWatch = watch(() => toValue(roomId), () => { void heartbeat(); }, { immediate: true });
    timer = window.setInterval(() => { void heartbeat(); }, roomPresenceHeartbeatInterval);
    document.addEventListener("visibilitychange", handleVisibilityChange);
  });

  onBeforeUnmount(() => {
    stopRoomWatch?.();
    stopRoomWatch = undefined;
    if (timer !== undefined) window.clearInterval(timer);
    timer = undefined;
    document.removeEventListener("visibilitychange", handleVisibilityChange);
  });
};
