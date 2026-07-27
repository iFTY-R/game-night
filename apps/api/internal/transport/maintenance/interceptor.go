package maintenance

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/platform/admin/operations"
)

var (
	// ErrInvalidConfiguration rejects a gate that cannot consult the authoritative state.
	ErrInvalidConfiguration = errors.New("maintenance interceptor configuration is invalid")
	// ErrMutationBlocked reports an intentional maintenance admission denial without exposing the operator reason.
	ErrMutationBlocked = errors.New("user mutations are blocked by maintenance")
	// ErrStateUnavailable fails closed when the PostgreSQL authority cannot provide a valid decision.
	ErrStateUnavailable = errors.New("maintenance state is unavailable")
)

// StateReader reads the versioned PostgreSQL maintenance singleton for each user mutation.
type StateReader interface {
	GetMaintenanceState(context.Context) (operations.MaintenanceState, error)
}

// Interceptor keeps the maintenance authority out of individual user-domain handlers.
type Interceptor struct{ states StateReader }

// NewInterceptor requires a live authority; no cached or process-local boolean can be supplied through this contract.
func NewInterceptor(states StateReader) (*Interceptor, error) {
	if states == nil {
		return nil, ErrInvalidConfiguration
	}
	return &Interceptor{states: states}, nil
}

// WrapUnary checks only mutations, preserving pure reads and authentication recovery while maintenance is active.
func (interceptor *Interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := ""
		if request != nil {
			procedure = request.Spec().Procedure
		}
		if classifyProcedure(procedure) != procedureMutation {
			return next(ctx, request)
		}
		if interceptor == nil || interceptor.states == nil {
			return nil, ErrStateUnavailable
		}
		// Every mutation re-reads PostgreSQL so an enabled version takes effect without a stale admission window.
		state, err := interceptor.states.GetMaintenanceState(ctx)
		if err != nil || state.Scope != operations.MaintenanceUserMutations || state.Version == 0 {
			return nil, ErrStateUnavailable
		}
		if state.Enabled {
			return nil, ErrMutationBlocked
		}
		return next(ctx, request)
	}
}

// Streaming methods are absent from the generated user contracts; the descriptor test locks that unary-only surface.
func (interceptor *Interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// Streaming methods are absent from the generated user contracts; the descriptor test locks that unary-only surface.
func (interceptor *Interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

var _ connect.Interceptor = (*Interceptor)(nil)
