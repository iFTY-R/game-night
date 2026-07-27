import {
  AdminBatchUserCommandType,
  AdminBatchUserItemState,
  AdminBatchUserOperationSortField,
  AdminUserPIIField,
  AdminUserSortField,
  ExecuteUserCommandRequestSchema,
  ExecuteUserCommandResponseSchema,
  AppendUserNoteRequestSchema,
  AppendUserNoteResponseSchema,
  CreateUserTagRequestSchema,
  CreateUserTagResponseSchema,
  DeleteUserTagRequestSchema,
  DeleteUserTagResponseSchema,
  GetUserPIIRequestSchema,
  GetUserPIIResponseSchema,
  GetUserRequestSchema,
  GetUserResponseSchema,
  ListUserNotesRequestSchema,
  ListUserNotesResponseSchema,
  ListUsersRequestSchema,
  ListUsersResponseSchema,
  ListUserTagsRequestSchema,
  ListUserTagsResponseSchema,
  ListBatchUserOperationItemsRequestSchema,
  ListBatchUserOperationItemsResponseSchema,
  ListBatchUserOperationsRequestSchema,
  ListBatchUserOperationsResponseSchema,
  PreviewUserCommandRequestSchema,
  PreviewUserCommandResponseSchema,
  PreviewBatchUserOperationRequestSchema,
  PreviewBatchUserOperationResponseSchema,
  StartBatchUserOperationRequestSchema,
  StartBatchUserOperationResponseSchema,
  GetBatchUserOperationRequestSchema,
  GetBatchUserOperationResponseSchema,
  CancelBatchUserOperationRequestSchema,
  CancelBatchUserOperationResponseSchema,
  RetryBatchUserOperationRequestSchema,
  RetryBatchUserOperationResponseSchema,
  SetUserTagsRequestSchema,
  SetUserTagsResponseSchema,
  UpdateUserTagRequestSchema,
  UpdateUserTagResponseSchema,
  type AdminUserFilter,
  type CancelBatchUserOperationResponse,
  type AppendUserNoteResponse,
  type CreateUserTagResponse,
  type DeleteUserTagResponse,
  type ExecuteUserCommandResponse,
  type GetBatchUserOperationResponse,
  type GetUserPIIResponse,
  type GetUserResponse,
  type ListBatchUserOperationItemsResponse,
  type ListBatchUserOperationsResponse,
  type ListUserNotesResponse,
  type ListUsersResponse,
  type ListUserTagsResponse,
  type PreviewBatchUserOperationResponse,
  type PreviewUserCommandResponse,
  type RetryBatchUserOperationResponse,
  type SetUserTagsResponse,
  type StartBatchUserOperationResponse,
  type UpdateUserTagResponse
} from "../../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import type { AdminUserCommandType } from "../../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import { AdminJobState, AdminSortDirection } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { callUnary, procedure, type UnaryRequestPolicy } from "./connect";

const userServiceName = "platform.admin.v1.AdminUserService";

// Read calls still require CSRF because the admin surface is session-cookie based.
const sessionReadRequest = { csrf: true, requestId: false } as const satisfies UnaryRequestPolicy;
// PII access and mutations must carry a request ID so audit rows can be correlated end-to-end.
const auditedUserRequest = { csrf: true, requestId: true } as const satisfies UnaryRequestPolicy;

export const adminUserRequestPolicies = {
  ListUsers: sessionReadRequest,
  GetUser: sessionReadRequest,
  GetUserPII: auditedUserRequest,
  ListUserTags: sessionReadRequest,
  CreateUserTag: auditedUserRequest,
  UpdateUserTag: auditedUserRequest,
  DeleteUserTag: auditedUserRequest,
  SetUserTags: auditedUserRequest,
  ListUserNotes: sessionReadRequest,
  AppendUserNote: auditedUserRequest,
  PreviewUserCommand: auditedUserRequest,
  ExecuteUserCommand: auditedUserRequest,
  PreviewBatchUserOperation: auditedUserRequest,
  StartBatchUserOperation: auditedUserRequest,
  GetBatchUserOperation: sessionReadRequest,
  ListBatchUserOperations: sessionReadRequest,
  ListBatchUserOperationItems: sessionReadRequest,
  CancelBatchUserOperation: auditedUserRequest,
  RetryBatchUserOperation: auditedUserRequest
} as const satisfies Record<string, UnaryRequestPolicy>;

