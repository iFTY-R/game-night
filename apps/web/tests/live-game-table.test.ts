import { createApp, h, nextTick, ref } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ActionInput, ProjectionReducer } from "@game-night/game-client";

const shared = vi.hoisted(() => {
  const remoteRoom = {
    roomId: "room-1",
    roomCode: "TABLE1",
    visibility: "ROOM_VISIBILITY_PRIVATE",
    status: "ROOM_STATUS_PLAYING",
    hostUserId: "user-1",
    participantCapacity: 8,
    participantAdmission: "ADMISSION_MODE_OPEN",
    spectatorAdmission: "ADMISSION_MODE_OPEN",
    members: [
      {
        userId: "user-1",
        username: "小满",
        role: "MEMBER_ROLE_PARTICIPANT",
        requestedRole: "MEMBER_ROLE_UNSPECIFIED",
        seatIndex: 0,
      },
    ],
    activeSessionId: "session-1",
    activeGameId: "three-rounds",
    lastFinishedSessionId: "",
    lastFinishedGameId: "",
    version: { roomVersion: "1", membershipVersion: "1" },
    activePause: undefined as undefined | {
      pauseId: string;
      sessionId: string;
      source: string;
      pausedByUserId: string;
      pausedAt: string;
    },
  };
  const roomStore = {
    userId: "user-1",
    displayName: "小满",
    roomId: "room-1",
    roomCode: "TABLE1",
    sessionId: "session-1",
    remoteRoom,
    enterRoom: vi.fn((roomId: string, roomCode: string) => {
      roomStore.roomId = roomId;
      roomStore.roomCode = roomCode;
    }),
    setSession: vi.fn((sessionId: string) => {
      roomStore.sessionId = sessionId;
    }),
    loadRoom: vi.fn(async () => roomStore.remoteRoom),
    exitRoom: vi.fn(),
    finishRemoteGame: vi.fn(),
  };
  return {
    api: {
      action: vi.fn(),
      getProjection: vi.fn(),
      openSubscription: vi.fn(),
    },
    router: {
      push: vi.fn(),
      replace: vi.fn(),
    },
    subscriptionRun: vi.fn(async (): Promise<void> => undefined),
    roomStore,
  };
});

vi.mock("@game-night/game-client", async () => {
  const actual = await vi.importActual<typeof import("@game-night/game-client")>("@game-night/game-client");
  class MockSubscriptionRunner<TView> {
    public run = shared.subscriptionRun as (client: unknown, adapter: unknown, signal: AbortSignal) => Promise<void>;
  }
  return {
    ...actual,
    SubscriptionRunner: MockSubscriptionRunner,
  };
});

vi.mock("vue-router", () => ({
  useRouter: () => shared.router,
}));

vi.mock("../src/stores/room", () => ({
  useRoomStore: () => shared.roomStore,
}));

vi.mock("../src/composables/use-room-presence-lease", () => ({
  useRoomPresenceLease: () => undefined,
}));

vi.mock("../src/api/client", async () => {
  const actual = await vi.importActual<typeof import("../src/api/client")>("../src/api/client");
  return {
    ...actual,
    gameClient: shared.api,
  };
});

import { ApiError, gameClient, type GameEnvelopeInput, type RoomSnapshot } from "../src/api/client";
import { isActiveRoomSession, useLiveGameTable } from "../src/composables/use-live-game-table";

interface TestView {
  readonly phase: string;
  readonly confirmed: boolean;
  readonly retryable: boolean;
  readonly moduleActions: readonly string[];
}

interface TestContext {
  readonly roomCode: string;
  readonly selfUserId: string;
  readonly viewerRole: "player" | "spectator" | "replay";
  readonly connection: "online" | "offline" | "reconnecting" | "draining";
  readonly players: readonly { readonly userId: string; readonly displayName: string; readonly connected: boolean }[];
}

const reducer: ProjectionReducer<TestView> = {
  fromProjection: (projection) => JSON.parse(new TextDecoder().decode(projection.view.payload)) as TestView,
  moduleActions: (view) => view.moduleActions,
  applyDelta: (current) => ({ view: current }),
};

const createRemoteRoom = () => ({
  roomId: "room-1",
  roomCode: "TABLE1",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_PLAYING",
  hostUserId: "user-1",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_OPEN",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [
    {
      userId: "user-1",
      username: "小满",
      role: "MEMBER_ROLE_PARTICIPANT",
      requestedRole: "MEMBER_ROLE_UNSPECIFIED",
      seatIndex: 0,
    },
  ],
  activeSessionId: "session-1",
  activeGameId: "three-rounds",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "1", membershipVersion: "1" },
  activePause: undefined as undefined | {
    pauseId: string;
    sessionId: string;
    source: string;
    pausedByUserId: string;
    pausedAt: string;
  },
});

