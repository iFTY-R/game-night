import {
  AdminSecretOperation,
  BeginAdminLoginRequestSchema,
  BeginAdminLoginResponseSchema,
  BeginTotpEnrollmentRequestSchema,
  BeginTotpEnrollmentResponseSchema,
  ChangeAdminPasswordRequestSchema,
  ChangeAdminPasswordResponseSchema,
  ChangeInitialPasswordRequestSchema,
  ChangeInitialPasswordResponseSchema,
  CompleteTotpEnrollmentRequestSchema,
  CompleteTotpEnrollmentResponseSchema,
  ConfirmAdminSecretReceiptRequestSchema,
  ConfirmAdminSecretReceiptResponseSchema,
  DisableTotpRequestSchema,
  DisableTotpResponseSchema,
  ElevateAdminSessionRequestSchema,
  ElevateAdminSessionResponseSchema,
  GetCurrentAdminSessionRequestSchema,
  GetCurrentAdminSessionResponseSchema,
  GetRuntimeReadinessRequestSchema,
  GetRuntimeReadinessResponseSchema,
  GetSetupStateRequestSchema,
  GetSetupStateResponseSchema,
  ListAdminSessionsRequestSchema,
  ListAdminSessionsResponseSchema,
  LoginPasswordRequestSchema,
  LoginPasswordResponseSchema,
  LogoutAdminRequestSchema,
  LogoutAdminResponseSchema,
  PreviewRevokeOtherAdminSessionsRequestSchema,
  PreviewRevokeOtherAdminSessionsResponseSchema,
  RegenerateAdminRecoveryCodesRequestSchema,
  RegenerateAdminRecoveryCodesResponseSchema,
  RevokeAdminSessionRequestSchema,
  RevokeAdminSessionResponseSchema,
  RevokeCurrentAdminElevationRequestSchema,
  RevokeCurrentAdminElevationResponseSchema,
  RevokeOtherAdminSessionsRequestSchema,
  RevokeOtherAdminSessionsResponseSchema,
  VerifyAdminRecoveryCodeRequestSchema,
  VerifyAdminRecoveryCodeResponseSchema,
  VerifyAdminTotpRequestSchema,
  VerifyAdminTotpResponseSchema,
  type BeginAdminLoginResponse,
  type BeginTotpEnrollmentResponse,
  type ChangeAdminPasswordResponse,
  type ChangeInitialPasswordResponse,
  type CompleteTotpEnrollmentResponse,
  type ConfirmAdminSecretReceiptResponse,
  type DisableTotpResponse,
  type ElevateAdminSessionResponse,
  type GetCurrentAdminSessionResponse,
  type GetRuntimeReadinessResponse,
  type GetSetupStateResponse,
  type ListAdminSessionsResponse,
  type LoginPasswordResponse,
  type LogoutAdminResponse,
  type PreviewRevokeOtherAdminSessionsResponse,
  type RegenerateAdminRecoveryCodesResponse,
  type RevokeAdminSessionResponse,
  type RevokeCurrentAdminElevationResponse,
  type RevokeOtherAdminSessionsResponse,
  type VerifyAdminRecoveryCodeResponse,
  type VerifyAdminTotpResponse
} from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import type { AdminElevationScope } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  callUnary,
  installSessionInvalidHandler,
  procedure,
  type UnaryOptions,
  type UnaryRequestPolicy
} from "./connect";

const authServiceName = "platform.admin.v1.AdminAuthService";

// Anonymous procedures neither trust a session nor require mutation audit correlation.
const anonymousRequest = { csrf: false, requestId: false } as const satisfies UnaryRequestPolicy;
// Password verification completes a challenge before a session exists, but the attempt is still audited.
const auditedChallengeRequest = { csrf: false, requestId: true } as const satisfies UnaryRequestPolicy;
// Session reads and operations with their own idempotency receipt require CSRF without a separate audit ID.
const sessionRequest = { csrf: true, requestId: false } as const satisfies UnaryRequestPolicy;
// Audited session mutations require both browser CSRF proof and a per-request correlation ID.
const auditedSessionRequest = { csrf: true, requestId: true } as const satisfies UnaryRequestPolicy;

