import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AdminNextStep, AdminSessionKind } from "../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { AdminSessionSummarySchema } from "../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";

const buildSession = () =>
  create(AdminSessionSummarySchema, {
    adminId: "admin-1",
    kind: AdminSessionKind.FULL,
    permissions: [1, 10],
    idleExpiresAt: create(TimestampSchema, { seconds: 1n }),
    absoluteExpiresAt: create(TimestampSchema, { seconds: 2n })
  });

describe("auth store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  afterEach(() => {
    vi.resetModules();
  });

  it("restores a full session through current-session self introspection", async () => {
    vi.doMock("../src/api/admin-auth", () => ({
      beginAdminLogin: vi.fn(),
      beginTotpEnrollment: vi.fn(),
      beginTotpRebind: vi.fn(),
      changeInitialPassword: vi.fn(),
      completeTotpEnrollment: vi.fn(),
      completeTotpRebind: vi.fn(),
      confirmAdminSecretReceipt: vi.fn(),
      getCurrentAdminSession: vi.fn(async () => ({ session: buildSession(), nextStep: AdminNextStep.AUTHENTICATED })),
      getSetupState: vi.fn(),
      loginPassword: vi.fn(),
      logoutAdmin: vi.fn(),
      logoutAllAdminSessions: vi.fn(),
      recoverAdmin: vi.fn(),
      verifyTotp: vi.fn()
    }));
    vi.doMock("../src/api/connect", () => ({
      createRequestId: () => "req-1",
      installSessionInvalidHandler: vi.fn()
    }));

    const { useAuthStore } = await import("../src/stores/auth");
    const store = useAuthStore();
    await store.restore();

    expect(store.restored).toBe(true);
    expect(store.currentStep).toBe("authenticated");
    expect(store.session?.adminId).toBe("admin-1");
  });

  it("falls back to setup state only when the current session is explicitly unauthenticated", async () => {
    const { AdminApiError } = await import("../src/api/errors");
    vi.doMock("../src/api/admin-auth", () => ({
      beginAdminLogin: vi.fn(),
      beginTotpEnrollment: vi.fn(),
      beginTotpRebind: vi.fn(),
      changeInitialPassword: vi.fn(),
      completeTotpEnrollment: vi.fn(),
      completeTotpRebind: vi.fn(),
      confirmAdminSecretReceipt: vi.fn(),
      getCurrentAdminSession: vi.fn(async () => {
        throw new AdminApiError({
          message: "invalid session",
          status: 401,
          code: "unauthenticated",
          businessKey: "admin.auth.invalid"
        });
      }),
      getSetupState: vi.fn(async () => ({ state: 1 })),
      loginPassword: vi.fn(),
      logoutAdmin: vi.fn(),
      logoutAllAdminSessions: vi.fn(),
      recoverAdmin: vi.fn(),
      verifyTotp: vi.fn()
    }));
    vi.doMock("../src/api/connect", () => ({
      createRequestId: () => "req-1",
      installSessionInvalidHandler: vi.fn()
    }));

    const { useAuthStore } = await import("../src/stores/auth");
    const store = useAuthStore();
    await store.restore();

    expect(store.currentStep).toBe("bootstrap");
  });
});
