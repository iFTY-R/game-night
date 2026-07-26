import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NMessageProvider } from "naive-ui";
import { defineComponent, h } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AdminPermission, AdminSessionKind, AdminSessionSummarySchema } from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminGameAnomalyFlag,
  AdminGameSummarySchema,
  AdminRoomAnomalyFlag,
  AdminRoomSummarySchema
} from "../../../contracts/gen/ts/platform/admin/v1/admin_room_pb";
import { GameSessionStatus } from "../../../contracts/gen/ts/platform/game/v1/game_pb";
import { AdmissionMode, RoomStatus } from "../../../contracts/gen/ts/platform/room/v1/room_pb";

const api = vi.hoisted(() => ({
  executeEmergencyRepair: vi.fn(),
  forceCloseRoom: vi.fn(),
  forceTerminateGame: vi.fn(),
  getGame: vi.fn(),
  getRoom: vi.fn(),
  listGames: vi.fn(),
  listRooms: vi.fn(),
  previewEmergencyRepair: vi.fn(),
  removeRoomMember: vi.fn(),
  setRoomAdmission: vi.fn()
}));

vi.mock("../src/api/admin-room", () => api);

import { useAuthStore } from "../src/stores/auth";
import RoomGameControlView from "../src/views/rooms/RoomGameControlView.vue";

const sampledAt = create(TimestampSchema, { seconds: 2_000_000_000n });
const RoomViewHost = defineComponent({
  setup() {
    return () => h(NMessageProvider, null, { default: () => h(RoomGameControlView) });
  }
});

const applyFullSession = (permissions: AdminPermission[]): void => {
  const auth = useAuthStore();
  auth.applySession(create(AdminSessionSummarySchema, {
    adminId: "admin-1",
    sessionId: "session-admin",
    kind: AdminSessionKind.FULL,
    permissions
  }));
};

describe("room and game control view", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
    api.listRooms.mockResolvedValue({
      rooms: [
        create(AdminRoomSummarySchema, {
          roomId: "room-1",
          roomCode: "ABCD",
          status: RoomStatus.PLAYING,
          hostUserId: "user-host",
          hostUsername: "host",
          participantCount: 3,
          spectatorCount: 2,
          participantAdmission: AdmissionMode.OPEN,
          spectatorAdmission: AdmissionMode.APPROVAL,
          roomVersion: 4n,
          membershipVersion: 5n,
          ownershipEpoch: 6n,
          lastActivityAt: sampledAt,
          anomalies: [AdminRoomAnomalyFlag.OWNER_STALE]
        })
      ],
      page: {}
    });
    api.listGames.mockResolvedValue({
      games: [
        create(AdminGameSummarySchema, {
          sessionId: "game-session-1",
          roomId: "room-1",
          roomCode: "ABCD",
          gameId: "dice",
          status: GameSessionStatus.ACTIVE,
          stateVersion: 7n,
          ownershipEpoch: 8n,
          lastProgressAt: sampledAt,
          anomalies: [AdminGameAnomalyFlag.NO_RECENT_PROGRESS]
        })
      ],
      page: {}
    });
  });

  it("loads real room and game lists for authorized operators", async () => {
    applyFullSession([AdminPermission.ROOMS_READ, AdminPermission.GAMES_READ]);

    const wrapper = mount(RoomViewHost);
    await flushPromises();

    expect(api.listRooms).toHaveBeenCalledWith(expect.objectContaining({ pageSize: 20, signal: expect.any(AbortSignal) }));
    expect(api.listGames).toHaveBeenCalledWith(expect.objectContaining({ pageSize: 20, signal: expect.any(AbortSignal) }));
    expect(wrapper.text()).toContain("房间与牌局");
    expect(wrapper.text()).toContain("ABCD");
    expect(wrapper.text()).toContain("Owner 过旧");
  });

  it("does not call game service without game read permission", async () => {
    applyFullSession([AdminPermission.ROOMS_READ]);

    mount(RoomViewHost);
    await flushPromises();

    expect(api.listRooms).toHaveBeenCalledTimes(1);
    expect(api.listGames).not.toHaveBeenCalled();
  });
});
