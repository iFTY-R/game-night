import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { AdminNextStep, AdminSecretOperation, AdminSessionKind, AdminSetupState, type AdminPermission, type AdminSessionSummary } from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import type { AnonymousChallenge, OperationResult } from "../../../../contracts/gen/ts/platform/common/v1/common_pb";
import {
  beginAdminLogin,
  beginTotpEnrollment,
  beginTotpRebind,
  changeInitialPassword,
  completeTotpEnrollment,
  completeTotpRebind,
  confirmAdminSecretReceipt,
  getCurrentAdminSession,
  getSetupState,
  loginPassword,
  logoutAdmin,
  logoutAllAdminSessions,
  recoverAdmin,
  verifyTotp
} from "../api/admin-auth";
import { createRequestId, installSessionInvalidHandler } from "../api/connect";
import { AdminApiError } from "../api/errors";

type AuthStep = "login" | "bootstrap" | "changePassword" | "enrollTotp" | "verifyMfa" | "rebindTotp" | "authenticated";

type SecretEnvelope = {
  operation: AdminSecretOperation;
  result: OperationResult | null;
  totpSecret?: string;
  otpauthUri?: string;
  recoveryCodes: string[];
};

const nextStepFromKind = (kind: AdminSessionKind): AdminNextStep => {
  switch (kind) {
    case AdminSessionKind.SETUP_PASSWORD_PENDING:
      return AdminNextStep.CHANGE_PASSWORD;
    case AdminSessionKind.TOTP_ENROLLMENT_PENDING:
      return AdminNextStep.ENROLL_TOTP;
    case AdminSessionKind.MFA_PENDING:
      return AdminNextStep.VERIFY_MFA;
    case AdminSessionKind.RECOVERY_PENDING:
      return AdminNextStep.REBIND_TOTP;
    case AdminSessionKind.FULL:
      return AdminNextStep.AUTHENTICATED;
    default:
      return AdminNextStep.UNSPECIFIED;
  }
};

const nextStepToUi = (nextStep: AdminNextStep, setupState: AdminSetupState): AuthStep => {
  switch (nextStep) {
    case AdminNextStep.CHANGE_PASSWORD:
      return "changePassword";
    case AdminNextStep.ENROLL_TOTP:
      return "enrollTotp";
    case AdminNextStep.VERIFY_MFA:
      return "verifyMfa";
    case AdminNextStep.REBIND_TOTP:
      return "rebindTotp";
    case AdminNextStep.AUTHENTICATED:
      return "authenticated";
    default:
      return setupState === AdminSetupState.BOOTSTRAP_PENDING ? "bootstrap" : "login";
  }
};

export const useAuthStore = defineStore("admin-auth", () => {
  const restored = ref(false);
  const restoring = ref(false);
  const session = ref<AdminSessionSummary | null>(null);
  const setupState = ref<AdminSetupState>(AdminSetupState.ACTIVE);
  const nextStep = ref<AdminNextStep>(AdminNextStep.UNSPECIFIED);
  const currentStep = ref<AuthStep>("login");
  const challenge = ref<AnonymousChallenge | null>(null);
  const requestFlowId = ref("");
  const secretEnvelope = ref<SecretEnvelope | null>(null);
  const errorMessage = ref("");
  const generation = ref(0);
  const activeController = ref<AbortController | null>(null);

  const permissions = computed<AdminPermission[]>(() => session.value?.permissions ?? []);
  const isRestricted = computed(() => session.value != null && session.value.kind !== AdminSessionKind.FULL);

  const clearSensitive = (): void => {
    challenge.value = null;
    requestFlowId.value = "";
    secretEnvelope.value = null;
  };

  const applySession = (current: AdminSessionSummary | null, currentNextStep: AdminNextStep): void => {
    session.value = current;
    nextStep.value = currentNextStep;
    currentStep.value = nextStepToUi(currentNextStep, setupState.value);
  };

  const beginRequest = (): { token: number; signal: AbortSignal } => {
    generation.value += 1;
    activeController.value?.abort();
    const controller = new AbortController();
    activeController.value = controller;
    return { token: generation.value, signal: controller.signal };
  };

  const guardCommit = (token: number, commit: () => void): void => {
    if (token !== generation.value) {
      return;
    }
    commit();
  };

  const syncAuthenticatedSession = async (token: number, signal: AbortSignal): Promise<void> => {
    // Some auth mutations only confirm the next step and require a follow-up self introspection
    // to hydrate the full admin session before the UI enters the unrestricted backend.
    const current = await getCurrentAdminSession();
    if (signal.aborted) {
      return;
    }
    guardCommit(token, () => {
      applySession(current.session ?? null, AdminNextStep.AUTHENTICATED);
      clearSensitive();
      errorMessage.value = "";
    });
  };

  const failClosed = (message: string): void => {
    clearSensitive();
    session.value = null;
    nextStep.value = AdminNextStep.UNSPECIFIED;
    currentStep.value = nextStepToUi(AdminNextStep.UNSPECIFIED, setupState.value);
    errorMessage.value = message;
  };

  const handleSessionLoss = (): void => {
    clearSensitive();
    session.value = null;
    nextStep.value = AdminNextStep.UNSPECIFIED;
    currentStep.value = nextStepToUi(AdminNextStep.UNSPECIFIED, setupState.value);
  };

  installSessionInvalidHandler(handleSessionLoss);

  const restore = async (): Promise<void> => {
    const { token, signal } = beginRequest();
    restoring.value = true;
    errorMessage.value = "";
    try {
      const current = await getCurrentAdminSession();
      guardCommit(token, () => {
        applySession(current.session ?? null, current.nextStep);
        restored.value = true;
        restoring.value = false;
      });
      return;
    } catch (error) {
      if (!(error instanceof AdminApiError) || error.businessKey !== "admin.auth.invalid") {
        guardCommit(token, () => {
          failClosed("会话恢复失败，请稍后重试。");
          restored.value = true;
          restoring.value = false;
        });
        return;
      }
    }
    try {
      const state = await getSetupState();
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        setupState.value = state.state;
        currentStep.value = nextStepToUi(AdminNextStep.UNSPECIFIED, state.state);
        session.value = null;
        nextStep.value = AdminNextStep.UNSPECIFIED;
        restored.value = true;
      });
    } finally {
      guardCommit(token, () => {
        restoring.value = false;
      });
    }
  };

  const startLogin = async (): Promise<void> => {
    const { token, signal } = beginRequest();
    const flowId = createRequestId();
    const response = await beginAdminLogin(flowId);
    if (signal.aborted) {
      return;
    }
    guardCommit(token, () => {
      requestFlowId.value = flowId;
      challenge.value = response.challenge ?? null;
      currentStep.value = "login";
      errorMessage.value = "";
    });
  };

  const submitPassword = async (password: string): Promise<void> => {
    if (!challenge.value?.challengeProof || !requestFlowId.value) {
      await startLogin();
    }
    const { token, signal } = beginRequest();
    const response = await loginPassword({
      challengeProof: challenge.value?.challengeProof ?? "",
      password,
      requestFlowId: requestFlowId.value
    });
    if (signal.aborted) {
      return;
    }
    if (response.nextStep === AdminNextStep.AUTHENTICATED) {
      await syncAuthenticatedSession(token, signal);
      return;
    }
    guardCommit(token, () => {
      nextStep.value = response.nextStep;
      currentStep.value = nextStepToUi(response.nextStep, setupState.value);
      errorMessage.value = "";
    });
  };

  const submitInitialPassword = async (newPassword: string): Promise<void> => {
    const { token, signal } = beginRequest();
    const response = await changeInitialPassword(newPassword);
    if (signal.aborted) {
      return;
    }
    if (response.nextStep === AdminNextStep.AUTHENTICATED) {
      await syncAuthenticatedSession(token, signal);
      return;
    }
    guardCommit(token, () => {
      nextStep.value = response.nextStep;
      currentStep.value = nextStepToUi(response.nextStep, setupState.value);
      clearSensitive();
    });
  };

  const openTotpEnrollment = async (): Promise<void> => {
    const response = await beginTotpEnrollment(createRequestId());
    secretEnvelope.value = {
      operation: AdminSecretOperation.TOTP_ENROLLMENT,
      result: response.result ?? null,
      totpSecret: response.totpSecret,
      otpauthUri: response.otpauthUri,
      recoveryCodes: []
    };
    currentStep.value = "enrollTotp";
  };

  const finishTotpEnrollment = async (totpCode: string): Promise<void> => {
    const current = secretEnvelope.value;
    if (!current?.result?.operationId) {
      throw new Error("missing_enrollment_operation");
    }
    const response = await completeTotpEnrollment({
      enrollmentOperationId: current.result.operationId,
      recoveryCodesOperationId: createRequestId(),
      totpCode
    });
    session.value = response.session ?? null;
    nextStep.value = response.session?.kind ? nextStepFromKind(response.session.kind) : AdminNextStep.AUTHENTICATED;
    secretEnvelope.value = {
      operation: AdminSecretOperation.INITIAL_RECOVERY_CODES,
      result: response.result ?? null,
      recoveryCodes: [...response.recoveryCodes]
    };
  };

  const submitTotp = async (totpCode: string): Promise<void> => {
    const response = await verifyTotp(totpCode);
    const current = response.session ?? null;
    applySession(current, current?.kind ? nextStepFromKind(current.kind) : AdminNextStep.AUTHENTICATED);
    clearSensitive();
  };

  const submitRecoveryCode = async (recoveryCode: string): Promise<void> => {
    const response = await recoverAdmin(recoveryCode);
    applySession(response.session ?? null, response.nextStep);
    clearSensitive();
  };

  const openTotpRebind = async (): Promise<void> => {
    const response = await beginTotpRebind(createRequestId());
    secretEnvelope.value = {
      operation: AdminSecretOperation.TOTP_REBIND,
      result: response.result ?? null,
      totpSecret: response.totpSecret,
      otpauthUri: response.otpauthUri,
      recoveryCodes: []
    };
    currentStep.value = "rebindTotp";
  };

  const finishTotpRebind = async (totpCode: string): Promise<void> => {
    const current = secretEnvelope.value;
    if (!current?.result?.operationId) {
      throw new Error("missing_rebind_operation");
    }
    const response = await completeTotpRebind({
      enrollmentOperationId: current.result.operationId,
      recoveryCodesOperationId: createRequestId(),
      totpCode
    });
    session.value = response.session ?? null;
    nextStep.value = response.session?.kind ? nextStepFromKind(response.session.kind) : AdminNextStep.AUTHENTICATED;
    secretEnvelope.value = {
      operation: AdminSecretOperation.REGENERATE_RECOVERY_CODES,
      result: response.result ?? null,
      recoveryCodes: [...response.recoveryCodes]
    };
  };

  const acknowledgeSecretReceipt = async (): Promise<void> => {
    const current = secretEnvelope.value;
    if (!current?.result?.operationId || !current.result.resultId) {
      return;
    }
    await confirmAdminSecretReceipt({
      operation: current.operation,
      operationId: current.result.operationId,
      resultId: current.result.resultId
    });
    clearSensitive();
    currentStep.value = nextStepToUi(nextStep.value, setupState.value);
  };

  const logoutCurrentSession = async (): Promise<void> => {
    await logoutAdmin();
    handleSessionLoss();
  };

  const logoutEverySession = async (): Promise<number> => {
    const revoked = await logoutAllAdminSessions();
    handleSessionLoss();
    return revoked;
  };

  return {
    acknowledgeSecretReceipt,
    challenge,
    clearSensitive,
    currentStep,
    errorMessage,
    finishTotpEnrollment,
    finishTotpRebind,
    isRestricted,
    logoutCurrentSession,
    logoutEverySession,
    nextStep,
    openTotpEnrollment,
    openTotpRebind,
    permissions,
    requestFlowId,
    restore,
    restored,
    restoring,
    secretEnvelope,
    session,
    setupState,
    startLogin,
    submitInitialPassword,
    submitPassword,
    submitRecoveryCode,
    submitTotp
  };
});
