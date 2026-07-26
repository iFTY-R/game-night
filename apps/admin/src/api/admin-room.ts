import { AdminSortDirection } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminGameSortField,
  AdminRepairType,
  AdminRoomSortField,
  ExecuteEmergencyRepairRequestSchema,
  ExecuteEmergencyRepairResponseSchema,
  ForceCloseRoomRequestSchema,
  ForceCloseRoomResponseSchema,
  ForceTerminateGameRequestSchema,
  ForceTerminateGameResponseSchema,
  GetGameRequestSchema,
  GetGameResponseSchema,
  GetRepairOperationRequestSchema,
  GetRepairOperationResponseSchema,
  GetRoomRequestSchema,
  GetRoomResponseSchema,
  ListGamesRequestSchema,
  ListGamesResponseSchema,
  ListRoomsRequestSchema,
  ListRoomsResponseSchema,
  PreviewEmergencyRepairRequestSchema,
  PreviewEmergencyRepairResponseSchema,
  RemoveRoomMemberRequestSchema,
  RemoveRoomMemberResponseSchema,
  SetRoomAdmissionRequestSchema,
  SetRoomAdmissionResponseSchema,
  type ExecuteEmergencyRepairResponse,
  type ForceCloseRoomResponse,
  type ForceTerminateGameResponse,
  type GetGameResponse,
  type GetRepairOperationResponse,
  type GetRoomResponse,
  type ListGamesResponse,
  type ListRoomsResponse,
  type PreviewEmergencyRepairResponse,
  type RemoveRoomMemberResponse,
  type SetRoomAdmissionResponse
} from "../../../../contracts/gen/ts/platform/admin/v1/admin_room_pb";
import type { GameSessionStatus } from "../../../../contracts/gen/ts/platform/game/v1/game_pb";
import type { AdmissionMode, RoomStatus } from "../../../../contracts/gen/ts/platform/room/v1/room_pb";
import { callUnary, procedure, type UnaryRequestPolicy } from "./connect";

const roomServiceName = "platform.admin.v1.AdminRoomService";

// Admin room/game reads use cookie sessions, so they must still include CSRF protection.
const sessionReadRequest = { csrf: true, requestId: false } as const satisfies UnaryRequestPolicy;
// Every state-changing room/game command is audit-correlated with a frontend operation/request id.
const auditedRoomRequest = { csrf: true, requestId: true } as const satisfies UnaryRequestPolicy;

export const adminRoomRequestPolicies = {
  ListRooms: sessionReadRequest,
  GetRoom: sessionReadRequest,
  ListGames: sessionReadRequest,
  GetGame: sessionReadRequest,
  SetRoomAdmission: auditedRoomRequest,
  RemoveRoomMember: auditedRoomRequest,
  ForceCloseRoom: auditedRoomRequest,
  ForceTerminateGame: auditedRoomRequest,
  PreviewEmergencyRepair: auditedRoomRequest,
  ExecuteEmergencyRepair: auditedRoomRequest,
  GetRepairOperation: sessionReadRequest
} as const satisfies Record<string, UnaryRequestPolicy>;