const methodListUsers = procedure(userServiceName, "ListUsers", ListUsersRequestSchema, ListUsersResponseSchema, adminUserRequestPolicies.ListUsers);
const methodGetUser = procedure(userServiceName, "GetUser", GetUserRequestSchema, GetUserResponseSchema, adminUserRequestPolicies.GetUser);
const methodGetUserPII = procedure(userServiceName, "GetUserPII", GetUserPIIRequestSchema, GetUserPIIResponseSchema, adminUserRequestPolicies.GetUserPII);
const methodListUserTags = procedure(userServiceName, "ListUserTags", ListUserTagsRequestSchema, ListUserTagsResponseSchema, adminUserRequestPolicies.ListUserTags);
const methodCreateUserTag = procedure(userServiceName, "CreateUserTag", CreateUserTagRequestSchema, CreateUserTagResponseSchema, adminUserRequestPolicies.CreateUserTag);
const methodUpdateUserTag = procedure(userServiceName, "UpdateUserTag", UpdateUserTagRequestSchema, UpdateUserTagResponseSchema, adminUserRequestPolicies.UpdateUserTag);
const methodDeleteUserTag = procedure(userServiceName, "DeleteUserTag", DeleteUserTagRequestSchema, DeleteUserTagResponseSchema, adminUserRequestPolicies.DeleteUserTag);
const methodSetUserTags = procedure(userServiceName, "SetUserTags", SetUserTagsRequestSchema, SetUserTagsResponseSchema, adminUserRequestPolicies.SetUserTags);
const methodListUserNotes = procedure(userServiceName, "ListUserNotes", ListUserNotesRequestSchema, ListUserNotesResponseSchema, adminUserRequestPolicies.ListUserNotes);
const methodAppendUserNote = procedure(userServiceName, "AppendUserNote", AppendUserNoteRequestSchema, AppendUserNoteResponseSchema, adminUserRequestPolicies.AppendUserNote);
const methodPreviewUserCommand = procedure(userServiceName, "PreviewUserCommand", PreviewUserCommandRequestSchema, PreviewUserCommandResponseSchema, adminUserRequestPolicies.PreviewUserCommand);
const methodExecuteUserCommand = procedure(userServiceName, "ExecuteUserCommand", ExecuteUserCommandRequestSchema, ExecuteUserCommandResponseSchema, adminUserRequestPolicies.ExecuteUserCommand);
const methodPreviewBatchUserOperation = procedure(userServiceName, "PreviewBatchUserOperation", PreviewBatchUserOperationRequestSchema, PreviewBatchUserOperationResponseSchema, adminUserRequestPolicies.PreviewBatchUserOperation);
const methodStartBatchUserOperation = procedure(userServiceName, "StartBatchUserOperation", StartBatchUserOperationRequestSchema, StartBatchUserOperationResponseSchema, adminUserRequestPolicies.StartBatchUserOperation);
const methodGetBatchUserOperation = procedure(userServiceName, "GetBatchUserOperation", GetBatchUserOperationRequestSchema, GetBatchUserOperationResponseSchema, adminUserRequestPolicies.GetBatchUserOperation);
const methodListBatchUserOperations = procedure(userServiceName, "ListBatchUserOperations", ListBatchUserOperationsRequestSchema, ListBatchUserOperationsResponseSchema, adminUserRequestPolicies.ListBatchUserOperations);
const methodListBatchUserOperationItems = procedure(userServiceName, "ListBatchUserOperationItems", ListBatchUserOperationItemsRequestSchema, ListBatchUserOperationItemsResponseSchema, adminUserRequestPolicies.ListBatchUserOperationItems);
const methodCancelBatchUserOperation = procedure(userServiceName, "CancelBatchUserOperation", CancelBatchUserOperationRequestSchema, CancelBatchUserOperationResponseSchema, adminUserRequestPolicies.CancelBatchUserOperation);
const methodRetryBatchUserOperation = procedure(userServiceName, "RetryBatchUserOperation", RetryBatchUserOperationRequestSchema, RetryBatchUserOperationResponseSchema, adminUserRequestPolicies.RetryBatchUserOperation);

