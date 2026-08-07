import { computed, onBeforeUnmount, onMounted, ref, watch, type Ref } from "vue";
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
import { ApiError, createOperationID, gameClient, type GameEnvelopeInput, type GameSessionSummaryWire, type RoomSnapshot } from "../api/client";
import { gameProjectionFromConnect } from "../api/game-projection";
import { memberDisplayName } from "../member-display";
import { useRoomStore } from "../stores/room";
import { useRoomPresenceLease } from "./use-room-presence-lease";

type TableConnection = "online" | "offline" | "reconnecting" | "draining";
type SimultaneousActionConflictResolution = "confirmed" | "retry" | "abort";

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

interface SimultaneousActionConflictContext<TView> {
  readonly attemptedAction: ActionInput;
  readonly latestView: TView;
  readonly latestAllowedActions: readonly string[];
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
  readonly resolveSimultaneousConflict?: (
    context: SimultaneousActionConflictContext<TView>,
  ) => SimultaneousActionConflictResolution;
}

interface LiveActionFence {
  readonly roomId: string;
  readonly sessionId: string;
  readonly userId: string;
}

/** Reports the latest room or session lifecycle observation to the game shell. */
export interface LiveSessionLifecycle {
  readonly known: boolean;
  readonly paused: boolean;
  readonly suspendedAt: string | null;
  /** Fences lifecycle writes against realtime ownership changes; this is distinct from the room host epoch. */
  readonly ownershipEpoch: string | null;
}

/** Simultaneous card-selection games may transparently replay at most two times after the initial conflicted write. */
const simultaneousActionRetryLimit = 2;
/** Only this canonical platform error proves that refreshing before a bounded retry is appropriate. */
const gameStateVersionConflictCode = "game.state.version_conflict";
// This business key is the only action failure that means the room should remain mounted while controls are withdrawn.
const gameSessionSuspendedKey = "game.session.suspended";

/** A game route remains valid only while the room still points at that exact active session. */
export const isActiveRoomSession = (snapshot: RoomSnapshot, sessionId: string): boolean =>
  snapshot.status.includes("PLAYING") && snapshot.activeSessionId === sessionId;

