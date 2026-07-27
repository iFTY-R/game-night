import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import {
  AdminAuditFilterSchema,
  AdminAuditService,
  ListAuditEventsRequestSchema,
  ListAuditEventsResponseSchema,
  type AdminAuditFilter,
  type ListAuditEventsResponse
} from "../../../../contracts/gen/ts/platform/admin/v1/admin_audit_pb";
import type { AuditAction, AuditActorType, AuditTargetType } from "../../../../contracts/gen/ts/platform/audit/v1/audit_pb";
import { callUnary, procedure, type UnaryRequestPolicy } from "./connect";

const auditServiceName = "platform.admin.v1.AdminAuditService";

// Audit queries are read-only but still use the session cookie to satisfy the admin transport contract.
const sessionReadRequest = { csrf: true, requestId: false } as const satisfies UnaryRequestPolicy;

export const adminAuditRequestPolicies = {
  ListAuditEvents: sessionReadRequest
} as const satisfies Record<string, UnaryRequestPolicy>;

const methodListAuditEvents = procedure(
  auditServiceName,
  "ListAuditEvents",
  ListAuditEventsRequestSchema,
  ListAuditEventsResponseSchema,
  adminAuditRequestPolicies.ListAuditEvents
);

export type ListAuditEventsInput = {
  actions?: AuditAction[];
  actorTypes?: AuditActorType[];
  actorId?: string;
  targetTypes?: AuditTargetType[];
  targetId?: string;
  requestId?: string;
  reasonCode?: string;
  occurredFrom?: Date | null;
  occurredTo?: Date | null;
  pageSize?: number;
  pageToken?: string;
  signal?: AbortSignal;
};

const toTimestamp = (value?: Date | null) => {
  if (!value) {
    return undefined;
  }
  const milliseconds = value.getTime();
  const seconds = Math.floor(milliseconds / 1000);
  const nanos = (milliseconds - seconds * 1000) * 1_000_000;
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos });
};

const hasFilter = (input: ListAuditEventsInput): boolean =>
  Boolean(
    input.actions?.length ||
      input.actorTypes?.length ||
      input.actorId?.trim() ||
      input.targetTypes?.length ||
      input.targetId?.trim() ||
      input.requestId?.trim() ||
      input.reasonCode?.trim() ||
      input.occurredFrom ||
      input.occurredTo
  );

const buildAuditFilter = (input: ListAuditEventsInput): AdminAuditFilter | undefined => {
  if (!hasFilter(input)) {
    return undefined;
  }
  const occurredFrom = toTimestamp(input.occurredFrom);
  const occurredTo = toTimestamp(input.occurredTo);
  return create(AdminAuditFilterSchema, {
    eventId: "",
    actions: input.actions ?? [],
    actorTypes: input.actorTypes ?? [],
    actorId: input.actorId?.trim() ?? "",
    targetTypes: input.targetTypes ?? [],
    targetId: input.targetId?.trim() ?? "",
    requestId: input.requestId?.trim() ?? "",
    reasonCode: input.reasonCode?.trim() ?? "",
    ...(occurredFrom ? { occurredFrom } : {}),
    ...(occurredTo ? { occurredTo } : {})
  } as never);
};

export const listAuditEvents = (input: ListAuditEventsInput = {}): Promise<ListAuditEventsResponse> => {
  const request: Record<string, unknown> = {
    pageSize: input.pageSize ?? 20,
    pageToken: input.pageToken ?? ""
  };
  const filter = buildAuditFilter(input);
  if (filter) {
    request.filter = filter;
  }
  return callUnary<ListAuditEventsResponse>(
    methodListAuditEvents,
    request,
    input.signal ? { signal: input.signal } : undefined
  );
};

export { AdminAuditService };
