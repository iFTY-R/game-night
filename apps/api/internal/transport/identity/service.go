// Package identity adapts the user identity domain to its isolated Connect service.
package identity

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	stderrors "errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	transporterrors "github.com/iFTY-R/game-night/apps/api/internal/transport/errors"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	domain "github.com/iFTY-R/game-night/platform/identity"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// RequestFlowIDHeader binds anonymous completion requests to the browser flow that began the challenge.
	RequestFlowIDHeader = "X-Request-Flow-ID"
	// RequestIDHeader supplies the stable audit correlation ID for user security mutations.
	RequestIDHeader = "X-Request-ID"
	// deviceCursorVersion permits future cursor format changes without ambiguous decoding.
	deviceCursorVersion byte = 1
	// deviceCursorBytes stores one version byte, nanosecond timestamp, and UUID.
	deviceCursorBytes = 1 + 8 + 16
	// maximumMetadataBytes bounds request correlation headers retained by audit commands.
	maximumMetadataBytes = 128
)

// Service keeps user-specific origin, CSRF, proxy, Cookie, and auth state out of administrator handlers.
type Service struct {
	domain  *domain.Service
	cookies *cookies.Manager
	origins *origin.UserValidator
	csrf    *csrf.UserValidator
	clients *proxy.Resolver
	clock   clock.Clock
}

// NewService validates complete user transport wiring before a generated handler can be registered.
func NewService(
	domainService *domain.Service,
	cookieManager *cookies.Manager,
	originValidator *origin.UserValidator,
	csrfValidator *csrf.UserValidator,
	clientResolver *proxy.Resolver,
	source clock.Clock,
) (*Service, error) {
	if domainService == nil || cookieManager == nil || originValidator == nil || csrfValidator == nil || clientResolver == nil || source == nil {
		return nil, domain.ErrInvalidIdentityRequest
	}
	return &Service{domain: domainService, cookies: cookieManager, origins: originValidator, csrf: csrfValidator, clients: clientResolver, clock: source}, nil
}

var _ identityv1connect.IdentityServiceHandler = (*Service)(nil)

// BeginIdentityBootstrap creates a login-CSRF challenge only after user-origin and client-address validation.
func (service *Service) BeginIdentityBootstrap(ctx context.Context, request *connect.Request[identityv1.BeginIdentityBootstrapRequest]) (*connect.Response[identityv1.BeginIdentityBootstrapResponse], error) {
	httpRequest := requestHTTP(request)
	acceptedOrigin, clientIP, err := service.originAndClient(httpRequest)
	if err != nil {
		return nil, err
	}
	flowID, err := requestFlowID(request.Msg.GetRequestFlowId())
	if err != nil {
		return nil, err
	}
	issued, err := service.domain.BeginIdentityBootstrap(ctx, domain.BeginIdentityBootstrapCommand{CanonicalOrigin: acceptedOrigin, RequestFlowID: flowID, ClientIP: clientIP})
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&identityv1.BeginIdentityBootstrapResponse{Challenge: challengeWire(issued)})
	if err := service.cookies.SetUserChallenge(responseHeader(response), issued); err != nil {
		return nil, err
	}
	return response, nil
}

// BootstrapIdentity creates or inspects a device without allowing malformed existing Cookies to fall back to creation.
func (service *Service) BootstrapIdentity(ctx context.Context, request *connect.Request[identityv1.BootstrapIdentityRequest]) (*connect.Response[identityv1.BootstrapIdentityResponse], error) {
	httpRequest := requestHTTP(request)
	acceptedOrigin, clientIP, err := service.originAndClient(httpRequest)
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, domain.ErrInvalidIdentityRequest
	}
	command := domain.BootstrapIdentityCommand{CanonicalOrigin: acceptedOrigin, OperationID: operationID, ClientIP: clientIP, DeviceLabel: request.Msg.GetDeviceLabel()}
	challengeCredential, err := cookies.ReadUserChallenge(httpRequest)
	if err != nil {
		return nil, domain.ErrDeviceAuthentication
	}
	flowID, err := singleMetadata(request.Header(), RequestFlowIDHeader)
	if err != nil {
		return nil, err
	}
	command.RequestFlowID = challenge.RequestFlowID(flowID)
	command.ChallengeCredentials = challenge.Credentials{CookieToken: challengeCredential.CookieToken(), BodyProof: request.Msg.GetChallengeProof()}
	credentials, present, err := cookies.ReadOptionalUserDevice(httpRequest)
	if err != nil {
		return nil, domain.ErrDeviceAuthentication
	}
	if present {
		csrfToken, validateErr := service.csrf.Validate(httpRequest)
		if validateErr != nil {
			return nil, validateErr
		}
		command.DeviceToken, command.CSRFToken = credentials.CookieToken(), csrfToken
	}
	result, err := service.domain.BootstrapIdentity(ctx, command)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&identityv1.BootstrapIdentityResponse{
		Result: operationResultWire(result.Operation), User: userWire(result.User), Device: credentialWire(result.Device, service.clock.Now()),
		CredentialInstruction: credentialInstructionWire(result.CredentialInstruction),
	})
	if err := service.applyDeviceResult(response, result.Device, result.DeviceSecrets, result.CredentialInstruction, result.AccountInstruction); err != nil {
		return nil, err
	}
	return response, nil
}

// CompleteOnboarding activates the current device identity and returns the one-time recovery code.
func (service *Service) CompleteOnboarding(ctx context.Context, request *connect.Request[identityv1.CompleteOnboardingRequest]) (*connect.Response[identityv1.CompleteOnboardingResponse], error) {
	auth, err := service.authenticatedWrite(requestHTTP(request))
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, domain.ErrInvalidIdentityRequest
	}
	result, err := service.domain.CompleteOnboarding(ctx, domain.CompleteOnboardingCommand{DeviceToken: auth.deviceToken, CSRFToken: auth.csrfToken, ClientIP: auth.clientIP, Username: request.Msg.GetUsername(), OperationID: operationID})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&identityv1.CompleteOnboardingResponse{Result: operationResultWire(result.Operation), User: userWire(result.User), RecoveryCode: result.RecoveryCode}), nil
}

// GetCurrentIdentity authenticates the bearer without requiring Redis, Origin, or a write CSRF header.
func (service *Service) GetCurrentIdentity(ctx context.Context, request *connect.Request[identityv1.GetCurrentIdentityRequest]) (*connect.Response[identityv1.GetCurrentIdentityResponse], error) {
	credentials, err := cookies.ReadUserDevice(requestHTTP(request))
	if err != nil {
		return nil, domain.ErrDeviceAuthentication
	}
	result, err := service.domain.GetCurrentIdentity(ctx, domain.GetCurrentIdentityCommand{DeviceToken: credentials.CookieToken()})
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&identityv1.GetCurrentIdentityResponse{User: userWire(result.User), CurrentDevice: credentialWire(result.Device, service.clock.Now())})
	if err := service.applyDeviceResult(response, result.Device, nil, result.CredentialInstruction, result.AccountInstruction); err != nil {
		return nil, err
	}
	return response, nil
}

// ChangeUsername applies the three-bucket policy only after Origin and double-submit CSRF validation.
func (service *Service) ChangeUsername(ctx context.Context, request *connect.Request[identityv1.ChangeUsernameRequest]) (*connect.Response[identityv1.ChangeUsernameResponse], error) {
	auth, err := service.authenticatedWrite(requestHTTP(request))
	if err != nil {
		return nil, err
	}
	result, err := service.domain.ChangeUsername(ctx, domain.ChangeUsernameCommand{DeviceToken: auth.deviceToken, CSRFToken: auth.csrfToken, ClientIP: auth.clientIP, Username: request.Msg.GetUsername()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&identityv1.ChangeUsernameResponse{User: userWire(result.User)}), nil
}

// RotateRecoveryCode returns the replayable one-time code under current device and CSRF authority.
func (service *Service) RotateRecoveryCode(ctx context.Context, request *connect.Request[identityv1.RotateRecoveryCodeRequest]) (*connect.Response[identityv1.RotateRecoveryCodeResponse], error) {
	auth, err := service.authenticatedWrite(requestHTTP(request))
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, domain.ErrInvalidIdentityRequest
	}
	requestID, err := singleMetadata(request.Header(), RequestIDHeader)
	if err != nil {
		return nil, err
	}
	result, err := service.domain.RotateRecoveryCode(ctx, domain.RotateRecoveryCodeCommand{DeviceToken: auth.deviceToken, CSRFToken: auth.csrfToken, OperationID: operationID, RequestID: requestID})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&identityv1.RotateRecoveryCodeResponse{Result: operationResultWire(result.Operation), RecoveryCode: result.RecoveryCode}), nil
}

// BeginRecoveryChallenge creates the user recovery challenge under the same isolated Origin policy as bootstrap.
func (service *Service) BeginRecoveryChallenge(ctx context.Context, request *connect.Request[identityv1.BeginRecoveryChallengeRequest]) (*connect.Response[identityv1.BeginRecoveryChallengeResponse], error) {
	httpRequest := requestHTTP(request)
	acceptedOrigin, _, err := service.originAndClient(httpRequest)
	if err != nil {
		return nil, err
	}
	flowID, err := requestFlowID(request.Msg.GetRequestFlowId())
	if err != nil {
		return nil, err
	}
	issued, err := service.domain.BeginRecoveryChallenge(ctx, domain.BeginRecoveryChallengeCommand{CanonicalOrigin: acceptedOrigin, RequestFlowID: flowID})
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&identityv1.BeginRecoveryChallengeResponse{Challenge: challengeWire(issued)})
	if err := service.cookies.SetUserChallenge(responseHeader(response), issued); err != nil {
		return nil, err
	}
	return response, nil
}

// BeginRecovery consumes the anonymous challenge but not the long-lived recovery credential.
func (service *Service) BeginRecovery(ctx context.Context, request *connect.Request[identityv1.BeginRecoveryRequest]) (*connect.Response[identityv1.BeginRecoveryResponse], error) {
	httpRequest := requestHTTP(request)
	acceptedOrigin, clientIP, err := service.originAndClient(httpRequest)
	if err != nil {
		return nil, err
	}
	challengeCredential, err := cookies.ReadUserChallenge(httpRequest)
	if err != nil {
		return nil, domain.ErrRecoveryInvalid
	}
	flowID, err := singleMetadata(request.Header(), RequestFlowIDHeader)
	if err != nil {
		return nil, err
	}
	result, err := service.domain.BeginRecovery(ctx, domain.BeginRecoveryCommand{
		CanonicalOrigin: acceptedOrigin, RequestFlowID: challenge.RequestFlowID(flowID),
		ChallengeCredentials: challenge.Credentials{CookieToken: challengeCredential.CookieToken(), BodyProof: request.Msg.GetChallengeProof()},
		RecoveryCode:         request.Msg.GetRecoveryCode(), ClientIP: clientIP,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&identityv1.BeginRecoveryResponse{RecoveryGrant: result.RecoveryGrant, ExpiresAt: timestamppb.New(result.ExpiresAt)}), nil
}

// CompleteRecovery atomically consumes the recovery authority and installs the exact replayed device credentials.
func (service *Service) CompleteRecovery(ctx context.Context, request *connect.Request[identityv1.CompleteRecoveryRequest]) (*connect.Response[identityv1.CompleteRecoveryResponse], error) {
	httpRequest := requestHTTP(request)
	acceptedOrigin, _, err := service.originAndClient(httpRequest)
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, domain.ErrInvalidIdentityRequest
	}
	policy, err := recoveryPolicyFromWire(request.Msg.GetDevicePolicy())
	if err != nil {
		return nil, err
	}
	requestID, err := singleMetadata(request.Header(), RequestIDHeader)
	if err != nil {
		return nil, err
	}
	result, err := service.domain.CompleteRecovery(ctx, domain.CompleteRecoveryCommand{CanonicalOrigin: acceptedOrigin, RecoveryGrant: request.Msg.GetRecoveryGrant(), OperationID: operationID, DeviceLabel: request.Msg.GetDeviceLabel(), DevicePolicy: policy, RequestID: requestID})
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&identityv1.CompleteRecoveryResponse{Result: operationResultWire(result.Operation), User: userWire(result.User), Device: credentialWire(result.Device, service.clock.Now()), RecoveryCode: result.RecoveryCode})
	if result.DeviceSecrets == nil {
		return nil, domain.ErrIdentityIntegrity
	}
	if err := service.cookies.SetUserDevice(responseHeader(response), result.Device, *result.DeviceSecrets); err != nil {
		return nil, err
	}
	return response, nil
}

// ConfirmSecretReceipt erases one exact envelope only after current device and CSRF authentication.
func (service *Service) ConfirmSecretReceipt(ctx context.Context, request *connect.Request[identityv1.ConfirmSecretReceiptRequest]) (*connect.Response[identityv1.ConfirmSecretReceiptResponse], error) {
	auth, err := service.authenticatedWrite(requestHTTP(request))
	if err != nil {
		return nil, err
	}
	operation, err := secretOperationFromWire(request.Msg.GetOperation())
	if err != nil {
		return nil, err
	}
	operationID, err := idempotency.ParseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, domain.ErrInvalidIdentityRequest
	}
	resultID, err := canonicalUUID(request.Msg.GetResultId())
	if err != nil {
		return nil, err
	}
	result, err := service.domain.ConfirmSecretReceipt(ctx, domain.ConfirmSecretReceiptCommand{DeviceToken: auth.deviceToken, CSRFToken: auth.csrfToken, Operation: operation, OperationID: operationID, ResultID: resultID})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&identityv1.ConfirmSecretReceiptResponse{Confirmed: result.Confirmed}), nil
}

// ListDevices returns a bounded user-owned page without requiring Redis or a write CSRF proof.
func (service *Service) ListDevices(ctx context.Context, request *connect.Request[identityv1.ListDevicesRequest]) (*connect.Response[identityv1.ListDevicesResponse], error) {
	credentials, err := cookies.ReadUserDevice(requestHTTP(request))
	if err != nil {
		return nil, domain.ErrDeviceAuthentication
	}
	after, pageSize, err := pageRequest(request.Msg.GetPage())
	if err != nil {
		return nil, err
	}
	result, err := service.domain.ListDevices(ctx, domain.ListDevicesCommand{DeviceToken: credentials.CookieToken(), IncludeRevoked: request.Msg.GetIncludeRevoked(), After: after, PageSize: pageSize})
	if err != nil {
		return nil, err
	}
	devices := make([]*identityv1.DeviceSummary, 0, len(result.Devices))
	for _, device := range result.Devices {
		devices = append(devices, summaryWire(device))
	}
	nextToken, err := encodeDeviceCursor(result.NextCursor)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&identityv1.ListDevicesResponse{Devices: devices, Page: &commonv1.PageInfo{NextPageToken: nextToken}}), nil
}

// RevokeDevice clears the user Cookie pair when the authenticated credential revokes itself.
func (service *Service) RevokeDevice(ctx context.Context, request *connect.Request[identityv1.RevokeDeviceRequest]) (*connect.Response[identityv1.RevokeDeviceResponse], error) {
	auth, err := service.authenticatedWrite(requestHTTP(request))
	if err != nil {
		return nil, err
	}
	credentialID, err := canonicalUUID(request.Msg.GetCredentialId())
	if err != nil {
		return nil, err
	}
	requestID, err := singleMetadata(request.Header(), RequestIDHeader)
	if err != nil {
		return nil, err
	}
	result, err := service.domain.RevokeDevice(ctx, domain.RevokeDeviceCommand{DeviceToken: auth.deviceToken, CSRFToken: auth.csrfToken, CredentialID: credentialID, Reason: request.Msg.GetReason(), RequestID: requestID})
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&identityv1.RevokeDeviceResponse{CurrentDeviceRevoked: result.CurrentDeviceRevoked})
	if result.CurrentDeviceRevoked || result.CredentialInstruction == domain.CredentialInstructionClear {
		if err := service.cookies.ClearUserDevice(responseHeader(response)); err != nil {
			return nil, err
		}
	}
	return response, nil
}

type writeAuthorization struct {
	deviceToken string
	csrfToken   string
	clientIP    string
}

func (service *Service) authenticatedWrite(request *http.Request) (writeAuthorization, error) {
	_, clientIP, err := service.originAndClient(request)
	if err != nil {
		return writeAuthorization{}, err
	}
	credentials, err := cookies.ReadUserDevice(request)
	if err != nil {
		return writeAuthorization{}, domain.ErrDeviceAuthentication
	}
	csrfToken, err := service.csrf.Validate(request)
	if err != nil {
		return writeAuthorization{}, err
	}
	return writeAuthorization{deviceToken: credentials.CookieToken(), csrfToken: csrfToken, clientIP: clientIP}, nil
}

func (service *Service) originAndClient(request *http.Request) (string, string, error) {
	accepted, err := service.origins.Validate(request)
	if err != nil {
		return "", "", err
	}
	client, err := service.clients.Resolve(request)
	if err != nil {
		return "", "", err
	}
	return accepted.Canonical(), client.String(), nil
}

func (service *Service) applyDeviceResult(response interface{ Header() http.Header }, credential domain.DeviceCredential, authority *domain.DeviceCookieWrite, instruction domain.CredentialInstruction, account domain.AccountInstruction) error {
	if account != domain.AccountInstructionNone {
		if err := service.cookies.ClearUserDevice(responseHeader(response)); err != nil {
			return err
		}
		var accountErr error
		switch account {
		case domain.AccountInstructionSuspended:
			accountErr = transporterrors.AccountSuspended()
		case domain.AccountInstructionDeleted:
			accountErr = transporterrors.AccountDeleted()
		default:
			return domain.ErrIdentityIntegrity
		}
		return attachResponseHeaders(accountErr, response.Header())
	}
	if instruction == domain.CredentialInstructionClear {
		if authority != nil {
			return domain.ErrIdentityIntegrity
		}
		if err := service.cookies.ClearUserDevice(responseHeader(response)); err != nil {
			return err
		}
	}
	if authority != nil {
		if err := service.cookies.SetUserDevice(responseHeader(response), credential, *authority); err != nil {
			return err
		}
	}
	return nil
}

// responseHeaderWriter exposes only append capability to Cookie transport code; Connect response mutation stays local.
type responseHeaderWriter struct{ header http.Header }

func (writer responseHeaderWriter) Add(key, value string) { writer.header.Add(key, value) }

func responseHeader(response interface{ Header() http.Header }) cookies.HeaderWriter {
	return responseHeaderWriter{header: response.Header()}
}

func attachResponseHeaders(err error, headers http.Header) error {
	var connectError *connect.Error
	if !stderrors.As(err, &connectError) {
		return err
	}
	for name, values := range headers {
		for _, value := range values {
			connectError.Meta().Add(name, value)
		}
	}
	return connectError
}

func requestHTTP[T any](request *connect.Request[T]) *http.Request {
	if request == nil {
		return nil
	}
	return &http.Request{Header: request.Header(), RemoteAddr: request.Peer().Addr}
}

func requestFlowID(value string) (challenge.RequestFlowID, error) {
	if !validMetadata(value) {
		return "", domain.ErrInvalidIdentityRequest
	}
	return challenge.RequestFlowID(value), nil
}

func singleMetadata(header http.Header, name string) (string, error) {
	var values []string
	for key, current := range header {
		if strings.EqualFold(key, name) {
			values = append(values, current...)
		}
	}
	if len(values) != 1 || !validMetadata(values[0]) {
		return "", domain.ErrInvalidIdentityRequest
	}
	return values[0], nil
}

func validMetadata(value string) bool {
	if value == "" || len(value) > maximumMetadataBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e || character == ',' {
			return false
		}
	}
	return true
}

func canonicalUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return uuid.Nil, domain.ErrInvalidIdentityRequest
	}
	return parsed, nil
}

func pageRequest(page *commonv1.PageRequest) (domain.DevicePageCursor, uint32, error) {
	if page == nil {
		return domain.DevicePageCursor{}, 0, nil
	}
	if page.GetPageSize() < 0 || page.GetPageSize() > int32(domain.MaximumDevicePageSize) {
		return domain.DevicePageCursor{}, 0, domain.ErrInvalidIdentityRequest
	}
	cursor, err := decodeDeviceCursor(page.GetPageToken())
	return cursor, uint32(page.GetPageSize()), err
}

func encodeDeviceCursor(cursor domain.DevicePageCursor) (string, error) {
	if cursor.CreatedAt.IsZero() && cursor.CredentialID == uuid.Nil {
		return "", nil
	}
	if cursor.CreatedAt.IsZero() || cursor.CredentialID == uuid.Nil {
		return "", domain.ErrInvalidIdentityRequest
	}
	raw := make([]byte, deviceCursorBytes)
	raw[0] = deviceCursorVersion
	binary.BigEndian.PutUint64(raw[1:9], uint64(cursor.CreatedAt.UnixNano()))
	copy(raw[9:], cursor.CredentialID[:])
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeDeviceCursor(value string) (domain.DevicePageCursor, error) {
	if value == "" {
		return domain.DevicePageCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) != deviceCursorBytes || raw[0] != deviceCursorVersion || base64.RawURLEncoding.EncodeToString(raw) != value {
		return domain.DevicePageCursor{}, domain.ErrInvalidIdentityRequest
	}
	credentialID, err := uuid.FromBytes(raw[9:])
	if err != nil || credentialID == uuid.Nil {
		return domain.DevicePageCursor{}, domain.ErrInvalidIdentityRequest
	}
	createdAt := time.Unix(0, int64(binary.BigEndian.Uint64(raw[1:9]))).UTC()
	if createdAt.IsZero() {
		return domain.DevicePageCursor{}, domain.ErrInvalidIdentityRequest
	}
	return domain.DevicePageCursor{CreatedAt: createdAt, CredentialID: credentialID}, nil
}

func challengeWire(issued domain.IssuedChallenge) *commonv1.AnonymousChallenge {
	return &commonv1.AnonymousChallenge{ChallengeProof: issued.Credentials.BodyProof, ExpiresAt: timestamppb.New(issued.Challenge.Snapshot().ExpiresAt)}
}

func operationResultWire(result domain.OperationResult) *commonv1.OperationResult {
	message := &commonv1.OperationResult{OperationId: result.OperationID.Value(), Replayed: result.Replayed}
	if result.ResultID != uuid.Nil {
		message.ResultId = result.ResultID.String()
	}
	if !result.SecretExpiresAt.IsZero() {
		message.SecretExpiresAt = timestamppb.New(result.SecretExpiresAt)
	}
	return message
}

func userWire(user domain.User) *identityv1.UserSummary {
	snapshot := user.Snapshot()
	if snapshot.ID == uuid.Nil {
		return nil
	}
	message := &identityv1.UserSummary{UserId: snapshot.ID.String(), Status: userStatusWire(snapshot.Status), Username: snapshot.Username, CreatedAt: timestamppb.New(snapshot.CreatedAt), UpdatedAt: timestamppb.New(snapshot.UpdatedAt)}
	if !snapshot.UsernameChangedAt.IsZero() {
		message.UsernameChangedAt = timestamppb.New(snapshot.UsernameChangedAt)
	}
	return message
}

func credentialWire(credential domain.DeviceCredential, now time.Time) *identityv1.DeviceSummary {
	snapshot := credential.Snapshot()
	if snapshot.CredentialID == uuid.Nil {
		return nil
	}
	message := &identityv1.DeviceSummary{CredentialId: snapshot.CredentialID.String(), Label: snapshot.Label, Status: deviceStateWire(credential.State(now)), CreatedAt: timestamppb.New(snapshot.CreatedAt), LastSeenAt: timestamppb.New(snapshot.LastSeenAt), IdleExpiresAt: timestamppb.New(snapshot.IdleExpiresAt), AbsoluteExpiresAt: timestamppb.New(snapshot.AbsoluteExpiresAt)}
	if !snapshot.RevokedAt.IsZero() {
		message.RevokedAt = timestamppb.New(snapshot.RevokedAt)
	}
	return message
}

func summaryWire(summary domain.DeviceSummary) *identityv1.DeviceSummary {
	message := &identityv1.DeviceSummary{CredentialId: summary.CredentialID.String(), Label: summary.Label, Status: deviceStateWire(summary.Status), CurrentDevice: summary.CurrentDevice, CreatedAt: timestamppb.New(summary.CreatedAt), LastSeenAt: timestamppb.New(summary.LastSeenAt), IdleExpiresAt: timestamppb.New(summary.IdleExpiresAt), AbsoluteExpiresAt: timestamppb.New(summary.AbsoluteExpiresAt)}
	if !summary.RevokedAt.IsZero() {
		message.RevokedAt = timestamppb.New(summary.RevokedAt)
	}
	return message
}

func userStatusWire(status domain.UserStatus) identityv1.UserStatus {
	switch status {
	case domain.UserStatusOnboarding:
		return identityv1.UserStatus_USER_STATUS_ONBOARDING
	case domain.UserStatusActive:
		return identityv1.UserStatus_USER_STATUS_ACTIVE
	case domain.UserStatusSuspended:
		return identityv1.UserStatus_USER_STATUS_SUSPENDED
	case domain.UserStatusDeleted:
		return identityv1.UserStatus_USER_STATUS_DELETED
	default:
		return identityv1.UserStatus_USER_STATUS_UNSPECIFIED
	}
}

func deviceStateWire(state domain.DeviceState) identityv1.DeviceStatus {
	switch state {
	case domain.DeviceStateActive:
		return identityv1.DeviceStatus_DEVICE_STATUS_ACTIVE
	case domain.DeviceStateRevoked:
		return identityv1.DeviceStatus_DEVICE_STATUS_REVOKED
	case domain.DeviceStateExpired:
		return identityv1.DeviceStatus_DEVICE_STATUS_EXPIRED
	default:
		return identityv1.DeviceStatus_DEVICE_STATUS_UNSPECIFIED
	}
}

func credentialInstructionWire(instruction domain.CredentialInstruction) identityv1.CredentialInstruction {
	if instruction == domain.CredentialInstructionClear {
		return identityv1.CredentialInstruction_CREDENTIAL_INSTRUCTION_CLEAR_COOKIE
	}
	return identityv1.CredentialInstruction_CREDENTIAL_INSTRUCTION_KEEP_COOKIE
}

func recoveryPolicyFromWire(policy identityv1.RecoveryDevicePolicy) (domain.RecoveryDevicePolicy, error) {
	switch policy {
	case identityv1.RecoveryDevicePolicy_RECOVERY_DEVICE_POLICY_KEEP_OTHER_DEVICES:
		return domain.RecoveryDevicePolicyKeepOtherDevices, nil
	case identityv1.RecoveryDevicePolicy_RECOVERY_DEVICE_POLICY_REVOKE_OTHER_DEVICES:
		return domain.RecoveryDevicePolicyRevokeOtherDevices, nil
	default:
		return 0, domain.ErrInvalidIdentityRequest
	}
}

func secretOperationFromWire(operation identityv1.IdentitySecretOperation) (domain.IdentitySecretOperation, error) {
	switch operation {
	case identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_BOOTSTRAP:
		return domain.IdentitySecretOperationBootstrap, nil
	case identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_ONBOARDING:
		return domain.IdentitySecretOperationOnboarding, nil
	case identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_RECOVERY:
		return domain.IdentitySecretOperationRecovery, nil
	case identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_RECOVERY_CODE_ROTATION:
		return domain.IdentitySecretOperationRecoveryCodeRotation, nil
	default:
		return 0, domain.ErrInvalidIdentityRequest
	}
}
