import { expect, test } from "@playwright/test";

import type { RoomSnapshot } from "../src/api/client";

const roomSnapshot = (roomId: string, roomCode: string): RoomSnapshot => ({
  roomId,
  roomCode,
  visibility: "ROOM_VISIBILITY_PRIVATE",
  status: "ROOM_STATUS_LOBBY",
  hostUserId: "guest-device",
  participantCapacity: 8,
  participantAdmission: "ADMISSION_MODE_OPEN",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  members: [{ userId: "guest-device", username: "阿青", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 }],
  activeSessionId: "",
  activeGameId: "",
  lastFinishedSessionId: "",
  lastFinishedGameId: "",
  version: { roomVersion: "7", membershipVersion: "3" },
});

test("invite deep link carries the room code through first-time identity setup", async ({ page }) => {
  const invitedRoomId = "00000000-0000-4000-8000-000000000019";
  await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ code: "unauthenticated", message: "identity.missing" }),
    });
  });
  await page.route("**/platform.identity.v1.IdentityService/BeginBootstrap", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ challenge: { challengeProof: "device-proof" } }),
    });
  });
  await page.route("**/platform.identity.v1.IdentityService/BootstrapIdentity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: "guest-device", status: "USER_STATUS_ONBOARDING", username: "" } }),
    });
  });
  await page.route("**/platform.identity.v1.IdentityService/CompleteOnboarding", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: "guest-device", status: "USER_STATUS_ACTIVE", username: "小满" } }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/JoinRoom", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ room: roomSnapshot(invitedRoomId, "N789") }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ room: roomSnapshot(invitedRoomId, "N789") }),
    });
  });
  await page.goto("/invite/N789");

  await expect(page.getByRole("heading", { name: "先设置你的用户名" })).toBeVisible();
  await page.getByRole("textbox", { name: "用户名" }).fill("小满");
  await page.getByRole("button", { name: "继续" }).click();

  await expect(page).toHaveURL(new RegExp(`/room/${invitedRoomId}$`));
  await expect(page.getByText("N789", { exact: true })).toBeVisible();
});

test("recognized device follows an invite without reopening the identity form", async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem(
      "game-night.room-context.v1",
      JSON.stringify({ schemaVersion: 1, displayName: "阿青", userId: "guest-device", roomId: null, roomCode: null, sessionId: null }),
    );
  });
  await routeRecognizedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/JoinRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot("00000000-0000-4000-8000-000000000020", "ROOM42") }) });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot("00000000-0000-4000-8000-000000000020", "ROOM42") }) });
  });
  await page.goto("/invite/ROOM42");

  await expect(page).toHaveURL(/\/room\/00000000-0000-4000-8000-000000000020$/);
  await expect(page.getByText("ROOM42", { exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: /桌面/ })).toBeVisible();
  await expect(page.locator('article[aria-label*="你的座位"]').locator("strong")).toHaveText("阿青");
});

test("room load denial never renders demo members", async ({ page }) => {
  const deniedRoomId = "00000000-0000-4000-8000-000000000044";
  await seedRecognizedDevice(page);
  await routeRecognizedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({
      status: 403,
      contentType: "application/json",
      body: JSON.stringify({ code: "permission_denied", message: "room.access.denied" }),
    });
  });

  await page.goto(`/room/${deniedRoomId}`);

  await expect(page.getByRole("alert")).toContainText("room.access.denied");
  await expect(page.getByText("小满", { exact: true })).toHaveCount(0);
  await expect(page.getByText("阿青", { exact: true })).toHaveCount(0);
  await expect(page.getByText("南风", { exact: true })).toHaveCount(0);
});