export type ListUsersFilterInput = {
  username?: string;
  status?: number;
  tagIds?: string[];
};

/**
 * Reuses the same filter shape for list reads and bulk-governance filter selection.
 */
export const buildAdminUserFilter = (input: ListUsersFilterInput): AdminUserFilter => {
  const filter: Record<string, unknown> = {
    userId: "",
    username: "",
    statuses: [],
    tagIds: [],
    presence: 0
  };
  if (input.username?.trim()) {
    filter.username = input.username.trim();
  }
  if (input.status) {
    filter.statuses = [input.status];
  }
  if (input.tagIds?.length) {
    filter.tagIds = input.tagIds;
  }
  return filter as AdminUserFilter;
};

export const listUsers = (input: {
  username?: string;
  status?: number;
  tagIds?: string[];
  pageSize?: number;
  pageToken?: string;
  signal?: AbortSignal;
}): Promise<ListUsersResponse> => {
  return callUnary<ListUsersResponse>(
    methodListUsers,
    {
      filter: buildAdminUserFilter(input),
      sort: { field: AdminUserSortField.LAST_ACTIVITY_AT, direction: AdminSortDirection.DESCENDING },
      pageSize: input.pageSize ?? 20,
      pageToken: input.pageToken ?? ""
    },
    input.signal ? { signal: input.signal } : undefined
  );
};

export const getUser = (input: { userId: string; signal?: AbortSignal }): Promise<GetUserResponse> =>
  callUnary<GetUserResponse>(methodGetUser, { userId: input.userId }, input.signal ? { signal: input.signal } : undefined);

export const getUserPII = (input: {
  userId: string;
  fields?: AdminUserPIIField[];
  reason: string;
  signal?: AbortSignal;
}): Promise<GetUserPIIResponse> =>
  callUnary<GetUserPIIResponse>(
    methodGetUserPII,
    { userId: input.userId, fields: input.fields ?? [AdminUserPIIField.ADMIN_USER_PII_FIELD_REAL_NAME], reason: input.reason },
    input.signal ? { signal: input.signal } : undefined
  );

export const listUserTags = (input: {
  namePrefix?: string;
  pageSize?: number;
  signal?: AbortSignal;
} = {}): Promise<ListUserTagsResponse> =>
  callUnary<ListUserTagsResponse>(
    methodListUserTags,
    { namePrefix: input.namePrefix ?? "", pageSize: input.pageSize ?? 100, pageToken: "" },
    input.signal ? { signal: input.signal } : undefined
  );

/**
 * Folds an operator-entered hex color to the canonical uppercase form the backend and the
 * PostgreSQL CHECK constraint enforce, so requests never fail on letter case alone.
 */
export const normalizeTagColor = (value: string): string => value.trim().toUpperCase();

/**
 * Mirrors the backend `^#[0-9A-F]{6}$` contract case-insensitively so forms can reject a
 * malformed color before the request instead of surfacing a server-side invalid_argument.
 */
export const isValidTagColorInput = (value: string): boolean => /^#[0-9a-fA-F]{6}$/.test(value.trim());

export const createUserTag = (input: {
  operationId: string;
  name: string;
  color: string;
  reason: string;
  expectedVersion: bigint;
  signal?: AbortSignal;
}): Promise<CreateUserTagResponse> =>
  callUnary<CreateUserTagResponse>(
    methodCreateUserTag,
    { operationId: input.operationId, name: input.name, color: normalizeTagColor(input.color), reason: input.reason, expectedVersion: input.expectedVersion },
    input.signal ? { signal: input.signal } : undefined
  );

