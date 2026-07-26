import {
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
  PreviewUserCommandRequestSchema,
  PreviewUserCommandResponseSchema,
  SetUserTagsRequestSchema,
  SetUserTagsResponseSchema,
  UpdateUserTagRequestSchema,
  UpdateUserTagResponseSchema,
  type AppendUserNoteResponse,
  type CreateUserTagResponse,
  type DeleteUserTagResponse,
  type ExecuteUserCommandResponse,
  type GetUserPIIResponse,
  type GetUserResponse,
  type ListUserNotesResponse,
  type ListUsersResponse,
  type ListUserTagsResponse,
  type PreviewUserCommandResponse,
  type SetUserTagsResponse,
  type UpdateUserTagResponse
} from "../../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import type { AdminUserCommandType } from "../../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";
import { AdminSortDirection } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
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
  ExecuteUserCommand: auditedUserRequest
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

export const listUsers = (input: {
  username?: string;
  status?: number;
  tagIds?: string[];
  pageSize?: number;
  pageToken?: string;
  signal?: AbortSignal;
}): Promise<ListUsersResponse> => {
  const filter: Record<string, unknown> = {};
  if (input.username?.trim()) {
    filter.username = input.username.trim();
  }
  if (input.status) {
    filter.statuses = [input.status];
  }
  if (input.tagIds?.length) {
    filter.tagIds = input.tagIds;
  }
  return callUnary<ListUsersResponse>(
    methodListUsers,
    {
      filter,
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
    { operationId: input.operationId, name: input.name, color: input.color, reason: input.reason, expectedVersion: input.expectedVersion },
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
      color: input.color,
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