const methodListRooms = procedure(roomServiceName, "ListRooms", ListRoomsRequestSchema, ListRoomsResponseSchema, adminRoomRequestPolicies.ListRooms);
const methodGetRoom = procedure(roomServiceName, "GetRoom", GetRoomRequestSchema, GetRoomResponseSchema, adminRoomRequestPolicies.GetRoom);
const methodListGames = procedure(roomServiceName, "ListGames", ListGamesRequestSchema, ListGamesResponseSchema, adminRoomRequestPolicies.ListGames);
const methodGetGame = procedure(roomServiceName, "GetGame", GetGameRequestSchema, GetGameResponseSchema, adminRoomRequestPolicies.GetGame);
const methodSetRoomAdmission = procedure(roomServiceName, "SetRoomAdmission", SetRoomAdmissionRequestSchema, SetRoomAdmissionResponseSchema, adminRoomRequestPolicies.SetRoomAdmission);
const methodRemoveRoomMember = procedure(roomServiceName, "RemoveRoomMember", RemoveRoomMemberRequestSchema, RemoveRoomMemberResponseSchema, adminRoomRequestPolicies.RemoveRoomMember);
const methodForceCloseRoom = procedure(roomServiceName, "ForceCloseRoom", ForceCloseRoomRequestSchema, ForceCloseRoomResponseSchema, adminRoomRequestPolicies.ForceCloseRoom);
const methodForceTerminateGame = procedure(roomServiceName, "ForceTerminateGame", ForceTerminateGameRequestSchema, ForceTerminateGameResponseSchema, adminRoomRequestPolicies.ForceTerminateGame);
const methodPreviewEmergencyRepair = procedure(roomServiceName, "PreviewEmergencyRepair", PreviewEmergencyRepairRequestSchema, PreviewEmergencyRepairResponseSchema, adminRoomRequestPolicies.PreviewEmergencyRepair);
const methodExecuteEmergencyRepair = procedure(roomServiceName, "ExecuteEmergencyRepair", ExecuteEmergencyRepairRequestSchema, ExecuteEmergencyRepairResponseSchema, adminRoomRequestPolicies.ExecuteEmergencyRepair);
const methodGetRepairOperation = procedure(roomServiceName, "GetRepairOperation", GetRepairOperationRequestSchema, GetRepairOperationResponseSchema, adminRoomRequestPolicies.GetRepairOperation);

export const listRooms = (input: {
  roomId?: string;
  roomCode?: string;
  statuses?: RoomStatus[];
  hostUserId?: string;
  memberUserId?: string;
  anomaliesOnly?: boolean;
  pageSize?: number;
  pageToken?: string;
  signal?: AbortSignal;
} = {}): Promise<ListRoomsResponse> => {
  const filter: Record<string, unknown> = {};
  if (input.roomId?.trim()) {
    filter.roomId = input.roomId.trim();
  }
  if (input.roomCode?.trim()) {
    filter.roomCode = input.roomCode.trim();
  }
  if (input.statuses?.length) {
    filter.statuses = input.statuses;
  }
  if (input.hostUserId?.trim()) {
    filter.hostUserId = input.hostUserId.trim();
  }
  if (input.memberUserId?.trim()) {
    filter.memberUserId = input.memberUserId.trim();
  }
  if (input.anomaliesOnly) {
    filter.anomaliesOnly = true;
  }
  return callUnary<ListRoomsResponse>(
    methodListRooms,
    {
      filter,
      sort: { field: AdminRoomSortField.LAST_ACTIVITY_AT, direction: AdminSortDirection.DESCENDING },
      pageSize: input.pageSize ?? 20,
      pageToken: input.pageToken ?? ""
    },
    input.signal ? { signal: input.signal } : undefined
  );
};

export const getRoom = (input: { roomId: string; signal?: AbortSignal }): Promise<GetRoomResponse> =>
  callUnary<GetRoomResponse>(methodGetRoom, { roomId: input.roomId }, input.signal ? { signal: input.signal } : undefined);

export const listGames = (input: {
  sessionId?: string;
  roomId?: string;
  gameIds?: string[];
  statuses?: GameSessionStatus[];
  anomaliesOnly?: boolean;
  pageSize?: number;
  pageToken?: string;
  signal?: AbortSignal;
} = {}): Promise<ListGamesResponse> => {
  const filter: Record<string, unknown> = {};
  if (input.sessionId?.trim()) {
    filter.sessionId = input.sessionId.trim();
  }
  if (input.roomId?.trim()) {
    filter.roomId = input.roomId.trim();
  }
  if (input.gameIds?.length) {
    filter.gameIds = input.gameIds;
  }
  if (input.statuses?.length) {
    filter.statuses = input.statuses;
  }
  if (input.anomaliesOnly) {
    filter.anomaliesOnly = true;
  }
  return callUnary<ListGamesResponse>(
    methodListGames,
    {
      filter,
      sort: { field: AdminGameSortField.LAST_PROGRESS_AT, direction: AdminSortDirection.DESCENDING },
      pageSize: input.pageSize ?? 20,
      pageToken: input.pageToken ?? ""
    },
    input.signal ? { signal: input.signal } : undefined
  );
};

