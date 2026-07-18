package secretresult

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Confirmation binds a destructive secret erasure to the same complete operation authorization.
type Confirmation struct {
	ResultID    uuid.UUID
	Binding     Binding
	ConfirmedAt time.Time
}

// Repository stores domain values without exposing PostgreSQL or generated query types.
type Repository interface {
	GetByIDForUpdate(context.Context, uuid.UUID) (Result, error)
	GetByOperationForUpdate(context.Context, Key) (Result, error)
	InsertAvailable(context.Context, Result) (Result, error)
	ConfirmCAS(context.Context, Confirmation) (Result, error)
	ExpireCAS(context.Context, Result, time.Time) (Result, error)
}

// TransactionWork executes against one repository bound to a single infrastructure transaction.
type TransactionWork func(context.Context, Repository) error

// UnitOfWork owns commit/rollback while keeping database handles out of the domain.
type UnitOfWork interface {
	Run(context.Context, TransactionWork) error
}
