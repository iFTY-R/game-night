package adminauth

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
)

const (
	// RequestFlowIDHeader binds LoginPassword to the browser flow used by BeginAdminLogin.
	RequestFlowIDHeader = "X-Request-Flow-ID"
	// RequestIDHeader is the independent audit correlation ID required by AdminIdentityService.
	RequestIDHeader      = "X-Request-ID"
	maximumMetadataBytes = 128
)

// ContextInterceptor owns only administrator Origin, CSRF, Cookie, proxy, and context namespaces.
type ContextInterceptor struct {
	origins *origin.AdminValidator
	csrf    *csrf.AdminValidator
	clients *proxy.Resolver
}

// NewContextInterceptor validates isolated administrator transport dependencies.
func NewContextInterceptor(origins *origin.AdminValidator, csrfValidator *csrf.AdminValidator, clients *proxy.Resolver) (*ContextInterceptor, error) {
	if origins == nil || csrfValidator == nil || clients == nil {
		return nil, admin.ErrInvalidInput
	}
	return &ContextInterceptor{origins: origins, csrf: csrfValidator, clients: clients}, nil
}

// WrapUnary injects the exact context shape required by the reviewed administrator procedure class.
func (interceptor *ContextInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		if interceptor == nil || request == nil || request.Spec().IsClient {
			return nil, admin.ErrInvalidInput
		}
		operation := request.Spec().Procedure
		if operation == adminv1connect.AdminAuthServiceGetSetupStateProcedure {
			return next(ctx, request)
		}
		httpRequest := &http.Request{Header: request.Header(), RemoteAddr: request.Peer().Addr}
		acceptedOrigin, err := interceptor.origins.Validate(httpRequest)
		if err != nil {
			return nil, err
		}
		clientIP, err := interceptor.clients.Resolve(httpRequest)
		if err != nil {
			return nil, err
		}
		transport := admin.AdminTransportContext{Origin: acceptedOrigin.Canonical(), ClientIP: clientIP.String()}
		switch {
		case operation == adminv1connect.AdminAuthServiceBeginAdminLoginProcedure:
			// Begin creates the only challenge Cookie and therefore has no credential input.
		case operation == adminv1connect.AdminAuthServiceLoginPasswordProcedure:
			challengeCredential, readErr := cookies.ReadAdminChallenge(httpRequest)
			if readErr != nil {
				return nil, admin.ErrAuthentication
			}
			flowID, readErr := singleMetadata(request.Header(), RequestFlowIDHeader)
			if readErr != nil {
				return nil, readErr
			}
			transport.CookieToken, transport.RequestFlowID = challengeCredential.CookieToken(), flowID
		case isAdminSessionOperation(operation):
			credentials, readErr := cookies.ReadAdminSession(httpRequest)
			if readErr != nil {
				return nil, admin.ErrAuthentication
			}
			csrfToken, validateErr := interceptor.csrf.Validate(httpRequest)
			if validateErr != nil {
				return nil, validateErr
			}
			transport.CookieToken, transport.CSRFToken = credentials.CookieToken(), csrfToken
			if isAdminIdentityOperation(operation) {
				requestID, metadataErr := singleMetadata(request.Header(), RequestIDHeader)
				if metadataErr != nil {
					return nil, metadataErr
				}
				transport.RequestFlowID = requestID
			}
		default:
			return nil, admin.ErrInvalidInput
		}
		return next(admin.WithAdminTransportContext(ctx, transport), request)
	}
}

// Streaming methods are not defined by the current contracts and pass through unchanged.
func (interceptor *ContextInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// Streaming methods are not defined by the current contracts and pass through unchanged.
func (interceptor *ContextInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func isAdminSessionOperation(operation string) bool {
	if isAdminIdentityOperation(operation) {
		return true
	}
	switch operation {
	case adminv1connect.AdminAuthServiceVerifyTotpProcedure,
		adminv1connect.AdminAuthServiceChangeInitialPasswordProcedure,
		adminv1connect.AdminAuthServiceBeginTotpEnrollmentProcedure,
		adminv1connect.AdminAuthServiceCompleteTotpEnrollmentProcedure,
		adminv1connect.AdminAuthServiceConfirmAdminSecretReceiptProcedure,
		adminv1connect.AdminAuthServiceRecoverAdminProcedure,
		adminv1connect.AdminAuthServiceChangeAdminPasswordProcedure,
		adminv1connect.AdminAuthServiceBeginTotpRebindProcedure,
		adminv1connect.AdminAuthServiceCompleteTotpRebindProcedure,
		adminv1connect.AdminAuthServiceRegenerateAdminRecoveryCodesProcedure,
		adminv1connect.AdminAuthServiceLogoutAdminProcedure,
		adminv1connect.AdminAuthServiceLogoutAllAdminSessionsProcedure:
		return true
	default:
		return false
	}
}

func isAdminIdentityOperation(operation string) bool {
	return strings.HasPrefix(operation, "/platform.admin.v1.AdminIdentityService/")
}

func singleMetadata(header http.Header, name string) (string, error) {
	var values []string
	for key, current := range header {
		if strings.EqualFold(key, name) {
			values = append(values, current...)
		}
	}
	if len(values) != 1 || !validMetadata(values[0]) {
		return "", admin.ErrInvalidInput
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

var _ connect.Interceptor = (*ContextInterceptor)(nil)
