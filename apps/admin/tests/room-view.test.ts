import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NInput, NMessageProvider, NSelect } from "naive-ui";
import { defineComponent, h } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AdminPermission, AdminSessionKind, AdminSessionSummarySchema } from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminGameAnomalyFlag,
  AdminGameDetailSchema,
  AdminGameSummarySchema,
  AdminRepairType,
  AdminRoomAnomalyFlag,
  AdminRoomDetailSchema,
  AdminRoomMemberSummarySchema,
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

// Keep drawer state assertions inside the mounted wrapper instead of Naive UI's teleported transition tree.
const drawerStubs = {
  NDrawer: defineComponent({
    name: "NDrawerStub",
    props: {
      show: {
        type: Boolean,
        default: false
      }
    },
    setup(props, { slots }) {
      return () => props.show ? h("div", { "data-testid": "drawer" }, slots.default?.()) : null;
    }
  }),
  NDrawerContent: defineComponent({
    name: "NDrawerContentStub",
    setup(_, { slots }) {
      return () => h("section", { "data-testid": "drawer-content" }, slots.default?.());
    }
  })
};

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
    api.getGame.mockResolvedValue({
      game: create(AdminGameDetailSchema, {
        summary: create(AdminGameSummarySchema, {
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
      }),
      sampledAt
    });
    api.getRoom.mockResolvedValue({
      room: create(AdminRoomDetailSchema, {
        summary: create(AdminRoomSummarySchema, {
          roomId: "room-1",
          roomCode: "ABCD",
          status: RoomStatus.PLAYING,
          hostUserId: "user-host",
          hostUsername: "host",
          participantAdmission: AdmissionMode.OPEN,
          spectatorAdmission: AdmissionMode.APPROVAL,
          roomVersion: 4n,
          membershipVersion: 5n,
          ownershipEpoch: 6n,
          lastActivityAt: sampledAt,
          anomalies: [AdminRoomAnomalyFlag.OWNER_STALE]
        }),
        members: [
          create(AdminRoomMemberSummarySchema, {
            userId: "user-host", username: "host", role: 1, requestedRole: 1, membershipVersion: 5n
          }),
          create(AdminRoomMemberSummarySchema, {
            userId: "user-member", username: "member", role: 1, requestedRole: 1, membershipVersion: 5n
          })
        ]
      }),
      sampledAt
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

  it("disables room controls that the playing-state aggregate rejects", async () => {
    applyFullSession([AdminPermission.ROOMS_READ, AdminPermission.ROOMS_CONTROL]);

    const wrapper = mount(RoomViewHost, {
      global: {
        stubs: {
          teleport: true,
          ...drawerStubs
        }
      }
    });
    await flushPromises();
    const roomButton = wrapper.findAll("button").find((button) => button.text().includes("ABCD"));
    await roomButton?.trigger("click");
    await flushPromises();

    expect(api.getRoom).toHaveBeenCalledWith({
      roomId: "room-1",
      signal: expect.any(AbortSignal)
    });
    expect(wrapper.text()).toContain("牌局进行中，请先在牌局页终止活跃牌局。");
    const saveAdmission = wrapper.findAll("button").find((button) => button.text() === "保存入场策略");
    const forceClose = wrapper.findAll("button").find((button) => button.text() === "强制关闭房间");
    const removeButtons = wrapper.findAll("button").filter((button) => button.text() === "移除");
    expect(saveAdmission?.attributes("disabled")).toBeDefined();
    expect(forceClose?.attributes("disabled")).toBeDefined();
    expect(removeButtons).toHaveLength(2);
    expect(removeButtons[0]?.attributes("disabled")).toBeDefined();
    expect(removeButtons[1]?.attributes("disabled")).toBeUndefined();
    expect(api.setRoomAdmission).not.toHaveBeenCalled();
    expect(api.forceCloseRoom).not.toHaveBeenCalled();
  });

  it("disables termination and terminate-repair preview for a finished game", async () => {
    applyFullSession([AdminPermission.ROOMS_READ, AdminPermission.GAMES_READ, AdminPermission.GAMES_CONTROL, AdminPermission.GAMES_REPAIR]);
    api.getGame.mockResolvedValue({
      game: create(AdminGameDetailSchema, {
        summary: create(AdminGameSummarySchema, {
          sessionId: "game-session-1",
          roomId: "room-1",
          roomCode: "ABCD",
          gameId: "dice",
          status: GameSessionStatus.FINISHED,
          stateVersion: 7n,
          ownershipEpoch: 8n,
          lastProgressAt: sampledAt
        })
      }),
      sampledAt
    });

    const wrapper = mount(RoomViewHost, {
      global: {
        stubs: {
          teleport: true,
          ...drawerStubs
        }
      }
    });
    await flushPromises();
    await wrapper.findAll(".n-tabs-tab").find((tab) => tab.text() === "牌局")?.trigger("click");
    await flushPromises();
    await wrapper.findAll("button").find((button) => button.text().includes("game-session-1"))?.trigger("click");
    await flushPromises();

    expect(api.getGame).toHaveBeenCalledWith({
      sessionId: "game-session-1",
      signal: expect.any(AbortSignal)
    });
    expect(wrapper.text()).toContain("牌局已完成，无需再次终止。");
    const forceTerminate = wrapper.findAll("button").find((button) => button.text() === "强制终止牌局");
    const previewRepair = wrapper.findAll("button").find((button) => button.text() === "预览修正");
    expect(forceTerminate?.attributes("disabled")).toBeDefined();
    expect(previewRepair?.attributes("disabled")).toBeDefined();
    expect(api.forceTerminateGame).not.toHaveBeenCalled();
    expect(api.previewEmergencyRepair).not.toHaveBeenCalled();
  });

  it("derives the room-link repair target and invalidates a preview when its reason changes", async () => {
    applyFullSession([AdminPermission.ROOMS_READ, AdminPermission.GAMES_READ, AdminPermission.GAMES_REPAIR]);
    api.getGame.mockResolvedValue({
      game: create(AdminGameDetailSchema, {
        summary: create(AdminGameSummarySchema, {
          sessionId: "game-session-1",
          roomId: "room-1",
          roomCode: "ABCD",
          gameId: "dice",
          status: GameSessionStatus.ACTIVE,
          stateVersion: 7n,
          ownershipEpoch: 8n,
          lastProgressAt: sampledAt,
          anomalies: [AdminGameAnomalyFlag.ROOM_LINK_MISMATCH]
        })
      }),
      sampledAt
    });
    api.previewEmergencyRepair.mockResolvedValue({
      repair: {
        repairId: "repair-1",
        repairType: AdminRepairType.REPAIR_ROOM_GAME_LINK,
        state: 1,
        targetId: "room-1",
        summary: "repair room link",
        previewDigest: "digest",
        repairVersion: 1n,
        irreversibleEffects: []
      },
      sampledAt
    });

    const wrapper = mount(RoomViewHost, {
      global: {
        stubs: {
          teleport: true,
          ...drawerStubs
        }
      }
    });
    await flushPromises();
    await wrapper.findAll(".n-tabs-tab").find((tab) => tab.text() === "牌局")?.trigger("click");
    await flushPromises();
    await wrapper.findAll("button").find((button) => button.text().includes("game-session-1"))?.trigger("click");
    await flushPromises();

    const repairSelect = wrapper.findAllComponents(NSelect).find((component) => {
      const options = component.props("options");
      return Array.isArray(options) && options.some((option) =>
        typeof option === "object" && option !== null && "label" in option && option.label === "修复房间牌局链接"
      );
    });
    expect(repairSelect).toBeDefined();
    await repairSelect!.vm.$emit("update:value", AdminRepairType.REPAIR_ROOM_GAME_LINK);
    await flushPromises();

    const targetInput = wrapper.findAllComponents(NInput).find((component) => component.props("placeholder") === "房间 ID");
    expect(targetInput).toBeDefined();
    expect(targetInput?.props("value")).toBe("room-1");
    expect(targetInput?.props("placeholder")).toBe("房间 ID");
    const reasonInput = wrapper.findAllComponents(NInput).find((component) => component.props("placeholder") === "说明为什么进入应急修正流程");
    reasonInput?.vm.$emit("update:value", "验证房间链接修正预览");
    await flushPromises();
    await wrapper.findAll("button").find((button) => button.text() === "预览修正")?.trigger("click");
    await flushPromises();

    expect(api.previewEmergencyRepair).toHaveBeenCalledWith({
      targetId: "room-1",
      repairType: AdminRepairType.REPAIR_ROOM_GAME_LINK,
      reason: "验证房间链接修正预览"
    });
    const executePreview = () => wrapper.findAll("button").find((button) => button.text() === "执行预览");
    expect(executePreview()?.attributes("disabled")).toBeUndefined();
    reasonInput?.vm.$emit("update:value", "原因已经变化");
    await flushPromises();
    expect(executePreview()?.attributes("disabled")).toBeDefined();
  });
});
