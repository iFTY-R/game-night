import type { VersionTuple } from "@game-night/game-client";

export const THREE_ROUNDS_GAME_ID = "three-rounds";
export const THREE_ROUNDS_SCHEMA_VERSION = 1;
export const THREE_ROUNDS_VIEW_MESSAGE = "session.view";
export const THREE_ROUNDS_DELTA_MESSAGE = "view.delta";
export const THREE_ROUNDS_REPLAY_MESSAGE = "session.replay";
export const THREE_ROUNDS_SUBMIT_SELECTION_ACTION = "round.submit_selection";
export const SESSION_FINISH_ACTION = "session.finish";

export const THREE_ROUNDS_VERSION: VersionTuple = {
  engine: "1.0.0",
  protocol: "1.0.0",
  client: "1.0.0",
};
