import { toBinary } from "@bufbuild/protobuf";
import { expect, test } from "@playwright/test";

import {
  LIARS_DICE_GAME_ID,
  LIARS_DICE_SCHEMA_VERSION,
  LIARS_DICE_VERSION,
  LIARS_DICE_VIEW_MESSAGE,
} from "../../../games/liars-dice/client/src/constants";
import { liarsDiceFixtureView } from "../../../games/liars-dice/client/src/fixture";
import { ViewSchema } from "../../../games/liars-dice/client/src/generated/game/liars_dice/v1/liars_dice_pb";
import type { RoomSnapshot } from "../src/api/client";

const sessionId = "00000000-0000-4000-8000-000000000003";
const roomId = "00000000-0000-4000-8000-000000000001";

/** Builds the authoritative room metadata that selects the versioned game client under test. */
const roomSnapshot = (overrides: Partial<RoomSnapshot> = {}): RoomSnapshot => ({
  roomId,
  roomCode: "N789",
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_PLAYING",
  hostUserId: "user-self",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_OPEN",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [{
    userId: "user-self",
    role: "MEMBER_ROLE_PARTICIPANT",
    requestedRole: "MEMBER_ROLE_UNSPECIFIED",
    seatIndex: 0,
  }],
  activeSessionId: sessionId,
  activeGameId: "liars-dice",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "1", membershipVersion: "1" },
  ...overrides,
});

test("live table exchanges a fresh ticket and recovers after WebSocket disconnect", async ({ page }) => {
  const view = liarsDiceFixtureView(Date.now() + 60_000);
  view.allowedActions = view.allowedActions.filter((action) => action !== "session.finish");
  const projection = {
    sessionId,
    stateVersion: "1",
    viewerKind: "VIEWER_KIND_PLAYER",
    view: {
      gameId: LIARS_DICE_GAME_ID,
      version: LIARS_DICE_VERSION,
      schemaVersion: LIARS_DICE_SCHEMA_VERSION,
      messageType: LIARS_DICE_VIEW_MESSAGE,
      payload: Buffer.from(toBinary(ViewSchema, view)).toString("base64"),
    },
    allowedActions: [...view.allowedActions, "session.finish"],
  };
  let subscriptionRequests = 0;
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));

  await page.addInitScript(({ storedRoomId, storedSessionId }) => {
    const NativeWebSocket = window.WebSocket;
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "你",
      userId: "user-self",
      roomId: storedRoomId,
      roomCode: "N789",
      sessionId: storedSessionId,
    }));

    const runtime = window as typeof window & {
      __realtimeConnections?: number;
      __realtimeBinaryHellos?: number;
    };
    runtime.__realtimeConnections = 0;
    runtime.__realtimeBinaryHellos = 0;

    class ControlledWebSocket extends EventTarget {
      public static readonly CONNECTING = 0;
      public static readonly OPEN = 1;
      public static readonly CLOSED = 3;
      public binaryType: BinaryType = "blob";
      public readyState = ControlledWebSocket.CONNECTING;
      public readonly connectionNumber: number;
      public readonly url: string;

      public constructor(url: string) {
        super();
        this.url = url;
        this.connectionNumber = (runtime.__realtimeConnections ?? 0) + 1;
        runtime.__realtimeConnections = this.connectionNumber;
        setTimeout(() => {
          this.readyState = ControlledWebSocket.OPEN;
          this.dispatchEvent(new Event("open"));
          if (this.connectionNumber === 1) {
            setTimeout(() => this.serverClose(), 80);
          }
        }, 0);
      }

      public send(data: unknown): void {
        if (data instanceof ArrayBuffer || ArrayBuffer.isView(data)) {
          runtime.__realtimeBinaryHellos = (runtime.__realtimeBinaryHellos ?? 0) + 1;
        }
      }

      public close(): void {
        this.readyState = ControlledWebSocket.CLOSED;
      }

      private serverClose(): void {
        this.readyState = ControlledWebSocket.CLOSED;
        const event = new Event("close");
        Object.defineProperty(event, "code", { value: 1006 });
        this.dispatchEvent(event);
      }
    }

    const RoutedWebSocket = new Proxy(NativeWebSocket, {
      construct(target, argumentsList) {
        const requestedURL = String(argumentsList[0] ?? "");
        if (new URL(requestedURL, window.location.href).pathname === "/realtime/game") {
          return new ControlledWebSocket(requestedURL);
        }
        return Reflect.construct(target, argumentsList);
      },
    });
    Object.defineProperty(window, "WebSocket", { configurable: true, value: RoutedWebSocket });
  }, { storedRoomId: roomId, storedSessionId: sessionId });

  await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: "user-self", status: "USER_STATUS_ACTIVE", username: "你" } }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot() }) });
  });
  await page.route("**/platform.game.v1.GameService/*", async (route) => {
    const method = new URL(route.request().url()).pathname.split("/").at(-1);
    if (method === "OpenSubscription") subscriptionRequests += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(method === "OpenSubscription"
        ? { ticket: "AQI=", grant: "AwQ=", projection }
        : { projection }),
    });
  });

  await page.goto(`/room/${roomId}/game/${sessionId}`);
  await expect(page.getByTestId("liars-dice-screen")).toBeVisible();
  await expect.poll(() => subscriptionRequests, { message: "OpenSubscription should be retried after disconnect" }).toBeGreaterThanOrEqual(2);
  await expect.poll(() => page.evaluate(() => (window as typeof window & { __realtimeConnections?: number }).__realtimeConnections ?? 0)).toBeGreaterThanOrEqual(2);
  await expect.poll(() => page.evaluate(() => (window as typeof window & { __realtimeBinaryHellos?: number }).__realtimeBinaryHellos ?? 0)).toBeGreaterThanOrEqual(2);
  await expect(page.getByText("已连接")).toBeVisible();
  await expect(page.getByTestId("bid-action")).toBeEnabled();
  await page.getByRole("button", { name: "展开操作区" }).click();
  await expect(page.getByRole("button", { name: "结束本局" })).toBeVisible();
  expect(pageErrors).toEqual([]);
});

