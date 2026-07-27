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
  adminAuditRequestPolicies,
  listAuditEvents
} from "../src/api/admin-audit";
import {
  adminUserRequestPolicies,
  cancelBatchUserOperation,
  executeUserCommand,
  getBatchUserOperation,
  getUserPII,
  listBatchUserOperationItems,
  listBatchUserOperations,
  listUsers,
  previewBatchUserOperation,
  previewUserCommand,
  retryBatchUserOperation,
  startBatchUserOperation,
  setUserTags
} from "../src/api/admin-user";
import {
  adminRoomRequestPolicies,
  forceCloseRoom,
  listRooms
} from "../src/api/admin-room";
import { createOperationId, installSessionInvalidHandler } from "../src/api/connect";
import { AdminApiError } from "../src/api/errors";
import { AdminAuthService } from "../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { AdminAuditService } from "../../../contracts/gen/ts/platform/admin/v1/admin_audit_pb";
import { AdminRoomService } from "../../../contracts/gen/ts/platform/admin/v1/admin_room_pb";
import {
  AdminBatchUserCommandType,
  AdminBatchUserItemState,
  AdminUserCommandType,
  AdminUserPIIField,
  AdminUserService
} from "../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import { AdminJobState } from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { BusinessErrorDetailSchema } from "../../../contracts/gen/ts/platform/common/v1/error_pb";

const base64BusinessError = (messageKey: string): string => {
  const detail = create(BusinessErrorDetailSchema, { messageKey });
  const binary = toBinary(BusinessErrorDetailSchema, detail);
  return btoa(String.fromCharCode(...binary));
};

describe("admin connect transport", () => {
  it("creates canonical base64url selectors for idempotent mutations", () => {
    const operationId = createOperationId();

    expect(operationId).toMatch(/^[A-Za-z0-9_-]{32}$/u);
  });

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

  it("keeps frontend request policies exhaustive against AdminAuditService", () => {
    expect(Object.keys(adminAuditRequestPolicies)).toEqual(AdminAuditService.methods.map((method) => method.name));
    expect(adminAuditRequestPolicies).toEqual({
      ListAuditEvents: { csrf: true, requestId: false }
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
      AppendUserNote: { csrf: true, requestId: true },
      PreviewUserCommand: { csrf: true, requestId: true },
      ExecuteUserCommand: { csrf: true, requestId: true },
      PreviewBatchUserOperation: { csrf: true, requestId: true },
      StartBatchUserOperation: { csrf: true, requestId: true },
      GetBatchUserOperation: { csrf: true, requestId: false },
      ListBatchUserOperations: { csrf: true, requestId: false },
      ListBatchUserOperationItems: { csrf: true, requestId: false },
      CancelBatchUserOperation: { csrf: true, requestId: true },
      RetryBatchUserOperation: { csrf: true, requestId: true }
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

  it("posts audit timeline reads to AdminAuditService without an audit request id", async () => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ events: [], page: {}, scannedEvents: 0 }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await listAuditEvents({ requestId: "req-1", reasonCode: "audit.review" });

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe("/platform.admin.v1.AdminAuditService/ListAuditEvents");
    expect(headers.get("X-CSRF-Token")).toBe("csrf-token");
    expect(headers.get("X-Request-ID")).toBeNull();
    expect(String(init.body)).toContain("req-1");
    expect(String(init.body)).toContain("audit.review");
  });

  it.each([
    ["GetUserPII", () => getUserPII({ userId: "user-1", fields: [AdminUserPIIField.ADMIN_USER_PII_FIELD_REAL_NAME], reason: "申诉核验" })],
    ["SetUserTags", () => setUserTags({ operationId: "op-1", userId: "user-1", tagIds: ["tag-1"], reason: "运营标注", expectedVersion: 2n })],
    ["PreviewUserCommand", () => previewUserCommand({
      userId: "user-1",
      command: { type: AdminUserCommandType.SUSPEND },
      reason: "风控处置",
      expectedUserVersion: 2n
    })],
    ["ExecuteUserCommand", () => executeUserCommand({
      operationId: "op-1",
      userId: "user-1",
      command: { type: AdminUserCommandType.SUSPEND },
      previewId: "preview-1",
      previewDigest: "digest",
      reason: "风控处置",
      expectedUserVersion: 2n
    })],
    ["PreviewBatchUserOperation", () => previewBatchUserOperation({
      selection: { mode: "explicit", users: [{ userId: "user-1", expectedUserVersion: 2n }] },
      command: AdminBatchUserCommandType.SUSPEND,
      reason: "批量风控处置"
    })],
    ["StartBatchUserOperation", () => startBatchUserOperation({
      operationId: "op-2",
      previewId: "preview-batch-1",
      previewDigest: "digest-batch",
      reason: "批量风控处置",
      expectedVersion: 3n
    })],
    ["CancelBatchUserOperation", () => cancelBatchUserOperation({
      operationId: "op-3",
      batchOperationId: "batch-1",
      reason: "批量任务撤回",
      expectedVersion: 4n
    })],
    ["RetryBatchUserOperation", () => retryBatchUserOperation({
      operationId: "op-4",
      batchOperationId: "batch-1",
      itemIds: ["item-1"],
      reason: "重试失败条目",
      expectedVersion: 5n
    })]
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

  it.each([
    ["GetBatchUserOperation", { batchOperation: {}, sampledAt: "1970-01-01T00:00:00Z" }, () => getBatchUserOperation({ batchOperationId: "batch-1" })],
    ["ListBatchUserOperations", { batchOperations: [], page: {} }, () => listBatchUserOperations({ states: [AdminJobState.RUNNING] })],
    ["ListBatchUserOperationItems", { items: [], page: {} }, () => listBatchUserOperationItems({ batchOperationId: "batch-1", states: [AdminBatchUserItemState.FAILED] })]
  ])("keeps request id off read-only batch procedure %s", async (procedureName, responseBody, invoke) => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(responseBody), {
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
    expect(headers.get("X-Request-ID")).toBeNull();
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

  it("treats a bare 401 unauthenticated response as a lost admin session", async () => {
    const invalidSpy = vi.fn();
    installSessionInvalidHandler(invalidSpy);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: "unauthenticated", message: "authentication required" }), {
          status: 401,
          headers: { "Content-Type": "application/json" }
        })
      )
    );

    await expect(verifyAdminTotp({ totpCode: "000000" })).rejects.toMatchObject({
      status: 401,
      code: "unauthenticated"
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