test("all viewers see the same server-projected member usernames", async ({ browser }) => {
  const roomId = "00000000-0000-4000-8000-000000000041";
  const hostId = "00000000-0000-4000-8000-000000000042";
  const guestId = "00000000-0000-4000-8000-000000000043";
  const snapshot: RoomSnapshot = {
    ...roomSnapshot(roomId, "NAMES1"),
    hostUserId: hostId,
    members: [
      { userId: hostId, username: "小满", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 },
      { userId: guestId, username: "阿青", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 1 },
    ],
  };
  const openViewer = async (userId: string, localName: string) => {
    const context = await browser.newContext();
    const page = await context.newPage();
    await page.addInitScript(({ currentUserId, currentLocalName }) => {
      localStorage.setItem("game-night.room-context.v1", JSON.stringify({
        schemaVersion: 1,
        displayName: currentLocalName,
        userId: currentUserId,
        roomId: null,
        roomCode: null,
        sessionId: null,
      }));
    }, { currentUserId: userId, currentLocalName: localName });
    await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ user: { userId, status: "USER_STATUS_ACTIVE", username: localName } }),
      });
    });
    await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: snapshot }) });
    });
    await page.route("**/platform.room.v1.RoomService/HeartbeatRoom", async (route) => {
      expect(route.request().postDataJSON()).toEqual({ roomId });
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ observedAt: "2026-07-22T12:00:00Z" }) });
    });
    const firstHeartbeat = page.waitForRequest("**/platform.room.v1.RoomService/HeartbeatRoom");
    await page.goto(`/room/${roomId}`);
    await firstHeartbeat;
    return { context, page };
  };

  const host = await openViewer(hostId, "本地主机名");
  const guest = await openViewer(guestId, "本地访客名");
  for (const [viewer, expectedSelfName] of [[host.page, "小满"], [guest.page, "阿青"]] as const) {
    const sharedTable = viewer.getByRole("region", { name: /桌面/ });
    await expect(sharedTable).toBeVisible();
    await expect(sharedTable.locator("article strong")).toHaveCount(2);
    expect((await sharedTable.locator("article strong").allTextContents()).sort()).toEqual(["小满", "阿青"]);
    await expect(sharedTable.locator('article[aria-label*="你的座位"]').locator("strong")).toHaveText(expectedSelfName);
    await expect(viewer.getByText("本地主机名", { exact: true })).toHaveCount(0);
    await expect(viewer.getByText("本地访客名", { exact: true })).toHaveCount(0);
  }
  await host.context.close();
  await guest.context.close();
});