/** Owns the platform transport lifecycle shared by every versioned game table. */
export const useLiveGameTable = <TView, TContext extends LiveTableContext>(options: UseLiveGameTableOptions<TView, TContext>) => {
  useRoomPresenceLease(() => options.roomId, { enabled: () => !options.fixtureMode.value });
  const router = useRouter();
  const room = useRoomStore();
  const liveStateVersion = ref(0);
  const pendingAction = ref<string | null>(null);
  // The outer projection includes platform-owned commands that are intentionally absent from the opaque game view.
  const authoritativeActions = ref<readonly string[]>([]);
  // Session lifecycle is delivered by full projections while room polling independently restores governance after missed fanout.
  const sessionSummary = ref<GameSessionSummaryWire | null>(null);
  const subscriptionRunner = new SubscriptionRunner<TView>();
  const lifecycleController = new AbortController();
  let subscriptionController: AbortController | undefined;
  let actionController: AbortController | undefined;
  let stopLiveState: (() => void) | undefined;
  let roomReconciliationTimer: number | undefined;
  let roomReconciliationPending = false;
  let returningToRoom = false;
  const roomPause = computed(() => {
    const activePause = room.remoteRoom?.activePause;
    return activePause?.sessionId === options.sessionId ? activePause : null;
  });
  const summaryPaused = computed(() => sessionSummary.value?.status.includes("SUSPENDED") ?? false);
  // Room and session notifications may arrive in either order; the latest authoritative observation drives presentation.
  const pauseObservation = ref({
    paused: roomPause.value !== null,
    suspendedAt: roomPause.value?.pausedAt ?? null,
  });
  const isPaused = computed(() => pauseObservation.value.paused);
  const suspendedAt = computed(() => pauseObservation.value.suspendedAt);

  /** Applies lifecycle metadata together so controls and countdowns cannot observe a mixed pause state. */
  const observeSessionSummary = (summary: GameSessionSummaryWire | null | undefined): void => {
    if (summary === undefined) return;
    sessionSummary.value = summary;
    if (summary === null) return;
    const paused = summary.status.includes("SUSPENDED");
    pauseObservation.value = {
      paused,
      suspendedAt: paused ? summary.suspendedAt ?? roomPause.value?.pausedAt ?? null : null,
    };
  };

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

  /** Guards post-conflict retries so stale identities, sessions, or controllers cannot write into a replaced table. */
  const captureLiveActionFence = (): LiveActionFence => ({
    roomId: room.roomId ?? options.roomId,
    sessionId: room.sessionId ?? options.sessionId,
    userId: room.userId,
  });

  /** Version-conflict recovery must stop as soon as the room/user/session scope or active controller changes. */
  const canContinueLiveAction = (controller: AbortController, fence: LiveActionFence): boolean =>
    !lifecycleController.signal.aborted
    && !controller.signal.aborted
    && actionController === controller
    && (room.roomId ?? options.roomId) === fence.roomId
    && (room.sessionId ?? options.sessionId) === fence.sessionId
    && room.userId === fence.userId;

  /** Only state-version conflicts are eligible for silent projection refresh and bounded replay. */
  const isGameStateVersionConflict = (error: unknown): error is ApiError =>
    error instanceof ApiError && error.code === gameStateVersionConflictCode;

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
      if (snapshot !== null && !isActiveRoomSession(snapshot, options.sessionId)) {
        await returnToRoom(snapshot);
        return;
      }
      const snapshotPaused = snapshot?.activePause?.sessionId === options.sessionId;
      if (snapshot !== null && snapshotPaused !== summaryPaused.value) {
        // Pure lifecycle changes keep the game state version stable, so a room mismatch explicitly refreshes session metadata.
        liveClient.accept(await fetchProjection(lifecycleController.signal));
      }
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

  /** Reads one full projection for both initial load and state-version conflict recovery. */
  const fetchProjection = async (signal: AbortSignal) => {
    const response = await gameClient.getProjection(
      options.roomId,
      options.sessionId,
      viewerKind(viewerRoleForRoom()),
      signal,
    );
    observeSessionSummary(response.session ?? null);
    return gameProjectionFromConnect(response.projection);
  };

  const loadLiveProjection = async (): Promise<void> => {
    try {
      liveClient.accept(await fetchProjection(lifecycleController.signal));
    } catch (error) {
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
      observeSessionSummary(response.session);
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
    if (!lifecycleController.signal.aborted) startLiveSubscription();
  };

  /** Recovers one optimistic-write conflict by refreshing the latest projection and asking the game view whether replay is safe. */
  const recoverSimultaneousActionConflict = async (
    input: ActionInput,
    controller: AbortController,
    fence: LiveActionFence,
    retries: number,
  ): Promise<{ handled: boolean; shouldRetry: boolean; nextStateVersion: number | null }> => {
    if (!canContinueLiveAction(controller, fence)) {
      return { handled: true, shouldRetry: false, nextStateVersion: null };
    }
    try {
      const projection = await fetchProjection(controller.signal);
      if (!canContinueLiveAction(controller, fence)) {
        return { handled: true, shouldRetry: false, nextStateVersion: null };
      }
      liveClient.accept(projection);
      const latest = liveClient.snapshot();
      if (latest.view === null) {
        return { handled: true, shouldRetry: false, nextStateVersion: null };
      }
      const resolution = options.resolveSimultaneousConflict?.({
        attemptedAction: input,
        latestView: latest.view,
        latestAllowedActions: latest.allowedActions,
      }) ?? "abort";
      if (resolution !== "retry" || retries >= simultaneousActionRetryLimit) {
        return { handled: true, shouldRetry: false, nextStateVersion: null };
      }
      if (!canContinueLiveAction(controller, fence)) {
        return { handled: true, shouldRetry: false, nextStateVersion: null };
      }
      return { handled: true, shouldRetry: true, nextStateVersion: latest.stateVersion };
    } catch (refreshError) {
      if (canContinueLiveAction(controller, fence)) {
        liveClient.markReconnecting(refreshError instanceof ApiError ? refreshError.code : "projection_unavailable");
      }
      return { handled: true, shouldRetry: false, nextStateVersion: null };
    }
  };

  /** Submits one authoritative action and immediately accepts the returned projection. */
  const submitLiveAction = async (input: ActionInput): Promise<boolean> => {
    if (options.fixtureMode.value) return false;
    if (pendingAction.value !== null || options.context.value.connection !== "online" || isPaused.value) return true;
    pendingAction.value = input.action;
    const controller = new AbortController();
    actionController?.abort();
    actionController = controller;
    const fence = captureLiveActionFence();
    let nextStateVersion = liveStateVersion.value;
    let retries = 0;
    try {
      while (canContinueLiveAction(controller, fence)) {
        try {
          const response = await gameClient.action(
            options.roomId,
            fence.userId,
            options.sessionId,
            nextStateVersion,
            createOperationID(),
            input.message,
            controller.signal,
          );
          if (!canContinueLiveAction(controller, fence)) break;
          liveClient.accept(gameProjectionFromConnect(response.projection));
          break;
        } catch (error) {
          if (!isGameStateVersionConflict(error)) {
            if (error instanceof ApiError && (error.businessKey === gameSessionSuspendedKey || error.code === gameSessionSuspendedKey)) {
              await reconcileRoomSession();
              break;
            }
            if (canContinueLiveAction(controller, fence)) {
              liveClient.markReconnecting(error instanceof ApiError ? error.code : "action_failed");
            }
            break;
          }
          const recovery = await recoverSimultaneousActionConflict(input, controller, fence, retries);
          if (!recovery.handled || !recovery.shouldRetry || recovery.nextStateVersion === null) break;
          nextStateVersion = recovery.nextStateVersion;
          retries += 1;
        }
      }
    } catch (error) {
      if (canContinueLiveAction(controller, fence)) {
        liveClient.markReconnecting(error instanceof ApiError ? error.code : "action_failed");
      }
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
    if (options.fixtureMode.value) return false;
    if (pendingAction.value !== null || isPaused.value) return true;
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
    if (options.fixtureMode.value) {
      options.context.value = { ...options.context.value, connection: "online" };
      return;
    }
    options.context.value = { ...options.context.value, connection: "reconnecting" };
    startLiveSubscription();
  };

  watch(roomPause, (pause) => {
    pauseObservation.value = { paused: pause !== null, suspendedAt: pause?.pausedAt ?? null };
    // Room governance writes do not advance game state, so align the session summary immediately instead of waiting for polling.
    if (!options.fixtureMode.value) void reconcileRoomSession();
  });

  watch(isPaused, (paused) => {
    if (!paused) return;
    // A pause racing an optimistic command cancels the local request; the server remains authoritative if it committed first.
    actionController?.abort();
    authoritativeActions.value = [];
    pendingAction.value = null;
  });

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
    allowedActions: computed(() => options.fixtureMode.value
      ? options.viewActions(options.view.value)
      : isPaused.value ? [] : authoritativeActions.value),
    isPaused,
    suspendedAt,
    lifecycle: computed<LiveSessionLifecycle>(() => ({
      known: sessionSummary.value !== null || (room.remoteRoom !== null && isActiveRoomSession(room.remoteRoom, options.sessionId)),
      paused: isPaused.value,
      suspendedAt: suspendedAt.value,
      ownershipEpoch: sessionSummary.value?.ownershipEpoch ?? null,
    })),
    sessionStatus: computed(() => sessionSummary.value?.status ?? ""),
    pendingAction,
    submitLiveAction,
    finishLiveSession,
    retry,
  };
};
