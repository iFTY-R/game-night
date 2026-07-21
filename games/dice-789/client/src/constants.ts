import type { VersionTuple } from "@game-night/game-client";

export const DICE_789_GAME_ID = "dice-789";
export const DICE_789_SCHEMA_VERSION = 1;
export const DICE_789_VIEW_MESSAGE = "session.view";
export const DICE_789_DELTA_MESSAGE = "view.delta";
export const DICE_789_REPLAY_MESSAGE = "session.replay";

export const DICE_789_ROLL_ACTION = "turn.roll";
export const DICE_789_CONFIRM_ACTION = "turn.confirm_landed";
export const DICE_789_ADD_ACTION = "pot.add";
export const DICE_789_TARGET_ACTION = "turn.choose_target";
export const DICE_789_REROLL_ACTION = "turn.reroll";
export const DICE_789_PASS_ACTION = "turn.pass";
export const DICE_789_DROPPED_ACTION = "turn.report_dropped";
export const SESSION_FINISH_ACTION = "session.finish";

export const DICE_789_VERSION: VersionTuple = { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" };
