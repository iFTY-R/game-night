package adminroom

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminauth"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	adminroomdomain "github.com/iFTY-R/game-night/platform/admin/room"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler adapts the server-authenticated AdminRoomService contract to the admin room domain service.
type Handler struct {
	adminv1connect.UnimplementedAdminRoomServiceHandler

	service *adminroomdomain.Service
	clock   clock.Clock
}

// NewService keeps actor construction in adminauth and leaves this transport as request/response mapping only.
func NewService(service *adminroomdomain.Service, source clock.Clock) (*Handler, error) {
	if service == nil || source == nil {
		return nil, adminroomdomain.ErrInvalidInput
	}
	return &Handler{service: service, clock: source}, nil
}

func (handler *Handler) ListRooms(ctx context.Context, request *connect.Request[adminv1.ListRoomsRequest]) (*connect.Response[adminv1.ListRoomsResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	query, err := roomListQuery(request.Msg)
	if err != nil {
		return nil, err
	}
	page, err := handler.service.ListRooms(ctx, actor, query)
	if err != nil {
		return nil, err
	}
	rooms := make([]*adminv1.AdminRoomSummary, 0, len(page.Rooms))
	for _, row := range page.Rooms {
		rooms = append(rooms, roomSummary(row))
	}
	return connect.NewResponse(&adminv1.ListRoomsResponse{
		Rooms: rooms,
		Page:  &adminv1.AdminPageInfo{NextPageToken: page.NextPageToken, SampledAt: timestamppb.New(page.SampledAt)},
	}), nil
}

func (handler *Handler) GetRoom(ctx context.Context, request *connect.Request[adminv1.GetRoomRequest]) (*connect.Response[adminv1.GetRoomResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	detail, err := handler.service.GetRoom(ctx, actor, roomID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetRoomResponse{Room: roomDetail(detail), SampledAt: timestamppb.New(detail.SampledAt)}), nil
}

func (handler *Handler) ListGames(ctx context.Context, request *connect.Request[adminv1.ListGamesRequest]) (*connect.Response[adminv1.ListGamesResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	query, err := gameListQuery(request.Msg)
	if err != nil {
		return nil, err
	}
	page, err := handler.service.ListGames(ctx, actor, query)
	if err != nil {
		return nil, err
	}
	games := make([]*adminv1.AdminGameSummary, 0, len(page.Games))
	for _, row := range page.Games {
		games = append(games, gameSummary(row))
	}
	return connect.NewResponse(&adminv1.ListGamesResponse{
		Games: games,
		Page:  &adminv1.AdminPageInfo{NextPageToken: page.NextPageToken, SampledAt: timestamppb.New(page.SampledAt)},
	}), nil
}

func (handler *Handler) GetGame(ctx context.Context, request *connect.Request[adminv1.GetGameRequest]) (*connect.Response[adminv1.GetGameResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseUUID(request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	detail, err := handler.service.GetGame(ctx, actor, sessionID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetGameResponse{Game: gameDetail(detail), SampledAt: timestamppb.New(detail.SampledAt)}), nil
}

func (handler *Handler) SetRoomAdmission(ctx context.Context, request *connect.Request[adminv1.SetRoomAdmissionRequest]) (*connect.Response[adminv1.SetRoomAdmissionResponse], error) {
	actor, operationID, err := actorAndOperation(ctx, request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	participant, spectator := admissionDomain(request.Msg.GetParticipantAdmission()), admissionDomain(request.Msg.GetSpectatorAdmission())
	if participant == "" || spectator == "" {
		return nil, adminroomdomain.ErrInvalidInput
	}
	result, err := handler.service.SetRoomAdmission(ctx, actor, adminroomdomain.SetAdmissionCommand{
		RoomID: roomID, ParticipantAdmission: participant, SpectatorAdmission: spectator,
		ExpectedRoomVersion: request.Msg.GetExpectedRoomVersion(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.SetRoomAdmissionResponse{
		Receipt: receipt(operationID, handler.clock.Now()), Outcome: outcome(result.Outcome), Room: roomSummary(result.Room),
	}), nil
}

func (handler *Handler) RemoveRoomMember(ctx context.Context, request *connect.Request[adminv1.RemoveRoomMemberRequest]) (*connect.Response[adminv1.RemoveRoomMemberResponse], error) {
	actor, operationID, err := actorAndOperation(ctx, request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	userID, err := parseUUID(request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.RemoveRoomMember(ctx, actor, adminroomdomain.RemoveMemberCommand{
		RoomID: roomID, UserID: userID, ExpectedRoomVersion: request.Msg.GetExpectedRoomVersion(),
		ExpectedMembershipVersion: request.Msg.GetExpectedMembershipVersion(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RemoveRoomMemberResponse{
		Receipt: receipt(operationID, handler.clock.Now()), Outcome: outcome(result.Outcome),
		Room: roomSummary(result.Room), RevokedConnections: int32(result.RevokedConnections),
	}), nil
}

func (handler *Handler) ForceCloseRoom(ctx context.Context, request *connect.Request[adminv1.ForceCloseRoomRequest]) (*connect.Response[adminv1.ForceCloseRoomResponse], error) {
	actor, operationID, err := actorAndOperation(ctx, request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.ForceCloseRoom(ctx, actor, adminroomdomain.ForceCloseRoomCommand{
		RoomID: roomID, ExpectedRoomVersion: request.Msg.GetExpectedRoomVersion(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ForceCloseRoomResponse{
		Receipt: receipt(operationID, handler.clock.Now()), Outcome: outcome(result.Outcome), Room: roomSummary(result.Room),
	}), nil
}

func (handler *Handler) ForceTerminateGame(ctx context.Context, request *connect.Request[adminv1.ForceTerminateGameRequest]) (*connect.Response[adminv1.ForceTerminateGameResponse], error) {
	actor, operationID, err := actorAndOperation(ctx, request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	sessionID, err := parseUUID(request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.ForceTerminateGame(ctx, actor, adminroomdomain.ForceTerminateGameCommand{
		SessionID: sessionID, Reason: request.Msg.GetReason(), ExpectedStateVersion: request.Msg.GetExpectedStateVersion(),
		ExpectedOwnershipEpoch: request.Msg.GetExpectedOwnershipEpoch(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ForceTerminateGameResponse{
		Receipt: receipt(operationID, handler.clock.Now()), Outcome: outcome(result.Outcome),
		Game: gameSummary(result.Game), RepairRequired: result.RepairRequired,
	}), nil
}

func (handler *Handler) PreviewEmergencyRepair(ctx context.Context, request *connect.Request[adminv1.PreviewEmergencyRepairRequest]) (*connect.Response[adminv1.PreviewEmergencyRepairResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	targetID, err := parseUUID(request.Msg.GetTargetId())
	if err != nil {
		return nil, err
	}
	repair, err := handler.service.PreviewEmergencyRepair(ctx, actor, adminroomdomain.PreviewEmergencyRepairCommand{
		TargetID: targetID, RepairType: repairTypeDomain(request.Msg.GetRepairType()), Reason: request.Msg.GetReason(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.PreviewEmergencyRepairResponse{
		Repair: repairOperation(repair), SampledAt: timestamppb.New(handler.clock.Now()),
	}), nil
}

func (handler *Handler) ExecuteEmergencyRepair(ctx context.Context, request *connect.Request[adminv1.ExecuteEmergencyRepairRequest]) (*connect.Response[adminv1.ExecuteEmergencyRepairResponse], error) {
	actor, operationID, err := actorAndOperation(ctx, request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	repairID, err := parseUUID(request.Msg.GetRepairId())
	if err != nil {
		return nil, err
	}
	repair, err := handler.service.ExecuteEmergencyRepair(ctx, actor, adminroomdomain.ExecuteEmergencyRepairCommand{
		RepairID: repairID, OperationID: operationID, ExpectedRepairVersion: request.Msg.GetExpectedRepairVersion(), Reason: request.Msg.GetReason(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ExecuteEmergencyRepairResponse{
		Receipt: receipt(operationID, repair.ExecutedAt), Repair: repairOperation(repair),
	}), nil
}

func (handler *Handler) GetRepairOperation(ctx context.Context, request *connect.Request[adminv1.GetRepairOperationRequest]) (*connect.Response[adminv1.GetRepairOperationResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	repairID, err := parseUUID(request.Msg.GetRepairId())
	if err != nil {
		return nil, err
	}
	repair, err := handler.service.GetRepairOperation(ctx, actor, repairID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetRepairOperationResponse{
		Repair: repairOperation(repair), SampledAt: timestamppb.New(handler.clock.Now()),
	}), nil
}

func requestActor(ctx context.Context) (admin.ActorContext, error) {
	actor, ok := adminauth.ActorFromContext(ctx)
	if !ok {
		return admin.ActorContext{}, admin.ErrAuthentication
	}
	return actor, nil
}

func actorAndOperation(ctx context.Context, value string) (admin.ActorContext, idempotency.OperationID, error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return admin.ActorContext{}, idempotency.OperationID{}, err
	}
	operationID, err := idempotency.ParseOperationID(strings.TrimSpace(value))
	if err != nil {
		return admin.ActorContext{}, idempotency.OperationID{}, adminroomdomain.ErrInvalidInput
	}
	return actor, operationID, nil
}

func roomListQuery(request *adminv1.ListRoomsRequest) (adminroomdomain.RoomListQuery, error) {
	if request == nil || request.GetPageToken() != "" {
		return adminroomdomain.RoomListQuery{}, adminroomdomain.ErrInvalidInput
	}
	filter := request.GetFilter()
	roomID, err := optionalUUID(filter.GetRoomId())
	if err != nil {
		return adminroomdomain.RoomListQuery{}, err
	}
	hostID, err := optionalUUID(filter.GetHostUserId())
	if err != nil {
		return adminroomdomain.RoomListQuery{}, err
	}
	memberID, err := optionalUUID(filter.GetMemberUserId())
	if err != nil {
		return adminroomdomain.RoomListQuery{}, err
	}
	statuses, err := roomStatuses(filter.GetStatuses())
	if err != nil {
		return adminroomdomain.RoomListQuery{}, err
	}
	sortField, direction, err := roomSort(request.GetSort())
	if err != nil {
		return adminroomdomain.RoomListQuery{}, err
	}
	return adminroomdomain.RoomListQuery{
		RoomID: roomID, RoomCode: filter.GetRoomCode(), Statuses: statuses, GameIDs: filter.GetGameIds(),
		HostUserID: hostID, MemberUserID: memberID, AnomaliesOnly: filter.GetAnomaliesOnly(),
		CreatedFrom: timeFromTimestamp(filter.GetCreatedFrom()), CreatedTo: timeFromTimestamp(filter.GetCreatedTo()),
		UpdatedFrom: timeFromTimestamp(filter.GetUpdatedFrom()), UpdatedTo: timeFromTimestamp(filter.GetUpdatedTo()),
		SortField: sortField, Direction: direction, PageSize: uint32(request.GetPageSize()),
	}, nil
}

func gameListQuery(request *adminv1.ListGamesRequest) (adminroomdomain.GameListQuery, error) {
	if request == nil || request.GetPageToken() != "" {
		return adminroomdomain.GameListQuery{}, adminroomdomain.ErrInvalidInput
	}
	filter := request.GetFilter()
	sessionID, err := optionalUUID(filter.GetSessionId())
	if err != nil {
		return adminroomdomain.GameListQuery{}, err
	}
	roomID, err := optionalUUID(filter.GetRoomId())
	if err != nil {
		return adminroomdomain.GameListQuery{}, err
	}
	statuses, err := gameStatuses(filter.GetStatuses())
	if err != nil {
		return adminroomdomain.GameListQuery{}, err
	}
	sortField, direction, err := gameSort(request.GetSort())
	if err != nil {
		return adminroomdomain.GameListQuery{}, err
	}
	return adminroomdomain.GameListQuery{
		SessionID: sessionID, RoomID: roomID, GameIDs: filter.GetGameIds(), Statuses: statuses,
		AnomaliesOnly: filter.GetAnomaliesOnly(), StartedFrom: timeFromTimestamp(filter.GetStartedFrom()),
		StartedTo: timeFromTimestamp(filter.GetStartedTo()), UpdatedFrom: timeFromTimestamp(filter.GetUpdatedFrom()),
		UpdatedTo: timeFromTimestamp(filter.GetUpdatedTo()), SortField: sortField, Direction: direction,
		PageSize: uint32(request.GetPageSize()),
	}, nil
}

func roomSummary(row adminroomdomain.RoomSummary) *adminv1.AdminRoomSummary {
	return &adminv1.AdminRoomSummary{
		RoomId: row.RoomID.String(), RoomCode: row.RoomCode, Status: roomStatusWire(row.Status),
		ActiveGameId: row.ActiveGameID, ActiveSessionId: optionalUUIDString(row.ActiveSessionID),
		HostUserId: row.HostUserID.String(), HostUsername: row.HostUsername,
		ParticipantCount: int32(row.ParticipantCount), SpectatorCount: int32(row.SpectatorCount),
		ParticipantAdmission: admissionWire(row.ParticipantAdmission), SpectatorAdmission: admissionWire(row.SpectatorAdmission),
		RoomVersion: row.RoomVersion, MembershipVersion: row.MembershipVersion, OwnershipEpoch: row.OwnershipEpoch,
		CreatedAt: timestampOrNil(row.CreatedAt), UpdatedAt: timestampOrNil(row.UpdatedAt), LastActivityAt: timestampOrNil(row.LastActivityAt),
		Owner: ownerSummary(row.Owner), Anomalies: roomAnomalies(row.Anomalies),
	}
}

func roomDetail(detail adminroomdomain.RoomDetail) *adminv1.AdminRoomDetail {
	members := make([]*adminv1.AdminRoomMemberSummary, 0, len(detail.Members))
	for _, member := range detail.Members {
		members = append(members, &adminv1.AdminRoomMemberSummary{
			UserId: member.UserID.String(), Username: member.Username, Role: memberRoleWire(member.Role),
			RequestedRole: memberRoleWire(member.RequestedRole), JoinedAt: timestampOrNil(member.JoinedAt),
			MembershipVersion: member.MembershipVersion, Online: member.Online,
		})
	}
	games := make([]*adminv1.AdminGameSummary, 0, len(detail.ActiveGames))
	for _, game := range detail.ActiveGames {
		games = append(games, gameSummary(game))
	}
	events := make([]*adminv1.AdminRoomEventSummary, 0, len(detail.RecentEvents))
	for _, event := range detail.RecentEvents {
		events = append(events, &adminv1.AdminRoomEventSummary{
			EventId: event.EventID, EventType: event.EventType, ActorUserId: optionalUUIDString(event.ActorUserID),
			Digest: event.Digest, OccurredAt: timestampOrNil(event.OccurredAt),
		})
	}
	return &adminv1.AdminRoomDetail{Summary: roomSummary(detail.Summary), Members: members, ActiveGames: games, RecentEvents: events}
}

func gameSummary(row adminroomdomain.GameSummary) *adminv1.AdminGameSummary {
	return &adminv1.AdminGameSummary{
		SessionId: row.SessionID.String(), RoomId: row.RoomID.String(), RoomCode: row.RoomCode,
		GameId: row.GameID, GameVersion: row.GameVersion, Status: gameStatusWire(row.Status),
		StateVersion: row.StateVersion, OwnershipEpoch: row.OwnershipEpoch,
		StartedAt: timestampOrNil(row.StartedAt), UpdatedAt: timestampOrNil(row.UpdatedAt),
		LastProgressAt: timestampOrNil(row.LastProgressAt), Owner: ownerSummary(row.Owner), Anomalies: gameAnomalies(row.Anomalies),
	}
}

func gameDetail(detail adminroomdomain.GameDetail) *adminv1.AdminGameDetail {
	participants := make([]*adminv1.AdminGameParticipantSummary, 0, len(detail.Participants))
	for _, participant := range detail.Participants {
		participants = append(participants, &adminv1.AdminGameParticipantSummary{
			UserId: participant.UserID.String(), Username: participant.Username, RoomRole: memberRoleWire(participant.RoomRole), Active: participant.Active,
		})
	}
	events := make([]*adminv1.AdminGameEventSummary, 0, len(detail.RecentEvents))
	for _, event := range detail.RecentEvents {
		events = append(events, &adminv1.AdminGameEventSummary{
			EventId: event.EventID, EventType: event.EventType, StateVersion: event.StateVersion,
			ActorUserId: optionalUUIDString(event.ActorUserID), Digest: event.Digest, OccurredAt: timestampOrNil(event.OccurredAt),
		})
	}
	return &adminv1.AdminGameDetail{Summary: gameSummary(detail.Summary), Participants: participants, RecentEvents: events}
}

func repairOperation(row adminroomdomain.RepairOperation) *adminv1.AdminRepairOperation {
	return &adminv1.AdminRepairOperation{
		RepairId: row.RepairID.String(), RepairType: repairTypeWire(row.RepairType), State: repairStateWire(row.State),
		TargetId: row.TargetID.String(), TargetDigest: hex.EncodeToString(row.TargetDigest), PreviewDigest: hex.EncodeToString(row.PreviewDigest),
		RepairVersion: row.Version, ExpectedRoomVersion: row.ExpectedRoomVersion, ExpectedMembershipVersion: row.ExpectedMembershipVersion,
		ExpectedStateVersion: row.ExpectedStateVersion, ExpectedOwnershipEpoch: row.ExpectedOwnershipEpoch,
		Summary: row.Summary, IrreversibleEffects: append([]string(nil), row.IrreversibleEffects...),
		BeforeSnapshotDigest: hex.EncodeToString(row.BeforeSnapshotDigest), AfterSnapshotDigest: hex.EncodeToString(row.AfterSnapshotDigest),
		RequestedByAdminId: row.RequestedByAdminID.String(), AuditEventId: optionalUUIDString(row.AuditEventID),
		CreatedAt: timestampOrNil(row.CreatedAt), ExpiresAt: timestampOrNil(row.ExpiresAt), ExecutedAt: timestampOrNil(row.ExecutedAt),
	}
}

func ownerSummary(row adminroomdomain.OwnerLeaseSummary) *adminv1.AdminOwnerLeaseSummary {
	if row.SessionID == uuid.Nil && row.OwnerInstance == "" && row.OwnerAddress == "" {
		return nil
	}
	return &adminv1.AdminOwnerLeaseSummary{
		SessionId: optionalUUIDString(row.SessionID), OwnerInstanceId: row.OwnerInstance, OwnerAddress: row.OwnerAddress,
		OwnershipEpoch: row.OwnershipEpoch, Freshness: ownerFreshnessWire(row.Freshness),
		ObservedAt: timestampOrNil(row.ObservedAt), ExpiresAt: timestampOrNil(row.ExpiresAt),
	}
}

func receipt(operationID idempotency.OperationID, completedAt time.Time) *adminv1.AdminOperationReceipt {
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	return &adminv1.AdminOperationReceipt{OperationId: operationID.Value(), CompletedAt: timestamppb.New(completedAt)}
}

func parseUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil || parsed.String() != strings.TrimSpace(value) {
		return uuid.Nil, adminroomdomain.ErrInvalidInput
	}
	return parsed, nil
}

func optionalUUID(value string) (uuid.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return uuid.Nil, nil
	}
	return parseUUID(value)
}

func optionalUUIDString(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func timestampOrNil(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

func timeFromTimestamp(value *timestamppb.Timestamp) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.AsTime()
}

func roomStatuses(values []roomv1.RoomStatus) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		status := string(roomStatusDomain(value))
		if status == "" {
			return nil, adminroomdomain.ErrInvalidInput
		}
		result = append(result, status)
	}
	return result, nil
}

func gameStatuses(values []gamev1.GameSessionStatus) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		status := gameStatusDomain(value)
		if status == "" {
			return nil, adminroomdomain.ErrInvalidInput
		}
		result = append(result, status)
	}
	return result, nil
}

func roomSort(value *adminv1.AdminRoomSort) (adminroomdomain.RoomSortField, adminroomdomain.SortDirection, error) {
	field := adminroomdomain.RoomSortUpdatedAt
	switch value.GetField() {
	case adminv1.AdminRoomSortField_ADMIN_ROOM_SORT_FIELD_UNSPECIFIED, adminv1.AdminRoomSortField_ADMIN_ROOM_SORT_FIELD_UPDATED_AT:
	case adminv1.AdminRoomSortField_ADMIN_ROOM_SORT_FIELD_CREATED_AT:
		field = adminroomdomain.RoomSortCreatedAt
	case adminv1.AdminRoomSortField_ADMIN_ROOM_SORT_FIELD_LAST_ACTIVITY_AT:
		field = adminroomdomain.RoomSortLastActivityAt
	case adminv1.AdminRoomSortField_ADMIN_ROOM_SORT_FIELD_ROOM_CODE:
		field = adminroomdomain.RoomSortRoomCode
	default:
		return "", "", adminroomdomain.ErrInvalidInput
	}
	direction, err := sortDirection(value.GetDirection())
	return field, direction, err
}

func gameSort(value *adminv1.AdminGameSort) (adminroomdomain.GameSortField, adminroomdomain.SortDirection, error) {
	field := adminroomdomain.GameSortUpdatedAt
	switch value.GetField() {
	case adminv1.AdminGameSortField_ADMIN_GAME_SORT_FIELD_UNSPECIFIED, adminv1.AdminGameSortField_ADMIN_GAME_SORT_FIELD_UPDATED_AT:
	case adminv1.AdminGameSortField_ADMIN_GAME_SORT_FIELD_STARTED_AT:
		field = adminroomdomain.GameSortStartedAt
	case adminv1.AdminGameSortField_ADMIN_GAME_SORT_FIELD_LAST_PROGRESS_AT:
		field = adminroomdomain.GameSortLastProgressAt
	case adminv1.AdminGameSortField_ADMIN_GAME_SORT_FIELD_SESSION_ID:
		field = adminroomdomain.GameSortSessionID
	default:
		return "", "", adminroomdomain.ErrInvalidInput
	}
	direction, err := sortDirection(value.GetDirection())
	return field, direction, err
}

func sortDirection(value adminv1.AdminSortDirection) (adminroomdomain.SortDirection, error) {
	switch value {
	case adminv1.AdminSortDirection_ADMIN_SORT_DIRECTION_UNSPECIFIED, adminv1.AdminSortDirection_ADMIN_SORT_DIRECTION_DESCENDING:
		return adminroomdomain.SortDescending, nil
	case adminv1.AdminSortDirection_ADMIN_SORT_DIRECTION_ASCENDING:
		return adminroomdomain.SortAscending, nil
	default:
		return "", adminroomdomain.ErrInvalidInput
	}
}

func admissionDomain(value roomv1.AdmissionMode) roomdomain.AdmissionMode {
	switch value {
	case roomv1.AdmissionMode_ADMISSION_MODE_OPEN:
		return roomdomain.AdmissionOpen
	case roomv1.AdmissionMode_ADMISSION_MODE_APPROVAL:
		return roomdomain.AdmissionApproval
	case roomv1.AdmissionMode_ADMISSION_MODE_CLOSED:
		return roomdomain.AdmissionClosed
	default:
		return ""
	}
}

func admissionWire(value string) roomv1.AdmissionMode {
	switch roomdomain.AdmissionMode(value) {
	case roomdomain.AdmissionOpen:
		return roomv1.AdmissionMode_ADMISSION_MODE_OPEN
	case roomdomain.AdmissionApproval:
		return roomv1.AdmissionMode_ADMISSION_MODE_APPROVAL
	case roomdomain.AdmissionClosed:
		return roomv1.AdmissionMode_ADMISSION_MODE_CLOSED
	default:
		return roomv1.AdmissionMode_ADMISSION_MODE_UNSPECIFIED
	}
}

func roomStatusDomain(value roomv1.RoomStatus) roomdomain.RoomStatus {
	switch value {
	case roomv1.RoomStatus_ROOM_STATUS_LOBBY:
		return roomdomain.RoomStatusLobby
	case roomv1.RoomStatus_ROOM_STATUS_PLAYING:
		return roomdomain.RoomStatusPlaying
	case roomv1.RoomStatus_ROOM_STATUS_POST_GAME:
		return roomdomain.RoomStatusPostGame
	case roomv1.RoomStatus_ROOM_STATUS_CLOSED:
		return roomdomain.RoomStatusClosed
	default:
		return ""
	}
}

func roomStatusWire(value string) roomv1.RoomStatus {
	switch roomdomain.RoomStatus(value) {
	case roomdomain.RoomStatusLobby:
		return roomv1.RoomStatus_ROOM_STATUS_LOBBY
	case roomdomain.RoomStatusPlaying:
		return roomv1.RoomStatus_ROOM_STATUS_PLAYING
	case roomdomain.RoomStatusPostGame:
		return roomv1.RoomStatus_ROOM_STATUS_POST_GAME
	case roomdomain.RoomStatusClosed:
		return roomv1.RoomStatus_ROOM_STATUS_CLOSED
	default:
		return roomv1.RoomStatus_ROOM_STATUS_UNSPECIFIED
	}
}

func memberRoleWire(value string) roomv1.MemberRole {
	switch value {
	case string(roomdomain.MemberRoleParticipant):
		return roomv1.MemberRole_MEMBER_ROLE_PARTICIPANT
	case string(roomdomain.MemberRoleSpectator):
		return roomv1.MemberRole_MEMBER_ROLE_SPECTATOR
	case string(roomdomain.MemberRoleWaiting):
		return roomv1.MemberRole_MEMBER_ROLE_WAITING
	default:
		return roomv1.MemberRole_MEMBER_ROLE_UNSPECIFIED
	}
}

func gameStatusDomain(value gamev1.GameSessionStatus) string {
	switch value {
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_ACTIVE:
		return "active"
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_SUSPENDED:
		return "suspended"
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_FINISHED:
		return "finished"
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_CANCELLED:
		return "cancelled"
	default:
		return ""
	}
}

func gameStatusWire(value string) gamev1.GameSessionStatus {
	switch value {
	case "active":
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_ACTIVE
	case "suspended":
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_SUSPENDED
	case "finished":
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_FINISHED
	case "cancelled":
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_CANCELLED
	default:
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_UNSPECIFIED
	}
}

func ownerFreshnessWire(value adminroomdomain.OwnerFreshness) adminv1.AdminOwnerFreshness {
	switch value {
	case adminroomdomain.OwnerFreshnessFresh:
		return adminv1.AdminOwnerFreshness_ADMIN_OWNER_FRESHNESS_FRESH
	case adminroomdomain.OwnerFreshnessStale:
		return adminv1.AdminOwnerFreshness_ADMIN_OWNER_FRESHNESS_STALE
	case adminroomdomain.OwnerFreshnessExpired:
		return adminv1.AdminOwnerFreshness_ADMIN_OWNER_FRESHNESS_EXPIRED
	case adminroomdomain.OwnerFreshnessMissing:
		return adminv1.AdminOwnerFreshness_ADMIN_OWNER_FRESHNESS_MISSING
	case adminroomdomain.OwnerFreshnessUnknown:
		return adminv1.AdminOwnerFreshness_ADMIN_OWNER_FRESHNESS_UNKNOWN
	default:
		return adminv1.AdminOwnerFreshness_ADMIN_OWNER_FRESHNESS_UNSPECIFIED
	}
}

func roomAnomalies(values []adminroomdomain.RoomAnomalyFlag) []adminv1.AdminRoomAnomalyFlag {
	result := make([]adminv1.AdminRoomAnomalyFlag, 0, len(values))
	for _, value := range values {
		switch value {
		case adminroomdomain.RoomAnomalyOwnerStale:
			result = append(result, adminv1.AdminRoomAnomalyFlag_ADMIN_ROOM_ANOMALY_FLAG_OWNER_STALE)
		case adminroomdomain.RoomAnomalyOwnerMissing:
			result = append(result, adminv1.AdminRoomAnomalyFlag_ADMIN_ROOM_ANOMALY_FLAG_OWNER_MISSING)
		case adminroomdomain.RoomAnomalyAllPlayersOffline:
			result = append(result, adminv1.AdminRoomAnomalyFlag_ADMIN_ROOM_ANOMALY_FLAG_ALL_PLAYERS_OFFLINE)
		case adminroomdomain.RoomAnomalyGameLinkMismatch:
			result = append(result, adminv1.AdminRoomAnomalyFlag_ADMIN_ROOM_ANOMALY_FLAG_ROOM_GAME_LINK_MISMATCH)
		}
	}
	return result
}

func gameAnomalies(values []adminroomdomain.GameAnomalyFlag) []adminv1.AdminGameAnomalyFlag {
	result := make([]adminv1.AdminGameAnomalyFlag, 0, len(values))
	for _, value := range values {
		switch value {
		case adminroomdomain.GameAnomalyOwnerStale:
			result = append(result, adminv1.AdminGameAnomalyFlag_ADMIN_GAME_ANOMALY_FLAG_OWNER_STALE)
		case adminroomdomain.GameAnomalyOwnerMissing:
			result = append(result, adminv1.AdminGameAnomalyFlag_ADMIN_GAME_ANOMALY_FLAG_OWNER_MISSING)
		case adminroomdomain.GameAnomalyNoRecentProgress:
			result = append(result, adminv1.AdminGameAnomalyFlag_ADMIN_GAME_ANOMALY_FLAG_NO_RECENT_PROGRESS)
		case adminroomdomain.GameAnomalyRoomLinkMismatch:
			result = append(result, adminv1.AdminGameAnomalyFlag_ADMIN_GAME_ANOMALY_FLAG_ROOM_LINK_MISMATCH)
		}
	}
	return result
}

func outcome(value adminroomdomain.CommandOutcome) adminv1.AdminRoomCommandOutcome {
	switch value {
	case adminroomdomain.CommandOutcomeExecuted:
		return adminv1.AdminRoomCommandOutcome_ADMIN_ROOM_COMMAND_OUTCOME_EXECUTED
	case adminroomdomain.CommandOutcomeNoChange:
		return adminv1.AdminRoomCommandOutcome_ADMIN_ROOM_COMMAND_OUTCOME_NO_CHANGE
	case adminroomdomain.CommandOutcomeVersionConflict:
		return adminv1.AdminRoomCommandOutcome_ADMIN_ROOM_COMMAND_OUTCOME_VERSION_CONFLICT
	case adminroomdomain.CommandOutcomeOwnerUnreachable:
		return adminv1.AdminRoomCommandOutcome_ADMIN_ROOM_COMMAND_OUTCOME_OWNER_UNREACHABLE
	case adminroomdomain.CommandOutcomeRepairRequired:
		return adminv1.AdminRoomCommandOutcome_ADMIN_ROOM_COMMAND_OUTCOME_REPAIR_REQUIRED
	case adminroomdomain.CommandOutcomeRejected:
		return adminv1.AdminRoomCommandOutcome_ADMIN_ROOM_COMMAND_OUTCOME_REJECTED
	default:
		return adminv1.AdminRoomCommandOutcome_ADMIN_ROOM_COMMAND_OUTCOME_UNSPECIFIED
	}
}

func repairTypeDomain(value adminv1.AdminRepairType) adminroomdomain.RepairType {
	switch value {
	case adminv1.AdminRepairType_ADMIN_REPAIR_TYPE_CLEAR_STALE_OWNER_LEASE:
		return adminroomdomain.RepairClearStaleOwnerLease
	case adminv1.AdminRepairType_ADMIN_REPAIR_TYPE_TERMINATE_UNRECOVERABLE_GAME:
		return adminroomdomain.RepairTerminateUnrecoverable
	case adminv1.AdminRepairType_ADMIN_REPAIR_TYPE_REPAIR_ROOM_GAME_LINK:
		return adminroomdomain.RepairRepairRoomGameLink
	default:
		return ""
	}
}

func repairTypeWire(value adminroomdomain.RepairType) adminv1.AdminRepairType {
	switch value {
	case adminroomdomain.RepairClearStaleOwnerLease:
		return adminv1.AdminRepairType_ADMIN_REPAIR_TYPE_CLEAR_STALE_OWNER_LEASE
	case adminroomdomain.RepairTerminateUnrecoverable:
		return adminv1.AdminRepairType_ADMIN_REPAIR_TYPE_TERMINATE_UNRECOVERABLE_GAME
	case adminroomdomain.RepairRepairRoomGameLink:
		return adminv1.AdminRepairType_ADMIN_REPAIR_TYPE_REPAIR_ROOM_GAME_LINK
	default:
		return adminv1.AdminRepairType_ADMIN_REPAIR_TYPE_UNSPECIFIED
	}
}

func repairStateWire(value string) adminv1.AdminRepairState {
	switch value {
	case adminroomdomain.RepairStatePreviewed:
		return adminv1.AdminRepairState_ADMIN_REPAIR_STATE_PREVIEWED
	case adminroomdomain.RepairStateExecuted:
		return adminv1.AdminRepairState_ADMIN_REPAIR_STATE_EXECUTED
	case adminroomdomain.RepairStateRejected:
		return adminv1.AdminRepairState_ADMIN_REPAIR_STATE_REJECTED
	case adminroomdomain.RepairStateExpired:
		return adminv1.AdminRepairState_ADMIN_REPAIR_STATE_EXPIRED
	default:
		return adminv1.AdminRepairState_ADMIN_REPAIR_STATE_UNSPECIFIED
	}
}

var _ adminv1connect.AdminRoomServiceHandler = (*Handler)(nil)
