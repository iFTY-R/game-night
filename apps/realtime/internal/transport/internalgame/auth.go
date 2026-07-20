package internalgame

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"connectrpc.com/connect"
)

const InternalTokenHeader = "X-Game-Night-Internal-Token"

var errInvalidInternalToken = errors.New("invalid realtime internal credential")

// NewTokenInterceptor authenticates private Connect calls without retaining the configured token in errors.
func NewTokenInterceptor(token string) (connect.Interceptor, error) {
	if len(token) < 32 || len(token) > 256 || strings.TrimSpace(token) != token {
		return nil, errInvalidInternalToken
	}
	expected := sha256.Sum256([]byte(token))
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			actual := sha256.Sum256([]byte(request.Header().Get(InternalTokenHeader)))
			if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
				return nil, connect.NewError(connect.CodeUnauthenticated, errInvalidInternalToken)
			}
			return next(ctx, request)
		}
	}), nil
}
