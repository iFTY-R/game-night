package admin

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminTransportContext carries cookie/header values kept outside protobuf messages by the API layer.
// Keeping these values in context prevents accidental JSON/protobuf caching of bearer credentials.
type AdminTransportContext struct {
	CookieToken   string
	CSRFToken     string
	Origin        string
	ClientIP      string
	RequestFlowID string
}

type adminTransportContextKey struct{}

func WithAdminTransportContext(ctx context.Context, transport AdminTransportContext) context.Context {
	return context.WithValue(ctx, adminTransportContextKey{}, transport)
}

func adminTransport(ctx context.Context) AdminTransportContext {
	transport, _ := ctx.Value(adminTransportContextKey{}).(AdminTransportContext)
	return transport
}

// AdminCookieEffects is implemented by the API transport so this adapter never owns Cookie names or policies.
type AdminCookieEffects interface {
	SetAdminChallenge(HeaderWriter, IssuedChallenge) error
	SetAdminSession(HeaderWriter, IssuedSession) error
	ClearAdminSession(HeaderWriter) error
}

// HeaderWriter is the only response mutation capability the domain adapter needs for Set-Cookie delivery.
type HeaderWriter interface {
	Add(string, string)
}

// ConnectAdminService is the transport adapter for AdminAuthService. It contains no persistence logic.
type ConnectAdminService struct {
	service       *Service
	cookieEffects AdminCookieEffects
}

func NewConnectAdminService(service *Service) (*ConnectAdminService, error) {
	if service == nil {
		return nil, ErrInvalidInput
	}
	return &ConnectAdminService{service: service}, nil
}

// NewConnectAdminServiceWithCookieEffects requires the API-owned Cookie delivery boundary for runtime handlers.
func NewConnectAdminServiceWithCookieEffects(service *Service, effects AdminCookieEffects) (*ConnectAdminService, error) {
	if service == nil || effects == nil {
		return nil, ErrInvalidInput
	}
	return &ConnectAdminService{service: service, cookieEffects: effects}, nil
}

var _ adminv1connect.AdminAuthServiceHandler = (*ConnectAdminService)(nil)

func (adapter *ConnectAdminService) GetSetupState(ctx context.Context, _ *connect.Request[adminv1.GetSetupStateRequest]) (*connect.Response[adminv1.GetSetupStateResponse], error) {
	state, err := adapter.service.GetSetupState(ctx)
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.GetSetupStateResponse{State: setupStateWire(state)}), nil
}

func (adapter *ConnectAdminService) BeginAdminLogin(ctx context.Context, request *connect.Request[adminv1.BeginAdminLoginRequest]) (*connect.Response[adminv1.BeginAdminLoginResponse], error) {
	transport := adminTransport(ctx)
	issued, err := adapter.service.BeginAdminLogin(ctx, AdminChallengeRequest{CanonicalOrigin: transport.Origin, RequestFlowID: challenge.RequestFlowID(request.Msg.GetRequestFlowId()), MaxAttempts: challenge.DefaultMaxAttempts})
	if err != nil {
		return nil, adminConnectError(err)
	}
	response := connect.NewResponse(&adminv1.BeginAdminLoginResponse{Challenge: &commonv1.AnonymousChallenge{ChallengeProof: issued.Credentials.BodyProof, ExpiresAt: timestamppb.New(issued.Challenge.Snapshot().ExpiresAt)}})
	if adapter.cookieEffects != nil {
		if err := adapter.cookieEffects.SetAdminChallenge(response.Header(), issued); err != nil {
			return nil, adminConnectError(err)
		}
	}
	return response, nil
}

func (adapter *ConnectAdminService) LoginPassword(ctx context.Context, request *connect.Request[adminv1.LoginPasswordRequest]) (*connect.Response[adminv1.LoginPasswordResponse], error) {
	transport := adminTransport(ctx)
	operationID, digest, err := transportOperation("admin.login_password", request.Msg.GetPassword())
	if err != nil {
		return nil, adminConnectError(err)
	}
	result, err := adapter.service.LoginPassword(ctx, LoginPasswordCommand{Credentials: challenge.Credentials{CookieToken: transport.CookieToken, BodyProof: request.Msg.GetChallengeProof()}, Password: request.Msg.GetPassword(), OperationID: operationID, RequestDigest: digest, CanonicalOrigin: transport.Origin, RequestFlowID: challenge.RequestFlowID(transport.RequestFlowID), ClientIP: transport.ClientIP})
	if err != nil {
		return nil, adminConnectError(err)
	}
	response := connect.NewResponse(&adminv1.LoginPasswordResponse{NextStep: nextStepWire(result.NextStep), ExpiresAt: timestamppb.New(result.ExpiresAt)})
	if err := adapter.setSessionCookie(response.Header(), result.Session); err != nil {
		return nil, adminConnectError(err)
	}
	return response, nil
}

func (adapter *ConnectAdminService) VerifyTotp(ctx context.Context, request *connect.Request[adminv1.VerifyTotpRequest]) (*connect.Response[adminv1.VerifyTotpResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	result, err := adapter.service.VerifyTotp(ctx, VerifyTOTPCommand{Session: session, SessionToken: transport.CookieToken, CSRFToken: transport.CSRFToken, Code: request.Msg.GetTotpCode(), ClientIP: transport.ClientIP})
	if err != nil {
		return nil, adminConnectError(err)
	}
	response := connect.NewResponse(&adminv1.VerifyTotpResponse{Session: sessionSummary(result.Session)})
	if err := adapter.setSessionCookie(response.Header(), result.Session); err != nil {
		return nil, adminConnectError(err)
	}
	return response, nil
}

func (adapter *ConnectAdminService) ChangeInitialPassword(ctx context.Context, request *connect.Request[adminv1.ChangeInitialPasswordRequest]) (*connect.Response[adminv1.ChangeInitialPasswordResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	result, err := adapter.service.ChangeInitialPassword(ctx, ChangePasswordCommand{Session: session, SessionToken: transport.CookieToken, CSRFToken: transport.CSRFToken, New: request.Msg.GetNewPassword(), ClientIP: transport.ClientIP})
	if err != nil {
		return nil, adminConnectError(err)
	}
	response := connect.NewResponse(&adminv1.ChangeInitialPasswordResponse{NextStep: adminv1.AdminNextStep_ADMIN_NEXT_STEP_ENROLL_TOTP, ExpiresAt: timestamppb.New(result.Session.Session.Snapshot().AbsoluteExpiresAt)})
	if err := adapter.setSessionCookie(response.Header(), result.Session); err != nil {
		return nil, adminConnectError(err)
	}
	return response, nil
}

func (adapter *ConnectAdminService) BeginTotpEnrollment(ctx context.Context, request *connect.Request[adminv1.BeginTotpEnrollmentRequest]) (*connect.Response[adminv1.BeginTotpEnrollmentResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, adminConnectError(ErrInvalidInput)
	}
	result, err := adapter.service.BeginTotpEnrollment(ctx, BeginEnrollmentCommand{Session: session, SessionToken: transport.CookieToken, CSRFToken: transport.CSRFToken, OperationID: operationID})
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.BeginTotpEnrollmentResponse{Result: operationResultWire(result.Operation), TotpSecret: result.Secret, OtpauthUri: result.URI}), nil
}

func (adapter *ConnectAdminService) CompleteTotpEnrollment(ctx context.Context, request *connect.Request[adminv1.CompleteTotpEnrollmentRequest]) (*connect.Response[adminv1.CompleteTotpEnrollmentResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	recoveryOperationID, err := idempotency.ParseOperationID(request.Msg.GetRecoveryCodesOperationId())
	if err != nil {
		return nil, adminConnectError(ErrInvalidInput)
	}
	result, err := adapter.service.CompleteTotpEnrollment(ctx, CompleteEnrollmentCommand{Session: session, SessionToken: transport.CookieToken, CSRFToken: transport.CSRFToken, EnrollmentOperationID: request.Msg.GetEnrollmentOperationId(), RecoveryCodesOperationID: recoveryOperationID, TOTPPasscode: request.Msg.GetTotpCode()})
	if err != nil {
		return nil, adminConnectError(err)
	}
	response := connect.NewResponse(&adminv1.CompleteTotpEnrollmentResponse{Result: operationResultWire(result.Operation), RecoveryCodes: result.RecoveryCodes, Session: sessionSummary(result.Session)})
	if err := adapter.setSessionCookie(response.Header(), result.Session); err != nil {
		return nil, adminConnectError(err)
	}
	return response, nil
}

func (adapter *ConnectAdminService) ConfirmAdminSecretReceipt(ctx context.Context, request *connect.Request[adminv1.ConfirmAdminSecretReceiptRequest]) (*connect.Response[adminv1.ConfirmAdminSecretReceiptResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, adminConnectError(ErrInvalidInput)
	}
	resultID, err := uuid.Parse(request.Msg.GetResultId())
	if err != nil || resultID.String() != request.Msg.GetResultId() {
		return nil, adminConnectError(ErrInvalidInput)
	}
	confirmed, err := adapter.service.ConfirmAdminSecretReceipt(ctx, session, transport.CookieToken, transport.CSRFToken, secretOperationScope(request.Msg.GetOperation()), operationID, resultID)
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.ConfirmAdminSecretReceiptResponse{Confirmed: confirmed}), nil
}

func (adapter *ConnectAdminService) RecoverAdmin(ctx context.Context, request *connect.Request[adminv1.RecoverAdminRequest]) (*connect.Response[adminv1.RecoverAdminResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	result, err := adapter.service.RecoverAdmin(ctx, RecoverCommand{Session: session, SessionToken: transport.CookieToken, CSRFToken: transport.CSRFToken, Code: request.Msg.GetRecoveryCode(), ClientIP: transport.ClientIP})
	if err != nil {
		return nil, adminConnectError(err)
	}
	response := connect.NewResponse(&adminv1.RecoverAdminResponse{NextStep: adminv1.AdminNextStep_ADMIN_NEXT_STEP_REBIND_TOTP, Session: sessionSummary(result.Session)})
	if err := adapter.setSessionCookie(response.Header(), result.Session); err != nil {
		return nil, adminConnectError(err)
	}
	return response, nil
}

func (adapter *ConnectAdminService) ChangeAdminPassword(ctx context.Context, request *connect.Request[adminv1.ChangeAdminPasswordRequest]) (*connect.Response[adminv1.ChangeAdminPasswordResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	result, err := adapter.service.ChangeAdminPassword(ctx, ChangePasswordCommand{Session: session, SessionToken: transport.CookieToken, CSRFToken: transport.CSRFToken, Current: request.Msg.GetCurrentPassword(), New: request.Msg.GetNewPassword(), ClientIP: transport.ClientIP})
	if err != nil {
		return nil, adminConnectError(err)
	}
	response := connect.NewResponse(&adminv1.ChangeAdminPasswordResponse{NextStep: adminv1.AdminNextStep_ADMIN_NEXT_STEP_REBIND_TOTP, Session: sessionSummary(result.Session)})
	if err := adapter.setSessionCookie(response.Header(), result.Session); err != nil {
		return nil, adminConnectError(err)
	}
	return response, nil
}

func (adapter *ConnectAdminService) BeginTotpRebind(ctx context.Context, request *connect.Request[adminv1.BeginTotpRebindRequest]) (*connect.Response[adminv1.BeginTotpRebindResponse], error) {
	result, err := adapter.BeginTotpEnrollment(ctx, connect.NewRequest(&adminv1.BeginTotpEnrollmentRequest{OperationId: request.Msg.GetOperationId()}))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.BeginTotpRebindResponse{Result: result.Msg.GetResult(), TotpSecret: result.Msg.GetTotpSecret(), OtpauthUri: result.Msg.GetOtpauthUri()}), nil
}

func (adapter *ConnectAdminService) CompleteTotpRebind(ctx context.Context, request *connect.Request[adminv1.CompleteTotpRebindRequest]) (*connect.Response[adminv1.CompleteTotpRebindResponse], error) {
	result, err := adapter.CompleteTotpEnrollment(ctx, connect.NewRequest(&adminv1.CompleteTotpEnrollmentRequest{EnrollmentOperationId: request.Msg.GetEnrollmentOperationId(), RecoveryCodesOperationId: request.Msg.GetRecoveryCodesOperationId(), TotpCode: request.Msg.GetTotpCode()}))
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&adminv1.CompleteTotpRebindResponse{Result: result.Msg.GetResult(), RecoveryCodes: result.Msg.GetRecoveryCodes(), Session: result.Msg.GetSession()})
	for _, value := range result.Header().Values("Set-Cookie") {
		response.Header().Add("Set-Cookie", value)
	}
	return response, nil
}

func (adapter *ConnectAdminService) RegenerateAdminRecoveryCodes(ctx context.Context, request *connect.Request[adminv1.RegenerateAdminRecoveryCodesRequest]) (*connect.Response[adminv1.RegenerateAdminRecoveryCodesResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, adminConnectError(ErrInvalidInput)
	}
	result, err := adapter.service.RegenerateAdminRecoveryCodes(ctx, RegenerateRecoveryCodesCommand{Session: session, SessionToken: transport.CookieToken, CSRFToken: transport.CSRFToken, OperationID: operationID, TOTPPasscode: request.Msg.GetTotpCode(), ClientIP: transport.ClientIP})
	if err != nil {
		return nil, adminConnectError(err)
	}
	return connect.NewResponse(&adminv1.RegenerateAdminRecoveryCodesResponse{Result: operationResultWire(result.Operation), RecoveryCodes: result.RecoveryCodes}), nil
}

func (adapter *ConnectAdminService) LogoutAdmin(ctx context.Context, _ *connect.Request[adminv1.LogoutAdminRequest]) (*connect.Response[adminv1.LogoutAdminResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	if err := adapter.service.LogoutAdmin(ctx, session, transport.CookieToken, transport.CSRFToken); err != nil {
		return nil, adminConnectError(err)
	}
	response := connect.NewResponse(&adminv1.LogoutAdminResponse{LoggedOut: true})
	if err := adapter.clearSessionCookie(response.Header()); err != nil {
		return nil, adminConnectError(err)
	}
	return response, nil
}

func (adapter *ConnectAdminService) LogoutAllAdminSessions(ctx context.Context, _ *connect.Request[adminv1.LogoutAllAdminSessionsRequest]) (*connect.Response[adminv1.LogoutAllAdminSessionsResponse], error) {
	transport := adminTransport(ctx)
	session, err := adapter.sessionFromTransport(ctx, transport)
	if err != nil {
		return nil, adminConnectError(err)
	}
	count, err := adapter.service.LogoutAllAdminSessions(ctx, session, transport.CookieToken, transport.CSRFToken)
	if err != nil {
		return nil, adminConnectError(err)
	}
	response := connect.NewResponse(&adminv1.LogoutAllAdminSessionsResponse{RevokedSessions: int32(count)})
	if err := adapter.clearSessionCookie(response.Header()); err != nil {
		return nil, adminConnectError(err)
	}
	return response, nil
}

func (adapter *ConnectAdminService) setSessionCookie(header HeaderWriter, session IssuedSession) error {
	if adapter.cookieEffects == nil {
		return nil
	}
	return adapter.cookieEffects.SetAdminSession(header, session)
}

func (adapter *ConnectAdminService) clearSessionCookie(header HeaderWriter) error {
	if adapter.cookieEffects == nil {
		return nil
	}
	return adapter.cookieEffects.ClearAdminSession(header)
}

func (adapter *ConnectAdminService) sessionFromTransport(ctx context.Context, transport AdminTransportContext) (Session, error) {
	selector, secret, err := parseSessionToken(transport.CookieToken)
	clear(secret)
	if err != nil {
		return Session{}, ErrAuthentication
	}
	var session Session
	err = adapter.service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		var err error
		session, err = transaction.Sessions().GetForUpdate(ctx, selector)
		return err
	})
	return session, err
}

func transportOperation(domain, value string) (idempotency.OperationID, idempotency.Digest, error) {
	entropy, err := security.RandomBytes(16)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, err
	}
	defer clearSessionBytes(entropy)
	operationID, err := idempotency.NewOperationID(entropy)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, err
	}
	digest := sha256.Sum256([]byte(domain + "\x00" + value))
	return operationID, digest, nil
}

func setupStateWire(state SetupState) adminv1.AdminSetupState {
	switch state {
	case SetupStateBootstrapPending:
		return adminv1.AdminSetupState_ADMIN_SETUP_STATE_BOOTSTRAP_PENDING
	case SetupStateSetupRequired:
		return adminv1.AdminSetupState_ADMIN_SETUP_STATE_SETUP_REQUIRED
	default:
		return adminv1.AdminSetupState_ADMIN_SETUP_STATE_ACTIVE
	}
}

func nextStepWire(step NextStep) adminv1.AdminNextStep {
	switch step {
	case NextStepChangePassword:
		return adminv1.AdminNextStep_ADMIN_NEXT_STEP_CHANGE_PASSWORD
	case NextStepEnrollTOTP, NextStepRebindTOTP:
		return adminv1.AdminNextStep_ADMIN_NEXT_STEP_ENROLL_TOTP
	case NextStepVerifyMFA:
		return adminv1.AdminNextStep_ADMIN_NEXT_STEP_VERIFY_MFA
	default:
		return adminv1.AdminNextStep_ADMIN_NEXT_STEP_AUTHENTICATED
	}
}

func sessionSummary(session IssuedSession) *adminv1.AdminSessionSummary {
	snapshot := session.Session.Snapshot()
	return &adminv1.AdminSessionSummary{AdminId: snapshot.AdminID.String(), Kind: sessionKindWire(snapshot.Kind), Permissions: permissionsWire(snapshot.Kind), IdleExpiresAt: timestamppb.New(snapshot.IdleExpiresAt), AbsoluteExpiresAt: timestamppb.New(snapshot.AbsoluteExpiresAt)}
}

func sessionKindWire(kind SessionKind) adminv1.AdminSessionKind {
	switch kind {
	case SessionKindSetupPasswordPending:
		return adminv1.AdminSessionKind_ADMIN_SESSION_KIND_SETUP_PASSWORD_PENDING
	case SessionKindTOTPEnrollmentPending:
		return adminv1.AdminSessionKind_ADMIN_SESSION_KIND_TOTP_ENROLLMENT_PENDING
	case SessionKindMFAPending:
		return adminv1.AdminSessionKind_ADMIN_SESSION_KIND_MFA_PENDING
	case SessionKindRecoveryPending:
		return adminv1.AdminSessionKind_ADMIN_SESSION_KIND_RECOVERY_PENDING
	default:
		return adminv1.AdminSessionKind_ADMIN_SESSION_KIND_FULL
	}
}

func permissionsWire(kind SessionKind) []adminv1.AdminPermission {
	if kind != SessionKindFull {
		return nil
	}
	return []adminv1.AdminPermission{
		adminv1.AdminPermission_ADMIN_PERMISSION_GET_USER, adminv1.AdminPermission_ADMIN_PERMISSION_GET_REAL_NAME,
		adminv1.AdminPermission_ADMIN_PERMISSION_UPDATE_REAL_NAME, adminv1.AdminPermission_ADMIN_PERMISSION_EXPORT_PROFILE,
		adminv1.AdminPermission_ADMIN_PERMISSION_MANAGE_RECOVERY, adminv1.AdminPermission_ADMIN_PERMISSION_FORCE_USERNAME,
		adminv1.AdminPermission_ADMIN_PERMISSION_SUSPEND_USER, adminv1.AdminPermission_ADMIN_PERMISSION_DELETE_USER,
		adminv1.AdminPermission_ADMIN_PERMISSION_REVOKE_DEVICE, adminv1.AdminPermission_ADMIN_PERMISSION_READ_AUDIT,
	}
}

func operationResultWire(result OperationResult) *commonv1.OperationResult {
	if !result.OperationID.Valid() || result.ResultID == uuid.Nil || result.SecretExpiresAt.IsZero() {
		return nil
	}
	return &commonv1.OperationResult{OperationId: result.OperationID.Value(), ResultId: result.ResultID.String(), SecretExpiresAt: timestamppb.New(result.SecretExpiresAt), Replayed: result.Replayed}
}

func secretOperationScope(operation adminv1.AdminSecretOperation) secretresult.Scope {
	switch operation {
	case adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_TOTP_ENROLLMENT:
		return secretresult.ScopeAdminTOTPEnrollment
	case adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_INITIAL_RECOVERY_CODES:
		return secretresult.ScopeAdminInitialRecoveryCodes
	case adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_TOTP_REBIND:
		return secretresult.ScopeAdminTOTPRebind
	case adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_REGENERATE_RECOVERY_CODES:
		return secretresult.ScopeAdminRegenerateRecoveryCodes
	case adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_ASSISTED_RECOVERY_GRANT:
		return secretresult.ScopeAdminAssistedRecoveryGrant
	default:
		return ""
	}
}

func adminConnectError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrPasswordPolicy),
		errors.Is(err, profile.ErrInvalidProfileInput), errors.Is(err, profile.ErrProfileExportCursor):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrAuthentication), errors.Is(err, ErrRecoveryInvalid):
		return connect.NewError(connect.CodeUnauthenticated, ErrAuthentication)
	case errors.Is(err, ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, identity.ErrUserNotFound), errors.Is(err, profile.ErrProfileNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, identity.ErrUsernameUnavailable):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, identity.ErrIdentityConcurrentTransition), errors.Is(err, identity.ErrDeviceConcurrentTransition),
		errors.Is(err, profile.ErrProfileConcurrentTransition), errors.Is(err, audit.ErrHeadConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, ratelimit.ErrRejected):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, ratelimit.ErrUnavailable), errors.Is(err, profile.ErrPIIKeyUnavailable),
		errors.Is(err, audit.ErrSensitiveWriteBlocked), errors.Is(err, audit.ErrRepositoryUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("administrator service temporarily unavailable"))
	case errors.Is(err, identity.ErrUserStatus), errors.Is(err, identity.ErrRecoveryConcurrentTransition),
		errors.Is(err, profile.ErrProfileExportClosed), errors.Is(err, profile.ErrProfileExportExpired),
		errors.Is(err, secretresult.ErrSecretNoLongerAvailable), errors.Is(err, ErrSessionExpired),
		errors.Is(err, ErrSessionRevoked), errors.Is(err, ErrUnavailable), errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("administrator service unavailable"))
	}
}

var _ = timestamppb.New(time.Time{})
