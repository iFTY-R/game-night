export class GameClientError extends Error {
  public constructor(
    public readonly code: string,
    message: string,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "GameClientError";
  }
}

export class DispatchFailure extends GameClientError {
  public constructor(code: string, message: string, public readonly retryable: boolean, options?: ErrorOptions) {
    super(code, message, options);
    this.name = "DispatchFailure";
  }
}

export type SubscriptionFailurePhase = "reconnecting" | "draining";

/** Describes whether a broken subscription should stop, retry normally, or wait for service draining. */
export class SubscriptionFailure extends GameClientError {
  public constructor(
    code: string,
    message: string,
    public readonly retryable = true,
    public readonly phase: SubscriptionFailurePhase = "reconnecting",
    public readonly retryAfterMs: number | null = null,
    options?: ErrorOptions,
  ) {
    super(code, message, options);
    this.name = "SubscriptionFailure";
    if (retryAfterMs !== null && (!Number.isSafeInteger(retryAfterMs) || retryAfterMs < 0)) {
      throw new TypeError("Subscription retry delay is invalid");
    }
  }
}

export const invalidProjection = (message: string): GameClientError =>
  new GameClientError("invalid_projection", message);

export const cursorGap = (message: string): GameClientError => new GameClientError("cursor_gap", message);

export const actionUnavailable = (action: string): GameClientError =>
  new GameClientError("action_unavailable", `Action is not currently allowed: ${action}`);

export const actionPending = (action: string): GameClientError =>
  new GameClientError("action_pending", `Action is already pending: ${action}`);
