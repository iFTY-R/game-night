package admin

import (
	"context"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	auditv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/audit/v1"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/profile"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectAdminIdentityService is the transport-only adapter for all AdminIdentityService RPCs.
type ConnectAdminIdentityService struct {
	service *IdentityService
	auth    *Service
}

func NewConnectAdminIdentityService(service *IdentityService, auth *Service) (*ConnectAdminIdentityService, error) {
	if service == nil || auth == nil {
		return nil, ErrInvalidInput
	}
	return &ConnectAdminIdentityService{service: service, auth: auth}, nil
}

var _ adminv1connect.AdminIdentityServiceHandler = (*ConnectAdminIdentityService)(nil)

func (adapter *ConnectAdminIdentityService) GetUser(ctx context.Context, request *connect.Request[adminv1.GetUserRequest]) (*connect.Response[adminv1.GetUserResponse], error) {
	authorization, err := adapter.authorization(ctx)
	if err != nil {
		return nil, adminConnectError(err)
	}
	command := GetUserCommand{Authorization: authorization, Username: request.Msg.GetUsername()}
	if value := request.Msg.GetUserId(); value != "" {
		command.UserID, err = parseCanonicalUUID(value)
		if err != nil {
			return nil, adminConnectError(err)
		}
	}
	user, err := adapter.service.GetUser(ctx, command)
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.GetUserResponse{User: adminUserWire(user)}), nil
}

