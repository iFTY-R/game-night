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

export const invalidProjection = (message: string): GameClientError =>
  new GameClientError("invalid_projection", message);

export const cursorGap = (message: string): GameClientError => new GameClientError("cursor_gap", message);

export const actionUnavailable = (action: string): GameClientError =>
  new GameClientError("action_unavailable", `Action is not currently allowed: ${action}`);

export const actionPending = (action: string): GameClientError =>
  new GameClientError("action_pending", `Action is already pending: ${action}`);
