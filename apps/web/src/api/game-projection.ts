import type { GameProjection, ViewerRole } from "@game-night/game-client";

import type { GameProjectionWire } from "./client";

/** Converts the Connect JSON projection into the shared versioned client contract. */
export const gameProjectionFromConnect = (wire: GameProjectionWire | undefined): GameProjection => {
  if (wire?.view?.version === undefined) {
    throw new Error("game_projection_missing");
  }
  return {
    kind: "projection",
    sessionId: wire.sessionId,
    stateVersion: safeStateVersion(wire.stateVersion),
    viewerRole: viewerRole(wire.viewerKind),
    view: {
      gameId: wire.view.gameId,
      version: { ...wire.view.version },
      schemaVersion: wire.view.schemaVersion,
      messageType: wire.view.messageType,
      payload: base64Bytes(wire.view.payload),
    },
    allowedActions: [...(wire.allowedActions ?? [])],
  };
};

const base64Bytes = (encoded: string): Uint8Array => {
  const binary = atob(encoded);
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
};

const safeStateVersion = (wire: string): number => {
  if (!/^[1-9]\d*$/.test(wire)) {
    throw new Error("game_state_version_invalid");
  }
  const version = Number(wire);
  if (!Number.isSafeInteger(version)) {
    throw new Error("game_state_version_unsupported");
  }
  return version;
};

const viewerRole = (wire: string): ViewerRole => {
  if (wire === "VIEWER_KIND_PLAYER") return "player";
  if (wire === "VIEWER_KIND_SPECTATOR") return "spectator";
  if (wire === "VIEWER_KIND_REPLAY") return "replay";
  throw new Error("game_viewer_kind_invalid");
};
