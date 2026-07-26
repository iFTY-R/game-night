import { create, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it, vi } from "vitest";
import {
  adminAuthRequestPolicies,
  changeAdminPassword,
  changeInitialPassword,
  getRuntimeReadiness,
  getSetupState,
  loginPassword,
  verifyAdminRecoveryCode,
  verifyAdminTotp
} from "../src/api/admin-auth";
import {
  adminUserRequestPolicies,
  getUserPII,
  listUsers,
  setUserTags
} from "../src/api/admin-user";
import {
  adminRoomRequestPolicies,
  forceCloseRoom,
  listRooms
} from "../src/api/admin-room";
import { installSessionInvalidHandler } from "../src/api/connect";
import { AdminApiError } from "../src/api/errors";
import { AdminAuthService } from "../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { AdminRoomService } from "../../../contracts/gen/ts/platform/admin/v1/admin_room_pb";
import { AdminUserPIIField, AdminUserService } from "../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import { BusinessErrorDetailSchema } from "../../../contracts/gen/ts/platform/common/v1/error_pb";

const base64BusinessError = (messageKey: string): string => {
  const detail = create(BusinessErrorDetailSchema, { messageKey });
  const binary = toBinary(BusinessErrorDetailSchema, detail);
  return btoa(String.fromCharCode(...binary));
};

