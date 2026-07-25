import { computed, ref, shallowRef } from "vue";
import { defineStore } from "pinia";
import { AdminAccountState, AdminSessionKind, type AdminPermission, type AdminSessionSummary } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import type { AnonymousChallenge } from "../../../../contracts/gen/ts/platform/common/v1/common_pb";
import {
  beginAdminLogin,
  changeInitialPassword,
  getCurrentAdminSession,
  getSetupState,
  loginPassword,
  logoutAdmin,
  verifyAdminRecoveryCode,
  verifyAdminTotp
} from "../api/admin-auth";
import { createRequestId, installSessionInvalidHandler } from "../api/connect";
import { AdminApiError } from "../api/errors";

export const useAuthStore = defineStore("admin-auth", () => {
  // Restoration state
  const restored = ref(false);
  const restoring = ref(false);

  // Session state - only session summary from backend
  const session = ref<AdminSessionSummary | null>(null);
  const setupState = ref<AdminAccountState>(AdminAccountState.ACTIVE);

  // Login flow state
  const challenge = ref<AnonymousChallenge | null>(null);
  const requestFlowId = ref("");

  // UI state
  const errorMessage = ref("");

  // Generation token for latest-response-wins guard
  const generation = ref(0);
  const activeController = shallowRef<AbortController | null>(null);

  // Computed properties
  const permissions = computed<AdminPermission[]>(() => session.value?.permissions ?? []);
  const isAuthenticated = computed(() => session.value?.kind === AdminSessionKind.FULL);

  /**
   * Clears sensitive data from memory.
   * Must be called on logout, route change, or abort.
   */
  const clearSensitive = (): void => {
    challenge.value = null;
    requestFlowId.value = "";
  };

  /**
   * Updates session state from backend response.
   */
  const applySession = (current: AdminSessionSummary | null): void => {
    session.value = current;
  };

  /**
   * Starts a new request generation and returns token + signal.
   * Aborts any previous in-flight request.
   */
  const beginRequest = (): { token: number; signal: AbortSignal } => {
    generation.value += 1;
    activeController.value?.abort();
    const controller = new AbortController();
    activeController.value = controller;
    return { token: generation.value, signal: controller.signal };
  };

  /**
   * Guards against stale responses by checking generation token.
   * Only commits if the token matches the current generation.
   */
  const guardCommit = (token: number, commit: () => void): void => {
    if (token !== generation.value) {
      return;
    }
    commit();
  };

  /**
   * Clears all state and shows error message.
   * Used when restore or critical operations fail.
   */
  const failClosed = (message: string): void => {
    clearSensitive();
    session.value = null;
    errorMessage.value = message;
  };

  /**
   * Handles session invalidation from backend (401 with admin.auth.invalid).
   * Clears session state but preserves setup state.
   */
  const handleSessionLoss = (): void => {
    clearSensitive();
    session.value = null;
  };

  // Install session invalid handler for connect layer
  installSessionInvalidHandler(handleSessionLoss);

  /**
   * Restores session state on page load.
   * Attempts GetCurrentAdminSession first, falls back to GetSetupState on auth error.
   */
  const restore = async (): Promise<void> => {
    const { token, signal } = beginRequest();
    restoring.value = true;
    errorMessage.value = "";

    try {
      const current = await getCurrentAdminSession({ signal });
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        applySession(current.session ?? null);
        restored.value = true;
        restoring.value = false;
      });
      return;
    } catch (error) {
      if (signal.aborted) {
        return;
      }
      // Only fall back to setup state if the error is explicit session invalid
      if (!(error instanceof AdminApiError) || error.businessKey !== "admin.auth.invalid") {
        guardCommit(token, () => {
          failClosed("会话恢复失败，请稍后重试。");
          restored.value = true;
          restoring.value = false;
        });
        return;
      }
    }

    // Session invalid, check setup state
    try {
      const state = await getSetupState({ signal });
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        setupState.value = state.state;
        session.value = null;
        restored.value = true;
        restoring.value = false;
      });
    } catch {
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        failClosed("无法获取初始化状态，请稍后重试。");
        restored.value = true;
        restoring.value = false;
      });
    }
  };

  /**
   * Starts login flow by requesting anonymous challenge.
   */
  const startLogin = async (): Promise<void> => {
    const { token, signal } = beginRequest();
    const flowId = createRequestId();
    try {
      const response = await beginAdminLogin(flowId, { signal });
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        requestFlowId.value = flowId;
        challenge.value = response.challenge ?? null;
        errorMessage.value = "";
      });
    } catch (error) {
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        errorMessage.value = error instanceof AdminApiError ? error.message : "登录失败，请稍后重试。";
      });
    }
  };

  /**
   * Submits password for login.
   * Handles three explicit outcomes: full session, requires_initial_password_change, requires_mfa.
   */
  const submitPassword = async (password: string): Promise<void> => {
    if (!challenge.value?.challengeProof || !requestFlowId.value) {
      await startLogin();
    }
    const { token, signal } = beginRequest();
    try {
      const response = await loginPassword({
        challengeProof: challenge.value?.challengeProof ?? "",
        password,
        requestFlowId: requestFlowId.value,
        signal
      });
      if (signal.aborted) {
        return;
      }

      guardCommit(token, () => {
        switch (response.outcome.case) {
          case "session":
            // Full session - user authenticated without additional steps
            applySession(response.outcome.value);
            clearSensitive();
            errorMessage.value = "";
            break;
          case "requiresInitialPasswordChange":
            // User must change initial password
            applySession(response.outcome.value.session ?? null);
            clearSensitive();
            errorMessage.value = "";
            break;
          case "requiresMfa":
            // User must complete MFA challenge
            applySession(response.outcome.value.session ?? null);
            clearSensitive();
            errorMessage.value = "";
            break;
          default:
            errorMessage.value = "登录响应格式错误。";
        }
      });
    } catch (error) {
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        errorMessage.value = error instanceof AdminApiError ? error.message : "登录失败，请稍后重试。";
      });
    }
  };

  /**
   * Submits new password for initial password change.
   * Transitions from SETUP_PASSWORD_PENDING to FULL session.
   */
  const submitInitialPassword = async (newPassword: string): Promise<void> => {
    const { token, signal } = beginRequest();
    try {
      const response = await changeInitialPassword({ newPassword, signal });
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        applySession(response.session ?? null);
        clearSensitive();
        errorMessage.value = "";
      });
    } catch (error) {
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        errorMessage.value = error instanceof AdminApiError ? error.message : "密码修改失败，请稍后重试。";
      });
    }
  };

  /**
   * Submits TOTP code for MFA verification.
   * Transitions from MFA_PENDING to FULL session.
   */
  const submitTotp = async (totpCode: string): Promise<void> => {
    const { token, signal } = beginRequest();
    try {
      const response = await verifyAdminTotp({ totpCode, signal });
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        applySession(response.session ?? null);
        clearSensitive();
        errorMessage.value = "";
      });
    } catch (error) {
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        errorMessage.value = error instanceof AdminApiError ? error.message : "验证码错误，请重试。";
      });
    }
  };

  /**
   * Submits recovery code for MFA verification.
   * Transitions from MFA_PENDING to FULL session.
   */
  const submitRecoveryCode = async (recoveryCode: string): Promise<void> => {
    const { token, signal } = beginRequest();
    try {
      const response = await verifyAdminRecoveryCode({ recoveryCode, signal });
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        applySession(response.session ?? null);
        clearSensitive();
        errorMessage.value = "";
      });
    } catch (error) {
      if (signal.aborted) {
        return;
      }
      guardCommit(token, () => {
        errorMessage.value = error instanceof AdminApiError ? error.message : "恢复码错误，请重试。";
      });
    }
  };

  /**
   * Logs out current session.
   * Clears all state and sensitive data.
   */
  const logoutCurrentSession = async (): Promise<void> => {
    const { signal } = beginRequest();
    try {
      await logoutAdmin({ signal });
    } finally {
      handleSessionLoss();
    }
  };

  return {
    // State
    restored,
    restoring,
    session,
    setupState,
    challenge,
    requestFlowId,
    errorMessage,
    generation,
    activeController,

    // Computed
    permissions,
    isAuthenticated,

    // Actions
    restore,
    startLogin,
    submitPassword,
    submitInitialPassword,
    submitTotp,
    submitRecoveryCode,
    logoutCurrentSession,
    clearSensitive,
    applySession
  };
});