test("room lobby keeps the shared table, tray, and start action reachable across phone orientations", async ({ page }) => {
  const roomId = "00000000-0000-4000-8000-000000000044";
  const hostId = "00000000-0000-4000-8000-000000000045";
  const room: RoomSnapshot = {
    ...roomSnapshot(roomId, "LAYOUT"),
    hostUserId: hostId,
    members: [
      { userId: hostId, username: "小满", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 0 },
      { userId: "00000000-0000-4000-8000-000000000046", username: "阿青", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 1 },
      { userId: "00000000-0000-4000-8000-000000000047", username: "南风", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 2 },
      { userId: "00000000-0000-4000-8000-000000000048", username: "老K", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 3 },
    ],
  };
  await page.addInitScript(({ currentUserId, currentRoomId }) => {
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "本地主机名",
      userId: currentUserId,
      roomId: currentRoomId,
      roomCode: "LAYOUT",
      sessionId: null,
    }));
  }, { currentUserId: hostId, currentRoomId: roomId });
  await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: hostId, status: "USER_STATUS_ACTIVE", username: "小满" } }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room }) });
  });
  await page.route("**/platform.room.v1.RoomService/HeartbeatRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ observedAt: "2026-07-23T12:00:00Z" }) });
  });

  for (const viewport of [{ width: 390, height: 844 }, { width: 844, height: 390 }] as const) {
    await page.setViewportSize(viewport);
    await page.goto(`/room/${roomId}`);

    const sharedTable = page.getByRole("region", { name: /桌面/ });
    const centerCard = page.getByLabel("本局玩法与开局");
    const tray = page.getByRole("region", { name: "规则与开局" });
    const startButton = centerCard.getByRole("button", { name: /开始|再开一局/ });
    const selfSeat = sharedTable.locator('article[aria-label*="你的座位"]');

    await expect(sharedTable).toBeVisible();
    await expect(centerCard).toBeVisible();
    await expect(selfSeat).toBeVisible();
    await expect(startButton).toBeVisible();
    await expect(startButton).toBeEnabled();
    await expect(tray).toBeVisible();

    const seatGeometry = await sharedTable.locator("article").evaluateAll((seats) => seats.map((seat) => {
      const box = seat.getBoundingClientRect();
      return {
        label: seat.getAttribute("aria-label") ?? "",
        left: box.left,
        top: box.top,
        right: box.right,
        bottom: box.bottom,
      };
    }));
    const centerBox = await centerCard.boundingBox();
    const tableBox = await sharedTable.boundingBox();
    const startBox = await startButton.boundingBox();
    const trayBox = await tray.boundingBox();

    expect(centerBox, `center card missing at ${viewport.width}x${viewport.height}`).not.toBeNull();
    expect(tableBox, `shared table missing at ${viewport.width}x${viewport.height}`).not.toBeNull();
    expect(startBox, `start button missing at ${viewport.width}x${viewport.height}`).not.toBeNull();
    expect(trayBox, `tray missing at ${viewport.width}x${viewport.height}`).not.toBeNull();
    if (centerBox === null || tableBox === null || startBox === null || trayBox === null) continue;

    for (const seatBox of seatGeometry) {
      const overlapX = Math.max(0, Math.min(centerBox.x + centerBox.width, seatBox.right) - Math.max(centerBox.x, seatBox.left));
      const overlapY = Math.max(0, Math.min(centerBox.y + centerBox.height, seatBox.bottom) - Math.max(centerBox.y, seatBox.top));
      expect(
        overlapX <= 1 || overlapY <= 1,
        `center card overlaps seat at ${viewport.width}x${viewport.height}: ${JSON.stringify({ centerBox, seatBox })}`,
      ).toBe(true);
    }

    expect(startBox.x).toBeGreaterThanOrEqual(0);
    expect(startBox.y).toBeGreaterThanOrEqual(0);
    expect(startBox.x + startBox.width).toBeLessThanOrEqual(viewport.width);
    expect(startBox.y + startBox.height).toBeLessThanOrEqual(viewport.height);

    if (viewport.width < viewport.height) {
      expect(trayBox.height).toBeLessThanOrEqual(viewport.height * 0.42 + 12);
    } else {
      expect(trayBox.width).toBeLessThanOrEqual(viewport.width * 0.35 + 12);
      expect(trayBox.x).toBeGreaterThanOrEqual(tableBox.x + tableBox.width);
    }
  }
});

test("switching invites never sends the previous room version", async ({ page }) => {
  const firstRoomId = "00000000-0000-4000-8000-000000000021";
  const nextRoomId = "00000000-0000-4000-8000-000000000022";
  await page.addInitScript(({ storedRoomId }) => {
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "阿青",
      userId: "guest-device",
      roomId: storedRoomId,
      roomCode: "ROOMA",
      sessionId: null,
    }));
  }, { storedRoomId: firstRoomId });
  await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: "guest-device", status: "USER_STATUS_ACTIVE", username: "阿青" } }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot(firstRoomId, "ROOMA") }) });
  });
  let joinBody: Record<string, unknown> | undefined;
  await page.route("**/platform.room.v1.RoomService/JoinRoom", async (route) => {
    joinBody = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot(nextRoomId, "ROOMB") }) });
  });

  await page.goto(`/room/${firstRoomId}`);
  await expect(page.getByText("ROOMA", { exact: true })).toBeVisible();
  await page.evaluate(() => {
    history.pushState({}, "", "/invite/ROOMB");
    dispatchEvent(new PopStateEvent("popstate"));
  });

  await expect(page).toHaveURL(new RegExp(`/room/${nextRoomId}$`));
  await expect.poll(() => joinBody).toMatchObject({ roomCode: "ROOMB", intent: "JOIN_INTENT_PARTICIPANT" });
  expect(joinBody).not.toHaveProperty("expectedVersion");
});

