export type ViewerRole = "player" | "spectator" | "replay";

export interface VersionTuple {
  readonly engine: string;
  readonly protocol: string;
  readonly client: string;
}

// GameEnvelope is an opaque client-visible payload; only the matching versioned game client may decode it.
export interface GameEnvelope {
  readonly gameId: string;
  readonly version: VersionTuple;
  readonly schemaVersion: number;
  readonly messageType: string;
  readonly payload: Uint8Array;
}

export type AllowedActions = readonly string[];

export interface GameProjection {
  readonly kind: "projection";
  readonly sessionId: string;
  readonly stateVersion: number;
  readonly viewerRole: ViewerRole;
  readonly view: GameEnvelope;
  readonly allowedActions: AllowedActions;
}

export interface GameDelta {
  readonly kind: "delta";
  readonly sessionId: string;
  readonly fromStateVersion: number;
  readonly toStateVersion: number;
  readonly viewerRole: ViewerRole;
  readonly messages: readonly GameEnvelope[];
}

export type GameUpdate = GameProjection | GameDelta;

export interface ReducedDelta<TView> {
  readonly view: TView;
  readonly allowedActions?: AllowedActions;
}

// ProjectionReducer belongs to one versioned game client and never receives authoritative state or raw events.
export interface ProjectionReducer<TView> {
  fromProjection(projection: GameProjection): TView;
  /** Returns the module-owned action prefix so transport-only deltas cannot erase platform commands. */
  moduleActions(view: TView): AllowedActions;
  applyDelta(current: TView, delta: GameDelta): ReducedDelta<TView>;
}

export interface ActionInput {
  readonly action: string;
  readonly message: GameEnvelope;
}

export interface ActionCommand extends ActionInput {
  readonly sessionId: string;
  readonly actionId: string;
  readonly expectedStateVersion: number;
}

export interface ActionReceipt {
  readonly sessionId: string;
  readonly actionId: string;
  readonly stateVersion: number;
  readonly resultCode: string;
  readonly replayed: boolean;
}

export interface DispatchContext {
  readonly signal: AbortSignal;
}

export type DispatchPort = (command: ActionCommand, context: DispatchContext) => Promise<ActionReceipt>;

export interface PendingAction {
  readonly command: ActionCommand;
  readonly startedAt: number;
}

export interface RetryAction {
  readonly command: ActionCommand;
  readonly attempts: number;
  readonly errorCode: string;
}

export type ConnectionPhase = "idle" | "online" | "reconnecting" | "draining" | "failed";

export interface GameClientState<TView> {
  readonly connection: ConnectionPhase;
  readonly sessionId: string | null;
  readonly stateVersion: number;
  readonly viewerRole: ViewerRole | null;
  readonly view: TView | null;
  readonly allowedActions: AllowedActions;
  readonly pending: readonly PendingAction[];
  readonly retries: readonly RetryAction[];
  readonly errorCode: string | null;
}

export interface SubscriptionCursor {
  readonly sessionId: string;
  readonly stateVersion: number;
  readonly viewerRole: ViewerRole;
}

export interface ReconnectAdapter {
  connect(cursor: SubscriptionCursor | null, signal: AbortSignal): Promise<AsyncIterable<GameUpdate>>;
}

export interface ReconnectPolicy {
  readonly initialDelayMs: number;
  readonly maximumDelayMs: number;
}

export type StateListener<TView> = (state: GameClientState<TView>) => void;
