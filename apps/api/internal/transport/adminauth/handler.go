package adminauth

import (
	"context"
	"crypto/sha256"
	"math"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/server"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler binds the transport-only policy and wire mapping around the admin security domain service.
type Handler struct {
	service   *admin.Service
	effects   *CookieEffects
	readiness *server.Readiness
}

// NewService validates the complete admin auth transport graph and returns the typed adapter mounted by the admin surface.
func NewService(
	service *admin.Service,
	effects *CookieEffects,
	readiness *server.Readiness,
) (*Handler, error) {
	if service == nil || effects == nil || readiness == nil {
		return nil, admin.ErrInvalidInput
	}
	return &Handler{service: service, effects: effects, readiness: readiness}, nil
}

// NewHandler mounts the typed adapter directly for transport-level tests and standalone callers.
func NewHandler(
	service *admin.Service,
	effects *CookieEffects,
	readiness *server.Readiness,
	options ...connect.HandlerOption,
) (string, http.Handler, error) {
	adapter, err := NewService(service, effects, readiness)
	if err != nil {
		return "", nil, err
	}
	path, handler := adminv1connect.NewAdminAuthServiceHandler(adapter, options...)
	return path, handler, nil
}

func (handler *Handler) GetSetupState(
	ctx context.Context,
	_ *connect.Request[adminv1.GetSetupStateRequest],
) (*connect.Response[adminv1.GetSetupStateResponse], error) {
	state, err := handler.service.GetSetupState(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetSetupStateResponse{State: setupStateWire(state)}), nil
}

func (handler *Handler) GetCurrentAdminSession(
	ctx context.Context,
	_ *connect.Request[adminv1.GetCurrentAdminSessionRequest],
) (*connect.Response[adminv1.GetCurrentAdminSessionResponse], error) {
	state, _, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	if state.view == nil {
		return nil, admin.ErrAuthentication
	}
	return connect.NewResponse(&adminv1.GetCurrentAdminSessionResponse{
		Session: sessionSummaryFromView(*state.view),
	}), nil
}

func (handler *Handler) GetRuntimeReadiness(
	ctx context.Context,
	_ *connect.Request[adminv1.GetRuntimeReadinessRequest],
) (*connect.Response[adminv1.GetRuntimeReadinessResponse], error) {
	_, _, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	ordinary := handler.readiness.RuntimeSnapshot(ctx, false)
	sensitive := handler.readiness.RuntimeSnapshot(ctx, true)
	return connect.NewResponse(&adminv1.GetRuntimeReadinessResponse{
		Ordinary:  runtimeReadinessState(ordinary),
		Sensitive: runtimeReadinessState(sensitive),
	}), nil
}

func (handler *Handler) BeginAdminLogin(
	ctx context.Context,
	request *connect.Request[adminv1.BeginAdminLoginRequest],
) (*connect.Response[adminv1.BeginAdminLoginResponse], error) {
	state, ok := currentRequestContext(ctx)
	if !ok {
		return nil, admin.ErrAuthentication
	}
	flowID, err := requestFlowID(request.Msg.GetRequestFlowId())
	if err != nil {
		return nil, err
	}
	issued, err := handler.service.BeginAdminLogin(ctx, admin.AdminChallengeRequest{
		CanonicalOrigin: state.transport.origin,
		RequestFlowID:   flowID,
		MaxAttempts:     challenge.DefaultMaxAttempts,
	})
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.BeginAdminLoginResponse{
		Challenge: &commonv1.AnonymousChallenge{
			ChallengeProof: issued.Credentials.BodyProof,
			ExpiresAt:      timestamppb.New(issued.Challenge.Snapshot().ExpiresAt),
		},
	})
	if err = handler.effects.SetAdminChallenge(responseHeader(response), issued); err != nil {
		return nil, err
	}
	return response, nil
}

func (handler *Handler) LoginPassword(
	ctx context.Context,
	request *connect.Request[adminv1.LoginPasswordRequest],
) (*connect.Response[adminv1.LoginPasswordResponse], error) {
	state, ok := currentRequestContext(ctx)
	if !ok {
		return nil, admin.ErrAuthentication
	}
	operationID, digest, err := transportOperation("admin.login_password", request.Msg.GetPassword())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.LoginPassword(ctx, admin.LoginPasswordCommand{
		Credentials: challenge.Credentials{
			CookieToken: state.transport.cookieToken,
			BodyProof:   request.Msg.GetChallengeProof(),
		},
		Password:        request.Msg.GetPassword(),
		OperationID:     operationID,
		RequestDigest:   digest,
		CanonicalOrigin: state.transport.origin,
		RequestFlowID:   challenge.RequestFlowID(state.transport.requestFlowID),
		RequestID:       state.transport.requestID,
		ClientIP:        state.transport.clientIP,
		UserAgent:       state.transport.userAgent,
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session.Session, result.Session.Token, result.Session.CSRFToken)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.LoginPasswordResponse{})
	switch {
	case result.RequiresInitialPasswordChange:
		response.Msg.Outcome = &adminv1.LoginPasswordResponse_RequiresInitialPasswordChange{
			RequiresInitialPasswordChange: &adminv1.LoginRequiresInitialPasswordChange{Session: sessionSummaryFromView(view)},
		}
	case result.RequiresMFA:
		response.Msg.Outcome = &adminv1.LoginPasswordResponse_RequiresMfa{
			RequiresMfa: &adminv1.LoginRequiresMfa{Session: sessionSummaryFromView(view)},
		}
	default:
		response.Msg.Outcome = &adminv1.LoginPasswordResponse_Session{Session: sessionSummaryFromView(view)}
	}
	if err = handler.effects.SetAdminSession(responseHeader(response), result.Session); err != nil {
		return nil, err
	}
	return response, nil
}

func (handler *Handler) VerifyAdminTotp(
	ctx context.Context,
	request *connect.Request[adminv1.VerifyAdminTotpRequest],
) (*connect.Response[adminv1.VerifyAdminTotpResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.VerifyAdminTotp(ctx, admin.VerifyTOTPCommand{
		Session:      actor.Session(),
		SessionToken: state.transport.cookieToken,
		CSRFToken:    state.transport.csrfToken,
		Code:         request.Msg.GetTotpCode(),
		RequestID:    actor.RequestID(),
		ClientIP:     actor.ClientIP(),
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session.Session, result.Session.Token, result.Session.CSRFToken)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.VerifyAdminTotpResponse{Session: sessionSummaryFromView(view)})
	if err = handler.effects.SetAdminSession(responseHeader(response), result.Session); err != nil {
		return nil, err
	}
	return response, nil
}

func (handler *Handler) VerifyAdminRecoveryCode(
	ctx context.Context,
	request *connect.Request[adminv1.VerifyAdminRecoveryCodeRequest],
) (*connect.Response[adminv1.VerifyAdminRecoveryCodeResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.VerifyAdminRecoveryCode(ctx, admin.RecoverCommand{
		Session:      actor.Session(),
		SessionToken: state.transport.cookieToken,
		CSRFToken:    state.transport.csrfToken,
		Code:         request.Msg.GetRecoveryCode(),
		RequestID:    actor.RequestID(),
		ClientIP:     actor.ClientIP(),
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session.Session, result.Session.Token, result.Session.CSRFToken)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.VerifyAdminRecoveryCodeResponse{Session: sessionSummaryFromView(view)})
	if err = handler.effects.SetAdminSession(responseHeader(response), result.Session); err != nil {
		return nil, err
	}
	return response, nil
}

func (handler *Handler) ChangeInitialPassword(
	ctx context.Context,
	request *connect.Request[adminv1.ChangeInitialPasswordRequest],
) (*connect.Response[adminv1.ChangeInitialPasswordResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.ChangeInitialPassword(ctx, admin.ChangePasswordCommand{
		Session:      actor.Session(),
		SessionToken: state.transport.cookieToken,
		CSRFToken:    state.transport.csrfToken,
		New:          request.Msg.GetNewPassword(),
		RequestID:    actor.RequestID(),
		ClientIP:     actor.ClientIP(),
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session.Session, result.Session.Token, result.Session.CSRFToken)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.ChangeInitialPasswordResponse{Session: sessionSummaryFromView(view)})
	if err = handler.effects.SetAdminSession(responseHeader(response), result.Session); err != nil {
		return nil, err
	}
	return response, nil
}

func (handler *Handler) ChangeAdminPassword(
	ctx context.Context,
	request *connect.Request[adminv1.ChangeAdminPasswordRequest],
) (*connect.Response[adminv1.ChangeAdminPasswordResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, admin.ErrInvalidInput
	}
	expectedPasswordVersion, err := wireVersion(request.Msg.GetExpectedPasswordVersion())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.ChangeAdminPassword(ctx, admin.ChangePasswordCommand{
		Session:                 actor.Session(),
		SessionToken:            state.transport.cookieToken,
		CSRFToken:               state.transport.csrfToken,
		Current:                 request.Msg.GetCurrentPassword(),
		New:                     request.Msg.GetNewPassword(),
		RequestID:               actor.RequestID(),
		ClientIP:                actor.ClientIP(),
		OperationID:             operationID,
		ExpectedPasswordVersion: expectedPasswordVersion,
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session.Session, result.Session.Token, result.Session.CSRFToken)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.ChangeAdminPasswordResponse{
		OperationId:     request.Msg.GetOperationId(),
		Session:         sessionSummaryFromView(view),
		RevokedSessions: int32(result.RevokedSessions),
	})
	if err = handler.effects.SetAdminSession(responseHeader(response), result.Session); err != nil {
		return nil, err
	}
	return response, nil
}

func (handler *Handler) BeginTotpEnrollment(
	ctx context.Context,
	request *connect.Request[adminv1.BeginTotpEnrollmentRequest],
) (*connect.Response[adminv1.BeginTotpEnrollmentResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, admin.ErrInvalidInput
	}
	result, err := handler.service.BeginTotpEnrollment(ctx, admin.BeginEnrollmentCommand{
		Session:         actor.Session(),
		SessionToken:    state.transport.cookieToken,
		CSRFToken:       state.transport.csrfToken,
		OperationID:     operationID,
		CurrentPassword: request.Msg.GetCurrentPassword(),
		RequestID:       actor.RequestID(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.BeginTotpEnrollmentResponse{
		Result:         operationResultWire(result.Operation),
		ManualEntryKey: result.Secret,
		OtpauthUri:     result.URI,
	}), nil
}

func (handler *Handler) CompleteTotpEnrollment(
	ctx context.Context,
	request *connect.Request[adminv1.CompleteTotpEnrollmentRequest],
) (*connect.Response[adminv1.CompleteTotpEnrollmentResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	recoveryCodesOperationID, err := idempotency.ParseOperationID(request.Msg.GetRecoveryCodesOperationId())
	if err != nil {
		return nil, admin.ErrInvalidInput
	}
	result, err := handler.service.CompleteTotpEnrollment(ctx, admin.CompleteEnrollmentCommand{
		Session:                  actor.Session(),
		SessionToken:             state.transport.cookieToken,
		CSRFToken:                state.transport.csrfToken,
		EnrollmentOperationID:    request.Msg.GetEnrollmentOperationId(),
		RecoveryCodesOperationID: recoveryCodesOperationID,
		TOTPPasscode:             request.Msg.GetTotpCode(),
		RequestID:                actor.RequestID(),
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session.Session, result.Session.Token, result.Session.CSRFToken)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.CompleteTotpEnrollmentResponse{
		Result:          operationResultWire(result.Operation),
		RecoveryCodes:   result.RecoveryCodes,
		Session:         sessionSummaryFromView(view),
		RevokedSessions: int32(result.RevokedSessions),
	})
	if err = handler.effects.SetAdminSession(responseHeader(response), result.Session); err != nil {
		return nil, err
	}
	return response, nil
}

func (handler *Handler) DisableTotp(
	ctx context.Context,
	request *connect.Request[adminv1.DisableTotpRequest],
) (*connect.Response[adminv1.DisableTotpResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, admin.ErrInvalidInput
	}
	expectedEnrollmentVersion, err := wireVersion(request.Msg.GetExpectedEnrollmentVersion())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.DisableTotp(ctx, admin.DisableTotpCommand{
		Session:                   actor.Session(),
		SessionToken:              state.transport.cookieToken,
		CSRFToken:                 state.transport.csrfToken,
		OperationID:               operationID,
		Reason:                    request.Msg.GetReason(),
		RequestID:                 actor.RequestID(),
		ExpectedEnrollmentVersion: expectedEnrollmentVersion,
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.viewForIssuedOrCurrent(ctx, result.Session, state.transport.cookieToken, state.transport.csrfToken)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.DisableTotpResponse{
		OperationId:     request.Msg.GetOperationId(),
		Session:         sessionSummaryFromView(view),
		RevokedSessions: int32(result.RevokedSessions),
		AlreadyDisabled: result.AlreadyDisabled,
	})
	if result.Session.Token != "" && result.Session.CSRFToken != "" {
		if err = handler.effects.SetAdminSession(responseHeader(response), result.Session); err != nil {
			return nil, err
		}
	}
	return response, nil
}

func (handler *Handler) RegenerateAdminRecoveryCodes(
	ctx context.Context,
	request *connect.Request[adminv1.RegenerateAdminRecoveryCodesRequest],
) (*connect.Response[adminv1.RegenerateAdminRecoveryCodesResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, admin.ErrInvalidInput
	}
	expectedRecoveryCodesVersion, err := wireVersion(request.Msg.GetExpectedRecoveryCodesVersion())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.RegenerateAdminRecoveryCodes(ctx, admin.RegenerateRecoveryCodesCommand{
		Session:                      actor.Session(),
		SessionToken:                 state.transport.cookieToken,
		CSRFToken:                    state.transport.csrfToken,
		OperationID:                  operationID,
		RequestID:                    actor.RequestID(),
		ExpectedRecoveryCodesVersion: expectedRecoveryCodesVersion,
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session, state.transport.cookieToken, state.transport.csrfToken)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RegenerateAdminRecoveryCodesResponse{
		Result:        operationResultWire(result.Operation),
		RecoveryCodes: result.RecoveryCodes,
		Session:       sessionSummaryFromView(view),
	}), nil
}

func (handler *Handler) ConfirmAdminSecretReceipt(
	ctx context.Context,
	request *connect.Request[adminv1.ConfirmAdminSecretReceiptRequest],
) (*connect.Response[adminv1.ConfirmAdminSecretReceiptResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := secretOperationScope(request.Msg.GetOperation())
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, admin.ErrInvalidInput
	}
	resultID, err := canonicalUUID(request.Msg.GetResultId())
	if err != nil {
		return nil, err
	}
	confirmed, err := handler.service.ConfirmAdminSecretReceipt(
		ctx,
		actor.Session(),
		state.transport.cookieToken,
		state.transport.csrfToken,
		scope,
		operationID,
		resultID,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ConfirmAdminSecretReceiptResponse{Confirmed: confirmed}), nil
}

func (handler *Handler) ElevateAdminSession(
	ctx context.Context,
	request *connect.Request[adminv1.ElevateAdminSessionRequest],
) (*connect.Response[adminv1.ElevateAdminSessionResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, admin.ErrInvalidInput
	}
	scope, err := elevationScopeFromWire(request.Msg.GetScope())
	if err != nil {
		return nil, err
	}
	permission, ok := permissionForElevationScope(scope)
	if !ok {
		return nil, admin.ErrPermissionDenied
	}
	if err = actor.Require(permission); err != nil {
		return nil, err
	}
	command := admin.ElevateSessionCommand{
		Session:         actor.Session(),
		SessionToken:    state.transport.cookieToken,
		CSRFToken:       state.transport.csrfToken,
		OperationID:     operationID,
		Scope:           scope,
		CurrentPassword: request.Msg.GetCurrentPassword(),
		RequestID:       actor.RequestID(),
		ClientIP:        actor.ClientIP(),
	}
	switch secondFactor := request.Msg.GetSecondFactor().(type) {
	case *adminv1.ElevateAdminSessionRequest_TotpCode:
		command.TOTPCode = secondFactor.TotpCode
	case *adminv1.ElevateAdminSessionRequest_RecoveryCode:
		command.RecoveryCode = secondFactor.RecoveryCode
	default:
		return nil, admin.ErrInvalidInput
	}
	result, err := handler.service.ElevateAdminSession(ctx, command)
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session, state.transport.cookieToken, state.transport.csrfToken)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ElevateAdminSessionResponse{
		OperationId:  request.Msg.GetOperationId(),
		Elevation:    elevationSummaryWire(result.Elevation),
		Session:      sessionSummaryFromView(view),
		SecondFactor: secondFactorWire(result.UsedRecoveryCode),
	}), nil
}

func (handler *Handler) RevokeCurrentAdminElevation(
	ctx context.Context,
	request *connect.Request[adminv1.RevokeCurrentAdminElevationRequest],
) (*connect.Response[adminv1.RevokeCurrentAdminElevationResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := elevationScopeFromWire(request.Msg.GetScope())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.RevokeCurrentAdminElevation(ctx, admin.RevokeCurrentElevationCommand{
		Session:      actor.Session(),
		SessionToken: state.transport.cookieToken,
		CSRFToken:    state.transport.csrfToken,
		Scope:        scope,
		RequestID:    actor.RequestID(),
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session, state.transport.cookieToken, state.transport.csrfToken)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RevokeCurrentAdminElevationResponse{
		Revoked: result.Revoked,
		Session: sessionSummaryFromView(view),
	}), nil
}

func (handler *Handler) ListAdminSessions(
	ctx context.Context,
	_ *connect.Request[adminv1.ListAdminSessionsRequest],
) (*connect.Response[adminv1.ListAdminSessionsResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.ListAdminSessions(ctx, admin.PreviewRevokeOtherSessionsCommand{
		Session:      actor.Session(),
		SessionToken: state.transport.cookieToken,
		CSRFToken:    state.transport.csrfToken,
	})
	if err != nil {
		return nil, err
	}
	sessions := make([]*adminv1.AdminSessionInfo, 0, len(result.Sessions))
	for _, item := range result.Sessions {
		sessions = append(sessions, sessionInfoWire(item))
	}
	return connect.NewResponse(&adminv1.ListAdminSessionsResponse{Sessions: sessions}), nil
}

func (handler *Handler) RevokeAdminSession(
	ctx context.Context,
	request *connect.Request[adminv1.RevokeAdminSessionRequest],
) (*connect.Response[adminv1.RevokeAdminSessionResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, admin.ErrInvalidInput
	}
	sessionID, err := canonicalUUID(request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	expectedSessionVersion, err := wireVersion(request.Msg.GetExpectedSessionVersion())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.RevokeAdminSession(ctx, admin.RevokeAdminSessionCommand{
		Session:                actor.Session(),
		SessionToken:           state.transport.cookieToken,
		CSRFToken:              state.transport.csrfToken,
		OperationID:            operationID,
		TargetSessionID:        sessionID,
		ExpectedSessionVersion: expectedSessionVersion,
		RequestID:              actor.RequestID(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RevokeAdminSessionResponse{
		OperationId: request.Msg.GetOperationId(),
		SessionId:   result.SessionID.String(),
		Revoked:     result.Revoked,
	}), nil
}

func (handler *Handler) PreviewRevokeOtherAdminSessions(
	ctx context.Context,
	_ *connect.Request[adminv1.PreviewRevokeOtherAdminSessionsRequest],
) (*connect.Response[adminv1.PreviewRevokeOtherAdminSessionsResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.PreviewRevokeOtherAdminSessions(ctx, admin.PreviewRevokeOtherSessionsCommand{
		Session:      actor.Session(),
		SessionToken: state.transport.cookieToken,
		CSRFToken:    state.transport.csrfToken,
	})
	if err != nil {
		return nil, err
	}
	sessions := make([]*adminv1.AdminSessionInfo, 0, len(result.Sessions))
	for _, item := range result.Sessions {
		sessions = append(sessions, sessionInfoWire(item))
	}
	return connect.NewResponse(&adminv1.PreviewRevokeOtherAdminSessionsResponse{
		PreviewVersion:        result.PreviewVersion,
		CurrentAdminVersion:   uint64(result.CurrentAdminVersion),
		CurrentSessionVersion: uint64(result.CurrentSessionVersion),
		OtherSessionCount:     int32(result.OtherSessionCount),
		Sessions:              sessions,
	}), nil
}

func (handler *Handler) RevokeOtherAdminSessions(
	ctx context.Context,
	request *connect.Request[adminv1.RevokeOtherAdminSessionsRequest],
) (*connect.Response[adminv1.RevokeOtherAdminSessionsResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, admin.ErrInvalidInput
	}
	expectedAdminVersion, err := wireVersion(request.Msg.GetExpectedAdminVersion())
	if err != nil {
		return nil, err
	}
	expectedCurrentSessionVersion, err := wireVersion(request.Msg.GetExpectedCurrentSessionVersion())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.RevokeOtherAdminSessions(ctx, admin.RevokeOtherSessionsCommand{
		Session:                       actor.Session(),
		SessionToken:                  state.transport.cookieToken,
		CSRFToken:                     state.transport.csrfToken,
		OperationID:                   operationID,
		PreviewVersion:                request.Msg.GetPreviewVersion(),
		ExpectedAdminVersion:          expectedAdminVersion,
		ExpectedCurrentSessionVersion: expectedCurrentSessionVersion,
		RequestID:                     actor.RequestID(),
	})
	if err != nil {
		return nil, err
	}
	view, err := handler.sessionViewFor(ctx, result.Session, state.transport.cookieToken, state.transport.csrfToken)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RevokeOtherAdminSessionsResponse{
		OperationId:     request.Msg.GetOperationId(),
		RevokedSessions: int32(result.RevokedSessions),
		Session:         sessionSummaryFromView(view),
	}), nil
}

func (handler *Handler) LogoutAdmin(
	ctx context.Context,
	_ *connect.Request[adminv1.LogoutAdminRequest],
) (*connect.Response[adminv1.LogoutAdminResponse], error) {
	state, actor, err := handler.requireActorContext(ctx)
	if err != nil {
		return nil, err
	}
	if err = handler.service.LogoutAdmin(ctx, actor.Session(), state.transport.cookieToken, state.transport.csrfToken); err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.LogoutAdminResponse{LoggedOut: true})
	if err = handler.effects.ClearAdminSession(responseHeader(response)); err != nil {
		return nil, err
	}
	return response, nil
}

func (handler *Handler) requireActorContext(ctx context.Context) (requestContext, admin.ActorContext, error) {
	state, ok := currentRequestContext(ctx)
	if !ok || state.actor == nil {
		return requestContext{}, admin.ActorContext{}, admin.ErrAuthentication
	}
	return state, *state.actor, nil
}

func (handler *Handler) sessionViewFor(
	ctx context.Context,
	session admin.Session,
	token string,
	csrfToken string,
) (admin.SessionView, error) {
	current, err := handler.service.GetCurrentAdminSession(ctx, admin.CurrentSessionCommand{
		Session:      session,
		SessionToken: token,
		CSRFToken:    csrfToken,
	})
	if err != nil {
		return admin.SessionView{}, err
	}
	return current.View, nil
}

func (handler *Handler) viewForIssuedOrCurrent(
	ctx context.Context,
	issued admin.IssuedSession,
	token string,
	csrfToken string,
) (admin.SessionView, error) {
	if issued.Token != "" && issued.CSRFToken != "" {
		return handler.sessionViewFor(ctx, issued.Session, issued.Token, issued.CSRFToken)
	}
	return handler.sessionViewFor(ctx, issued.Session, token, csrfToken)
}

func setupStateWire(state admin.SetupState) adminv1.AdminAccountState {
	switch state {
	case admin.SetupStateBootstrapPending:
		return adminv1.AdminAccountState_ADMIN_ACCOUNT_STATE_BOOTSTRAP_PENDING
	case admin.SetupStateSetupRequired:
		return adminv1.AdminAccountState_ADMIN_ACCOUNT_STATE_SETUP_REQUIRED
	case admin.SetupStateActive:
		return adminv1.AdminAccountState_ADMIN_ACCOUNT_STATE_ACTIVE
	default:
		return adminv1.AdminAccountState_ADMIN_ACCOUNT_STATE_UNSPECIFIED
	}
}

func sessionSummaryFromView(view admin.SessionView) *adminv1.AdminSessionSummary {
	snapshot := view.Session.Snapshot()
	permissions := make([]adminv1.AdminPermission, 0, len(view.Permissions.AllPermissions()))
	for _, permission := range view.Permissions.AllPermissions() {
		wire, ok := permissionWire(permission)
		if ok {
			permissions = append(permissions, wire)
		}
	}
	elevations := make([]*adminv1.AdminElevationSummary, 0, len(view.Elevations.AllElevations()))
	for _, elevation := range view.Elevations.AllElevations() {
		elevations = append(elevations, elevationSummaryWire(elevation))
	}
	return &adminv1.AdminSessionSummary{
		AdminId:           snapshot.AdminID.String(),
		SessionId:         snapshot.ID.String(),
		Kind:              sessionKindWire(snapshot.Kind),
		Permissions:       permissions,
		Mfa:               mfaStateFromView(view),
		Elevations:        elevations,
		AdminVersion:      uint64(snapshot.AdminVersion),
		PasswordVersion:   uint64(snapshot.PasswordVersion),
		SessionVersion:    uint64(snapshot.SessionVersion),
		IdleExpiresAt:     timestamppb.New(snapshot.IdleExpiresAt),
		AbsoluteExpiresAt: timestamppb.New(snapshot.AbsoluteExpiresAt),
	}
}

func mfaStateFromView(view admin.SessionView) *adminv1.AdminMfaState {
	var enrollmentVersion uint64
	if view.Enrollment != nil {
		enrollmentVersion = uint64(view.Enrollment.Snapshot().EnrollmentVersion)
	}
	return &adminv1.AdminMfaState{
		Enabled:                view.Enrollment != nil,
		RecoveryCodesRemaining: int32(view.RecoveryCodes.RemainingActive),
		EnrollmentVersion:      enrollmentVersion,
		RecoveryCodesVersion:   uint64(view.RecoveryCodes.SetVersion),
	}
}

func sessionInfoWire(info admin.SessionInfo) *adminv1.AdminSessionInfo {
	snapshot := info.Session.Snapshot()
	scopes := make([]adminv1.AdminElevationScope, 0, len(info.ActiveElevationScopes))
	for _, scope := range info.ActiveElevationScopes {
		wire, ok := elevationScopeWire(scope)
		if ok {
			scopes = append(scopes, wire)
		}
	}
	return &adminv1.AdminSessionInfo{
		SessionId:             snapshot.ID.String(),
		Kind:                  sessionKindWire(snapshot.Kind),
		Current:               info.Current,
		SessionVersion:        uint64(snapshot.SessionVersion),
		CreatedAt:             timestamppb.New(snapshot.CreatedAt),
		LastActivityAt:        timestamppb.New(snapshot.LastSeenAt),
		IdleExpiresAt:         timestamppb.New(snapshot.IdleExpiresAt),
		AbsoluteExpiresAt:     timestamppb.New(snapshot.AbsoluteExpiresAt),
		ClientIp:              snapshot.ClientIP,
		UserAgent:             snapshot.UserAgent,
		ActiveElevationScopes: scopes,
	}
}

func sessionKindWire(kind admin.SessionKind) adminv1.AdminSessionKind {
	switch kind {
	case admin.SessionKindSetupPasswordPending:
		return adminv1.AdminSessionKind_ADMIN_SESSION_KIND_SETUP_PASSWORD_PENDING
	case admin.SessionKindMFAPending:
		return adminv1.AdminSessionKind_ADMIN_SESSION_KIND_MFA_PENDING
	case admin.SessionKindFull:
		return adminv1.AdminSessionKind_ADMIN_SESSION_KIND_FULL
	default:
		return adminv1.AdminSessionKind_ADMIN_SESSION_KIND_UNSPECIFIED
	}
}

func permissionWire(permission admin.Permission) (adminv1.AdminPermission, bool) {
	switch permission {
	case admin.PermissionOverviewRead:
		return adminv1.AdminPermission_ADMIN_PERMISSION_OVERVIEW_READ, true
	case admin.PermissionUsersRead:
		return adminv1.AdminPermission_ADMIN_PERMISSION_USERS_READ, true
	case admin.PermissionUsersReadPII:
		return adminv1.AdminPermission_ADMIN_PERMISSION_USERS_READ_PII, true
	case admin.PermissionUsersAnnotate:
		return adminv1.AdminPermission_ADMIN_PERMISSION_USERS_ANNOTATE, true
	case admin.PermissionUsersGovern:
		return adminv1.AdminPermission_ADMIN_PERMISSION_USERS_GOVERN, true
	case admin.PermissionUsersExport:
		return adminv1.AdminPermission_ADMIN_PERMISSION_USERS_EXPORT, true
	case admin.PermissionRoomsRead:
		return adminv1.AdminPermission_ADMIN_PERMISSION_ROOMS_READ, true
	case admin.PermissionRoomsControl:
		return adminv1.AdminPermission_ADMIN_PERMISSION_ROOMS_CONTROL, true
	case admin.PermissionGamesRead:
		return adminv1.AdminPermission_ADMIN_PERMISSION_GAMES_READ, true
	case admin.PermissionGamesControl:
		return adminv1.AdminPermission_ADMIN_PERMISSION_GAMES_CONTROL, true
	case admin.PermissionGamesRepair:
		return adminv1.AdminPermission_ADMIN_PERMISSION_GAMES_REPAIR, true
	case admin.PermissionSecurityRead:
		return adminv1.AdminPermission_ADMIN_PERMISSION_SECURITY_READ, true
	case admin.PermissionSecurityManagePassword:
		return adminv1.AdminPermission_ADMIN_PERMISSION_SECURITY_MANAGE_PASSWORD, true
	case admin.PermissionSecurityManageMFA:
		return adminv1.AdminPermission_ADMIN_PERMISSION_SECURITY_MANAGE_MFA, true
	case admin.PermissionSecurityManageSessions:
		return adminv1.AdminPermission_ADMIN_PERMISSION_SECURITY_MANAGE_SESSIONS, true
	case admin.PermissionAuditRead:
		return adminv1.AdminPermission_ADMIN_PERMISSION_AUDIT_READ, true
	case admin.PermissionAuditExport:
		return adminv1.AdminPermission_ADMIN_PERMISSION_AUDIT_EXPORT, true
	case admin.PermissionOperationsRead:
		return adminv1.AdminPermission_ADMIN_PERMISSION_OPERATIONS_READ, true
	case admin.PermissionOperationsMaintain:
		return adminv1.AdminPermission_ADMIN_PERMISSION_OPERATIONS_MAINTAIN, true
	default:
		return adminv1.AdminPermission_ADMIN_PERMISSION_UNSPECIFIED, false
	}
}

func elevationSummaryWire(elevation admin.Elevation) *adminv1.AdminElevationSummary {
	snapshot := elevation.Snapshot()
	scope, ok := elevationScopeWire(snapshot.Scope)
	if !ok {
		return nil
	}
	return &adminv1.AdminElevationSummary{
		Scope:     scope,
		GrantedAt: timestamppb.New(snapshot.GrantedAt),
		ExpiresAt: timestamppb.New(snapshot.ExpiresAt),
	}
}

func elevationScopeWire(scope admin.ElevationScope) (adminv1.AdminElevationScope, bool) {
	switch scope {
	case admin.ElevationScopeUsersBulkGovernance:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_BULK_GOVERNANCE, true
	case admin.ElevationScopeUsersRevokeDevices:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_REVOKE_DEVICES, true
	case admin.ElevationScopeUsersDelete:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_DELETE, true
	case admin.ElevationScopeRoomsForceClose:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_ROOMS_FORCE_CLOSE, true
	case admin.ElevationScopeGamesForceTerminate:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_GAMES_FORCE_TERMINATE, true
	case admin.ElevationScopeGamesEmergencyRepair:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_GAMES_EMERGENCY_REPAIR, true
	case admin.ElevationScopeOperationsMaintenance:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_OPERATIONS_MAINTENANCE, true
	case admin.ElevationScopeSecurityDisableMFA:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_SECURITY_DISABLE_MFA, true
	case admin.ElevationScopeSecurityRegenerateRecoveryCodes:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_SECURITY_REGENERATE_RECOVERY_CODES, true
	case admin.ElevationScopeSecurityRevokeSessions:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_SECURITY_REVOKE_SESSIONS, true
	case admin.ElevationScopeAuditExportSensitive:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_AUDIT_EXPORT_SENSITIVE, true
	default:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_UNSPECIFIED, false
	}
}

func elevationScopeFromWire(scope adminv1.AdminElevationScope) (admin.ElevationScope, error) {
	switch scope {
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_BULK_GOVERNANCE:
		return admin.ElevationScopeUsersBulkGovernance, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_REVOKE_DEVICES:
		return admin.ElevationScopeUsersRevokeDevices, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_DELETE:
		return admin.ElevationScopeUsersDelete, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_ROOMS_FORCE_CLOSE:
		return admin.ElevationScopeRoomsForceClose, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_GAMES_FORCE_TERMINATE:
		return admin.ElevationScopeGamesForceTerminate, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_GAMES_EMERGENCY_REPAIR:
		return admin.ElevationScopeGamesEmergencyRepair, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_OPERATIONS_MAINTENANCE:
		return admin.ElevationScopeOperationsMaintenance, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_SECURITY_DISABLE_MFA:
		return admin.ElevationScopeSecurityDisableMFA, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_SECURITY_REGENERATE_RECOVERY_CODES:
		return admin.ElevationScopeSecurityRegenerateRecoveryCodes, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_SECURITY_REVOKE_SESSIONS:
		return admin.ElevationScopeSecurityRevokeSessions, nil
	case adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_AUDIT_EXPORT_SENSITIVE:
		return admin.ElevationScopeAuditExportSensitive, nil
	default:
		return "", admin.ErrInvalidInput
	}
}

func operationResultWire(result admin.OperationResult) *commonv1.OperationResult {
	message := &commonv1.OperationResult{OperationId: result.OperationID.Value(), Replayed: result.Replayed}
	if result.ResultID != uuid.Nil {
		message.ResultId = result.ResultID.String()
	}
	if !result.SecretExpiresAt.IsZero() {
		message.SecretExpiresAt = timestamppb.New(result.SecretExpiresAt)
	}
	return message
}

func secondFactorWire(usedRecoveryCode bool) adminv1.AdminElevationSecondFactor {
	if usedRecoveryCode {
		return adminv1.AdminElevationSecondFactor_ADMIN_ELEVATION_SECOND_FACTOR_RECOVERY_CODE
	}
	return adminv1.AdminElevationSecondFactor_ADMIN_ELEVATION_SECOND_FACTOR_TOTP
}

func secretOperationScope(operation adminv1.AdminSecretOperation) (secretresult.Scope, error) {
	switch operation {
	case adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_TOTP_ENROLLMENT:
		return secretresult.ScopeAdminTOTPEnrollment, nil
	case adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_INITIAL_RECOVERY_CODES:
		return secretresult.ScopeAdminInitialRecoveryCodes, nil
	case adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_REGENERATED_RECOVERY_CODES:
		return secretresult.ScopeAdminRegenerateRecoveryCodes, nil
	default:
		return "", admin.ErrInvalidInput
	}
}

func requestFlowID(value string) (challenge.RequestFlowID, error) {
	if !validMetadata(value) {
		return "", admin.ErrInvalidInput
	}
	return challenge.RequestFlowID(value), nil
}

func canonicalUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil || parsed.String() != strings.TrimSpace(value) {
		return uuid.Nil, admin.ErrInvalidInput
	}
	return parsed, nil
}

func wireVersion(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, admin.ErrInvalidInput
	}
	return int64(value), nil
}

func enrollmentVersionOf(enrollment *admin.Enrollment) int64 {
	if enrollment == nil {
		return 0
	}
	return enrollment.Snapshot().EnrollmentVersion
}

func transportOperation(domain string, value string) (idempotency.OperationID, idempotency.Digest, error) {
	entropy, err := security.RandomBytes(16)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, err
	}
	defer clear(entropy)
	operationID, err := idempotency.NewOperationID(entropy)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, err
	}
	digestValue := sha256.Sum256([]byte(domain + "\x00" + value))
	return operationID, digestValue, nil
}

var _ adminv1connect.AdminAuthServiceHandler = (*Handler)(nil)