const roomSnapshot = (status: string, activeSessionId: string): RoomSnapshot => ({
  roomId: "room-1",
  roomCode: "TABLE1",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status,
  hostUserId: "host-1",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_CLOSED",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [],
  activeSessionId,
  activeGameId: "dice-789",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "1", membershipVersion: "1" },
});

const baseView = (overrides: Partial<TestView> = {}): TestView => ({
  phase: "stage-1",
  confirmed: false,
  retryable: true,
  moduleActions: ["round.commit"],
  ...overrides,
});

const projectionResponse = (view: TestView, stateVersion: number, allowedActions = view.moduleActions) => ({
  projection: {
    sessionId: "session-1",
    stateVersion: String(stateVersion),
    viewerKind: "VIEWER_KIND_PLAYER",
    view: {
      gameId: "three-rounds",
      version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
      schemaVersion: 1,
      messageType: "test.view",
      payload: btoa(JSON.stringify(view)),
    },
    allowedActions: [...allowedActions],
  },
});

const actionInput = (message = createEnvelope()): ActionInput => ({
  action: "round.commit",
  message,
});

function createEnvelope(): GameEnvelopeInput {
  return {
    gameId: "three-rounds",
    version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
    schemaVersion: 1,
    messageType: "test.action",
    payload: new TextEncoder().encode('{"pick":["A♠"]}'),
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((innerResolve, innerReject) => {
    resolve = innerResolve;
    reject = innerReject;
  });
  return { promise, resolve, reject };
}

const settle = async (): Promise<void> => {
  await Promise.resolve();
  await nextTick();
};

const resetSharedState = (): void => {
  shared.roomStore.userId = "user-1";
  shared.roomStore.displayName = "小满";
  shared.roomStore.roomId = "room-1";
  shared.roomStore.roomCode = "TABLE1";
  shared.roomStore.sessionId = "session-1";
  shared.roomStore.remoteRoom = createRemoteRoom();
  shared.roomStore.enterRoom.mockReset();
  shared.roomStore.enterRoom.mockImplementation((roomId: string, roomCode: string) => {
    shared.roomStore.roomId = roomId;
    shared.roomStore.roomCode = roomCode;
  });
  shared.roomStore.setSession.mockReset();
  shared.roomStore.setSession.mockImplementation((sessionId: string) => {
    shared.roomStore.sessionId = sessionId;
  });
  shared.roomStore.loadRoom.mockReset();
  shared.roomStore.loadRoom.mockImplementation(async () => shared.roomStore.remoteRoom);
  shared.roomStore.exitRoom.mockReset();
  shared.roomStore.finishRemoteGame.mockReset();
  shared.api.action.mockReset();
  shared.api.getProjection.mockReset();
  shared.api.openSubscription.mockReset();
  shared.router.push.mockReset();
  shared.router.replace.mockReset();
  shared.subscriptionRun.mockReset();
  shared.subscriptionRun.mockResolvedValue(undefined);
};

async function mountLiveTable(options: {
  readonly awaitConnection?: TestContext["connection"];
  readonly resolveSimultaneousConflict?: (view: TestView) => "confirmed" | "retry" | "abort";
} = {}) {
  const fixtureMode = ref(false);
  const view = ref(baseView());
  const context = ref<TestContext>({
    roomCode: "TABLE1",
    selfUserId: "",
    viewerRole: "player",
    connection: "offline",
    players: [],
  });
  let liveTable!: ReturnType<typeof useLiveGameTable<TestView, TestContext>>;
  const root = document.createElement("div");
  document.body.appendChild(root);
  const app = createApp({
    setup() {
      liveTable = useLiveGameTable<TestView, TestContext>({
        roomId: "room-1",
        sessionId: "session-1",
        fixtureMode,
        reducer,
        view,
        context,
        players: () => [{ userId: "user-1", seatIndex: 0 }],
        viewActions: (currentView) => currentView.moduleActions,
        finished: () => false,
        ...(options.resolveSimultaneousConflict === undefined
          ? {}
          : {
              resolveSimultaneousConflict: ({ latestView }) => options.resolveSimultaneousConflict!(latestView),
            }),
      });
      return () => h("div");
    },
  });
  app.mount(root);
  if (options.awaitConnection === undefined) {
    await vi.waitFor(() => expect(context.value.connection).toBe("online"));
  } else {
    await vi.waitFor(() => expect(context.value.connection).toBe(options.awaitConnection));
  }
  return { app, root, liveTable, view, context };
}

describe("live game route lifecycle", () => {
  beforeEach(() => {
    resetSharedState();
    vi.spyOn(globalThis.crypto, "randomUUID")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000001")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000002")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000003")
      .mockReturnValue("00000000-0000-4000-8000-000000000099");
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.body.replaceChildren();
  });

  it("keeps only the exact active playing session on the game route", () => {
    expect(isActiveRoomSession(roomSnapshot("ROOM_STATUS_PLAYING", "session-1"), "session-1")).toBe(true);
    expect(isActiveRoomSession(roomSnapshot("ROOM_STATUS_PLAYING", "session-2"), "session-1")).toBe(false);
    expect(isActiveRoomSession(roomSnapshot("ROOM_STATUS_POST_GAME", ""), "session-1")).toBe(false);
  });

  it("keeps authoritative actions closed when the live projection bootstrap is forbidden", async () => {
    const subscriptionGate = deferred<void>();
    shared.api.getProjection.mockRejectedValueOnce(new ApiError(403, "projection_forbidden", "forbidden"));
    shared.subscriptionRun.mockImplementation(() => subscriptionGate.promise);
    const harness = await mountLiveTable({ awaitConnection: "reconnecting" });

    await vi.waitFor(() => expect(shared.subscriptionRun).toHaveBeenCalledTimes(1));

    expect(harness.liveTable.allowedActions.value).toEqual([]);
    subscriptionGate.resolve();
    harness.app.unmount();
  });

  it("keeps the table mounted while a suspended session withdraws every game action", async () => {
    const pausedAt = "2026-07-26T02:00:00Z";
    shared.roomStore.remoteRoom = {
      ...createRemoteRoom(),
      activePause: {
        pauseId: "pause-1",
        sessionId: "session-1",
        source: "PAUSE_SOURCE_HOST",
        pausedByUserId: "user-1",
        pausedAt,
      },
    };
    shared.api.getProjection.mockResolvedValueOnce({
      ...projectionResponse(baseView(), 1),
      session: {
        sessionId: "session-1",
        roomId: "room-1",
        gameId: "three-rounds",
        stateVersion: "1",
        ownershipEpoch: "7",
        status: "GAME_SESSION_STATUS_SUSPENDED",
        suspendedAt: pausedAt,
      },
    });
    const harness = await mountLiveTable();

    expect(harness.liveTable.isPaused.value).toBe(true);
    expect(harness.liveTable.lifecycle.value.ownershipEpoch).toBe("7");
    expect(harness.liveTable.suspendedAt.value).toBe(pausedAt);
    expect(harness.liveTable.allowedActions.value).toEqual([]);
    await harness.liveTable.submitLiveAction(actionInput());
    expect(shared.api.action).not.toHaveBeenCalled();
    expect(shared.router.replace).not.toHaveBeenCalled();

    harness.app.unmount();
  });

  it("accepts a newer active session lifecycle while the room pause snapshot is stale", async () => {
    shared.roomStore.remoteRoom = {
      ...createRemoteRoom(),
      activePause: {
        pauseId: "pause-1",
        sessionId: "session-1",
        source: "PAUSE_SOURCE_HOST",
        pausedByUserId: "user-1",
        pausedAt: "2026-07-26T02:00:00Z",
      },
    };
    shared.api.getProjection.mockResolvedValueOnce({
      ...projectionResponse(baseView(), 1),
      session: {
        sessionId: "session-1",
        roomId: "room-1",
        gameId: "three-rounds",
        stateVersion: "1",
        ownershipEpoch: "8",
        status: "GAME_SESSION_STATUS_ACTIVE",
      },
    });
    const harness = await mountLiveTable();

    expect(harness.liveTable.lifecycle.value.known).toBe(true);
    expect(harness.liveTable.lifecycle.value.ownershipEpoch).toBe("8");
    expect(harness.liveTable.isPaused.value).toBe(false);
    expect(harness.liveTable.suspendedAt.value).toBeNull();
    harness.app.unmount();
  });

  it("keeps the table reconnecting instead of faking online after a forbidden bootstrap retry", async () => {
    const subscriptionGate = deferred<void>();
    shared.api.getProjection.mockRejectedValueOnce(new ApiError(404, "projection_missing", "missing"));
    shared.subscriptionRun.mockImplementation(() => subscriptionGate.promise);
    const harness = await mountLiveTable({ awaitConnection: "reconnecting" });

    harness.liveTable.retry();
    await settle();

    expect(harness.context.value.connection).toBe("reconnecting");
    subscriptionGate.resolve();
    harness.app.unmount();
  });

  it("retries by reopening the real live subscription after an unauthorized bootstrap", async () => {
    const firstSubscription = deferred<void>();
    const secondSubscription = deferred<void>();
    shared.api.getProjection.mockRejectedValueOnce(new ApiError(401, "projection_unauthorized", "unauthorized"));
    shared.subscriptionRun
      .mockImplementationOnce(() => firstSubscription.promise)
      .mockImplementationOnce(() => secondSubscription.promise);
    const harness = await mountLiveTable({ awaitConnection: "reconnecting" });

    await vi.waitFor(() => expect(shared.subscriptionRun).toHaveBeenCalledTimes(1));
    harness.liveTable.retry();
    await vi.waitFor(() => expect(shared.subscriptionRun).toHaveBeenCalledTimes(2));

    firstSubscription.resolve();
    secondSubscription.resolve();
    harness.app.unmount();
  });

  it("treats a refreshed already-confirmed view as success without replaying the action", async () => {
    shared.api.getProjection
      .mockResolvedValueOnce(projectionResponse(baseView(), 1))
      .mockResolvedValueOnce(projectionResponse(baseView({ confirmed: true, moduleActions: [] }), 2, []));
    shared.api.action.mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"));
    const harness = await mountLiveTable({
      resolveSimultaneousConflict: (latestView) => (latestView.confirmed ? "confirmed" : "retry"),
    });

    await harness.liveTable.submitLiveAction(actionInput());

    expect(shared.api.action).toHaveBeenCalledTimes(1);
    expect(shared.api.getProjection).toHaveBeenCalledTimes(2);
    expect(harness.view.value.confirmed).toBe(true);
    expect(harness.context.value.connection).toBe("online");
    harness.app.unmount();
  });

  it("replays a still-valid simultaneous action with a fresh action id and latest state version", async () => {
    const command = createEnvelope();
    shared.api.getProjection
      .mockResolvedValueOnce(projectionResponse(baseView(), 1))
      .mockResolvedValueOnce(projectionResponse(baseView(), 2));
    shared.api.action
      .mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"))
      .mockResolvedValueOnce(projectionResponse(baseView({ confirmed: true, moduleActions: [] }), 3, []));
    const harness = await mountLiveTable({
      resolveSimultaneousConflict: (latestView) => (latestView.phase === "stage-1" && !latestView.confirmed ? "retry" : "abort"),
    });

    await harness.liveTable.submitLiveAction(actionInput(command));

    expect(shared.api.action).toHaveBeenCalledTimes(2);
    expect(shared.api.action.mock.calls[0]?.[3]).toBe(1);
    expect(shared.api.action.mock.calls[1]?.[3]).toBe(2);
    expect(shared.api.action.mock.calls[0]?.[4]).toBe("00000000-0000-4000-8000-000000000001");
    expect(shared.api.action.mock.calls[1]?.[4]).toBe("00000000-0000-4000-8000-000000000002");
    expect(shared.api.action.mock.calls[0]?.[5]).toBe(command);
    expect(shared.api.action.mock.calls[1]?.[5]).toBe(command);
    expect(harness.view.value.confirmed).toBe(true);
    harness.app.unmount();
  });

  it("aborts replay when the refreshed view no longer permits the original decision", async () => {
    shared.api.getProjection
      .mockResolvedValueOnce(projectionResponse(baseView(), 1))
      .mockResolvedValueOnce(projectionResponse(baseView({ phase: "stage-2", retryable: false, moduleActions: [] }), 2, []));
    shared.api.action.mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"));
    const harness = await mountLiveTable({
      resolveSimultaneousConflict: (latestView) => (latestView.phase === "stage-1" && latestView.retryable ? "retry" : "abort"),
    });

    await harness.liveTable.submitLiveAction(actionInput());

    expect(shared.api.action).toHaveBeenCalledTimes(1);
    expect(harness.view.value.phase).toBe("stage-2");
    expect(harness.context.value.connection).toBe("online");
    harness.app.unmount();
  });

  it("refreshes and stops on a version conflict when no game-specific resolver is installed", async () => {
    shared.api.getProjection
      .mockResolvedValueOnce(projectionResponse(baseView(), 1))
      .mockResolvedValueOnce(projectionResponse(baseView({ phase: "stage-2", moduleActions: [] }), 2, []));
    shared.api.action.mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"));
    const harness = await mountLiveTable();

    await harness.liveTable.submitLiveAction(actionInput());

    expect(shared.api.action).toHaveBeenCalledTimes(1);
    expect(shared.api.getProjection).toHaveBeenCalledTimes(2);
    expect(harness.view.value.phase).toBe("stage-2");
    expect(harness.context.value.connection).toBe("online");
    harness.app.unmount();
  });

  it("stops after two recovery replays even if the same action keeps conflicting", async () => {
    shared.api.getProjection
      .mockResolvedValueOnce(projectionResponse(baseView(), 1))
      .mockResolvedValueOnce(projectionResponse(baseView(), 2))
      .mockResolvedValueOnce(projectionResponse(baseView(), 3))
      .mockResolvedValueOnce(projectionResponse(baseView(), 4));
    shared.api.action
      .mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"))
      .mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"))
      .mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"));
    const harness = await mountLiveTable({
      resolveSimultaneousConflict: () => "retry",
    });

    await harness.liveTable.submitLiveAction(actionInput());

    expect(shared.api.action).toHaveBeenCalledTimes(3);
    expect(shared.api.action.mock.calls.map((call) => call[4])).toEqual([
      "00000000-0000-4000-8000-000000000001",
      "00000000-0000-4000-8000-000000000002",
      "00000000-0000-4000-8000-000000000003",
    ]);
    expect(harness.view.value.phase).toBe("stage-1");
    expect(harness.context.value.connection).toBe("online");
    harness.app.unmount();
  });

  it("cancels refreshed conflict handling when the acting identity changes before the projection returns", async () => {
    const refresh = deferred<ReturnType<typeof projectionResponse>>();
    shared.api.getProjection
      .mockResolvedValueOnce(projectionResponse(baseView(), 1))
      .mockImplementationOnce(() => refresh.promise);
    shared.api.action.mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"));
    const harness = await mountLiveTable({
      resolveSimultaneousConflict: () => "retry",
    });

    const submitting = harness.liveTable.submitLiveAction(actionInput());
    await vi.waitFor(() => expect(shared.api.getProjection).toHaveBeenCalledTimes(2));
    shared.roomStore.userId = "user-2";
    refresh.resolve(projectionResponse(baseView({ confirmed: true }), 2));
    await submitting;
    await settle();

    expect(shared.api.action).toHaveBeenCalledTimes(1);
    expect(harness.view.value.confirmed).toBe(false);
    harness.app.unmount();
  });

  it.each([
    ["identity", () => { shared.roomStore.userId = "user-2"; }],
    ["room", () => { shared.roomStore.roomId = "room-2"; }],
    ["session", () => { shared.roomStore.sessionId = "session-2"; }],
  ])("ignores a failed conflict refresh after the %s fence changes", async (_scope, invalidateFence) => {
    const refresh = deferred<ReturnType<typeof projectionResponse>>();
    shared.api.getProjection
      .mockResolvedValueOnce(projectionResponse(baseView(), 1))
      .mockImplementationOnce(() => refresh.promise);
    shared.api.action.mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"));
    const harness = await mountLiveTable({
      resolveSimultaneousConflict: () => "retry",
    });

    const submitting = harness.liveTable.submitLiveAction(actionInput());
    await vi.waitFor(() => expect(shared.api.getProjection).toHaveBeenCalledTimes(2));
    invalidateFence();
    refresh.reject(new ApiError(503, "projection_unavailable", "unavailable"));
    await submitting;
    await settle();

    expect(shared.api.action).toHaveBeenCalledTimes(1);
    expect(harness.context.value.connection).toBe("online");
    harness.app.unmount();
  });

  it("cancels refreshed conflict handling after the table unmounts", async () => {
    const refresh = deferred<ReturnType<typeof projectionResponse>>();
    shared.api.getProjection
      .mockResolvedValueOnce(projectionResponse(baseView(), 1))
      .mockImplementationOnce(() => refresh.promise);
    shared.api.action.mockRejectedValueOnce(new ApiError(409, "game.state.version_conflict", "conflict"));
    const harness = await mountLiveTable({
      resolveSimultaneousConflict: () => "retry",
    });

    const submitting = harness.liveTable.submitLiveAction(actionInput());
    await vi.waitFor(() => expect(shared.api.getProjection).toHaveBeenCalledTimes(2));
    harness.app.unmount();
    refresh.resolve(projectionResponse(baseView({ confirmed: true }), 2));
    await submitting;
    await settle();

    expect(shared.api.action).toHaveBeenCalledTimes(1);
    expect(shared.router.replace).not.toHaveBeenCalled();
  });
});
