import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import {
  AdminOperationsService,
  ApplyCacheRefreshRequestSchema,
  ApplyCacheRefreshResponseSchema,
  ApplyMaintenanceChangeRequestSchema,
  ApplyMaintenanceChangeResponseSchema,
  ApplyTaskRetryRequestSchema,
  ApplyTaskRetryResponseSchema,
  GetMaintenanceStateRequestSchema,
  GetMaintenanceStateResponseSchema,
  GetOperationsSnapshotRequestSchema,
  GetOperationsSnapshotResponseSchema,
  PreviewCacheRefreshRequestSchema,
  PreviewCacheRefreshResponseSchema,
  PreviewMaintenanceChangeRequestSchema,
  PreviewMaintenanceChangeResponseSchema,
  PreviewTaskRetryRequestSchema,
  PreviewTaskRetryResponseSchema,
  type AdminCacheNamespace,
  type AdminMaintenanceScope,
  type AdminRetryTaskKind,
  type ApplyCacheRefreshResponse,
  type ApplyMaintenanceChangeResponse,
  type ApplyTaskRetryResponse,
  type GetMaintenanceStateResponse,
  type GetOperationsSnapshotResponse,
  type PreviewCacheRefreshResponse,
  type PreviewMaintenanceChangeResponse,
  type PreviewTaskRetryResponse
} from "../../../../contracts/gen/ts/platform/admin/v1/admin_operations_pb";
import { callUnary, procedure, type UnaryRequestPolicy } from "./connect";

const serviceName = "platform.admin.v1.AdminOperationsService";
const sessionRead = { csrf: true, requestId: false } as const satisfies UnaryRequestPolicy;
// Every preview and apply call is security relevant and receives a server-correlatable request ID.
const auditedCommand = { csrf: true, requestId: true } as const satisfies UnaryRequestPolicy;

export const adminOperationsRequestPolicies = {
  GetOperationsSnapshot: sessionRead,
  GetMaintenanceState: sessionRead,
  PreviewMaintenanceChange: auditedCommand,
  ApplyMaintenanceChange: auditedCommand,
  PreviewCacheRefresh: auditedCommand,
  ApplyCacheRefresh: auditedCommand,
  PreviewTaskRetry: auditedCommand,
  ApplyTaskRetry: auditedCommand
} as const satisfies Record<string, UnaryRequestPolicy>;

const getSnapshotMethod = procedure(serviceName, "GetOperationsSnapshot", GetOperationsSnapshotRequestSchema, GetOperationsSnapshotResponseSchema, sessionRead);
const getMaintenanceStateMethod = procedure(serviceName, "GetMaintenanceState", GetMaintenanceStateRequestSchema, GetMaintenanceStateResponseSchema, sessionRead);
const previewMaintenanceChangeMethod = procedure(serviceName, "PreviewMaintenanceChange", PreviewMaintenanceChangeRequestSchema, PreviewMaintenanceChangeResponseSchema, auditedCommand);
const applyMaintenanceChangeMethod = procedure(serviceName, "ApplyMaintenanceChange", ApplyMaintenanceChangeRequestSchema, ApplyMaintenanceChangeResponseSchema, auditedCommand);
const previewCacheRefreshMethod = procedure(serviceName, "PreviewCacheRefresh", PreviewCacheRefreshRequestSchema, PreviewCacheRefreshResponseSchema, auditedCommand);
const applyCacheRefreshMethod = procedure(serviceName, "ApplyCacheRefresh", ApplyCacheRefreshRequestSchema, ApplyCacheRefreshResponseSchema, auditedCommand);
const previewTaskRetryMethod = procedure(serviceName, "PreviewTaskRetry", PreviewTaskRetryRequestSchema, PreviewTaskRetryResponseSchema, auditedCommand);
const applyTaskRetryMethod = procedure(serviceName, "ApplyTaskRetry", ApplyTaskRetryRequestSchema, ApplyTaskRetryResponseSchema, auditedCommand);

const toTimestamp = (value?: Date | null) => {
  if (!value) return undefined;
  const milliseconds = value.getTime();
  const seconds = Math.floor(milliseconds / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: (milliseconds - seconds * 1000) * 1_000_000 });
};