// adminAuthRequestPolicies is kept exhaustive against the generated service descriptor by transport tests.
export const adminAuthRequestPolicies = {
  GetSetupState: anonymousRequest,
  GetCurrentAdminSession: sessionRequest,
  GetRuntimeReadiness: sessionRequest,
  BeginAdminLogin: anonymousRequest,
  LoginPassword: auditedChallengeRequest,
  VerifyAdminTotp: auditedSessionRequest,
  VerifyAdminRecoveryCode: auditedSessionRequest,
  ChangeInitialPassword: auditedSessionRequest,
  ChangeAdminPassword: auditedSessionRequest,
  BeginTotpEnrollment: auditedSessionRequest,
  CompleteTotpEnrollment: auditedSessionRequest,
  DisableTotp: auditedSessionRequest,
  RegenerateAdminRecoveryCodes: auditedSessionRequest,
  ConfirmAdminSecretReceipt: sessionRequest,
  ElevateAdminSession: auditedSessionRequest,
  RevokeCurrentAdminElevation: auditedSessionRequest,
  ListAdminSessions: sessionRequest,
  RevokeAdminSession: auditedSessionRequest,
  PreviewRevokeOtherAdminSessions: sessionRequest,
  RevokeOtherAdminSessions: auditedSessionRequest,
  LogoutAdmin: sessionRequest
} as const satisfies Record<string, UnaryRequestPolicy>;

const methodGetSetupState = procedure(
  authServiceName,
  "GetSetupState",
  GetSetupStateRequestSchema,
  GetSetupStateResponseSchema,
  adminAuthRequestPolicies.GetSetupState
);

const methodBeginAdminLogin = procedure(
  authServiceName,
  "BeginAdminLogin",
  BeginAdminLoginRequestSchema,
  BeginAdminLoginResponseSchema,
  adminAuthRequestPolicies.BeginAdminLogin
);

const methodGetCurrentAdminSession = procedure(
  authServiceName,
  "GetCurrentAdminSession",
  GetCurrentAdminSessionRequestSchema,
  GetCurrentAdminSessionResponseSchema,
  adminAuthRequestPolicies.GetCurrentAdminSession
);

const methodGetRuntimeReadiness = procedure(
  authServiceName,
  "GetRuntimeReadiness",
  GetRuntimeReadinessRequestSchema,
  GetRuntimeReadinessResponseSchema,
  adminAuthRequestPolicies.GetRuntimeReadiness
);

const methodLoginPassword = procedure(
  authServiceName,
  "LoginPassword",
  LoginPasswordRequestSchema,
  LoginPasswordResponseSchema,
  adminAuthRequestPolicies.LoginPassword
);

const methodVerifyAdminTotp = procedure(
  authServiceName,
  "VerifyAdminTotp",
  VerifyAdminTotpRequestSchema,
  VerifyAdminTotpResponseSchema,
  adminAuthRequestPolicies.VerifyAdminTotp
);

const methodVerifyAdminRecoveryCode = procedure(
  authServiceName,
  "VerifyAdminRecoveryCode",
  VerifyAdminRecoveryCodeRequestSchema,
  VerifyAdminRecoveryCodeResponseSchema,
  adminAuthRequestPolicies.VerifyAdminRecoveryCode
);

const methodChangeInitialPassword = procedure(
  authServiceName,
  "ChangeInitialPassword",
  ChangeInitialPasswordRequestSchema,
  ChangeInitialPasswordResponseSchema,
  adminAuthRequestPolicies.ChangeInitialPassword
);

const methodLogoutAdmin = procedure(
  authServiceName,
  "LogoutAdmin",
  LogoutAdminRequestSchema,
  LogoutAdminResponseSchema,
  adminAuthRequestPolicies.LogoutAdmin
);

