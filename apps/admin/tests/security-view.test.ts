import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NButton } from "naive-ui";
import { defineComponent, h, ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  AdminElevationScope,
  AdminElevationSummarySchema,
  AdminSessionInfoSchema,
  AdminSessionKind,
  AdminSessionSummarySchema
} from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";

const api = vi.hoisted(() => ({
  beginTotpEnrollment: vi.fn(),
  changeAdminPassword: vi.fn(),
  completeTotpEnrollment: vi.fn(),
  confirmAdminSecretReceipt: vi.fn(),
  disableTotp: vi.fn(),
  elevateAdminSession: vi.fn(),
  listAdminSessions: vi.fn(),
  previewRevokeOtherAdminSessions: vi.fn(),
  regenerateAdminRecoveryCodes: vi.fn(),
  revokeAdminSession: vi.fn(),
  revokeOtherAdminSessions: vi.fn()
}));

vi.mock("../src/api/admin-auth", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../src/api/admin-auth")>()),
  ...api
}));
vi.mock("../src/api/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../src/api/connect")>()),
  createRequestId: () => "operation-1"
}));

import { useAuthStore } from "../src/stores/auth";
import AdminSessionsTable from "../src/views/security/components/AdminSessionsTable.vue";
import ElevationDialog from "../src/views/security/components/ElevationDialog.vue";
import RevokeOtherSessionsDialog from "../src/views/security/components/RevokeOtherSessionsDialog.vue";
import SessionSecurityView from "../src/views/security/SessionSecurityView.vue";

const expiresAt = create(TimestampSchema, { seconds: 2_000_000_000n });

const buildSession = (mfaEnabled: boolean) =>
  create(AdminSessionSummarySchema, {
    adminId: "admin-1",
    sessionId: "session-current",
    kind: AdminSessionKind.FULL,
    permissions: [12, 13, 14, 15],
    adminVersion: 5n,
    passwordVersion: 3n,
    sessionVersion: 2n,
    idleExpiresAt: expiresAt,
    absoluteExpiresAt: expiresAt,
    mfa: {
      enabled: mfaEnabled,
      enrollmentVersion: mfaEnabled ? 4n : 0n,
      recoveryCodesVersion: mfaEnabled ? 2n : 0n,
      recoveryCodesRemaining: mfaEnabled ? 7 : 0
    }
  });

const AppDialogStub = defineComponent({
  name: "AppDialog",
  emits: ["closed"],
  setup(_props, { emit, expose, slots }) {
    const open = ref(false);
    const toggleDialog = (next: boolean): void => {
      const wasOpen = open.value;
      open.value = next;
      if (wasOpen && !next) {
        emit("closed");
      }
    };
    expose({ toggleDialog });
    return () =>
      open.value
        ? h("section", { "data-testid": "dialog" }, slots.default?.({ close: () => toggleDialog(false) }))
        : null;
  }
});

const deferred = <T>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((fulfill) => {
    resolve = fulfill;
  });
  return { promise, resolve };
};

const securityViewStubs = {
  AdminSessionsTable: true,
  ChangePasswordDialog: true,
  DisableTotpDialog: true,
  RecoveryCodesDialog: true,
  RevokeOtherSessionsDialog: true,
  TotpSetupDialog: true
};