func (adapter *ConnectAdminIdentityService) GetRealName(ctx context.Context, request *connect.Request[adminv1.GetRealNameRequest]) (*connect.Response[adminv1.GetRealNameResponse], error) {
	authorization, userID, err := adapter.authorizationAndUserID(ctx, request.Msg.GetUserId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	result, err := adapter.service.GetRealName(ctx, GetRealNameCommand{Authorization: authorization, UserID: userID, Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.GetRealNameResponse{Profile: realNameWire(result)}), nil
}

func (adapter *ConnectAdminIdentityService) UpdateRealName(ctx context.Context, request *connect.Request[adminv1.UpdateRealNameRequest]) (*connect.Response[adminv1.UpdateRealNameResponse], error) {
	authorization, userID, err := adapter.authorizationAndUserID(ctx, request.Msg.GetUserId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	result, err := adapter.service.UpdateRealName(ctx, UpdateRealNameCommand{Authorization: authorization, UserID: userID, RealName: request.Msg.GetRealName(), Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.UpdateRealNameResponse{Profile: realNameWire(result)}), nil
}

func (adapter *ConnectAdminIdentityService) CreateUserProfileExport(ctx context.Context, request *connect.Request[adminv1.CreateUserProfileExportRequest]) (*connect.Response[adminv1.CreateUserProfileExportResponse], error) {
	authorization, err := adapter.authorization(ctx)
	if err != nil {
		return nil, adminConnectError(err)
	}
	filter, err := exportFilterFromWire(request.Msg.GetFilter())
	if err != nil {
		return nil, adminConnectError(err)
	}
	fields, err := profileFieldsFromWire(request.Msg.GetFields())
	if err != nil {
		return nil, adminConnectError(err)
	}
	result, err := adapter.service.CreateProfileExport(ctx, CreateProfileExportCommand{Authorization: authorization, Filter: filter, Fields: fields, Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	snapshot := result.Snapshot()
	return connect.NewResponse(&adminv1.CreateUserProfileExportResponse{ExportId: snapshot.ExportID.String(), SchemaVersion: snapshot.SchemaVersion, ExpiresAt: timestamppb.New(snapshot.ExpiresAt)}), nil
}

func (adapter *ConnectAdminIdentityService) GetUserProfileExportPage(ctx context.Context, request *connect.Request[adminv1.GetUserProfileExportPageRequest]) (*connect.Response[adminv1.GetUserProfileExportPageResponse], error) {
	authorization, err := adapter.authorization(ctx)
	if err != nil {
		return nil, adminConnectError(err)
	}
	exportID, err := parseCanonicalUUID(request.Msg.GetExportId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	result, err := adapter.service.GetProfileExportPage(ctx, GetProfileExportPageCommand{Authorization: authorization, ExportID: exportID, Cursor: request.Msg.GetCursor(), PageSize: request.Msg.GetPageSize()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	records := make([]*adminv1.ProfileExportRecord, 0, len(result.Records))
	for _, record := range result.Records {
		records = append(records, &adminv1.ProfileExportRecord{UserId: record.UserID.String(), Username: record.Username, RealName: record.RealName, ProfileVersion: record.ProfileVersion})
	}
	return connect.NewResponse(&adminv1.GetUserProfileExportPageResponse{Records: records, NextCursor: result.NextCursor, Complete: result.Complete}), nil
}

func (adapter *ConnectAdminIdentityService) CompleteUserProfileExport(ctx context.Context, request *connect.Request[adminv1.CompleteUserProfileExportRequest]) (*connect.Response[adminv1.CompleteUserProfileExportResponse], error) {
	authorization, err := adapter.authorization(ctx)
	if err != nil {
		return nil, adminConnectError(err)
	}
	exportID, err := parseCanonicalUUID(request.Msg.GetExportId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	completed, err := adapter.service.CompleteProfileExport(ctx, authorization, exportID)
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.CompleteUserProfileExportResponse{Completed: completed}), nil
}

func (adapter *ConnectAdminIdentityService) AbortUserProfileExport(ctx context.Context, request *connect.Request[adminv1.AbortUserProfileExportRequest]) (*connect.Response[adminv1.AbortUserProfileExportResponse], error) {
	authorization, err := adapter.authorization(ctx)
	if err != nil {
		return nil, adminConnectError(err)
	}
	exportID, err := parseCanonicalUUID(request.Msg.GetExportId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	aborted, err := adapter.service.AbortProfileExport(ctx, authorization, exportID, request.Msg.GetReason())
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.AbortUserProfileExportResponse{Aborted: aborted}), nil
}

func (adapter *ConnectAdminIdentityService) CreateAssistedRecoveryGrant(ctx context.Context, request *connect.Request[adminv1.CreateAssistedRecoveryGrantRequest]) (*connect.Response[adminv1.CreateAssistedRecoveryGrantResponse], error) {
	authorization, userID, err := adapter.authorizationAndUserID(ctx, request.Msg.GetUserId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, adminConnectError(ErrInvalidInput)
	}
	result, err := adapter.service.CreateAssistedRecoveryGrant(ctx, CreateAssistedRecoveryGrantCommand{Authorization: authorization, UserID: userID, OperationID: operationID, RevokeOtherDevices: request.Msg.GetRevokeOtherDevices(), Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.CreateAssistedRecoveryGrantResponse{Result: operationResultWire(result.Operation), AssistedRecoveryGrant: result.Code, ExpiresAt: timestamppb.New(result.ExpiresAt)}), nil
}

func (adapter *ConnectAdminIdentityService) ForceChangeUsername(ctx context.Context, request *connect.Request[adminv1.ForceChangeUsernameRequest]) (*connect.Response[adminv1.ForceChangeUsernameResponse], error) {
	authorization, userID, err := adapter.authorizationAndUserID(ctx, request.Msg.GetUserId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	user, err := adapter.service.ForceChangeUsername(ctx, GovernanceCommand{Authorization: authorization, UserID: userID, Reason: request.Msg.GetReason()}, request.Msg.GetUsername())
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.ForceChangeUsernameResponse{User: adminUserWire(user)}), nil
}

func (adapter *ConnectAdminIdentityService) SuspendUser(ctx context.Context, request *connect.Request[adminv1.SuspendUserRequest]) (*connect.Response[adminv1.SuspendUserResponse], error) {
	authorization, userID, err := adapter.authorizationAndUserID(ctx, request.Msg.GetUserId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	user, err := adapter.service.SuspendUser(ctx, GovernanceCommand{Authorization: authorization, UserID: userID, Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.SuspendUserResponse{User: adminUserWire(user)}), nil
}

func (adapter *ConnectAdminIdentityService) UnsuspendUser(ctx context.Context, request *connect.Request[adminv1.UnsuspendUserRequest]) (*connect.Response[adminv1.UnsuspendUserResponse], error) {
	authorization, userID, err := adapter.authorizationAndUserID(ctx, request.Msg.GetUserId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	user, err := adapter.service.UnsuspendUser(ctx, GovernanceCommand{Authorization: authorization, UserID: userID, Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.UnsuspendUserResponse{User: adminUserWire(user)}), nil
}

func (adapter *ConnectAdminIdentityService) DeleteUser(ctx context.Context, request *connect.Request[adminv1.DeleteUserRequest]) (*connect.Response[adminv1.DeleteUserResponse], error) {
	authorization, userID, err := adapter.authorizationAndUserID(ctx, request.Msg.GetUserId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	user, err := adapter.service.DeleteUser(ctx, GovernanceCommand{Authorization: authorization, UserID: userID, Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.DeleteUserResponse{User: adminUserWire(user)}), nil
}

func (adapter *ConnectAdminIdentityService) RevokeUserDevice(ctx context.Context, request *connect.Request[adminv1.RevokeUserDeviceRequest]) (*connect.Response[adminv1.RevokeUserDeviceResponse], error) {
	authorization, userID, err := adapter.authorizationAndUserID(ctx, request.Msg.GetUserId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	credentialID, err := parseCanonicalUUID(request.Msg.GetCredentialId())
	if err != nil {
		return nil, adminConnectError(err)
	}
	revoked, err := adapter.service.RevokeUserDevice(ctx, RevokeUserDeviceCommand{Authorization: authorization, UserID: userID, CredentialID: credentialID, Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.RevokeUserDeviceResponse{Revoked: revoked}), nil
}

func (adapter *ConnectAdminIdentityService) ListAuditEvents(ctx context.Context, request *connect.Request[adminv1.ListAuditEventsRequest]) (*connect.Response[adminv1.ListAuditEventsResponse], error) {
	authorization, err := adapter.authorization(ctx)
	if err != nil {
		return nil, adminConnectError(err)
	}
	command := ListAuditEventsCommand{Authorization: authorization}
	if value := request.Msg.GetActorAdminId(); value != "" {
		command.ActorAdminID, err = parseCanonicalUUID(value)
		if err != nil {
			return nil, adminConnectError(err)
		}
	}
	if value := request.Msg.GetTargetUserId(); value != "" {
		command.TargetUserID, err = parseCanonicalUUID(value)
		if err != nil {
			return nil, adminConnectError(err)
		}
	}
	for _, action := range request.Msg.GetActions() {
		converted := audit.Action(action)
		if !converted.Valid() {
			return nil, adminConnectError(ErrInvalidInput)
		}
		command.Actions = append(command.Actions, converted)
	}
	if startedAt := request.Msg.GetStartedAt(); startedAt != nil {
		if err = startedAt.CheckValid(); err != nil {
			return nil, adminConnectError(ErrInvalidInput)
		}
		command.StartedAt = startedAt.AsTime()
	}
	if endedAt := request.Msg.GetEndedAt(); endedAt != nil {
		if err = endedAt.CheckValid(); err != nil {
			return nil, adminConnectError(ErrInvalidInput)
		}
		command.EndedAt = endedAt.AsTime()
	}
	if page := request.Msg.GetPage(); page != nil {
		if page.GetPageSize() < 0 {
			return nil, adminConnectError(ErrInvalidInput)
		}
		command.PageSize, command.PageToken = uint32(page.GetPageSize()), page.GetPageToken()
	}
	result, err := adapter.service.ListAuditEvents(ctx, command)
	if err != nil {
		return nil, adminConnectError(err)
	}
	events := make([]*auditv1.SignedAuditEvent, 0, len(result.Events))
	for _, event := range result.Events {
		events = append(events, signedAuditEventWire(event))
	}
	return connect.NewResponse(&adminv1.ListAuditEventsResponse{Events: events, Page: &commonv1.PageInfo{NextPageToken: result.NextPageToken}}), nil
}

func (adapter *ConnectAdminIdentityService) authorization(ctx context.Context) (IdentityAuthorization, error) {
	transport := adminTransport(ctx)
	session, err := (&ConnectAdminService{service: adapter.auth}).sessionFromTransport(ctx, transport)
	if err != nil {
		return IdentityAuthorization{}, err
	}
	return IdentityAuthorization{Session: session, SessionToken: transport.CookieToken, CSRFToken: transport.CSRFToken, RequestID: transport.RequestFlowID}, nil
}

func (adapter *ConnectAdminIdentityService) authorizationAndUserID(ctx context.Context, encoded string) (IdentityAuthorization, uuid.UUID, error) {
	authorization, err := adapter.authorization(ctx)
	if err != nil {
		return IdentityAuthorization{}, uuid.Nil, err
	}
	userID, err := parseCanonicalUUID(encoded)
	return authorization, userID, err
}

func parseCanonicalUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return uuid.Nil, ErrInvalidInput
	}
	return parsed, nil
}

func adminUserWire(user identity.User) *adminv1.AdminUserView {
	snapshot := user.Snapshot()
	result := &adminv1.AdminUserView{UserId: snapshot.ID.String(), Status: userStatusWire(snapshot.Status), Username: snapshot.Username, CreatedAt: timestamppb.New(snapshot.CreatedAt), UpdatedAt: timestamppb.New(snapshot.UpdatedAt)}
	if !snapshot.UsernameChangedAt.IsZero() {
		result.UsernameChangedAt = timestamppb.New(snapshot.UsernameChangedAt)
	}
	return result
}

func realNameWire(result RealNameResult) *adminv1.RealNameProfile {
	message := &adminv1.RealNameProfile{UserId: result.UserID.String(), RealName: result.RealName, ProfileVersion: result.ProfileVersion}
	if !result.UpdatedAt.IsZero() {
		message.UpdatedAt = timestamppb.New(result.UpdatedAt)
	}
	if result.UpdatedByAdminID != uuid.Nil {
		message.UpdatedByAdminId = result.UpdatedByAdminID.String()
	}
	return message
}

func userStatusWire(status identity.UserStatus) identityv1.UserStatus {
	switch status {
	case identity.UserStatusOnboarding:
		return identityv1.UserStatus_USER_STATUS_ONBOARDING
	case identity.UserStatusActive:
		return identityv1.UserStatus_USER_STATUS_ACTIVE
	case identity.UserStatusSuspended:
		return identityv1.UserStatus_USER_STATUS_SUSPENDED
	case identity.UserStatusDeleted:
		return identityv1.UserStatus_USER_STATUS_DELETED
	default:
		return identityv1.UserStatus_USER_STATUS_UNSPECIFIED
	}
}

func userStatusFromWire(status identityv1.UserStatus) (identity.UserStatus, error) {
	switch status {
	case identityv1.UserStatus_USER_STATUS_ONBOARDING:
		return identity.UserStatusOnboarding, nil
	case identityv1.UserStatus_USER_STATUS_ACTIVE:
		return identity.UserStatusActive, nil
	case identityv1.UserStatus_USER_STATUS_SUSPENDED:
		return identity.UserStatusSuspended, nil
	case identityv1.UserStatus_USER_STATUS_DELETED:
		return identity.UserStatusDeleted, nil
	default:
		return "", ErrInvalidInput
	}
}

func exportFilterFromWire(message *adminv1.ProfileExportFilter) (ProfileExportFilter, error) {
	if message == nil {
		return ProfileExportFilter{}, ErrInvalidInput
	}
	filter := ProfileExportFilter{UserIDs: make([]uuid.UUID, 0, len(message.GetUserIds())), Statuses: make([]identity.UserStatus, 0, len(message.GetStatuses()))}
	for _, value := range message.GetUserIds() {
		userID, err := parseCanonicalUUID(value)
		if err != nil {
			return ProfileExportFilter{}, err
		}
		filter.UserIDs = append(filter.UserIDs, userID)
	}
	for _, value := range message.GetStatuses() {
		status, err := userStatusFromWire(value)
		if err != nil {
			return ProfileExportFilter{}, err
		}
		filter.Statuses = append(filter.Statuses, status)
	}
	return filter, nil
}

func profileFieldsFromWire(values []adminv1.ProfileField) ([]profile.Field, error) {
	fields := make([]profile.Field, 0, len(values))
	for _, value := range values {
		if value != adminv1.ProfileField_PROFILE_FIELD_REAL_NAME {
			return nil, ErrInvalidInput
		}
		fields = append(fields, profile.FieldRealName)
	}
	return fields, nil
}

func signedAuditEventWire(event audit.SignedEvent) *auditv1.SignedAuditEvent {
	snapshot := event.Snapshot()
	value := snapshot.Event
	return &auditv1.SignedAuditEvent{
		Event: &auditv1.AuditEvent{
			SchemaVersion: value.SchemaVersion, ChainId: string(value.ChainID), EventId: value.EventID.String(), Sequence: value.Sequence,
			PreviousHash: value.PreviousHash.Bytes(), RequestId: value.RequestID, OccurredAt: timestamppb.New(value.OccurredAt),
			Actor:  &auditv1.AuditActor{Type: auditv1.AuditActorType(value.Actor.Type()), ActorId: value.Actor.ID()},
			Target: &auditv1.AuditTarget{Type: auditv1.AuditTargetType(value.Target.Type()), TargetId: value.Target.ID()},
			Action: auditv1.AuditAction(value.Action), ReasonCode: value.ReasonCode, DetailDigest: value.DetailDigest, SigningKeyVersion: value.SigningKeyVersion,
		},
		CanonicalEvent: snapshot.CanonicalEvent, EventHash: snapshot.EventHash.Bytes(), Signature: snapshot.Signature,
	}
}