const methodChangeAdminPassword = procedure(
  authServiceName,
  "ChangeAdminPassword",
  ChangeAdminPasswordRequestSchema,
  ChangeAdminPasswordResponseSchema,
  adminAuthRequestPolicies.ChangeAdminPassword
);

const methodBeginTotpEnrollment = procedure(
  authServiceName,
  "BeginTotpEnrollment",
  BeginTotpEnrollmentRequestSchema,
  BeginTotpEnrollmentResponseSchema,
  adminAuthRequestPolicies.BeginTotpEnrollment
);

const methodCompleteTotpEnrollment = procedure(
  authServiceName,
  "CompleteTotpEnrollment",
  CompleteTotpEnrollmentRequestSchema,
  CompleteTotpEnrollmentResponseSchema,
  adminAuthRequestPolicies.CompleteTotpEnrollment
);

const methodDisableTotp = procedure(
  authServiceName,
  "DisableTotp",
  DisableTotpRequestSchema,
  DisableTotpResponseSchema,
  adminAuthRequestPolicies.DisableTotp
);

const methodRegenerateAdminRecoveryCodes = procedure(
  authServiceName,
  "RegenerateAdminRecoveryCodes",
  RegenerateAdminRecoveryCodesRequestSchema,
  RegenerateAdminRecoveryCodesResponseSchema,
  adminAuthRequestPolicies.RegenerateAdminRecoveryCodes
);

const methodConfirmAdminSecretReceipt = procedure(
  authServiceName,
  "ConfirmAdminSecretReceipt",
  ConfirmAdminSecretReceiptRequestSchema,
  ConfirmAdminSecretReceiptResponseSchema,
  adminAuthRequestPolicies.ConfirmAdminSecretReceipt
);

const methodElevateAdminSession = procedure(
  authServiceName,
  "ElevateAdminSession",
  ElevateAdminSessionRequestSchema,
  ElevateAdminSessionResponseSchema,
  adminAuthRequestPolicies.ElevateAdminSession
);

const methodRevokeCurrentAdminElevation = procedure(
  authServiceName,
  "RevokeCurrentAdminElevation",
  RevokeCurrentAdminElevationRequestSchema,
  RevokeCurrentAdminElevationResponseSchema,
  adminAuthRequestPolicies.RevokeCurrentAdminElevation
);

const methodListAdminSessions = procedure(
  authServiceName,
  "ListAdminSessions",
  ListAdminSessionsRequestSchema,
  ListAdminSessionsResponseSchema,
  adminAuthRequestPolicies.ListAdminSessions
);

const methodRevokeAdminSession = procedure(
  authServiceName,
  "RevokeAdminSession",
  RevokeAdminSessionRequestSchema,
  RevokeAdminSessionResponseSchema,
  adminAuthRequestPolicies.RevokeAdminSession
);

const methodPreviewRevokeOtherAdminSessions = procedure(
  authServiceName,
  "PreviewRevokeOtherAdminSessions",
  PreviewRevokeOtherAdminSessionsRequestSchema,
  PreviewRevokeOtherAdminSessionsResponseSchema,
  adminAuthRequestPolicies.PreviewRevokeOtherAdminSessions
);

const methodRevokeOtherAdminSessions = procedure(
  authServiceName,
  "RevokeOtherAdminSessions",
  RevokeOtherAdminSessionsRequestSchema,
  RevokeOtherAdminSessionsResponseSchema,
  adminAuthRequestPolicies.RevokeOtherAdminSessions
);

export { installSessionInvalidHandler };

// Anonymous API
export const getSetupState = (options?: UnaryOptions): Promise<GetSetupStateResponse> =>
  callUnary<GetSetupStateResponse>(methodGetSetupState, {}, options);

export const beginAdminLogin = (requestFlowId: string, options?: UnaryOptions): Promise<BeginAdminLoginResponse> =>
  callUnary<BeginAdminLoginResponse>(methodBeginAdminLogin, { requestFlowId }, options);

