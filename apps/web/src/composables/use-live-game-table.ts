import { computed, onBeforeUnmount, onMounted, ref, type Ref } from "vue";
import { useRouter } from "vue-router";

import {
  GameClient as ViewerGameClient,
  SubscriptionFailure,
  SubscriptionRunner,
  type ActionInput,
  type ConnectionPhase,
  type GameClientState,
  type ProjectionReducer,
  type SubscriptionCursor,
  type ViewerRole,
} from "@game-night/game-client";

import { BrowserRealtimeAdapter } from "../api/browser-realtime";
import { ApiError, gameClient, type GameEnvelopeInput, type RoomSnapshot } from "../api/client";
import { gameProjectionFromConnect } from "../api/game-projection";
import { memberDisplayName } from "../member-display";
import { useRoomStore } from "../stores/room";
import { useRoomPresenceLease } from "./use-room-presence-lease";

type TableConnection = "online" | "offline" | "reconnecting" | "draining";

interface LivePlayer {
  readonly userId: string;
  readonly seatIndex: number;
}

interface PlayerPresentation {
  readonly userId: string;
  readonly displayName: string;
  readonly avatarText?: string;
  readonly connected: boolean;
  readonly host?: boolean;
  readonly seatIndex?: number;
}

interface LiveTableContext {
  readonly roomCode: string;
  readonly selfUserId: string;
  readonly viewerRole: "player" | "spectator" | "replay";
  readonly connection: TableConnection;
  readonly players: readonly PlayerPresentation[];
}

interface UseLiveGameTableOptions<TView, TContext extends LiveTableContext> {
  readonly roomId: string;
  readonly sessionId: string;
  readonly fixtureMode: Ref<boolean>;
  readonly reducer: ProjectionReducer<TView>;
  readonly view: Ref<TView>;
  readonly context: Ref<TContext>;
  readonly players: (view: TView) => readonly LivePlayer[];
  readonly viewActions: (view: TView) => readonly string[];
  readonly finished: (view: TView) => boolean;
}

/** A game route remains valid only while the room still points at that exact active session. */
export const isActiveRoomSession = (snapshot: RoomSnapshot, sessionId: string): boolean =>
  snapshot.status.includes("PLAYING") && snapshot.activeSessionId === sessionId;

