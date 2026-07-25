package profile

import (
	"context"

	"github.com/google/uuid"
)

// Repository persists the encrypted profile aggregate. Reads that precede a
// sensitive mutation use GetForUpdate so the caller can keep authorization,
// audit, and the profile CAS in one database transaction.
type Repository interface {
	GetByID(context.Context, uuid.UUID) (UserProfile, error)
	GetForUpdate(context.Context, uuid.UUID) (UserProfile, error)
	Insert(context.Context, UserProfile) (UserProfile, error)
	UpdateCAS(context.Context, UserProfile, UserProfile) (UserProfile, error)
}
