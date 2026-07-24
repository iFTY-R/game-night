import {
  AdminSecretOperation,
  BeginAdminLoginRequestSchema,
  BeginAdminLoginResponseSchema,
  ChangeInitialPasswordRequestSchema,
  ChangeInitialPasswordResponseSchema,
  CompleteTotpEnrollmentRequestSchema,
  CompleteTotpEnrollmentResponseSchema,
  CompleteTotpRebindRequestSchema,
  CompleteTotpRebindResponseSchema,
  ConfirmAdminSecretReceiptRequestSchema,
  ConfirmAdminSecretReceiptResponseSchema,
  GetSetupStateRequestSchema,
  GetSetupStateResponseSchema,
  GetCurrentAdminSessionRequestSchema,
  GetCurrentAdminSessionResponseSchema,
  GetRuntimeReadinessRequestSchema,
  GetRuntimeReadinessResponseSchema,
  LoginPasswordRequestSchema,
  LoginPasswordResponseSchema,
  LogoutAdminRequestSchema,
  LogoutAdminResponseSchema,
  LogoutAllAdminSessionsRequestSchema,
  LogoutAllAdminSessionsResponseSchema,
  RecoverAdminRequestSchema,
  RecoverAdminResponseSchema,
  VerifyTotpRequestSchema,
  VerifyTotpResponseSchema,
  BeginTotpEnrollmentRequestSchema,
  BeginTotpEnrollmentResponseSchema,
  BeginTotpRebindRequestSchema,
  BeginTotpRebindResponseSchema,
  type BeginAdminLoginResponse,
  type BeginTotpEnrollmentResponse,
  type BeginTotpRebindResponse,
  type CompleteTotpEnrollmentResponse,
  type CompleteTotpRebindResponse,
  type ConfirmAdminSecretReceiptResponse,
  type GetSetupStateResponse,
  type GetCurrentAdminSessionResponse,
  type GetRuntimeReadinessResponse,
  type LoginPasswordResponse,
  type LogoutAdminResponse,
  type LogoutAllAdminSessionsResponse,
  type RecoverAdminResponse,
  type VerifyTotpResponse
} from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { callUnary, installSessionInvalidHandler, procedure } from "./connect";

const authServiceName = "platform.admin.v1.AdminAuthService";

const methodGetSetupState = procedure(
  authServiceName,
  "GetSetupState",
  GetSetupStateRequestSchema,
  GetSetupStateResponseSchema,
  "anonymous"
);

const methodGetCurrentAdminSession = procedure(
  authServiceName,
  "GetCurrentAdminSession",
  GetCurrentAdminSessionRequestSchema,
  GetCurrentAdminSessionResponseSchema,
  "session"
);

const methodGetRuntimeReadiness = procedure(
  authServiceName,
  "GetRuntimeReadiness",
  GetRuntimeReadinessRequestSchema,
  GetRuntimeReadinessResponseSchema,
  "session"
);

const methodBeginAdminLogin = procedure(
  authServiceName,
  "BeginAdminLogin",
  BeginAdminLoginRequestSchema,
  BeginAdminLoginResponseSchema,
  "anonymous"
);

const methodLoginPassword = procedure(
  authServiceName,
  "LoginPassword",
  LoginPasswordRequestSchema,
  LoginPasswordResponseSchema,
  "session"
);

const methodChangeInitialPassword = procedure(
  authServiceName,
  "ChangeInitialPassword",
  ChangeInitialPasswordRequestSchema,
  ChangeInitialPasswordResponseSchema,
  "session"
);

const methodBeginTotpEnrollment = procedure(
  authServiceName,
  "BeginTotpEnrollment",
  BeginTotpEnrollmentRequestSchema,
  BeginTotpEnrollmentResponseSchema,
  "session"
);

const methodCompleteTotpEnrollment = procedure(
  authServiceName,
  "CompleteTotpEnrollment",
  CompleteTotpEnrollmentRequestSchema,
  CompleteTotpEnrollmentResponseSchema,
  "session"
);

const methodVerifyTotp = procedure(
  authServiceName,
  "VerifyTotp",
  VerifyTotpRequestSchema,
  VerifyTotpResponseSchema,
  "session"
);

const methodRecoverAdmin = procedure(
  authServiceName,
  "RecoverAdmin",
  RecoverAdminRequestSchema,
  RecoverAdminResponseSchema,
  "session"
);