export const updateUserTag = (input: {
  operationId: string;
  tagId: string;
  name: string;
  color: string;
  reason: string;
  expectedVersion: bigint;
  signal?: AbortSignal;
}): Promise<UpdateUserTagResponse> =>
  callUnary<UpdateUserTagResponse>(
    methodUpdateUserTag,
    {
      operationId: input.operationId,
      tagId: input.tagId,
      name: input.name,
      color: normalizeTagColor(input.color),
      reason: input.reason,
      expectedVersion: input.expectedVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const deleteUserTag = (input: {
  operationId: string;
  tagId: string;
  reason: string;
  expectedVersion: bigint;
  signal?: AbortSignal;
}): Promise<DeleteUserTagResponse> =>
  callUnary<DeleteUserTagResponse>(
    methodDeleteUserTag,
    { operationId: input.operationId, tagId: input.tagId, reason: input.reason, expectedVersion: input.expectedVersion },
    input.signal ? { signal: input.signal } : undefined
  );

export const setUserTags = (input: {
  operationId: string;
  userId: string;
  tagIds: string[];
  reason: string;
  expectedVersion: bigint;
  signal?: AbortSignal;
}): Promise<SetUserTagsResponse> =>
  callUnary<SetUserTagsResponse>(
    methodSetUserTags,
    { operationId: input.operationId, userId: input.userId, tagIds: input.tagIds, reason: input.reason, expectedVersion: input.expectedVersion },
    input.signal ? { signal: input.signal } : undefined
  );

export const listUserNotes = (input: {
  userId: string;
  pageSize?: number;
  signal?: AbortSignal;
}): Promise<ListUserNotesResponse> =>
  callUnary<ListUserNotesResponse>(
    methodListUserNotes,
    { userId: input.userId, pageSize: input.pageSize ?? 20, pageToken: "" },
    input.signal ? { signal: input.signal } : undefined
  );

export const appendUserNote = (input: {
  operationId: string;
  userId: string;
  body: string;
  reason: string;
  expectedVersion: bigint;
  signal?: AbortSignal;
}): Promise<AppendUserNoteResponse> =>
  callUnary<AppendUserNoteResponse>(
    methodAppendUserNote,
    {
      operationId: input.operationId,
      userId: input.userId,
      body: input.body,
      reason: input.reason,
      expectedVersion: input.expectedVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export type AdminUserCommandInput = {
  type: AdminUserCommandType;
  roomId?: string;
  expectedRoomVersion?: bigint;
  expectedMembershipVersion?: bigint;
};

const userCommandPayload = (command: AdminUserCommandInput): Record<string, unknown> => ({
  type: command.type,
  roomId: command.roomId ?? "",
  expectedRoomVersion: command.expectedRoomVersion ?? 0n,
  expectedMembershipVersion: command.expectedMembershipVersion ?? 0n
});

export const previewUserCommand = (input: {
  userId: string;
  command: AdminUserCommandInput;
  reason: string;
  expectedUserVersion: bigint;
  signal?: AbortSignal;
}): Promise<PreviewUserCommandResponse> =>
  callUnary<PreviewUserCommandResponse>(
    methodPreviewUserCommand,
    {
      userId: input.userId,
      command: userCommandPayload(input.command),
      reason: input.reason,
      expectedUserVersion: input.expectedUserVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const executeUserCommand = (input: {
  operationId: string;
  userId: string;
  command: AdminUserCommandInput;
  previewId: string;
  previewDigest: string;
  reason: string;
  expectedUserVersion: bigint;
  signal?: AbortSignal;
}): Promise<ExecuteUserCommandResponse> =>
  callUnary<ExecuteUserCommandResponse>(
    methodExecuteUserCommand,
    {
      operationId: input.operationId,
      userId: input.userId,
      command: userCommandPayload(input.command),
      previewId: input.previewId,
      previewDigest: input.previewDigest,
      reason: input.reason,
      expectedUserVersion: input.expectedUserVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export type BatchUserExplicitTargetInput = {
  userId: string;
  expectedUserVersion: bigint;
};

export type BatchUserSelectionInput =
  | { mode: "filter"; filter: ListUsersFilterInput }
  | { mode: "explicit"; users: BatchUserExplicitTargetInput[] };

/**
 * The batch endpoints accept either the current list filter or an explicit version-bound user set.
 */
export const buildBatchUserSelection = (selection: BatchUserSelectionInput): Record<string, unknown> =>
  selection.mode === "filter"
    ? {
        selection: {
          case: "filter",
          value: buildAdminUserFilter(selection.filter)
        }
      }
    : {
        selection: {
          case: "explicitTargets",
          value: {
            users: selection.users.map((user) => ({
              userId: user.userId,
              expectedUserVersion: user.expectedUserVersion
            }))
          }
        }
      };

export const previewBatchUserOperation = (input: {
  selection: BatchUserSelectionInput;
  command: AdminBatchUserCommandType;
  reason: string;
  signal?: AbortSignal;
}): Promise<PreviewBatchUserOperationResponse> =>
  callUnary<PreviewBatchUserOperationResponse>(
    methodPreviewBatchUserOperation,
    {
      selection: buildBatchUserSelection(input.selection),
      command: input.command,
      reason: input.reason
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const startBatchUserOperation = (input: {
  operationId: string;
  previewId: string;
  previewDigest: string;
  reason: string;
  expectedVersion: bigint;
  signal?: AbortSignal;
}): Promise<StartBatchUserOperationResponse> =>
  callUnary<StartBatchUserOperationResponse>(
    methodStartBatchUserOperation,
    {
      operationId: input.operationId,
      previewId: input.previewId,
      previewDigest: input.previewDigest,
      reason: input.reason,
      expectedVersion: input.expectedVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const getBatchUserOperation = (input: {
  batchOperationId: string;
  signal?: AbortSignal;
}): Promise<GetBatchUserOperationResponse> =>
  callUnary<GetBatchUserOperationResponse>(
    methodGetBatchUserOperation,
    { batchOperationId: input.batchOperationId },
    input.signal ? { signal: input.signal } : undefined
  );

export const listBatchUserOperations = (input: {
  states?: AdminJobState[];
  commands?: AdminBatchUserCommandType[];
  pageSize?: number;
  pageToken?: string;
  signal?: AbortSignal;
} = {}): Promise<ListBatchUserOperationsResponse> =>
  callUnary<ListBatchUserOperationsResponse>(
    methodListBatchUserOperations,
    {
      states: input.states ?? [],
      commands: input.commands ?? [],
      sortField: AdminBatchUserOperationSortField.UPDATED_AT,
      sortDirection: AdminSortDirection.DESCENDING,
      pageSize: input.pageSize ?? 20,
      pageToken: input.pageToken ?? ""
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const listBatchUserOperationItems = (input: {
  batchOperationId: string;
  states?: AdminBatchUserItemState[];
  pageSize?: number;
  pageToken?: string;
  signal?: AbortSignal;
}): Promise<ListBatchUserOperationItemsResponse> =>
  callUnary<ListBatchUserOperationItemsResponse>(
    methodListBatchUserOperationItems,
    {
      batchOperationId: input.batchOperationId,
      states: input.states ?? [],
      pageSize: input.pageSize ?? 50,
      pageToken: input.pageToken ?? ""
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const cancelBatchUserOperation = (input: {
  operationId: string;
  batchOperationId: string;
  reason: string;
  expectedVersion: bigint;
  signal?: AbortSignal;
}): Promise<CancelBatchUserOperationResponse> =>
  callUnary<CancelBatchUserOperationResponse>(
    methodCancelBatchUserOperation,
    {
      operationId: input.operationId,
      batchOperationId: input.batchOperationId,
      reason: input.reason,
      expectedVersion: input.expectedVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const retryBatchUserOperation = (input: {
  operationId: string;
  batchOperationId: string;
  itemIds: string[];
  reason: string;
  expectedVersion: bigint;
  signal?: AbortSignal;
}): Promise<RetryBatchUserOperationResponse> =>
  callUnary<RetryBatchUserOperationResponse>(
    methodRetryBatchUserOperation,
    {
      operationId: input.operationId,
      batchOperationId: input.batchOperationId,
      itemIds: input.itemIds,
      reason: input.reason,
      expectedVersion: input.expectedVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );
