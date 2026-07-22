import { toBinary } from "@bufbuild/protobuf";
import { expect, test, type Page } from "@playwright/test";

import { DICE_789_GAME_ID, DICE_789_REPLAY_MESSAGE, DICE_789_SCHEMA_VERSION, DICE_789_VERSION } from "../../../games/dice-789/client/src/constants";
import { dice789ReplayFixture } from "../../../games/dice-789/client/src/fixture";
import { ReplaySchema as Dice789ReplaySchema } from "../../../games/dice-789/client/src/generated/game/dice789/v1/dice_789_pb";
import { LIARS_DICE_GAME_ID, LIARS_DICE_REPLAY_MESSAGE, LIARS_DICE_SCHEMA_VERSION, LIARS_DICE_VERSION } from "../../../games/liars-dice/client/src/constants";
import { liarsDiceReplayFixture } from "../../../games/liars-dice/client/src/fixture";
import { ReplaySchema as LiarsDiceReplaySchema } from "../../../games/liars-dice/client/src/generated/game/liars_dice/v1/liars_dice_pb";
import { MEET_BY_CHANCE_GAME_ID, MEET_BY_CHANCE_REPLAY_MESSAGE, MEET_BY_CHANCE_SCHEMA_VERSION, MEET_BY_CHANCE_VERSION } from "../../../games/meet-by-chance/client/src/constants";
import { meetByChanceReplayFixture } from "../../../games/meet-by-chance/client/src/fixture";
import { ReplaySchema as MeetByChanceReplaySchema } from "../../../games/meet-by-chance/client/src/generated/game/meet_by_chance/v1/meet_by_chance_pb";
import type { RoomSnapshot } from "../src/api/client";

const roomId = "00000000-0000-4000-8000-000000000001";
const sessionId = "00000000-0000-4000-8000-000000000003";

const replays = [
  {
    gameId: LIARS_DICE_GAME_ID,
    messageType: LIARS_DICE_REPLAY_MESSAGE,
    schemaVersion: LIARS_DICE_SCHEMA_VERSION,
    version: LIARS_DICE_VERSION,
    payload: toBinary(LiarsDiceReplaySchema, liarsDiceReplayFixture()),
    testId: "liars-dice-replay-screen",
  },
  {
    gameId: DICE_789_GAME_ID,
    messageType: DICE_789_REPLAY_MESSAGE,
    schemaVersion: DICE_789_SCHEMA_VERSION,
    version: DICE_789_VERSION,
    payload: toBinary(Dice789ReplaySchema, dice789ReplayFixture()),
    testId: "dice-789-replay-screen",
  },
  {
    gameId: MEET_BY_CHANCE_GAME_ID,
    messageType: MEET_BY_CHANCE_REPLAY_MESSAGE,
    schemaVersion: MEET_BY_CHANCE_SCHEMA_VERSION,
    version: MEET_BY_CHANCE_VERSION,
    payload: toBinary(MeetByChanceReplaySchema, meetByChanceReplayFixture()),
    testId: "meet-by-chance-replay-screen",
  },
] as const;

const roomSnapshot = (gameId: string): RoomSnapshot => ({
  roomId,
  roomCode: "N789",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_POST_GAME",
  hostUserId: "user-self",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_CLOSED",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [{ userId: "user-self", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 3 }],
  activeSessionId: "",
  activeGameId: "",
  lastFinishedSessionId: sessionId,
  lastFinishedGameId: gameId,
  version: { roomVersion: "2", membershipVersion: "1" },
});

/** Installs one finished room and its viewer-safe replay projection without a realtime transport. */
const mockReplay = async (page: Page, replay: typeof replays[number]): Promise<void> => {
  await page.addInitScript(({ storedRoomId, storedSessionId }) => {
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "你",
      userId: "user-self",
      roomId: storedRoomId,
      roomCode: "N789",
      sessionId: storedSessionId,
    }));
  }, { storedRoomId: roomId, storedSessionId: sessionId });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot(replay.gameId) }) });
  });
  await page.route("**/platform.game.v1.GameService/GetReplayProjection", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        complete: true,
        session: {
          sessionId,
          roomId,
          gameId: replay.gameId,
          version: replay.version,
          stateVersion: "9",
          status: "GAME_SESSION_STATUS_FINISHED",
        },
        projection: {
          sessionId,
          stateVersion: "9",
          viewerKind: "VIEWER_KIND_REPLAY",
          view: {
            gameId: replay.gameId,
            version: replay.version,
            schemaVersion: replay.schemaVersion,
            messageType: replay.messageType,
            payload: Buffer.from(replay.payload).toString("base64"),
          },
          allowedActions: [],
        },
      }),
    });
  });
};

for (const replay of replays) {
  test(`loads the authorized ${replay.gameId} replay without opening realtime`, async ({ page }) => {
    await mockReplay(page, replay);
    let realtimeConnections = 0;
    page.on("websocket", (socket) => {
      if (new URL(socket.url()).pathname === "/realtime/game") realtimeConnections += 1;
    });

    await page.goto(`/room/${roomId}/replay/${sessionId}`);

    await expect(page.getByTestId(replay.testId)).toBeVisible();
    expect(realtimeConnections).toBe(0);
  });
}

test("post-game room opens the last finished session replay", async ({ page }) => {
  await mockReplay(page, replays[0]);

  await page.goto(`/room/${roomId}`);
  await page.getByRole("button", { name: "查看上一局复盘" }).click();

  await expect(page).toHaveURL(new RegExp(`/room/${roomId}/replay/${sessionId}$`));
  await expect(page.getByTestId("liars-dice-replay-screen")).toBeVisible();
});

test("historical participant replay does not depend on current room access", async ({ page }) => {
  await mockReplay(page, replays[0]);
  await page.unroute("**/platform.room.v1.RoomService/GetRoom");
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 403, contentType: "application/json", body: JSON.stringify({ code: "permission_denied" }) });
  });

  await page.goto(`/room/${roomId}/replay/${sessionId}`);

  await expect(page.getByTestId("liars-dice-replay-screen")).toBeVisible();
});

test("replay access denial fails closed with an explicit state", async ({ page }) => {
  await mockReplay(page, replays[0]);
  await page.unroute("**/platform.game.v1.GameService/GetReplayProjection");
  await page.route("**/platform.game.v1.GameService/GetReplayProjection", async (route) => {
    await route.fulfill({ status: 403, contentType: "application/json", body: JSON.stringify({ code: "permission_denied" }) });
  });

  await page.goto(`/room/${roomId}/replay/${sessionId}`);

  await expect(page.getByRole("heading", { name: "这局复盘暂时不可用。" })).toBeVisible();
  await expect(page.getByText("你没有查看这局复盘的权限")).toBeVisible();
});