test("home prioritizes the actor's hosted room and restores it", async ({ page }) => {
  const hostedRoomId = "00000000-0000-4000-8000-000000000031";
  const joinedRoomId = "00000000-0000-4000-8000-000000000032";
  await seedRecognizedDevice(page);
  await routeRecognizedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/ListMyRooms", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        rooms: [
          myRoomCard(hostedRoomId, "MINE01", true, "阿青"),
          myRoomCard(joinedRoomId, "JOIN01", false, "小满"),
        ],
        page: {},
      }),
    });
  });
  await page.route("**/platform.room.v1.RoomService/ListPublicRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot(hostedRoomId, "MINE01") }) });
  });

  await page.goto("/");

  const roomButtons = page.locator(".my-room-card");
  await expect(roomButtons).toHaveCount(2);
  await expect(roomButtons.first()).toContainText("我的房间");
  await expect(roomButtons.first()).toContainText("MINE01");
  await roomButtons.first().click();
  await expect(page).toHaveURL(new RegExp(`/room/${hostedRoomId}$`));
});

test("public lobby joins through the server-projected primary action", async ({ page }) => {
  const publicRoomId = "00000000-0000-4000-8000-000000000033";
  await seedRecognizedDevice(page);
  await routeRecognizedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/ListMyRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  await page.route("**/platform.room.v1.RoomService/ListPublicRooms", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ rooms: [{
        roomId: publicRoomId,
        hostUsername: "南风",
        status: "ROOM_STATUS_LOBBY",
        participantCapacity: 8,
        participantCount: 2,
        spectatorCount: 0,
        waitingCount: 0,
        participantAdmission: "ADMISSION_MODE_OPEN",
        spectatorAdmission: "ADMISSION_MODE_OPEN",
        activeGameId: "",
        viewerRole: "MEMBER_ROLE_UNSPECIFIED",
        viewerRequestedRole: "MEMBER_ROLE_UNSPECIFIED",
        primaryAction: "PUBLIC_ROOM_PRIMARY_ACTION_JOIN",
      }], page: {} }),
    });
  });
  let joinBody: Record<string, unknown> | undefined;
  await page.route("**/platform.room.v1.RoomService/JoinRoom", async (route) => {
    joinBody = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: roomSnapshot(publicRoomId, "OPEN01") }) });
  });

  await page.goto("/");
  await page.getByRole("button", { name: "加入房间" }).click();

  await expect.poll(() => joinBody).toMatchObject({ roomId: publicRoomId, intent: "JOIN_INTENT_PARTICIPANT" });
  await expect(page).toHaveURL(new RegExp(`/room/${publicRoomId}$`));
});

test("creating a public room preserves the selected game", async ({ page }) => {
  const createdRoomId = "00000000-0000-4000-8000-000000000034";
  await seedRecognizedDevice(page);
  await routeRecognizedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/ListMyRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  await page.route("**/platform.room.v1.RoomService/ListPublicRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  const created = { ...roomSnapshot(createdRoomId, "PUB789"), visibility: "ROOM_VISIBILITY_PUBLIC", selectedGameId: "liars-dice" };
  let createBody: Record<string, unknown> | undefined;
  let selectBody: Record<string, unknown> | undefined;
  await page.route("**/platform.room.v1.RoomService/CreateRoom", async (route) => {
    createBody = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: created }) });
  });
  await page.route("**/platform.room.v1.RoomService/SelectRoomGame", async (route) => {
    selectBody = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ room: { ...created, selectedGameId: "dice-789", version: { roomVersion: "8", membershipVersion: "3" } } }),
    });
  });

  await page.goto("/");
  await page.getByRole("button", { name: /789.*两颗骰子/ }).click();
  await page.getByRole("button", { name: "公开大厅" }).click();
  await page.getByRole("button", { name: "创建789房间" }).click();

  await expect.poll(() => createBody).toMatchObject({ visibility: "ROOM_VISIBILITY_PUBLIC" });
  await expect.poll(() => selectBody).toMatchObject({
    roomId: createdRoomId,
    gameId: "dice-789",
    expectedVersion: { roomVersion: "7", membershipVersion: "3" },
  });
  await expect(page).toHaveURL(new RegExp(`/room/${createdRoomId}\\?game=dice-789$`));
  await expect(page.getByRole("button", { name: /789.*两颗骰子/ })).toHaveAttribute("aria-pressed", "true");
});