describe("security settings", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
  });

  it("shows account-level MFA state without retaining authentication secrets", () => {
    const auth = useAuthStore();
    auth.session = buildSession(true);

    const wrapper = mount(SessionSecurityView, { global: { stubs: securityViewStubs } });

    expect(wrapper.text()).toContain("已启用");
    expect(wrapper.text()).toContain("7 / 10");
    expect(wrapper.text()).toContain("停用 MFA");
    expect("password" in auth).toBe(false);
    expect("totpCode" in auth).toBe(false);
    expect("recoveryCodes" in auth).toBe(false);
  });

  it("offers enrollment when MFA is disabled", () => {
    const auth = useAuthStore();
    auth.session = buildSession(false);

    const wrapper = mount(SessionSecurityView, { global: { stubs: securityViewStubs } });

    expect(wrapper.text()).toContain("未启用");
    expect(wrapper.text()).toContain("启用 MFA");
    expect(wrapper.text()).not.toContain("停用 MFA");
  });

  it("loads the server-owned session list", async () => {
    api.listAdminSessions.mockResolvedValue({
      sessions: [
        create(AdminSessionInfoSchema, {
          sessionId: "session-current",
          current: true,
          kind: AdminSessionKind.FULL,
          sessionVersion: 2n,
          createdAt: expiresAt,
          lastActivityAt: expiresAt,
          idleExpiresAt: expiresAt,
          absoluteExpiresAt: expiresAt,
          clientIp: "127.0.0.1",
          userAgent: "test-browser"
        })
      ]
    });

    mount(AdminSessionsTable);
    await flushPromises();

    expect(api.listAdminSessions).toHaveBeenCalledWith({ signal: expect.any(AbortSignal) });
  });

  it("uses password-only elevation while MFA is disabled", async () => {
    const auth = useAuthStore();
    auth.session = buildSession(false);
    const elevation = create(AdminElevationSummarySchema, {
      scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
      expiresAt
    });
    api.elevateAdminSession.mockResolvedValue({ elevation });
    const onElevated = vi.fn();

    const wrapper = mount(ElevationDialog, { global: { stubs: { AppDialog: AppDialogStub } } });
    (wrapper.vm as unknown as { toggleDialog: (open: boolean, payload: object) => void }).toggleDialog(true, {
      scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
      allowRecoveryCode: false,
      onElevated
    });
    await flushPromises();

    const inputs = wrapper.findAll("input");
    expect(inputs).toHaveLength(1);
    await inputs[0]!.setValue("correct horse battery staple");
    const submit = wrapper.findAll("button").find((button) => button.text().includes("提升权限"));
    await submit!.trigger("click");
    await flushPromises();

    expect(api.elevateAdminSession).toHaveBeenCalledWith({
      operationId: "operation-1",
      scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
      currentPassword: "correct horse battery staple",
      signal: expect.any(AbortSignal)
    });
    expect(onElevated).toHaveBeenCalledWith(elevation);
  });

  it("requires TOTP for a non-substitutable elevation while MFA is enabled", async () => {
    const auth = useAuthStore();
    auth.session = buildSession(true);
    const elevation = create(AdminElevationSummarySchema, {
      scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
      expiresAt
    });
    api.elevateAdminSession.mockResolvedValue({ elevation });

    const wrapper = mount(ElevationDialog, { global: { stubs: { AppDialog: AppDialogStub } } });
    (wrapper.vm as unknown as { toggleDialog: (open: boolean, payload: object) => void }).toggleDialog(true, {
      scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
      allowRecoveryCode: false,
      onElevated: vi.fn()
    });
    await flushPromises();

    const inputs = wrapper.findAll("input");
    expect(inputs).toHaveLength(2);
    await inputs[0]!.setValue("correct horse battery staple");
    await inputs[1]!.setValue("123456");
    const submit = wrapper.findAll("button").find((button) => button.text().includes("提升权限"));
    await submit!.trigger("click");
    await flushPromises();

    expect(api.elevateAdminSession).toHaveBeenCalledWith({
      operationId: "operation-1",
      scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
      currentPassword: "correct horse battery staple",
      totpCode: "123456",
      signal: expect.any(AbortSignal)
    });
  });

  it("keeps a reopened elevation request busy when the aborted request settles late", async () => {
    const auth = useAuthStore();
    auth.applySession(buildSession(false));
    const elevation = create(AdminElevationSummarySchema, {
      scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
      expiresAt
    });
    const first = deferred<{ elevation: typeof elevation }>();
    const second = deferred<{ elevation: typeof elevation }>();
    api.elevateAdminSession.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const firstCallback = vi.fn();
    const secondCallback = vi.fn();

    const wrapper = mount(ElevationDialog, { global: { stubs: { AppDialog: AppDialogStub } } });
    const dialog = wrapper.vm as unknown as { toggleDialog: (open: boolean, payload?: object) => void };
    dialog.toggleDialog(true, {
      scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
      allowRecoveryCode: false,
      onElevated: firstCallback
    });
    await flushPromises();
    await wrapper.find("input").setValue("first password");
    await wrapper.findAll("button").find((button) => button.text().includes("提升权限"))!.trigger("click");
    await flushPromises();
    expect(api.elevateAdminSession).toHaveBeenCalledTimes(1);

    dialog.toggleDialog(false);
    await flushPromises();
    dialog.toggleDialog(true, {
      scope: AdminElevationScope.SECURITY_REVOKE_SESSIONS,
      allowRecoveryCode: false,
      onElevated: secondCallback
    });
    await flushPromises();
    await wrapper.find("input").setValue("second password");
    const reopenedSubmit = wrapper.findAllComponents(NButton).find((button) => button.text().includes("提升权限"));
    expect(reopenedSubmit?.props("loading")).toBe(false);
    await wrapper.findAll("button").find((button) => button.text().includes("提升权限"))!.trigger("click");
    await flushPromises();
    expect(api.elevateAdminSession).toHaveBeenCalledTimes(2);

    first.resolve({ elevation });
    await flushPromises();

    const activeSubmit = wrapper.findAllComponents(NButton).find((button) => button.text().includes("提升权限"));
    expect(activeSubmit?.props("loading")).toBe(true);
    expect(firstCallback).not.toHaveBeenCalled();

    second.resolve({ elevation });
    await flushPromises();
    expect(secondCallback).toHaveBeenCalledWith(elevation);
  });

  it("uses preview versions and TOTP-only elevation before revoking other sessions", async () => {
    const auth = useAuthStore();
    auth.session = buildSession(true);
    api.previewRevokeOtherAdminSessions.mockResolvedValue({
      previewVersion: "preview-v1",
      currentAdminVersion: 8n,
      currentSessionVersion: 6n,
      otherSessionCount: 1,
      sessions: [
        create(AdminSessionInfoSchema, {
          sessionId: "session-other",
          kind: AdminSessionKind.FULL,
          sessionVersion: 3n,
          createdAt: expiresAt,
          lastActivityAt: expiresAt,
          idleExpiresAt: expiresAt,
          absoluteExpiresAt: expiresAt
        })
      ]
    });
    api.revokeOtherAdminSessions.mockResolvedValue({
      operationId: "operation-1",
      revokedSessions: 1,
      session: buildSession(true)
    });

    const ElevationDialogStub = defineComponent({
      name: "ElevationDialog",
      setup(_props, { expose }) {
        expose({
          toggleDialog: (open: boolean, payload?: { scope: AdminElevationScope; allowRecoveryCode: boolean; onElevated: (value: object) => void }) => {
            if (open && payload) {
              expect(payload.scope).toBe(AdminElevationScope.SECURITY_REVOKE_SESSIONS);
              expect(payload.allowRecoveryCode).toBe(false);
              payload.onElevated({ scope: payload.scope });
            }
          }
        });
        return () => null;
      }
    });

    const wrapper = mount(RevokeOtherSessionsDialog, {
      global: { stubs: { AppDialog: AppDialogStub, ElevationDialog: ElevationDialogStub } }
    });
    (wrapper.vm as unknown as { toggleDialog: (open: boolean) => void }).toggleDialog(true);
    await flushPromises();

    const proceed = wrapper.findAll("button").find((button) => button.text().includes("继续撤销"));
    expect(proceed).toBeDefined();
    await proceed!.trigger("click");
    await flushPromises();

    expect(api.revokeOtherAdminSessions).toHaveBeenCalledWith({
      operationId: "operation-1",
      previewVersion: "preview-v1",
      expectedAdminVersion: 8n,
      expectedCurrentSessionVersion: 6n,
      signal: expect.any(AbortSignal)
    });
  });
});
