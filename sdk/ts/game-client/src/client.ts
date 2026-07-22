import { actionPending, actionUnavailable, cursorGap, DispatchFailure, GameClientError, invalidProjection } from "./errors";
import type {
  ActionCommand,
  ActionInput,
  ActionReceipt,
  DispatchPort,
  GameClientState,
  GameDelta,
  GameProjection,
  GameUpdate,
  PendingAction,
  ProjectionReducer,
  RetryAction,
  StateListener,
  SubscriptionCursor,
} from "./types";
import { validateAllowedActions, validateDelta, validateProjection } from "./validate";

export interface GameClientOptions<TView> {
  readonly reducer: ProjectionReducer<TView>;
  readonly dispatch: DispatchPort;
  readonly createActionId?: () => string;
  readonly now?: () => number;
}

interface PendingEntry {
  readonly pending: PendingAction;
  readonly controller: AbortController;
}

// GameClient owns only viewer-safe state, action idempotency, and connection metadata.
export class GameClient<TView> {
  readonly #reducer: ProjectionReducer<TView>;
  readonly #dispatch: DispatchPort;
  readonly #createActionId: () => string;
  readonly #now: () => number;
  readonly #listeners = new Set<StateListener<TView>>();
  readonly #pending = new Map<string, PendingEntry>();
  readonly #retries = new Map<string, RetryAction>();
  #state: GameClientState<TView>;

  public constructor(options: GameClientOptions<TView>) {
    this.#reducer = options.reducer;
    this.#dispatch = options.dispatch;
    this.#createActionId = options.createActionId ?? (() => globalThis.crypto.randomUUID());
    this.#now = options.now ?? (() => Date.now());
    this.#state = {
      connection: "idle",
      sessionId: null,
      stateVersion: 0,
      viewerRole: null,
      view: null,
      allowedActions: [],
      pending: [],
      retries: [],
      errorCode: null,
    };
  }

  public snapshot(): GameClientState<TView> {
    return this.#state;
  }

  public cursor(): SubscriptionCursor | null {
    if (this.#state.sessionId === null || this.#state.viewerRole === null || this.#state.stateVersion === 0) {
      return null;
    }
    return {
      sessionId: this.#state.sessionId,
      stateVersion: this.#state.stateVersion,
      viewerRole: this.#state.viewerRole,
    };
  }

  public subscribe(listener: StateListener<TView>): () => void {
    this.#listeners.add(listener);
    listener(this.#state);
    return () => this.#listeners.delete(listener);
  }

  public accept(update: GameUpdate): void {
    if (update.kind === "projection") {
      this.#acceptProjection(update);
      return;
    }
    this.#acceptDelta(update);
  }

  public async dispatch(input: ActionInput): Promise<ActionReceipt> {
    if (!this.#state.allowedActions.includes(input.action)) {
      throw actionUnavailable(input.action);
    }
    if (this.#pending.has(input.action)) {
      throw actionPending(input.action);
    }
    if (this.#state.sessionId === null || this.#state.stateVersion === 0) {
      throw invalidProjection("Cannot dispatch without an accepted projection");
    }
    const command: ActionCommand = {
      ...input,
      sessionId: this.#state.sessionId,
      actionId: this.#createActionId(),
      expectedStateVersion: this.#state.stateVersion,
    };
    this.#retries.delete(input.action);
    return this.#perform(command, 0);
  }

  public async retry(action: string): Promise<ActionReceipt> {
    const retry = this.#retries.get(action);
    if (retry === undefined) {
      throw new GameClientError("retry_unavailable", `No retry is available for action: ${action}`);
    }
    if (this.#pending.has(action)) {
      throw actionPending(action);
    }
    return this.#perform(retry.command, retry.attempts);
  }

  public cancel(action?: string): void {
    if (action !== undefined) {
      this.#pending.get(action)?.controller.abort();
      return;
    }
    this.#pending.forEach((entry) => entry.controller.abort());
  }

  public markReconnecting(errorCode: string | null = null): void {
    this.#replaceState({ connection: "reconnecting", errorCode });
  }

  public markOnline(): void {
    this.#replaceState({ connection: "online", errorCode: null });
  }

  public markDraining(): void {
    this.#replaceState({ connection: "draining", errorCode: null });
  }

  public fail(errorCode: string): void {
    this.#replaceState({ connection: "failed", errorCode });
  }

  public dispose(): void {
    this.cancel();
    this.#listeners.clear();
  }

  #acceptProjection(projection: GameProjection): void {
    validateProjection(projection);
    if (this.#state.sessionId !== null && this.#state.sessionId !== projection.sessionId) {
      throw invalidProjection("Projection belongs to another session");
    }
    // An HTTP action projection can overtake an already queued WebSocket frame; never roll viewer state backward.
    if (this.#state.sessionId === projection.sessionId && projection.stateVersion < this.#state.stateVersion) {
      return;
    }
    const view = this.#reducer.fromProjection(projection);
    this.#state = {
      ...this.#state,
      connection: "online",
      sessionId: projection.sessionId,
      stateVersion: projection.stateVersion,
      viewerRole: projection.viewerRole,
      view,
      allowedActions: [...projection.allowedActions],
      errorCode: null,
    };
    this.#emit();
  }

  #acceptDelta(delta: GameDelta): void {
    validateDelta(delta);
    // A projection returned by the action API may already include this exact committed delta.
    if (
      this.#state.sessionId === delta.sessionId &&
      this.#state.viewerRole === delta.viewerRole &&
      delta.toStateVersion <= this.#state.stateVersion
    ) {
      return;
    }
    if (
      this.#state.sessionId === null ||
      this.#state.viewerRole === null ||
      this.#state.view === null ||
      delta.sessionId !== this.#state.sessionId ||
      delta.viewerRole !== this.#state.viewerRole ||
      delta.fromStateVersion !== this.#state.stateVersion
    ) {
      this.markReconnecting("cursor_gap");
      throw cursorGap("Delta does not continue the accepted viewer cursor");
    }
    const reduced = this.#reducer.applyDelta(this.#state.view, delta);
    const allowedActions = reduced.allowedActions ?? this.#state.allowedActions;
    validateAllowedActions(allowedActions);
    this.#state = {
      ...this.#state,
      connection: "online",
      stateVersion: delta.toStateVersion,
      view: reduced.view,
      allowedActions: [...allowedActions],
      errorCode: null,
    };
    this.#emit();
  }

  async #perform(command: ActionCommand, attempts: number): Promise<ActionReceipt> {
    const controller = new AbortController();
    this.#pending.set(command.action, {
      pending: { command, startedAt: this.#now() },
      controller,
    });
    this.#syncActionState();
    try {
      const receipt = await this.#dispatch(command, { signal: controller.signal });
      if (receipt.sessionId !== command.sessionId || receipt.actionId !== command.actionId || receipt.stateVersion <= 0) {
        throw new DispatchFailure("invalid_receipt", "Dispatch returned a mismatched receipt", false);
      }
      this.#retries.delete(command.action);
      return receipt;
    } catch (error) {
      const failure = normalizeDispatchFailure(error);
      if (failure.retryable && !controller.signal.aborted) {
        this.#retries.set(command.action, {
          command,
          attempts: attempts + 1,
          errorCode: failure.code,
        });
      }
      throw failure;
    } finally {
      this.#pending.delete(command.action);
      this.#syncActionState();
    }
  }

  #syncActionState(): void {
    this.#state = {
      ...this.#state,
      pending: [...this.#pending.values()].map((entry) => entry.pending),
      retries: [...this.#retries.values()],
    };
    this.#emit();
  }

  #replaceState(patch: Partial<GameClientState<TView>>): void {
    this.#state = { ...this.#state, ...patch };
    this.#emit();
  }

  #emit(): void {
    this.#listeners.forEach((listener) => listener(this.#state));
  }
}

const normalizeDispatchFailure = (error: unknown): DispatchFailure => {
  if (error instanceof DispatchFailure) {
    return error;
  }
  if (error instanceof DOMException && error.name === "AbortError") {
    return new DispatchFailure("action_cancelled", "Action was cancelled", false, { cause: error });
  }
  return new DispatchFailure("dispatch_failed", "Action dispatch failed", true, { cause: error });
};