/** Reads the bounded operations snapshot using the administrator cookie and CSRF proof. */
export const getOperationsSnapshot = (signal?: AbortSignal): Promise<GetOperationsSnapshotResponse> =>
  callUnary<GetOperationsSnapshotResponse>(getSnapshotMethod, {}, signal ? { signal } : undefined);

/** Reads maintenance authority directly so optional probe failures cannot hide the admission state. */
export const getMaintenanceState = (signal?: AbortSignal): Promise<GetMaintenanceStateResponse> =>
  callUnary<GetMaintenanceStateResponse>(getMaintenanceStateMethod, {}, signal ? { signal } : undefined);

export const previewMaintenanceChange = (input: {
  enabled: boolean;
  scope: AdminMaintenanceScope;
  reason: string;
  plannedEndAt?: Date | null;
  signal?: AbortSignal;
}): Promise<PreviewMaintenanceChangeResponse> =>
  callUnary<PreviewMaintenanceChangeResponse>(
    previewMaintenanceChangeMethod,
    { enabled: input.enabled, scope: input.scope, reason: input.reason, plannedEndAt: toTimestamp(input.plannedEndAt) },
    input.signal ? { signal: input.signal } : undefined
  );

export const applyMaintenanceChange = (input: {
  operationId: string;
  enabled: boolean;
  scope: AdminMaintenanceScope;
  reason: string;
  plannedEndAt?: Date | null;
  expectedVersion: bigint;
  previewDigest: string;
  signal?: AbortSignal;
}): Promise<ApplyMaintenanceChangeResponse> =>
  callUnary<ApplyMaintenanceChangeResponse>(
    applyMaintenanceChangeMethod,
    {
      operationId: input.operationId,
      enabled: input.enabled,
      scope: input.scope,
      reason: input.reason,
      plannedEndAt: toTimestamp(input.plannedEndAt),
      expectedVersion: input.expectedVersion,
      previewDigest: input.previewDigest
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const previewCacheRefresh = (input: {
  namespace: AdminCacheNamespace;
  reason: string;
  signal?: AbortSignal;
}): Promise<PreviewCacheRefreshResponse> =>
  callUnary<PreviewCacheRefreshResponse>(previewCacheRefreshMethod, { namespace: input.namespace, reason: input.reason }, input.signal ? { signal: input.signal } : undefined);

export const applyCacheRefresh = (input: {
  operationId: string;
  namespace: AdminCacheNamespace;
  reason: string;
  expectedGeneration: bigint;
  previewDigest: string;
  signal?: AbortSignal;
}): Promise<ApplyCacheRefreshResponse> =>
  callUnary<ApplyCacheRefreshResponse>(
    applyCacheRefreshMethod,
    {
      operationId: input.operationId,
      namespace: input.namespace,
      reason: input.reason,
      expectedGeneration: input.expectedGeneration,
      previewDigest: input.previewDigest
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const previewTaskRetry = (input: {
  taskKind: AdminRetryTaskKind;
  taskId: string;
  reason: string;
  signal?: AbortSignal;
}): Promise<PreviewTaskRetryResponse> =>
  callUnary<PreviewTaskRetryResponse>(
    previewTaskRetryMethod,
    { taskKind: input.taskKind, taskId: input.taskId, reason: input.reason },
    input.signal ? { signal: input.signal } : undefined
  );

export const applyTaskRetry = (input: {
  operationId: string;
  taskKind: AdminRetryTaskKind;
  taskId: string;
  reason: string;
  expectedTaskVersion: bigint;
  previewDigest: string;
  signal?: AbortSignal;
}): Promise<ApplyTaskRetryResponse> =>
  callUnary<ApplyTaskRetryResponse>(
    applyTaskRetryMethod,
    {
      operationId: input.operationId,
      taskKind: input.taskKind,
      taskId: input.taskId,
      reason: input.reason,
      expectedTaskVersion: input.expectedTaskVersion,
      previewDigest: input.previewDigest
    },
    input.signal ? { signal: input.signal } : undefined
  );

export { AdminOperationsService };