/** Owns the platform transport lifecycle shared by every versioned game table. */
export const useLiveGameTable = <TView, TContext extends LiveTableContext>(options: UseLiveGameTableOptions<TView, TContext>) => {
  useRoomPresenceLease(() => options.roomId, { enabled: () => !options.fixtureMode.value });
  const router = useRouter();
  const room = useRoomStore();
  const liveFallback = ref(false);
  const liveStateVersion = ref(0);
  const pendingAction = ref<string | null>(null);
  // The outer projection includes platform-owned commands that are intentionally absent from the opaque game view.
  const authoritativeActions = ref<readonly string[]>([]);
  const subscriptionRunner = new SubscriptionRunner<TView>();
  const lifecycleController = new AbortController();
  let subscriptionController: AbortController | undefined;
  let actionController: AbortController | undefined;
  let stopLiveState: (() => void) | undefined;
  let roomReconciliationTimer: number | undefined;
  let roomReconciliationPending = false;
  let returningToRoom = false;

  // Mutations use authenticated Connect commands; this SDK instance owns only viewer-safe projection state.
  const liveClient = new ViewerGameClient<TView>({
    reducer: options.reducer,
    dispatch: async () => {
      throw new Error("live_dispatch_port_unused");
    },
  });

  const connectionState = (phase: ConnectionPhase): TableConnection => {
    if (phase === "online" || phase === "reconnecting" || phase === "draining") return phase;
    return "offline";
  };

  /** Leaves a terminal or inaccessible room instead of routing the viewer into a stale room shell. */
  const exitUnavailableRoom = async (message: string): Promise<void> => {
    room.exitRoom(message);
    if (!lifecycleController.signal.aborted) await router.replace({ name: "home" });
  };

  /** Refreshes the room before leaving so the destination follows the aggregate's authoritative lifecycle. */
  const returnToRoom = async (knownSnapshot?: RoomSnapshot | null): Promise<void> => {
    if (returningToRoom || lifecycleController.signal.aborted) return;
    returningToRoom = true;
    subscriptionController?.abort();
    let snapshot = knownSnapshot;
    try {
      if (snapshot === undefined) snapshot = await room.loadRoom(options.roomId);
    } catch (error) {
      if (error instanceof ApiError && [403, 404].includes(error.status)) {
        await exitUnavailableRoom("你已无法继续访问这个房间");
        return;
      }
      // The room page owns transient recovery when this best-effort refresh is unavailable.
    }
    if (snapshot?.status.includes("CLOSED")) {
      await exitUnavailableRoom("房主已解散房间，当前游戏已结束");
      return;
    }
    if (!lifecycleController.signal.aborted) {
      await router.replace({ name: "room", params: { roomId: options.roomId } });
    }
  };

  /** Reconciles a lost game subscription with the room aggregate without issuing overlapping reads. */
  const reconcileRoomSession = async (): Promise<void> => {
    if (roomReconciliationPending || returningToRoom || lifecycleController.signal.aborted) return;
    roomReconciliationPending = true;
    try {
      const snapshot = await room.loadRoom(options.roomId);
      if (snapshot !== null && !isActiveRoomSession(snapshot, options.sessionId)) await returnToRoom(snapshot);
    } catch (error) {
      if (error instanceof ApiError && [403, 404].includes(error.status)) {
        await exitUnavailableRoom("你已无法继续访问这个房间");
      }
      // Subscription retry remains responsible for other transient room-read failures.
    } finally {
      roomReconciliationPending = false;
    }
  };

  /** Applies an immutable SDK snapshot and derives presentation-only member labels from room state. */
  const applyLiveState = (state: GameClientState<TView>): void => {
    if (state.view === null) {
      options.context.value = { ...options.context.value, connection: connectionState(state.connection) };
      if (state.connection === "failed") void reconcileRoomSession();
      return;
    }
    options.view.value = state.view;
    liveStateVersion.value = state.stateVersion;
    authoritativeActions.value = [...state.allowedActions];
    options.context.value = {
      ...options.context.value,
      selfUserId: room.userId,
      roomCode: room.roomCode ?? options.context.value.roomCode,
      viewerRole: state.viewerRole ?? options.context.value.viewerRole,
      connection: connectionState(state.connection),
      players: options.players(state.view).map((player) => {
        const member = room.remoteRoom?.members.find((candidate) => candidate.userId === player.userId);
        const displayName = memberDisplayName(player.userId, member?.username);
        return {
          userId: player.userId,
          displayName,
          avatarText: displayName.slice(0, 1),
          connected: true,
          host: player.userId === room.remoteRoom?.hostUserId,
          seatIndex: player.seatIndex,
        };
      }),
    } as TContext;
    if (options.finished(state.view)) {
      void returnToRoom();
    } else if (state.connection === "failed") {
      void reconcileRoomSession();
    }
  };

  const viewerRoleForRoom = (): Exclude<ViewerRole, "replay"> => {
    const member = room.remoteRoom?.members.find((candidate) => candidate.userId === room.userId);
    return member?.role.includes("SPECTATOR") ? "spectator" : "player";
  };

  const viewerKind = (role: Exclude<ViewerRole, "replay">): string =>
    role === "spectator" ? "VIEWER_KIND_SPECTATOR" : "VIEWER_KIND_PLAYER";

  /** Resolves the explicit deployment endpoint or the same-origin development proxy. */
  const realtimeWebSocketURL = (): string => {
    const configured = String(import.meta.env.VITE_REALTIME_URL ?? "").trim();
    const url = new URL(configured || "/realtime/game", window.location.href);
    if (url.protocol === "http:") url.protocol = "ws:";
    if (url.protocol === "https:") url.protocol = "wss:";
    return url.toString();
  };

  const loadLiveProjection = async (): Promise<void> => {
    try {
      const response = await gameClient.getProjection(
        options.roomId,
        options.sessionId,
        viewerKind(viewerRoleForRoom()),
        lifecycleController.signal,
      );
      liveClient.accept(gameProjectionFromConnect(response.projection));
    } catch (error) {
      if (import.meta.env.DEV && error instanceof ApiError && [401, 403, 404].includes(error.status)) liveFallback.value = true;
      liveClient.markReconnecting(error instanceof ApiError ? error.code : "projection_unavailable");
    }
  };

  const subscriptionFailure = (error: ApiError): SubscriptionFailure =>
    new SubscriptionFailure(error.code, error.message, ![401, 403, 404].includes(error.status), "reconnecting", null, { cause: error });

  /** Refreshes room membership once if the server reports that the viewer role changed. */
  const openLiveSubscription = async (cursor: SubscriptionCursor | null, signal: AbortSignal) => {
    let role = viewerRoleForRoom();
    const request = async () => gameClient.openSubscription(
      options.roomId,
      options.sessionId,
      viewerKind(role),
      cursor?.stateVersion ?? 0,
      signal,
    );
    let response;
    try {
      response = await request();
    } catch (error) {
      if (!(error instanceof ApiError) || error.status !== 403) {
        if (error instanceof ApiError) throw subscriptionFailure(error);
        throw error;
      }
      try {
        const snapshot = await room.loadRoom(options.roomId);
        if (snapshot !== null && !isActiveRoomSession(snapshot, options.sessionId)) {
          void returnToRoom(snapshot);
          throw new SubscriptionFailure("session_finished", "游戏会话已结束", false);
        }
      } catch (refreshError) {
        if (refreshError instanceof SubscriptionFailure) throw refreshError;
        if (refreshError instanceof ApiError) throw subscriptionFailure(refreshError);
        throw refreshError;
      }
      const refreshedRole = viewerRoleForRoom();
      if (refreshedRole === role) throw subscriptionFailure(error);
      role = refreshedRole;
      try {
        response = await request();
      } catch (retryError) {
        if (retryError instanceof ApiError) throw subscriptionFailure(retryError);
        throw retryError;
      }
    }
    try {
      return { ticket: response.ticket, grant: response.grant, projection: gameProjectionFromConnect(response.projection) };
    } catch (error) {
      throw new SubscriptionFailure("invalid_subscription_projection", "订阅投影无效", false, "reconnecting", null, { cause: error });
    }
  };

  /** Replaces the active socket attempt so retry and teardown cannot leave overlapping subscriptions. */
  const startLiveSubscription = (): void => {
    subscriptionController?.abort();
    const controller = new AbortController();
    subscriptionController = controller;
    const adapter = new BrowserRealtimeAdapter({ url: realtimeWebSocketURL, openSubscription: openLiveSubscription });
    void subscriptionRunner.run(liveClient, adapter, controller.signal).catch((error: unknown) => {
      if (!controller.signal.aborted) liveClient.fail(error instanceof Error ? error.name : "subscription_failed");
    });
  };

  const initializeLiveTable = async (): Promise<void> => {
    try {
      const snapshot = await room.loadRoom(options.roomId);
      if (snapshot !== null && !isActiveRoomSession(snapshot, options.sessionId)) {
        await returnToRoom();
        return;
      }
    } catch {
      // Projection authorization remains authoritative when a room refresh is temporarily unavailable.
    }
    await loadLiveProjection();
    if (!liveFallback.value && !lifecycleController.signal.aborted) startLiveSubscription();
  };

  /** Submits one authoritative action and immediately accepts the returned projection. */
  const submitLiveAction = async (input: ActionInput): Promise<boolean> => {
    if (options.fixtureMode.value || liveFallback.value) return false;
    if (pendingAction.value !== null || options.context.value.connection !== "online") return true;
    pendingAction.value = input.action;
    const controller = new AbortController();
    actionController?.abort();
    actionController = controller;
    try {
      const response = await gameClient.action(
        options.roomId,
        room.userId,
        options.sessionId,
        liveStateVersion.value,
        crypto.randomUUID(),
        input.message,
        controller.signal,
      );
      liveClient.accept(gameProjectionFromConnect(response.projection));
    } catch (error) {
      if (!controller.signal.aborted) liveClient.markReconnecting(error instanceof ApiError ? error.code : "action_failed");
    } finally {
      if (actionController === controller) {
        actionController = undefined;
        pendingAction.value = null;
      }
    }
    return true;
  };

  /** Finishes through the room aggregate so room and game status change atomically. */
  const finishLiveSession = async (command: GameEnvelopeInput): Promise<boolean> => {
    if (options.fixtureMode.value || liveFallback.value) return false;
    if (pendingAction.value !== null) return true;
    pendingAction.value = "session.finish";
    try {
      if (room.remoteRoom?.version === undefined) throw new Error("room_snapshot_missing");
      await room.finishRemoteGame(options.sessionId, liveStateVersion.value, command);
      await router.push({ name: "room", params: { roomId: options.roomId } });
    } catch {
      liveClient.markReconnecting("finish_failed");
    } finally {
      pendingAction.value = null;
    }
    return true;
  };

  const retry = (): void => {
    if (options.fixtureMode.value || liveFallback.value) {
      options.context.value = { ...options.context.value, connection: "online" };
      return;
    }
    options.context.value = { ...options.context.value, connection: "reconnecting" };
    startLiveSubscription();
  };

  onMounted(() => {
    if (options.fixtureMode.value) return;
    if (room.roomId !== options.roomId) {
      const roomCode = room.remoteRoom?.roomId === options.roomId ? room.remoteRoom.roomCode : options.roomId.toUpperCase().slice(0, 6);
      room.enterRoom(options.roomId, roomCode);
    }
    room.setSession(options.sessionId);
    options.context.value = { ...options.context.value, connection: "reconnecting", selfUserId: room.userId };
    stopLiveState = liveClient.subscribe(applyLiveState);
    void initializeLiveTable();
    // Session fanout is the fast path; polling also covers idle/cancel transitions that do not advance a game projection.
    roomReconciliationTimer = window.setInterval(() => {
      if (document.visibilityState !== "hidden") void reconcileRoomSession();
    }, 2_500);
  });

  onBeforeUnmount(() => {
    lifecycleController.abort();
    subscriptionController?.abort();
    actionController?.abort();
    if (roomReconciliationTimer !== undefined) window.clearInterval(roomReconciliationTimer);
    stopLiveState?.();
    liveClient.dispose();
  });

  return {
    liveFallback: computed(() => liveFallback.value),
    allowedActions: computed(() => options.fixtureMode.value || liveFallback.value
      ? options.viewActions(options.view.value)
      : authoritativeActions.value),
    pendingAction,
    submitLiveAction,
    finishLiveSession,
    retry,
  };
};
