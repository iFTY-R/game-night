import type { GameProjection } from "@game-night/game-client";

export const fixedViewports = {
  portrait: { width: 390, height: 844 },
  landscape: { width: 844, height: 390 },
  androidSmall: { width: 360, height: 740 },
} as const;

export const fixtureSeats = [
  { seatIndex: 0, userId: "user-0", displayName: "阿青", avatarText: "青", connected: true, active: false },
  { seatIndex: 1, userId: "user-1", displayName: "小满", avatarText: "满", connected: true, active: true },
  { seatIndex: 2, userId: "user-2", displayName: "南风", avatarText: "南", connected: true, active: false },
  { seatIndex: 3, userId: "user-3", displayName: "北岸", avatarText: "北", connected: false, active: false },
] as const;

export const fixtureProjection = (stateVersion = 12): GameProjection => ({
  kind: "projection",
  sessionId: "fixture-session",
  stateVersion,
  viewerRole: "player",
  view: {
    gameId: "fixture-game",
    version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
    schemaVersion: 1,
    messageType: "viewer.state",
    payload: new Uint8Array([stateVersion]),
  },
  allowedActions: ["round.roll"],
});
