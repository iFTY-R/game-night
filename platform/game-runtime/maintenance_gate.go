package gameruntime

import (
	"context"

	adminoperations "github.com/iFTY-R/game-night/platform/admin/operations"
)

// MaintenanceStateReader exposes only the PostgreSQL-backed authority required to admit user mutations.
type MaintenanceStateReader interface {
	GetMaintenanceState(context.Context) (adminoperations.MaintenanceState, error)
}

// MutationGate makes one fresh authority decision for each user-driven runtime mutation.
// Implementations must not cache decisions because maintenance changes are effective immediately.
type MutationGate interface {
	CheckUserMutation(context.Context) error
}

type maintenanceMutationGate struct {
	states MaintenanceStateReader
}

// NewMaintenanceMutationGate adapts the authoritative operations state reader to the runtime admission boundary.
func NewMaintenanceMutationGate(states MaintenanceStateReader) (MutationGate, error) {
	if states == nil {
		return nil, ErrInvalidSessionInput
	}
	return &maintenanceMutationGate{states: states}, nil
}

// CheckUserMutation fails closed when PostgreSQL cannot produce a current, valid maintenance decision.
func (gate *maintenanceMutationGate) CheckUserMutation(ctx context.Context) error {
	if gate == nil || gate.states == nil || ctx == nil {
		return ErrMutationStateUnavailable
	}
	state, err := gate.states.GetMaintenanceState(ctx)
	if err != nil || state.Version == 0 || state.Scope != adminoperations.MaintenanceUserMutations {
		return ErrMutationStateUnavailable
	}
	if state.Enabled {
		return ErrMutationBlocked
	}
	return nil
}

var _ MutationGate = (*maintenanceMutationGate)(nil)
