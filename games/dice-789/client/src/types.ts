import type { ActionInput } from "@game-night/game-client";
import type { ConnectionState } from "@game-night/game-ui-kit";

import type { Replay, View } from "./generated/game/dice789/v1/dice_789_pb";

export type Dice789View = View;
export type Dice789Replay = Replay;
export type Dice789ActionInput = ActionInput;

export interface Dice789PlayerPresentation {
  readonly userId: string;
  readonly displayName: string;
  readonly avatarText?: string;
  readonly connected: boolean;
  readonly host?: boolean;
  readonly seatIndex?: number;
}

export interface Dice789TableContext {
  readonly roomCode: string;
  readonly selfUserId: string;
  readonly viewerRole: "player" | "spectator" | "replay";
  readonly connection: ConnectionState;
  readonly players: readonly Dice789PlayerPresentation[];
}

export interface Dice789ReplayContext {
  readonly roomCode: string;
  readonly players: readonly Dice789PlayerPresentation[];
}
