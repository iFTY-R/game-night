import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  AdminAccountState,
  AdminSessionKind,
  AdminSessionSummarySchema
} from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { LoginPasswordResponseSchema } from "../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { AdminApiError } from "../src/api/errors";

const api = vi.hoisted(() => ({
  beginAdminLogin: vi.fn(),
  changeInitialPassword: vi.fn(),
  getCurrentAdminSession: vi.fn(),
  getSetupState: vi.fn(),
  loginPassword: vi.fn(),
  logoutAdmin: vi.fn(),
  verifyAdminRecoveryCode: vi.fn(),
  verifyAdminTotp: vi.fn()
}));

vi.mock("../src/api/admin-auth", () => api);
vi.mock("../src/api/connect", () => ({
  createRequestId: () => "request-1",
  installSessionInvalidHandler: vi.fn()
}));

import { useAuthStore } from "../src/stores/auth";

const futureSeconds = BigInt(Math.floor(Date.now() / 1000) + 3600);

const buildSession = (kind: AdminSessionKind = AdminSessionKind.FULL, adminId = "admin-1") =>
  create(AdminSessionSummarySchema, {
    adminId,
    sessionId: `session-${adminId}`,
    kind,
    permissions: [1, 10],
    adminVersion: 5n,
    passwordVersion: 3n,
    sessionVersion: 2n,
    idleExpiresAt: create(TimestampSchema, { seconds: futureSeconds }),
    absoluteExpiresAt: create(TimestampSchema, { seconds: futureSeconds + 3600n })
  });

describe("admin auth store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
  });

  it("restores the canonical session summary", async () => {
    api.getCurrentAdminSession.mockResolvedValue({ session: buildSession() });

    const store = useAuthStore();
    await store.restore();

    expect(store.restored).toBe(true);
    expect(store.session?.adminId).toBe("admin-1");
    expect(store.session?.kind).toBe(AdminSessionKind.FULL);
    expect(store.permissions).toEqual([1, 10]);
  });

  it("preserves a setup-password session during restore", async () => {
    api.getCurrentAdminSession.mockResolvedValue({
      session: buildSession(AdminSessionKind.SETUP_PASSWORD_PENDING)
    });

    const store = useAuthStore();
    await store.restore();

    expect(store.session?.kind).toBe(AdminSessionKind.SETUP_PASSWORD_PENDING);
  });

  it("falls back to setup state only for a stable invalid-session error", async () => {
    api.getCurrentAdminSession.mockRejectedValue(
      new AdminApiError({
        message: "invalid session",
        status: 401,
        code: "unauthenticated",
        businessKey: "admin.auth.invalid"
      })
    );
    api.getSetupState.mockResolvedValue({ state: AdminAccountState.BOOTSTRAP_PENDING });

    const store = useAuthStore();
    await store.restore();

    expect(store.session).toBeNull();
    expect(store.setupState).toBe(AdminAccountState.BOOTSTRAP_PENDING);
  });

  it.each([
    ["session", AdminSessionKind.FULL],
    ["requiresInitialPasswordChange", AdminSessionKind.SETUP_PASSWORD_PENDING],
    ["requiresMfa", AdminSessionKind.MFA_PENDING]
  ] as const)("applies the %s password-login outcome", async (outcome, kind) => {
    const session = buildSession(kind);
    api.beginAdminLogin.mockResolvedValue({ challenge: { challengeProof: "proof-1" } });
    api.loginPassword.mockResolvedValue(
      create(LoginPasswordResponseSchema, {
        outcome:
          outcome === "session"
            ? { case: "session", value: session }
            : { case: outcome, value: { session } }
      })
    );

    const store = useAuthStore();
    await store.submitPassword("secret");

    expect(api.loginPassword).toHaveBeenCalledWith(
      expect.objectContaining({
        challengeProof: "proof-1",
        password: "secret",
        requestFlowId: "request-1",
        signal: expect.any(AbortSignal)
      })
    );
    expect(store.session?.kind).toBe(kind);
  });

  it("changes the initial password without retaining it in the store", async () => {
    api.changeInitialPassword.mockResolvedValue({ session: buildSession() });

    const store = useAuthStore();
    await store.submitInitialPassword("new-secret-value");

    expect(api.changeInitialPassword).toHaveBeenCalledWith(
      expect.objectContaining({ newPassword: "new-secret-value", signal: expect.any(AbortSignal) })
    );
    expect(store.session?.kind).toBe(AdminSessionKind.FULL);
    expect("password" in store).toBe(false);
  });

  it("completes MFA with either TOTP or a recovery code", async () => {
    api.verifyAdminTotp.mockResolvedValue({ session: buildSession() });
    api.verifyAdminRecoveryCode.mockResolvedValue({ session: buildSession() });
    const store = useAuthStore();

    await store.submitTotp("123456");
    expect(api.verifyAdminTotp).toHaveBeenCalledWith(
      expect.objectContaining({ totpCode: "123456", signal: expect.any(AbortSignal) })
    );

    await store.submitRecoveryCode("recovery-code");
    expect(api.verifyAdminRecoveryCode).toHaveBeenCalledWith(
      expect.objectContaining({ recoveryCode: "recovery-code", signal: expect.any(AbortSignal) })
    );
    expect(store.session?.kind).toBe(AdminSessionKind.FULL);
  });

  it("clears the local session even when logout delivery fails", async () => {
    api.getCurrentAdminSession.mockResolvedValue({ session: buildSession() });
    api.logoutAdmin.mockRejectedValue(new Error("offline"));
    const store = useAuthStore();
    await store.restore();

    await expect(store.logoutCurrentSession()).rejects.toThrow("offline");
    expect(store.session).toBeNull();
  });

  it("commits only the newest concurrent login response", async () => {
    let resolveFirst!: () => void;
    let resolveSecond!: () => void;
    const firstGate = new Promise<void>((resolve) => {
      resolveFirst = resolve;
    });
    const secondGate = new Promise<void>((resolve) => {
      resolveSecond = resolve;
    });
    api.beginAdminLogin.mockResolvedValue({ challenge: { challengeProof: "proof-1" } });
    api.loginPassword
      .mockImplementationOnce(async () => {
        await firstGate;
        return create(LoginPasswordResponseSchema, {
          outcome: { case: "session", value: buildSession(AdminSessionKind.FULL, "admin-1") }
        });
      })
      .mockImplementationOnce(async () => {
        await secondGate;
        return create(LoginPasswordResponseSchema, {
          outcome: { case: "session", value: buildSession(AdminSessionKind.FULL, "admin-2") }
        });
      });

    const store = useAuthStore();
    await store.startLogin();
    const first = store.submitPassword("first");
    const second = store.submitPassword("second");
    resolveSecond();
    await second;
    resolveFirst();
    await first;

    expect(store.session?.adminId).toBe("admin-2");
  });
});
