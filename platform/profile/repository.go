package profile

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identity"
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

// ExportRepository persists an immutable, encrypted export snapshot and its
// short-lived lifecycle. Page reads use the ordinal keyset cursor supplied by
// the caller; the adapter never turns this into offset pagination.
type ExportRepository interface {
	ListSources(context.Context, []uuid.UUID, []identity.UserStatus) ([]ExportSource, error)
	InsertContext(context.Context, ProfileExportContext) (ProfileExportContext, error)
	InsertItem(context.Context, ProfileExportItem) (ProfileExportItem, error)
	GetContextForUpdate(context.Context, uuid.UUID) (ProfileExportContext, error)
	ListItems(context.Context, uuid.UUID, int64, int32) ([]ProfileExportItem, error)
	CompleteCAS(context.Context, uuid.UUID, uuid.UUID, time.Time) (ProfileExportContext, error)
	AbortCAS(context.Context, uuid.UUID, uuid.UUID, time.Time) (ProfileExportContext, error)
	ExpireCAS(context.Context, uuid.UUID, time.Time) (ProfileExportContext, error)
}