export const getGame = (input: { sessionId: string; signal?: AbortSignal }): Promise<GetGameResponse> =>
  callUnary<GetGameResponse>(methodGetGame, { sessionId: input.sessionId }, input.signal ? { signal: input.signal } : undefined);

export const setRoomAdmission = (input: {
  operationId: string;
  roomId: string;
  participantAdmission: AdmissionMode;
  spectatorAdmission: AdmissionMode;
  reason: string;
  expectedRoomVersion: bigint;
  signal?: AbortSignal;
}): Promise<SetRoomAdmissionResponse> =>
  callUnary<SetRoomAdmissionResponse>(
    methodSetRoomAdmission,
    {
      operationId: input.operationId,
      roomId: input.roomId,
      participantAdmission: input.participantAdmission,
      spectatorAdmission: input.spectatorAdmission,
      reason: input.reason,
      expectedRoomVersion: input.expectedRoomVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const removeRoomMember = (input: {
  operationId: string;
  roomId: string;
  userId: string;
  reason: string;
  expectedRoomVersion: bigint;
  expectedMembershipVersion: bigint;
  signal?: AbortSignal;
}): Promise<RemoveRoomMemberResponse> =>
  callUnary<RemoveRoomMemberResponse>(
    methodRemoveRoomMember,
    {
      operationId: input.operationId,
      roomId: input.roomId,
      userId: input.userId,
      reason: input.reason,
      expectedRoomVersion: input.expectedRoomVersion,
      expectedMembershipVersion: input.expectedMembershipVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const forceCloseRoom = (input: {
  operationId: string;
  roomId: string;
  reason: string;
  expectedRoomVersion: bigint;
  signal?: AbortSignal;
}): Promise<ForceCloseRoomResponse> =>
  callUnary<ForceCloseRoomResponse>(
    methodForceCloseRoom,
    {
      operationId: input.operationId,
      roomId: input.roomId,
      reason: input.reason,
      expectedRoomVersion: input.expectedRoomVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const forceTerminateGame = (input: {
  operationId: string;
  sessionId: string;
  reason: string;
  expectedStateVersion: bigint;
  expectedOwnershipEpoch: bigint;
  signal?: AbortSignal;
}): Promise<ForceTerminateGameResponse> =>
  callUnary<ForceTerminateGameResponse>(
    methodForceTerminateGame,
    {
      operationId: input.operationId,
      sessionId: input.sessionId,
      reason: input.reason,
      expectedStateVersion: input.expectedStateVersion,
      expectedOwnershipEpoch: input.expectedOwnershipEpoch
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const previewEmergencyRepair = (input: {
  targetId: string;
  repairType: AdminRepairType;
  reason: string;
  signal?: AbortSignal;
}): Promise<PreviewEmergencyRepairResponse> =>
  callUnary<PreviewEmergencyRepairResponse>(
    methodPreviewEmergencyRepair,
    { targetId: input.targetId, repairType: input.repairType, reason: input.reason },
    input.signal ? { signal: input.signal } : undefined
  );

export const executeEmergencyRepair = (input: {
  operationId: string;
  repairId: string;
  reason: string;
  expectedRepairVersion: bigint;
  signal?: AbortSignal;
}): Promise<ExecuteEmergencyRepairResponse> =>
  callUnary<ExecuteEmergencyRepairResponse>(
    methodExecuteEmergencyRepair,
    {
      operationId: input.operationId,
      repairId: input.repairId,
      reason: input.reason,
      expectedRepairVersion: input.expectedRepairVersion
    },
    input.signal ? { signal: input.signal } : undefined
  );

export const getRepairOperation = (input: { repairId: string; signal?: AbortSignal }): Promise<GetRepairOperationResponse> =>
  callUnary<GetRepairOperationResponse>(methodGetRepairOperation, { repairId: input.repairId }, input.signal ? { signal: input.signal } : undefined);