export const getCurrentAdminSession = (options?: UnaryOptions): Promise<GetCurrentAdminSessionResponse> =>
  callUnary<GetCurrentAdminSessionResponse>(methodGetCurrentAdminSession, {}, options);

export const getRuntimeReadiness = (options?: UnaryOptions): Promise<GetRuntimeReadinessResponse> =>
  callUnary<GetRuntimeReadinessResponse>(methodGetRuntimeReadiness, {}, options);

export const loginPassword = (input: {
  challengeProof: string;
  password: string;
  requestFlowId: string;
  signal?: AbortSignal;
}): Promise<LoginPasswordResponse> =>
  callUnary<LoginPasswordResponse>(
    methodLoginPassword,
    { challengeProof: input.challengeProof, password: input.password },
    { flowId: input.requestFlowId, ...(input.signal && { signal: input.signal }) }
  );

export const verifyAdminTotp = (input: { totpCode: string; signal?: AbortSignal }): Promise<VerifyAdminTotpResponse> =>
  callUnary<VerifyAdminTotpResponse>(methodVerifyAdminTotp, { totpCode: input.totpCode }, input.signal ? { signal: input.signal } : undefined);

export const verifyAdminRecoveryCode = (input: {
  recoveryCode: string;
  signal?: AbortSignal;
}): Promise<VerifyAdminRecoveryCodeResponse> =>
  callUnary<VerifyAdminRecoveryCodeResponse>(
    methodVerifyAdminRecoveryCode,
    { recoveryCode: input.recoveryCode },
    input.signal ? { signal: input.signal } : undefined
  );

export const changeInitialPassword = (input: { newPassword: string; signal?: AbortSignal }): Promise<ChangeInitialPasswordResponse> =>
  callUnary<ChangeInitialPasswordResponse>(methodChangeInitialPassword, { newPassword: input.newPassword }, input.signal ? { signal: input.signal } : undefined);

export const logoutAdmin = (options?: UnaryOptions): Promise<LogoutAdminResponse> =>
  callUnary<LogoutAdminResponse>(methodLogoutAdmin, {}, options);