test("finished liars dice session URL returns to the room without loading stale projection", async ({ page }) => {
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

  let gameRequests = 0;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname.includes("/platform.game.v1.GameService/")) gameRequests += 1;
  });
  await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: "user-self", status: "USER_STATUS_ACTIVE", username: "你" } }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        room: roomSnapshot({
          status: "ROOM_STATUS_POST_GAME",
          activeSessionId: "",
          activeGameId: "",
          lastFinishedSessionId: sessionId,
          lastFinishedGameId: "liars-dice",
        }),
      }),
    });
  });

  await page.goto(`/room/${roomId}/game/${sessionId}`);

  await expect(page).toHaveURL(new RegExp(`/room/${roomId}$`));
  expect(gameRequests).toBe(0);
});

test("active table returns a member home when the host closes the room", async ({ page }) => {
  await page.addInitScript(({ storedRoomId, storedSessionId }) => {
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "阿青",
      userId: "member-user",
      roomId: storedRoomId,
      roomCode: "N789",
      sessionId: storedSessionId,
    }));
  }, { storedRoomId: roomId, storedSessionId: sessionId });

  let roomReads = 0;
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    roomReads += 1;
    const snapshot = roomSnapshot({ hostUserId: "host-user" });
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        room: roomReads >= 3
          ? { ...snapshot, status: "ROOM_STATUS_CLOSED", activeSessionId: "", activeGameId: "" }
          : snapshot,
      }),
    });
  });
  await page.route("**/platform.game.v1.GameService/*", async (route) => {
    await route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ code: "not_found", message: "fixture fallback" }) });
  });

  await page.goto(`/room/${roomId}/game/${sessionId}`);

  await expect(page.getByTestId("liars-dice-screen")).toBeVisible();
  await expect(page).toHaveURL(/\/$/, { timeout: 7_000 });
  await expect(page.getByRole("status").filter({ hasText: "房主已解散房间，当前游戏已结束" })).toBeVisible();
  await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem("game-night.room-context.v1") ?? "{}") as { sessionId?: unknown })).toMatchObject({ sessionId: null });
});
