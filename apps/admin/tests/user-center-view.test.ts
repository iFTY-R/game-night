import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NMessageProvider, NPopconfirm, NSelect } from "naive-ui";
import { defineComponent, h } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  AdminElevationScope,
  AdminElevationSummarySchema,
  AdminPermission,
  AdminSessionKind,
  AdminSessionSummarySchema
} from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminUserCommandOutcome,
  AdminUserCommandType,
  AdminUserDetailSchema,
  AdminUserRoomSummarySchema,
  AdminUserStatus,
  AdminUserSummarySchema,
  PreviewUserCommandResponseSchema
} from "../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";

const api = vi.hoisted(() => ({
  appendUserNote: vi.fn(),
  createUserTag: vi.fn(),
  executeUserCommand: vi.fn(),
  getUser: vi.fn(),
  getUserPII: vi.fn(),
  listUserNotes: vi.fn(),
  listUsers: vi.fn(),
  listUserTags: vi.fn(),
  previewUserCommand: vi.fn(),
  setUserTags: vi.fn()
}));

const elevationState = vi.hoisted(() => ({
  calls: [] as Array<{ open: boolean; payload?: unknown }>,
  lastPayload: null as null | {
    scope: AdminElevationScope;
    allowRecoveryCode: boolean;
    onElevated: (summary: unknown) => void;
  }
}));

vi.mock("../src/api/admin-user", async () => {
  const actual = await vi.importActual<typeof import("../src/api/admin-user")>("../src/api/admin-user");
  return {
    ...actual,
    appendUserNote: api.appendUserNote,
    createUserTag: api.createUserTag,
    executeUserCommand: api.executeUserCommand,
    getUser: api.getUser,
    getUserPII: api.getUserPII,
    listUserNotes: api.listUserNotes,
    listUsers: api.listUsers,
    listUserTags: api.listUserTags,
    previewUserCommand: api.previewUserCommand,
    setUserTags: api.setUserTags
  };
});

vi.mock("../src/api/connect", async () => {
  const actual = await vi.importActual<typeof import("../src/api/connect")>("../src/api/connect");
  return {
    ...actual,
    createOperationId: () => "op-fixed"
  };
});

vi.mock("../src/views/security/components/ElevationDialog.vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    default: defineComponent({
      name: "ElevationDialogStub",
      setup(_, { expose }) {
        expose({
          toggleDialog(open: boolean, payload?: unknown) {
            elevationState.calls.push({ open, payload });
            elevationState.lastPayload = open
              ? payload as typeof elevationState.lastPayload
              : null;
          }
        });
        return () => h("div");
      }
    })
  };
});

import { useAuthStore } from "../src/stores/auth";
import UserCenterView from "../src/views/users/UserCenterView.vue";

const sampledAt = create(TimestampSchema, { seconds: 2_000_000_000n });

const buildSession = (permissions: AdminPermission[]) =>
  create(AdminSessionSummarySchema, {
    adminId: "admin-1",
    sessionId: "session-1",
    kind: AdminSessionKind.FULL,
    permissions,
    adminVersion: 8n,
    passwordVersion: 3n,
    sessionVersion: 4n,
    idleExpiresAt: sampledAt,
    absoluteExpiresAt: sampledAt,
    mfa: {
      enabled: true,
      enrollmentVersion: 2n,
      recoveryCodesVersion: 1n,
      recoveryCodesRemaining: 5
    }
  });

const userSummaryFactory = (userId: string, username: string, version: bigint) =>
  create(AdminUserSummarySchema, {
    userId,
    username,
    status: AdminUserStatus.ACTIVE,
    createdAt: sampledAt,
    updatedAt: sampledAt,
    lastActivityAt: sampledAt,
    online: true,
    version
  });

const roomFactory = () =>
  create(AdminUserRoomSummarySchema, {
    roomId: "room-1",
    roomCode: "ROOM-001",
    membershipRole: 1,
    roomVersion: 10n,
    membershipVersion: 12n
  });

const userDetailFactory = (summary: ReturnType<typeof userSummaryFactory>) =>
  create(AdminUserDetailSchema, {
    summary,
    rooms: [roomFactory()],
    devices: [],
    recentGames: [],
    recentGovernance: [],
    recentNotes: []
  });

const previewFactory = (requiredElevation: AdminElevationScope) =>
  create(PreviewUserCommandResponseSchema, {
    previewId: "preview-1",
    previewDigest: "digest-1",
    expectedUserVersion: 6n,
    affectedDevices: 4,
    affectedRooms: 1,
    blockers: [],
    requiredElevation,
    expiresAt: sampledAt
  });

const Host = defineComponent({
  setup() {
    return () => h(NMessageProvider, null, {
      default: () => h(UserCenterView)
    });
  }
});

const mountView = (permissions: AdminPermission[]): VueWrapper => {
  const auth = useAuthStore();
  auth.session = buildSession(permissions);
  return mount(Host, {
    global: {
      stubs: {
        BatchGovernancePanel: true,
        teleport: true
      }
    }
  });
};

const openUserDrawer = async (wrapper: VueWrapper, index = 0): Promise<void> => {
  await flushPromises();
  const userRows = wrapper.findAll(".user-list__item");
  expect(userRows[index]).toBeDefined();
  await userRows[index]!.trigger("click");
  await flushPromises();
};

const openGovernanceTab = async (wrapper: VueWrapper): Promise<void> => {
  const governanceTab = wrapper.findAll(".n-tabs-tab").find((tab) => tab.text().includes("治理"));
  expect(governanceTab).toBeDefined();
  await governanceTab!.trigger("click");
  await flushPromises();
};

