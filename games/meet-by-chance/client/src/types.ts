import type { ActionInput } from "@game-night/game-client";
import type { ConnectionState } from "@game-night/game-ui-kit";

import type { Replay, View } from "./generated/game/meet_by_chance/v1/meet_by_chance_pb";

export type MeetByChanceView = View;
export type MeetByChanceReplay = Replay;
export type MeetByChanceActionInput = ActionInput;

export interface MeetByChancePlayerPresentation {
  readonly userId: string;
  readonly displayName: string;
  readonly avatarText?: string;
  readonly connected: boolean;
  readonly host?: boolean;
  readonly seatIndex?: number;
}

export interface MeetByChanceTableContext {
  readonly roomCode: string;
  readonly selfUserId: string;
  readonly viewerRole: "player" | "spectator" | "replay";
  readonly connection: ConnectionState;
  readonly players: readonly MeetByChancePlayerPresentation[];
}

export interface MeetByChanceReplayContext {
  readonly roomCode: string;
  readonly players: readonly MeetByChancePlayerPresentation[];
}
