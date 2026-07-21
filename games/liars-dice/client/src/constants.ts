import type { VersionTuple } from "@game-night/game-client";

export const LIARS_DICE_GAME_ID = "liars-dice";
export const LIARS_DICE_SCHEMA_VERSION = 1;
export const LIARS_DICE_VIEW_MESSAGE = "session.view";
export const LIARS_DICE_DELTA_MESSAGE = "view.delta";
export const LIARS_DICE_REPLAY_MESSAGE = "session.replay";
export const LIARS_DICE_BID_ACTION = "round.bid";
export const LIARS_DICE_OPEN_ACTION = "round.open";
export const SESSION_FINISH_ACTION = "session.finish";

export const LIARS_DICE_VERSION: VersionTuple = {
  engine: "1.0.0",
  protocol: "1.0.0",
  client: "1.0.0",
};
