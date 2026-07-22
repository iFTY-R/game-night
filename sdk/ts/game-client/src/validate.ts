import { invalidProjection } from "./errors";
import type { AllowedActions, GameDelta, GameEnvelope, GameProjection, ViewerRole } from "./types";

const identifierPattern = /^[a-z0-9]+(?:[._-][a-z0-9]+)*$/;
const versionPattern = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;

export const validateEnvelope = (value: GameEnvelope): void => {
  if (
    !identifierPattern.test(value.gameId) ||
    !identifierPattern.test(value.messageType) ||
    !versionPattern.test(value.version.engine) ||
    !versionPattern.test(value.version.protocol) ||
    !versionPattern.test(value.version.client) ||
    !Number.isSafeInteger(value.schemaVersion) ||
    value.schemaVersion <= 0 ||
    !(value.payload instanceof Uint8Array)
  ) {
    throw invalidProjection("Game envelope is malformed");
  }
};

export const validateViewerRole = (role: ViewerRole): void => {
  if (role !== "player" && role !== "spectator" && role !== "replay") {
    throw invalidProjection("Viewer role is not supported");
  }
};

export const validateAllowedActions = (actions: AllowedActions): void => {
  if (actions.length > 128 || new Set(actions).size !== actions.length || actions.some((action) => !identifierPattern.test(action))) {
    throw invalidProjection("Allowed actions are malformed");
  }
};

/**
 * Confirms that platform authorization preserves every module-owned action in
 * order while allowing platform-only actions to be appended to the projection.
 */
export const hasOrderedActionPrefix = (moduleActions: AllowedActions, projectionActions: AllowedActions): boolean => (
  projectionActions.length >= moduleActions.length
  && moduleActions.every((action, index) => projectionActions[index] === action)
);

export const validateProjection = (projection: GameProjection): void => {
  if (!projection.sessionId || !Number.isSafeInteger(projection.stateVersion) || projection.stateVersion <= 0) {
    throw invalidProjection("Projection cursor is malformed");
  }
  validateViewerRole(projection.viewerRole);
  validateEnvelope(projection.view);
  validateAllowedActions(projection.allowedActions);
};

export const validateDelta = (delta: GameDelta): void => {
  if (
    !delta.sessionId ||
    !Number.isSafeInteger(delta.fromStateVersion) ||
    !Number.isSafeInteger(delta.toStateVersion) ||
    delta.fromStateVersion <= 0 ||
    delta.toStateVersion <= delta.fromStateVersion ||
    delta.messages.length === 0 ||
    delta.messages.length > 1024
  ) {
    throw invalidProjection("Delta cursor is malformed");
  }
  validateViewerRole(delta.viewerRole);
  delta.messages.forEach(validateEnvelope);
};
