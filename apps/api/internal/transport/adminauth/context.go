package adminauth

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/platform/admin"
)

const maximumUserAgentBytes = 512

type transportContext struct {
	cookieToken   string
	csrfToken     string
	origin        string
	clientIP      string
	userAgent     string
	requestID     string
	requestFlowID string
}

type requestContext struct {
	transport transportContext
	view      *admin.SessionView
	actor     *admin.ActorContext
}

type requestContextKey struct{}

func withRequestContext(ctx context.Context, value requestContext) context.Context {
	return context.WithValue(ctx, requestContextKey{}, value)
}

func currentRequestContext(ctx context.Context) (requestContext, bool) {
	value, ok := ctx.Value(requestContextKey{}).(requestContext)
	return value, ok
}

func actorFromView(
	view admin.SessionView,
	requestID string,
	origin string,
	clientIP string,
	userAgent string,
) (admin.ActorContext, error) {
	return admin.NewActorContext(
		view.Session.Snapshot().AdminID,
		view.Session.Snapshot().ID,
		view.Session,
		view.Permissions,
		view.Elevations,
		enrollmentVersionOf(view.Enrollment),
		requestID,
		origin,
		clientIP,
		userAgent,
	)
}

type responseHeaderWriter struct{ header http.Header }

func (writer responseHeaderWriter) Add(key string, value string) { writer.header.Add(key, value) }

func responseHeader(response interface{ Header() http.Header }) responseHeaderWriter {
	return responseHeaderWriter{header: response.Header()}
}

func requestHTTP(request connect.AnyRequest) *http.Request {
	if request == nil {
		return nil
	}
	return &http.Request{Header: request.Header(), RemoteAddr: request.Peer().Addr}
}

func normalizedUserAgent(header http.Header) string {
	value := strings.TrimSpace(header.Get("User-Agent"))
	if len(value) <= maximumUserAgentBytes {
		return value
	}
	return value[:maximumUserAgentBytes]
}