export const changeAdminPassword = (input: {
  operationId: string;
  currentPassword: string;
  newPassword: string;
  expectedPasswordVersion: bigint;
  signal?: AbortSignal;
}): Promise<ChangeAdminPasswordResponse> =>
  callUnary<ChangeAdminPasswordResponse>(
    methodChangeAdminPassword,
    {
      operationId: input.operationId,
      currentPassword: input.currentPassword,
      newPassword: input.newPassword,
      expectedPasswordVersion: input.expectedPasswordVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const beginTotpEnrollment = (input: {
  operationId: string;
  currentPassword: string;
  signal?: AbortSignal;
}): Promise<BeginTotpEnrollmentResponse> =>
  callUnary<BeginTotpEnrollmentResponse>(
    methodBeginTotpEnrollment,
    { operationId: input.operationId, currentPassword: input.currentPassword },
    input.signal ? { signal: input.signal } : undefined
  );

export const completeTotpEnrollment = (input: {
  enrollmentOperationId: string;
  recoveryCodesOperationId: string;
  totpCode: string;
  signal?: AbortSignal;
}): Promise<CompleteTotpEnrollmentResponse> =>
  callUnary<CompleteTotpEnrollmentResponse>(
    methodCompleteTotpEnrollment,
    {
      enrollmentOperationId: input.enrollmentOperationId,
      recoveryCodesOperationId: input.recoveryCodesOperationId,
      totpCode: input.totpCode
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const disableTotp = (input: {
  operationId: string;
  reason: string;
  expectedEnrollmentVersion: bigint;
  signal?: AbortSignal;
}): Promise<DisableTotpResponse> =>
  callUnary<DisableTotpResponse>(
    methodDisableTotp,
    { operationId: input.operationId, reason: input.reason, expectedEnrollmentVersion: input.expectedEnrollmentVersion },
    input.signal ? { signal: input.signal } : undefined
  );

export const regenerateAdminRecoveryCodes = (input: {
  operationId: string;
  expectedRecoveryCodesVersion: bigint;
  signal?: AbortSignal;
}): Promise<RegenerateAdminRecoveryCodesResponse> =>
  callUnary<RegenerateAdminRecoveryCodesResponse>(
    methodRegenerateAdminRecoveryCodes,
    { operationId: input.operationId, expectedRecoveryCodesVersion: input.expectedRecoveryCodesVersion },
    input.signal ? { signal: input.signal } : undefined
  );

export const confirmAdminSecretReceipt = (input: {
  operation: AdminSecretOperation;
  operationId: string;
  resultId: string;
  signal?: AbortSignal;
}): Promise<boolean> =>
  callUnary<ConfirmAdminSecretReceiptResponse>(
    methodConfirmAdminSecretReceipt,
    { operation: input.operation, operationId: input.operationId, resultId: input.resultId },
    input.signal ? { signal: input.signal } : undefined
  ).then((response) => response.confirmed);

export const elevateAdminSession = (input: {
  operationId: string;
  scope: AdminElevationScope;
  currentPassword: string;
  totpCode?: string;
  recoveryCode?: string;
  signal?: AbortSignal;
}): Promise<ElevateAdminSessionResponse> => {
  const request: Record<string, unknown> = {
    operationId: input.operationId,
    scope: input.scope,
    currentPassword: input.currentPassword
  };
  if (input.totpCode) {
    request.secondFactor = { case: "totpCode", value: input.totpCode };
  } else if (input.recoveryCode) {
    request.secondFactor = { case: "recoveryCode", value: input.recoveryCode };
  }
  return callUnary<ElevateAdminSessionResponse>(methodElevateAdminSession, request, input.signal ? { signal: input.signal } : undefined);
};

export const revokeCurrentAdminElevation = (input: {
  scope: AdminElevationScope;
  signal?: AbortSignal;
}): Promise<RevokeCurrentAdminElevationResponse> =>
  callUnary<RevokeCurrentAdminElevationResponse>(methodRevokeCurrentAdminElevation, { scope: input.scope }, input.signal ? { signal: input.signal } : undefined);

export const listAdminSessions = (options?: UnaryOptions): Promise<ListAdminSessionsResponse> =>
  callUnary<ListAdminSessionsResponse>(methodListAdminSessions, {}, options);

export const revokeAdminSession = (input: {
  operationId: string;
  sessionId: string;
  expectedSessionVersion: bigint;
  signal?: AbortSignal;
}): Promise<RevokeAdminSessionResponse> =>
  callUnary<RevokeAdminSessionResponse>(
    methodRevokeAdminSession,
    { operationId: input.operationId, sessionId: input.sessionId, expectedSessionVersion: input.expectedSessionVersion },
    input.signal ? { signal: input.signal } : undefined
  );

export const previewRevokeOtherAdminSessions = (options?: UnaryOptions): Promise<PreviewRevokeOtherAdminSessionsResponse> =>
  callUnary<PreviewRevokeOtherAdminSessionsResponse>(methodPreviewRevokeOtherAdminSessions, {}, options);

export const revokeOtherAdminSessions = (input: {
  operationId: string;
  previewVersion: string;
  expectedAdminVersion: bigint;
  expectedCurrentSessionVersion: bigint;
  signal?: AbortSignal;
}): Promise<RevokeOtherAdminSessionsResponse> =>
  callUnary<RevokeOtherAdminSessionsResponse>(
    methodRevokeOtherAdminSessions,
    {
      operationId: input.operationId,
      previewVersion: input.previewVersion,
      expectedAdminVersion: input.expectedAdminVersion,
      expectedCurrentSessionVersion: input.expectedCurrentSessionVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );
