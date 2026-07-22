import { describe, expect, it } from "vitest";

import type { RoomSnapshot } from "../src/api/client";
import { isActiveRoomSession } from "../src/composables/use-live-game-table";

const roomSnapshot = (status: string, activeSessionId: string): RoomSnapshot => ({
  roomId: "room-1",
  roomCode: "TABLE1",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status,
  hostUserId: "host-1",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_CLOSED",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [],
  activeSessionId,
  activeGameId: "dice-789",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "1", membershipVersion: "1" },
});

describe("live game route lifecycle", () => {
  it("keeps only the exact active playing session on the game route", () => {
    expect(isActiveRoomSession(roomSnapshot("ROOM_STATUS_PLAYING", "session-1"), "session-1")).toBe(true);
    expect(isActiveRoomSession(roomSnapshot("ROOM_STATUS_PLAYING", "session-2"), "session-1")).toBe(false);
    expect(isActiveRoomSession(roomSnapshot("ROOM_STATUS_POST_GAME", ""), "session-1")).toBe(false);
  });
});
