import type { ActionInput } from "@game-night/game-client";
import type { ConnectionState } from "@game-night/game-ui-kit";

import type { Replay, View } from "./generated/game/three_rounds/v1/three_rounds_pb";

export type ThreeRoundsView = View;
export type ThreeRoundsReplay = Replay;
export type ThreeRoundsActionInput = ActionInput;
export type ThreeRoundsFixtureState =
  | "active"
  | "pending"
  | "round-two"
  | "round-three"
  | "final"
  | "spectator"
  | "reconnecting"
  | "finished"
  | "replay";

export type ThreeRoundsConflictResolution = "confirmed" | "retry" | "abort";

export interface ThreeRoundsPlayerPresentation {
  readonly userId: string;
  readonly displayName: string;
  readonly avatarText?: string;
  readonly connected: boolean;
  readonly host?: boolean;
  readonly seatIndex?: number;
}

export interface ThreeRoundsTableContext {
  readonly roomCode: string;
  readonly selfUserId: string;
  readonly viewerRole: "player" | "spectator" | "replay";
  readonly connection: ConnectionState;
  readonly players: readonly ThreeRoundsPlayerPresentation[];
}

export interface ThreeRoundsReplayContext {
  readonly roomCode: string;
  readonly players: readonly ThreeRoundsPlayerPresentation[];
}