test("room polling renders newly joined members without reloading the host page", async ({ page }) => {
  const createdRoomId = "00000000-0000-4000-8000-000000000035";
  await seedRecognizedDevice(page);
  await routeRecognizedIdentity(page);
  await page.route("**/platform.room.v1.RoomService/ListMyRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  await page.route("**/platform.room.v1.RoomService/ListPublicRooms", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ rooms: [], page: {} }) });
  });
  const created = { ...roomSnapshot(createdRoomId, "SYNC35"), selectedGameId: "liars-dice" };
  const refreshed = {
    ...created,
    members: [
      ...created.members,
      { userId: "friend-device", username: "小满", role: "MEMBER_ROLE_PARTICIPANT", requestedRole: "MEMBER_ROLE_UNSPECIFIED", seatIndex: 1 },
    ],
    version: { roomVersion: "7", membershipVersion: "4" },
  };
  const asConnectJson = (snapshot: RoomSnapshot) => ({
    ...snapshot,
    // ProtoJSON omits the default int32 value, so the first participant has no seatIndex property on the wire.
    members: snapshot.members.map((member) => {
      if (member.seatIndex !== 0) return member;
      const { seatIndex: _omitted, ...wireMember } = member;
      return wireMember;
    }),
  });
  await page.route("**/platform.room.v1.RoomService/CreateRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: asConnectJson(created) }) });
  });
  await page.route("**/platform.room.v1.RoomService/GetRoom", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ room: asConnectJson(refreshed) }) });
  });
  await page.route("**/platform.room.v1.RoomService/ListGameRulePresets", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ presets: [] }) });
  });

  await page.goto("/");
  await page.getByRole("button", { name: "创建吹牛骰子房间" }).click();

  await expect(page).toHaveURL(new RegExp(`/room/${createdRoomId}\\?game=liars-dice$`));
  await expect(page.locator(".member-row__copy strong")).toHaveText(["阿青", "小满"], { timeout: 7_000 });
});

const seedRecognizedDevice = async (page: import("@playwright/test").Page): Promise<void> => {
  await page.addInitScript(() => {
    localStorage.setItem("game-night.room-context.v1", JSON.stringify({
      schemaVersion: 1,
      displayName: "阿青",
      userId: "guest-device",
      roomId: null,
      roomCode: null,
      sessionId: null,
    }));
  });
};

const routeRecognizedIdentity = async (page: import("@playwright/test").Page): Promise<void> => {
  await page.route("**/platform.identity.v1.IdentityService/GetCurrentIdentity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: { userId: "guest-device", status: "USER_STATUS_ACTIVE", username: "阿青" } }),
    });
  });
};

const myRoomCard = (roomId: string, roomCode: string, isHost: boolean, hostUsername: string) => ({
  roomId,
  roomCode,
  visibility: "ROOM_VISIBILITY_PRIVATE",
  hostUsername,
  status: "ROOM_STATUS_LOBBY",
  isHost,
  participantCapacity: 8,
  participantCount: 1,
  spectatorCount: 0,
  waitingCount: 0,
  participantAdmission: "ADMISSION_MODE_OPEN",
  spectatorAdmission: "ADMISSION_MODE_OPEN",
  activeGameId: "",
  lastFinishedGameId: "",
  viewerRole: "MEMBER_ROLE_PARTICIPANT",
  viewerRequestedRole: "MEMBER_ROLE_UNSPECIFIED",
});