describe("admin connect transport", () => {
  it("keeps frontend request policies exhaustive against AdminAuthService", () => {
    expect(Object.keys(adminAuthRequestPolicies)).toEqual(AdminAuthService.methods.map((method) => method.name));
    expect(adminAuthRequestPolicies).toEqual({
      GetSetupState: { csrf: false, requestId: false },
      GetCurrentAdminSession: { csrf: true, requestId: false },
      GetRuntimeReadiness: { csrf: true, requestId: false },
      BeginAdminLogin: { csrf: false, requestId: false },
      LoginPassword: { csrf: false, requestId: true },
      VerifyAdminTotp: { csrf: true, requestId: true },
      VerifyAdminRecoveryCode: { csrf: true, requestId: true },
      ChangeInitialPassword: { csrf: true, requestId: true },
      ChangeAdminPassword: { csrf: true, requestId: true },
      BeginTotpEnrollment: { csrf: true, requestId: true },
      CompleteTotpEnrollment: { csrf: true, requestId: true },
      DisableTotp: { csrf: true, requestId: true },
      RegenerateAdminRecoveryCodes: { csrf: true, requestId: true },
      ConfirmAdminSecretReceipt: { csrf: true, requestId: false },
      ElevateAdminSession: { csrf: true, requestId: true },
      RevokeCurrentAdminElevation: { csrf: true, requestId: true },
      ListAdminSessions: { csrf: true, requestId: false },
      RevokeAdminSession: { csrf: true, requestId: true },
      PreviewRevokeOtherAdminSessions: { csrf: true, requestId: false },
      RevokeOtherAdminSessions: { csrf: true, requestId: true },
      LogoutAdmin: { csrf: true, requestId: false }
    });
  });

  it("keeps frontend request policies scoped to implemented AdminUserService procedures", () => {
    const implementedMethods = AdminUserService.methods
      .map((method) => method.name)
      .filter((name) => name in adminUserRequestPolicies);
    expect(Object.keys(adminUserRequestPolicies)).toEqual(implementedMethods);
    expect(adminUserRequestPolicies).toEqual({
      ListUsers: { csrf: true, requestId: false },
      GetUser: { csrf: true, requestId: false },
      GetUserPII: { csrf: true, requestId: true },
      ListUserTags: { csrf: true, requestId: false },
      CreateUserTag: { csrf: true, requestId: true },
      UpdateUserTag: { csrf: true, requestId: true },
      DeleteUserTag: { csrf: true, requestId: true },
      SetUserTags: { csrf: true, requestId: true },
      ListUserNotes: { csrf: true, requestId: false },
      AppendUserNote: { csrf: true, requestId: true }
    });
  });

  it("keeps frontend request policies exhaustive against AdminRoomService", () => {
    expect(Object.keys(adminRoomRequestPolicies)).toEqual(AdminRoomService.methods.map((method) => method.name));
    expect(adminRoomRequestPolicies).toEqual({
      ListRooms: { csrf: true, requestId: false },
      GetRoom: { csrf: true, requestId: false },
      ListGames: { csrf: true, requestId: false },
      GetGame: { csrf: true, requestId: false },
      SetRoomAdmission: { csrf: true, requestId: true },
      RemoveRoomMember: { csrf: true, requestId: true },
      ForceCloseRoom: { csrf: true, requestId: true },
      ForceTerminateGame: { csrf: true, requestId: true },
      PreviewEmergencyRepair: { csrf: true, requestId: true },
      ExecuteEmergencyRepair: { csrf: true, requestId: true },
      GetRepairOperation: { csrf: true, requestId: false }
    });
  });

  it("posts admin auth requests with relative path and flow/csrf headers", async () => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ requiresMfa: { session: { kind: "ADMIN_SESSION_KIND_MFA_PENDING" } } }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await loginPassword({
      challengeProof: "challenge-proof",
      password: "secret-password",
      requestFlowId: "flow-1"
    });

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe("/platform.admin.v1.AdminAuthService/LoginPassword");
    expect(headers.get("X-CSRF-Token")).toBeNull();
    expect(headers.get("X-Request-Flow-ID")).toBe("flow-1");
    expect(headers.get("X-Request-ID")).toMatch(/[0-9a-f-]{8,}/i);
    expect(headers.get("Connect-Protocol-Version")).toBe("1");
    expect(init.credentials).toBe("include");
    expect(String(init.body)).toContain("challengeProof");
  });

  it("posts implemented user-center reads and audited mutations to AdminUserService", async () => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ users: [], page: {} }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await listUsers({ username: "alice" });

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe("/platform.admin.v1.AdminUserService/ListUsers");
    expect(headers.get("X-CSRF-Token")).toBe("csrf-token");
    expect(headers.get("X-Request-ID")).toBeNull();
    expect(String(init.body)).toContain("alice");
  });

  it("posts implemented room reads and audited controls to AdminRoomService", async () => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ rooms: [], page: {} }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await listRooms({ roomCode: "ABCD" });

    const [readUrl, readInit] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const readHeaders = new Headers(readInit.headers);
    expect(readUrl).toBe("/platform.admin.v1.AdminRoomService/ListRooms");
    expect(readHeaders.get("X-CSRF-Token")).toBe("csrf-token");
    expect(readHeaders.get("X-Request-ID")).toBeNull();
    expect(String(readInit.body)).toContain("ABCD");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify({ outcome: "ADMIN_ROOM_COMMAND_OUTCOME_EXECUTED" }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    await forceCloseRoom({
      operationId: "op-1",
      roomId: "room-1",
      reason: "风控处置",
      expectedRoomVersion: 2n
    });

    const [writeUrl, writeInit] = fetchSpy.mock.calls[1] as [string, RequestInit];
    const writeHeaders = new Headers(writeInit.headers);
    expect(writeUrl).toBe("/platform.admin.v1.AdminRoomService/ForceCloseRoom");
    expect(writeHeaders.get("X-CSRF-Token")).toBe("csrf-token");
    expect(writeHeaders.get("X-Request-ID")).toMatch(/[0-9a-f-]{8,}/i);
  });

  it.each([
    ["GetUserPII", () => getUserPII({ userId: "user-1", fields: [AdminUserPIIField.ADMIN_USER_PII_FIELD_REAL_NAME], reason: "申诉核验" })],
    ["SetUserTags", () => setUserTags({ operationId: "op-1", userId: "user-1", tagIds: ["tag-1"], reason: "运营标注", expectedVersion: 2n })]
  ])("assigns request ids to audited user-center procedure %s", async (procedureName, invoke) => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({}), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await invoke();

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe(`/platform.admin.v1.AdminUserService/${procedureName}`);
    expect(headers.get("X-CSRF-Token")).toBe("csrf-token");
    expect(headers.get("X-Request-ID")).toMatch(/[0-9a-f-]{8,}/i);
  });

  it("assigns request ids to protected mutation procedures", async () => {
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ operationId: "op-1", session: {}, revokedSessions: 2 }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await changeAdminPassword({
      operationId: "op-1",
      currentPassword: "old",
      newPassword: "new",
      expectedPasswordVersion: 1n
    });

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe("/platform.admin.v1.AdminAuthService/ChangeAdminPassword");
    expect(headers.get("X-Request-ID")).toMatch(/[0-9a-f-]{8,}/i);
  });

  it.each([
    ["VerifyAdminTotp", () => verifyAdminTotp({ totpCode: "123456" })],
    ["VerifyAdminRecoveryCode", () => verifyAdminRecoveryCode({ recoveryCode: "recovery-code" })],
    ["ChangeInitialPassword", () => changeInitialPassword({ newPassword: "replacement-password" })]
  ])("assigns request ids to staged authentication procedure %s", async (procedureName, invoke) => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({}), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await invoke();

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe(`/platform.admin.v1.AdminAuthService/${procedureName}`);
    expect(headers.get("X-CSRF-Token")).toBe("csrf-token");
    expect(headers.get("X-Request-ID")).toMatch(/[0-9a-f-]{8,}/i);
  });

  it("requests runtime readiness through the authenticated admin service", async () => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          ordinary: { mode: "ordinary", ready: true, components: { postgresql: "ready" } },
          sensitive: { mode: "sensitive_write", ready: false, components: { redis: "unavailable" } }
        }),
        { status: 200, headers: { "Content-Type": "application/json" } }
      )
    );
    vi.stubGlobal("fetch", fetchSpy);

    const response = await getRuntimeReadiness();

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe("/platform.admin.v1.AdminAuthService/GetRuntimeReadiness");
    expect(headers.get("X-CSRF-Token")).toBe("csrf-token");
    expect(response.ordinary?.components.postgresql).toBe("ready");
    expect(response.sensitive?.ready).toBe(false);
  });

  it("decodes Connect Any details and invokes the session invalid handler", async () => {
    const invalidSpy = vi.fn();
    installSessionInvalidHandler(invalidSpy);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            code: "unauthenticated",
            message: "admin.auth.invalid",
            details: [
              {
                type: "type.googleapis.com/platform.common.v1.BusinessErrorDetail",
                value: base64BusinessError("admin.auth.invalid")
              }
            ]
          }),
          { status: 401, headers: { "Content-Type": "application/json" } }
        )
      )
    );

    await expect(verifyAdminTotp({ totpCode: "000000" })).rejects.toMatchObject({
      businessKey: "admin.auth.invalid",
      message: "登录状态已失效，请重新登录。"
    });
    expect(invalidSpy).toHaveBeenCalledTimes(1);
  });

  it("supports anonymous setup requests without csrf", async () => {
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ state: "ADMIN_ACCOUNT_STATE_ACTIVE" }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await getSetupState();

    const setupHeaders = new Headers((fetchSpy.mock.calls[0] as [string, RequestInit])[1].headers);
    expect(setupHeaders.get("X-CSRF-Token")).toBeNull();
  });
});
