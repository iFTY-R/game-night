import type { ConnectionState } from "@game-night/game-ui-kit";
import type { ActionInput } from "@game-night/game-client";

import type { BidMode, Replay, View } from "./generated/game/liars_dice/v1/liars_dice_pb";

export type LiarsDiceView = View;
export type LiarsDiceReplay = Replay;
export type LiarsDiceActionInput = ActionInput;

export interface BidDraft {
  readonly quantity: number;
  readonly face: number;
  readonly mode: BidMode;
}

export interface BidValidation {
  readonly valid: boolean;
  readonly code: string;
  readonly message: string;
  readonly risky: boolean;
}

export interface PlayerPresentation {
  readonly userId: string;
  readonly displayName: string;
  readonly avatarText?: string;
  readonly connected: boolean;
  readonly host?: boolean;
  readonly seatIndex?: number;
}

export interface LiarsDiceTableContext {
  readonly roomCode: string;
  readonly selfUserId: string;
  readonly viewerRole: "player" | "spectator" | "replay";
  readonly connection: ConnectionState;
  readonly players: readonly PlayerPresentation[];
}

export interface LiarsDiceReplayContext {
  readonly roomCode: string;
  readonly players: readonly PlayerPresentation[];
}