const findGovernanceCommandSelect = (wrapper: VueWrapper) =>
  wrapper.findAllComponents(NSelect).find((component) =>
    Array.isArray(component.props("options")) &&
    (component.props("options") as Array<{ label: string }>).some((option) => option.label.includes("移出当前房间") || option.label.includes("撤销全部设备"))
  );

describe("user center governance", () => {
  const alice = userSummaryFactory("user-1", "alice", 5n);
  const bob = userSummaryFactory("user-2", "bob", 7n);

  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
    elevationState.calls = [];
    elevationState.lastPayload = null;
    api.listUserTags.mockResolvedValue({ tags: [], catalogVersion: 1n });
    api.listUsers.mockResolvedValue({
      users: [alice, bob],
      page: {}
    });
    api.getUser.mockImplementation(async ({ userId }: { userId: string }) => ({
      user: userId === bob.userId ? userDetailFactory(bob) : userDetailFactory(alice)
    }));
    api.listUserNotes.mockResolvedValue({
      notes: [],
      page: {}
    });
    api.appendUserNote.mockResolvedValue({});
    api.createUserTag.mockResolvedValue({});
    api.getUserPII.mockResolvedValue({
      values: [],
      accessAuditEventId: ""
    });
    api.setUserTags.mockResolvedValue({});
  });

  it("obtains preview-scoped elevation before executing revoke-all-devices", async () => {
    api.previewUserCommand.mockResolvedValue(previewFactory(AdminElevationScope.USERS_REVOKE_DEVICES));
    api.executeUserCommand.mockResolvedValue({
      outcome: AdminUserCommandOutcome.EXECUTED,
      user: userSummaryFactory("user-1", "alice", 6n)
    });

    const wrapper = mountView([AdminPermission.USERS_GOVERN]);
    await openUserDrawer(wrapper);
    await openGovernanceTab(wrapper);

    const commandSelect = findGovernanceCommandSelect(wrapper);
    expect(commandSelect).toBeDefined();
    await commandSelect!.vm.$emit("update:value", AdminUserCommandType.REVOKE_ALL_DEVICES);
    await flushPromises();

    const reason = wrapper.find('textarea[placeholder*="说明治理原因"]');
    expect(reason.exists()).toBe(true);
    await reason.setValue("撤销高风险设备");
    const previewButton = wrapper.findAll("button").find((button) => button.text().includes("预览治理"));
    expect(previewButton).toBeDefined();
    await previewButton!.trigger("click");
    await flushPromises();

    expect(api.previewUserCommand).toHaveBeenCalledWith({
      userId: "user-1",
      command: { type: AdminUserCommandType.REVOKE_ALL_DEVICES },
      reason: "撤销高风险设备",
      expectedUserVersion: 5n
    });

    const confirm = wrapper.findComponent(NPopconfirm);
    expect(confirm.exists()).toBe(true);
    await confirm.vm.$emit("positive-click");
    await flushPromises();

    expect(api.executeUserCommand).not.toHaveBeenCalled();
    expect(elevationState.lastPayload?.scope).toBe(AdminElevationScope.USERS_REVOKE_DEVICES);
    expect(elevationState.lastPayload?.allowRecoveryCode).toBe(false);

    elevationState.lastPayload?.onElevated(create(AdminElevationSummarySchema, {
      scope: AdminElevationScope.USERS_REVOKE_DEVICES,
      expiresAt: sampledAt
    }));
    await flushPromises();

    expect(api.executeUserCommand).toHaveBeenCalledWith({
      operationId: "op-fixed",
      userId: "user-1",
      command: { type: AdminUserCommandType.REVOKE_ALL_DEVICES },
      previewId: "preview-1",
      previewDigest: "digest-1",
      reason: "撤销高风险设备",
      expectedUserVersion: 6n
    });
  });

  it("drops a stale elevation callback after the operator switches to another user", async () => {
    api.previewUserCommand.mockResolvedValue(previewFactory(AdminElevationScope.USERS_DELETE));

    const wrapper = mountView([AdminPermission.USERS_GOVERN]);
    await openUserDrawer(wrapper, 0);
    await openGovernanceTab(wrapper);

    const commandSelect = findGovernanceCommandSelect(wrapper);
    expect(commandSelect).toBeDefined();
    await commandSelect!.vm.$emit("update:value", AdminUserCommandType.DELETE);
    await flushPromises();

    const reason = wrapper.find('textarea[placeholder*="说明治理原因"]');
    await reason.setValue("删除违规账号");
    await wrapper.findAll("button").find((button) => button.text().includes("预览治理"))!.trigger("click");
    await flushPromises();

    await wrapper.findComponent(NPopconfirm).vm.$emit("positive-click");
    await flushPromises();
    expect(elevationState.lastPayload?.scope).toBe(AdminElevationScope.USERS_DELETE);

    await openUserDrawer(wrapper, 1);
    elevationState.lastPayload?.onElevated(create(AdminElevationSummarySchema, {
      scope: AdminElevationScope.USERS_DELETE,
      expiresAt: sampledAt
    }));
    await flushPromises();

    expect(api.executeUserCommand).not.toHaveBeenCalled();
  });

  it("keeps room-removal governance available for rooms-control operators", async () => {
    const wrapper = mountView([AdminPermission.ROOMS_CONTROL]);
    await openUserDrawer(wrapper);
    await openGovernanceTab(wrapper);

    expect(wrapper.text()).not.toContain("当前管理员无可用的单用户治理动作权限");
    expect(wrapper.text()).toContain("目标房间");

    const commandSelect = findGovernanceCommandSelect(wrapper);
    expect(commandSelect).toBeDefined();
    expect(commandSelect!.props("options")).toEqual([
      { label: "移出当前房间", value: AdminUserCommandType.REMOVE_FROM_CURRENT_ROOM }
    ]);
  });
});
