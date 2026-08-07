import type { DescMessage, JsonValue, Message } from "@bufbuild/protobuf";
import { create, fromBinary, fromJson, toJson } from "@bufbuild/protobuf";
import { BusinessErrorDetailSchema, type BusinessErrorDetail } from "../../../../contracts/gen/ts/platform/common/v1/error_pb";
import { readAdminCsrfToken } from "./cookies";
import { AdminApiError, isSessionInvalidError } from "./errors";

type UnaryMethod = {
  path: string;
  input: DescMessage;
  output: DescMessage;
  policy: UnaryRequestPolicy;
};

const adminPathPrefix = "/admin";

// UnaryRequestPolicy mirrors the backend's independent CSRF and audit-correlation requirements.
export type UnaryRequestPolicy = Readonly<{
  csrf: boolean;
  requestId: boolean;
}>;

type ConnectErrorWire = {
  code?: string;
  message?: string;
  details?: Array<{ type?: string; value?: unknown }>;
};

export type UnaryOptions = {
  signal?: AbortSignal;
  flowId?: string;
  requestId?: string;
};

let onSessionInvalid: (() => void) | null = null;

export const installSessionInvalidHandler = (handler: (() => void) | null): void => {
  onSessionInvalid = handler;
};

export const createRequestId = (): string => {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `admin-${Date.now()}-${Math.random().toString(16).slice(2)}`;
};

/**
 * Creates the canonical Base64URL operation selector required by backend idempotency validation.
 * This is intentionally distinct from a request trace ID: UUID text is valid for tracing but not for the operation contract.
 */
export const createOperationId = (): string => {
  const bytes = new Uint8Array(24);
  if (typeof crypto === "undefined" || typeof crypto.getRandomValues !== "function") {
    throw new Error("浏览器未提供安全随机数，无法发起幂等操作。");
  }
  crypto.getRandomValues(bytes);
  return btoa(String.fromCharCode(...bytes))
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/u, "");
};

const withHeaders = (method: UnaryMethod, options: UnaryOptions): Headers => {
  const headers = new Headers({
    Accept: "application/json",
    "Connect-Protocol-Version": "1",
    "Content-Type": "application/json"
  });
  if (method.policy.csrf) {
    const csrf = readAdminCsrfToken();
    if (csrf) {
      headers.set("X-CSRF-Token", csrf);
    }
  }
  if (options.flowId) {
    headers.set("X-Request-Flow-ID", options.flowId);
  }
  if (method.policy.requestId) {
    headers.set("X-Request-ID", options.requestId ?? createRequestId());
  }
  return headers;
};

const asDate = (value: unknown): Date | undefined => {
  if (!value || typeof value !== "object") {
    return undefined;
  }
  const seconds = Reflect.get(value, "seconds");
  if (typeof seconds === "string" || typeof seconds === "number") {
    return new Date(Number(seconds) * 1000);
  }
  return undefined;
};

const parseBusinessDetail = (body: ConnectErrorWire): BusinessErrorDetail | null => {
  for (const detail of body.details ?? []) {
    if (!detail || typeof detail !== "object") {
      continue;
    }
    if (typeof detail.type !== "string" || !detail.type.endsWith("platform.common.v1.BusinessErrorDetail")) {
      continue;
    }
    try {
      if (typeof detail.value === "string") {
        const decoded = atob(detail.value.replace(/-/g, "+").replace(/_/g, "/"));
        const bytes = Uint8Array.from(decoded, (character) => character.charCodeAt(0));
        return fromBinary(BusinessErrorDetailSchema, bytes);
      }
      if (detail.value && typeof detail.value === "object") {
        return fromJson(BusinessErrorDetailSchema, detail.value as JsonValue);
      }
    } catch {
      return null;
    }
  }
  return null;
};

const businessErrorMessages: Readonly<Record<string, string>> = {
  "admin.auth.invalid": "登录状态已失效，请重新登录。",
  "request.csrf.invalid": "登录状态已失效，请重新登录。",
  "request.invalid": "请求参数无效，请检查后重试。",
  "audit.write.unavailable": "安全审计暂时不可用，请稍后重试。",
  "admin.operations.preview_expired": "操作预览已失效，请重新生成预览。",
  "admin.operations.retry_limit": "该任务已达到人工重试上限。",
  "admin.version.conflict": "数据版本已变化，已重新加载最新状态。",
  "admin.elevation.required": "该操作需要重新验证管理员身份。",
  "admin.elevation.expired": "权限提升已过期，请重新验证身份。",
  "service.temporarily_unavailable": "服务暂时不可用，请稍后重试。"
};

const parseError = async (response: Response): Promise<AdminApiError> => {
  let body: ConnectErrorWire = {};
  try {
    body = (await response.json()) as ConnectErrorWire;
  } catch {
    body = {};
  }
  const detail = parseBusinessDetail(body);
  const businessKey = detail?.messageKey || "service.temporarily_unavailable";
  const message = businessErrorMessages[businessKey] ??
    (typeof body.message === "string" && body.message && body.message !== businessKey ? body.message : "请求失败，请稍后再试。");
  const payload: ConstructorParameters<typeof AdminApiError>[0] = {
    message,
    status: response.status,
    code: typeof body.code === "string" && body.code ? body.code : "unknown",
    businessKey
  };
  if (detail?.fieldViolations) {
    payload.fieldViolations = detail.fieldViolations;
  }
  const retryAt = asDate(detail?.retryAt);
  if (retryAt) {
    payload.retryAt = retryAt;
  }
  const error = new AdminApiError(payload);
  if (isSessionInvalidError(error)) {
    onSessionInvalid?.();
  }
  return error;
};

export const callUnary = async <Output extends Message>(
  method: UnaryMethod,
  init: Record<string, unknown>,
  options: UnaryOptions = {}
): Promise<Output> => {
  const request = create(method.input, init as never);
  const requestInit: RequestInit = {
    method: "POST",
    credentials: "include",
    headers: withHeaders(method, options),
    body: JSON.stringify(toJson(method.input, request as never))
  };
  if (options.signal) {
    requestInit.signal = options.signal;
  }
  const response = await fetch(method.path, requestInit);
  if (!response.ok) {
    throw await parseError(response);
  }
  const json = (await response.json()) as unknown;
  return fromJson(method.output, json as JsonValue) as Output;
};

export const procedure = (
  service: string,
  method: string,
  input: DescMessage,
  output: DescMessage,
  policy: UnaryRequestPolicy
): UnaryMethod => ({
  path: `${adminPathPrefix}/${service}/${method}`,
  input,
  output,
  policy
});