const methodBeginTotpRebind = procedure(
  authServiceName,
  "BeginTotpRebind",
  BeginTotpRebindRequestSchema,
  BeginTotpRebindResponseSchema,
  "session"
);

const methodCompleteTotpRebind = procedure(
  authServiceName,
  "CompleteTotpRebind",
  CompleteTotpRebindRequestSchema,
  CompleteTotpRebindResponseSchema,
  "session"
);

const methodConfirmSecretReceipt = procedure(
  authServiceName,
  "ConfirmAdminSecretReceipt",
  ConfirmAdminSecretReceiptRequestSchema,
  ConfirmAdminSecretReceiptResponseSchema,
  "session"
);

const methodLogoutAdmin = procedure(
  authServiceName,
  "LogoutAdmin",
  LogoutAdminRequestSchema,
  LogoutAdminResponseSchema,
  "session"
);

const methodLogoutAllAdminSessions = procedure(
  authServiceName,
  "LogoutAllAdminSessions",
  LogoutAllAdminSessionsRequestSchema,
  LogoutAllAdminSessionsResponseSchema,
  "session"
);

export { installSessionInvalidHandler };

export const getSetupState = (): Promise<GetSetupStateResponse> => callUnary<GetSetupStateResponse>(methodGetSetupState, {});

export const beginAdminLogin = (requestFlowId: string): Promise<BeginAdminLoginResponse> =>
  callUnary<BeginAdminLoginResponse>(methodBeginAdminLogin, { requestFlowId });

export const loginPassword = (input: {
  challengeProof: string;
  password: string;
  requestFlowId: string;
}): Promise<LoginPasswordResponse> =>
  callUnary<LoginPasswordResponse>(
    methodLoginPassword,
    { challengeProof: input.challengeProof, password: input.password },
    { flowId: input.requestFlowId }
  );

export const changeInitialPassword = (newPassword: string): Promise<LoginPasswordResponse> =>
  callUnary<LoginPasswordResponse>(methodChangeInitialPassword, { newPassword });

export const beginTotpEnrollment = (operationId: string): Promise<BeginTotpEnrollmentResponse> =>
  callUnary<BeginTotpEnrollmentResponse>(methodBeginTotpEnrollment, { operationId });

export const completeTotpEnrollment = (input: {
  enrollmentOperationId: string;
  recoveryCodesOperationId: string;
  totpCode: string;
}): Promise<CompleteTotpEnrollmentResponse> => callUnary<CompleteTotpEnrollmentResponse>(methodCompleteTotpEnrollment, input);

export const verifyTotp = (totpCode: string): Promise<VerifyTotpResponse> =>
  callUnary<VerifyTotpResponse>(methodVerifyTotp, { totpCode });

export const recoverAdmin = (recoveryCode: string): Promise<RecoverAdminResponse> =>
  callUnary<RecoverAdminResponse>(methodRecoverAdmin, { recoveryCode });

export const beginTotpRebind = (operationId: string): Promise<BeginTotpRebindResponse> =>
  callUnary<BeginTotpRebindResponse>(methodBeginTotpRebind, { operationId });

export const completeTotpRebind = (input: {
  enrollmentOperationId: string;
  recoveryCodesOperationId: string;
  totpCode: string;
}): Promise<CompleteTotpRebindResponse> => callUnary<CompleteTotpRebindResponse>(methodCompleteTotpRebind, input);

export const confirmAdminSecretReceipt = (input: {
  operation: AdminSecretOperation;
  operationId: string;
  resultId: string;
}): Promise<boolean> =>
  callUnary<ConfirmAdminSecretReceiptResponse>(methodConfirmSecretReceipt, input).then((response) => response.confirmed);

export const logoutAdmin = (): Promise<boolean> =>
  callUnary<LogoutAdminResponse>(methodLogoutAdmin, {}).then((response) => response.loggedOut);

export const logoutAllAdminSessions = (): Promise<number> =>
  callUnary<LogoutAllAdminSessionsResponse>(methodLogoutAllAdminSessions, {}).then((response) => response.revokedSessions);

export const getCurrentAdminSession = (): Promise<GetCurrentAdminSessionResponse> =>
  callUnary<GetCurrentAdminSessionResponse>(methodGetCurrentAdminSession, {});

export const getRuntimeReadiness = (): Promise<GetRuntimeReadinessResponse> =>
  callUnary<GetRuntimeReadinessResponse>(methodGetRuntimeReadiness, {});
