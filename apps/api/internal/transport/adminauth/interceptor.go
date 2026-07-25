package adminauth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	"github.com/iFTY-R/game-night/platform/admin"
)

const (
	// RequestFlowIDHeader binds LoginPassword to the browser flow that created the admin challenge.
	RequestFlowIDHeader = "X-Request-Flow-ID"
	// RequestIDHeader carries the stable audit correlation ID for reviewed admin mutations.
	RequestIDHeader      = "X-Request-ID"
	maximumMetadataBytes = 128
)

type sessionInspector interface {
	ResolveSession(context.Context, string) (admin.Session, error)
	GetCurrentAdminSession(context.Context, admin.CurrentSessionCommand) (admin.CurrentSessionResult, error)
}

// ContextInterceptor normalizes transport metadata, authenticates reviewed procedures, and attaches a read-only actor context.
type ContextInterceptor struct {
	sessions sessionInspector
	origins  *origin.AdminValidator
	csrf     *csrf.AdminValidator
	clients  *proxy.Resolver
}

// NewContextInterceptor validates isolated administrator transport dependencies.
func NewContextInterceptor(
	sessions sessionInspector,
	origins *origin.AdminValidator,
	csrfValidator *csrf.AdminValidator,
	clients *proxy.Resolver,
) (*ContextInterceptor, error) {
	if sessions == nil || origins == nil || csrfValidator == nil || clients == nil {
		return nil, admin.ErrInvalidInput
	}
	return &ContextInterceptor{sessions: sessions, origins: origins, csrf: csrfValidator, clients: clients}, nil
}

// WrapUnary injects the exact transport and actor context shape required by the rebuilt admin procedure set.
func (interceptor *ContextInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		if interceptor == nil || request == nil || request.Spec().IsClient {
			return nil, admin.ErrInvalidInput
		}
		policy, ok := policyForProcedure(request.Spec().Procedure)
		if !ok {
			return nil, admin.ErrPermissionDenied
		}
		httpRequest := requestHTTP(request)
		acceptedOrigin, err := interceptor.origins.Validate(httpRequest)
		if err != nil {
			return nil, err
		}
		clientIP, err := interceptor.clients.Resolve(httpRequest)
		if err != nil {
			return nil, err
		}
		state := requestContext{
			transport: transportContext{
				origin:    acceptedOrigin.Canonical(),
				clientIP:  clientIP.String(),
				userAgent: normalizedUserAgent(request.Header()),
			},
		}
		if policy.requiresRequestID {
			state.transport.requestID, err = singleMetadata(request.Header(), RequestIDHeader)
			if err != nil {
				return nil, err
			}
		}
		if policy.requiresChallenge {
			credentials, readErr := cookies.ReadAdminChallenge(httpRequest)
			if readErr != nil {
				return nil, admin.ErrAuthentication
			}
			state.transport.cookieToken = credentials.CookieToken()
		}
		if policy.requiresRequestFlowID {
			state.transport.requestFlowID, err = singleMetadata(request.Header(), RequestFlowIDHeader)
			if err != nil {
				return nil, err
			}
		}
		if policy.session != sessionRequirementNone {
			credentials, readErr := cookies.ReadAdminSession(httpRequest)
			if readErr != nil {
				return nil, admin.ErrAuthentication
			}
			state.transport.cookieToken = credentials.CookieToken()
			if policy.requiresCSRF {
				state.transport.csrfToken, err = interceptor.csrf.Validate(httpRequest)
				if err != nil {
					return nil, err
				}
			}
			session, resolveErr := interceptor.sessions.ResolveSession(ctx, state.transport.cookieToken)
			if resolveErr != nil {
				return nil, resolveErr
			}
			current, currentErr := interceptor.sessions.GetCurrentAdminSession(ctx, admin.CurrentSessionCommand{
				Session:      session,
				SessionToken: state.transport.cookieToken,
				CSRFToken:    state.transport.csrfToken,
			})
			if currentErr != nil {
				return nil, currentErr
			}
			actor, actorErr := actorFromView(
				current.View,
				state.transport.requestID,
				state.transport.origin,
				state.transport.clientIP,
				state.transport.userAgent,
			)
			if actorErr != nil {
				return nil, actorErr
			}
			state.view = &current.View
			state.actor = &actor
			if err = enforceStaticPolicy(policy, actor); err != nil {
				return nil, err
			}
		}
		return next(withRequestContext(ctx, state), request)
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

func enforceStaticPolicy(policy procedurePolicy, actor admin.ActorContext) error {
	switch policy.session {
	case sessionRequirementAuthenticated:
		// Any authenticated setup, MFA-pending, or full session may continue.
	case sessionRequirementSetup:
		if actor.Session().Snapshot().Kind != admin.SessionKindSetupPasswordPending {
			return admin.ErrPermissionDenied
		}
	case sessionRequirementMFAPending:
		if actor.Session().Snapshot().Kind != admin.SessionKindMFAPending {
			return admin.ErrPermissionDenied
		}
	case sessionRequirementFull:
		if actor.Session().Snapshot().Kind != admin.SessionKindFull {
			return admin.ErrPermissionDenied
		}
	}
	if policy.permission != "" {
		if err := actor.Require(policy.permission); err != nil {
			return err
		}
	}
	if policy.elevation != "" {
		if err := actor.RequireElevation(policy.elevation, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
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
