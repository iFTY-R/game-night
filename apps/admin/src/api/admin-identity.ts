import {
  AdminIdentityService,
  GetRealNameRequestSchema,
  GetRealNameResponseSchema,
  GetUserRequestSchema,
  GetUserResponseSchema,
  ListAuditEventsRequestSchema,
  ListAuditEventsResponseSchema,
  SuspendUserRequestSchema,
  SuspendUserResponseSchema,
  UnsuspendUserRequestSchema,
  UnsuspendUserResponseSchema,
  UpdateRealNameRequestSchema,
  UpdateRealNameResponseSchema,
  type GetRealNameResponse,
  type GetUserRequest,
  type GetUserResponse,
  type ListAuditEventsResponse,
  type SuspendUserResponse,
  type UnsuspendUserResponse,
  type UpdateRealNameResponse
} from "../../../../contracts/gen/ts/platform/admin/v1/admin_identity_pb";
import type { AuditAction } from "../../../../contracts/gen/ts/platform/audit/v1/audit_pb";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { callUnary, createRequestId, procedure } from "./connect";

const serviceName = "platform.admin.v1.AdminIdentityService";

const methodGetUser = procedure(serviceName, "GetUser", GetUserRequestSchema, GetUserResponseSchema, "identity");
const methodGetRealName = procedure(serviceName, "GetRealName", GetRealNameRequestSchema, GetRealNameResponseSchema, "identity");
const methodUpdateRealName = procedure(
  serviceName,
  "UpdateRealName",
  UpdateRealNameRequestSchema,
  UpdateRealNameResponseSchema,
  "identity"
);
const methodSuspendUser = procedure(
  serviceName,
  "SuspendUser",
  SuspendUserRequestSchema,
  SuspendUserResponseSchema,
  "identity"
);
const methodUnsuspendUser = procedure(
  serviceName,
  "UnsuspendUser",
  UnsuspendUserRequestSchema,
  UnsuspendUserResponseSchema,
  "identity"
);
const methodListAuditEvents = procedure(
  serviceName,
  "ListAuditEvents",
  ListAuditEventsRequestSchema,
  ListAuditEventsResponseSchema,
  "identity"
);

export const lookupUser = (lookup: GetUserRequest["lookup"]): Promise<GetUserResponse> =>
  callUnary<GetUserResponse>(methodGetUser, { lookup }, { requestId: createRequestId() });

export const getRealName = (userId: string, reason: string): Promise<GetRealNameResponse> =>
  callUnary<GetRealNameResponse>(methodGetRealName, { userId, reason }, { requestId: createRequestId() });

export const updateRealName = (input: {
  userId: string;
  realName: string;
  reason: string;
}): Promise<UpdateRealNameResponse> => callUnary<UpdateRealNameResponse>(methodUpdateRealName, input, { requestId: createRequestId() });

export const suspendUser = (input: { userId: string; reason: string }): Promise<SuspendUserResponse> =>
  callUnary<SuspendUserResponse>(methodSuspendUser, input, { requestId: createRequestId() });

export const unsuspendUser = (input: { userId: string; reason: string }): Promise<UnsuspendUserResponse> =>
  callUnary<UnsuspendUserResponse>(methodUnsuspendUser, input, { requestId: createRequestId() });

export const listAuditEvents = (input: {
  actorAdminId?: string;
  targetUserId?: string;
  actions: AuditAction[];
  startedAt?: Timestamp;
  endedAt?: Timestamp;
  pageSize: number;
  pageToken?: string;
}): Promise<ListAuditEventsResponse> =>
  callUnary<ListAuditEventsResponse>(
    methodListAuditEvents,
    {
      actorAdminId: input.actorAdminId ?? "",
      targetUserId: input.targetUserId ?? "",
      actions: input.actions,
      startedAt: input.startedAt,
      endedAt: input.endedAt,
      page: { pageSize: input.pageSize, pageToken: input.pageToken ?? "" }
    },
    { requestId: createRequestId() }
  );
