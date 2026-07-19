package metrics

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
)

// RPCObserver receives only allowlisted procedure names, bounded Connect results, and elapsed time.
type RPCObserver interface {
	ObserveRPC(operation, result string, duration time.Duration)
}

// NewUnaryInterceptor builds server-side RPC observability that never inspects messages or metadata.
func NewUnaryInterceptor(logger *slog.Logger, observer RPCObserver, rpcOperations ...string) (connect.Interceptor, error) {
	if logger == nil || observer == nil || len(rpcOperations) == 0 {
		return nil, errors.New("invalid RPC observability configuration")
	}
	operations := make(map[string]struct{}, len(rpcOperations))
	for _, operation := range rpcOperations {
		if !validLabelValue(operation) {
			return nil, errors.New("invalid RPC observability configuration")
		}
		if _, exists := operations[operation]; exists {
			return nil, errors.New("invalid RPC observability configuration")
		}
		operations[operation] = struct{}{}
	}

	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			startedAt := time.Now()
			operation := ResultUnknown
			if request != nil {
				if candidate := request.Spec().Procedure; allowedOperation(operations, candidate) {
					operation = candidate
				}
			}

			response, err := next(ctx, request)
			duration := time.Since(startedAt)
			result := rpcResult(err)
			observer.ObserveRPC(operation, result, duration)
			logger.LogAttrs(ctx, slog.LevelInfo, "rpc completed",
				slog.String("operation", operation),
				slog.String("result", result),
				slog.Float64("duration_ms", float64(duration)/float64(time.Millisecond)),
			)
			return response, err
		}
	}), nil
}

func allowedOperation(operations map[string]struct{}, operation string) bool {
	_, allowed := operations[operation]
	return allowed
}

func rpcResult(err error) string {
	if err == nil {
		return "ok"
	}
	return boundedRPCResult(connect.CodeOf(err).String())
}
