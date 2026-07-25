package user

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ExportJob persists only a versioned filter snapshot and encrypted object metadata.
type ExportJob struct {
	ID                  uuid.UUID
	ActorAdminID        uuid.UUID
	OperationID         string
	RequestDigest       [32]byte
	FilterSchemaVersion uint32
	FilterSnapshot      []byte
	FilterDigest        [32]byte
	Fields              []string
	MaskingPolicy       string
	State               string
	MatchedUsers        int64
	ExportedUsers       int64
	FailedUsers         int64
	ResultObjectKey     string
	ResultDigest        [32]byte
	ResultKeyVersion    uint32
	ResultSchemaVersion uint32
	ResultExpiresAt     time.Time
	ErrorMessageKey     string
	LeaseOwner          string
	LeaseUntil          time.Time
	Version             uint64
	CreatedAt           time.Time
	StartedAt           time.Time
	CompletedAt         time.Time
	UpdatedAt           time.Time
}

// CreateExportJobCommand creates one immutable projection and output TTL.
type CreateExportJobCommand struct {
	Job ExportJob
}

// CompleteExportJobCommand commits encrypted object metadata while the worker still owns the lease.
type CompleteExportJobCommand struct {
	Job              ExportJob
	NextState        string
	MatchedUsers     int64
	ExportedUsers    int64
	FailedUsers      int64
	ResultObjectKey  string
	ResultDigest     [32]byte
	ResultKeyVersion uint32
	CompletedAt      time.Time
}

// FailExportJobCommand records bounded progress and a stable failure key while the worker still owns the lease.
type FailExportJobCommand struct {
	Job             ExportJob
	MatchedUsers    int64
	ExportedUsers   int64
	FailedUsers     int64
	ErrorMessageKey string
	CompletedAt     time.Time
}

// DeleteExportResultCommand clears encrypted object metadata using the exact export version seen by the caller.
type DeleteExportResultCommand struct {
	ExportID        uuid.UUID
	ExpectedVersion uint64
	DeletedAt       time.Time
}

// DownloadGrant stores only a versioned token digest and its exact export/session binding.
type DownloadGrant struct {
	ID                    uuid.UUID
	ExportID              uuid.UUID
	ActorAdminID          uuid.UUID
	SessionID             uuid.UUID
	OperationID           string
	RequestDigest         [32]byte
	TokenDigest           [32]byte
	TokenKeyVersion       uint32
	ExpectedExportVersion uint64
	MaskingPolicy         string
	State                 string
	CreatedAt             time.Time
	ExpiresAt             time.Time
	ConsumedAt            time.Time
	RevokedAt             time.Time
	Version               uint64
}

// ConsumedExport identifies the already-authorized encrypted object without returning a public URL.
type ConsumedExport struct {
	Grant               DownloadGrant
	ResultObjectKey     string
	ResultDigest        [32]byte
	ResultKeyVersion    uint32
	ResultSchemaVersion uint32
	ResultExpiresAt     time.Time
	MaskingPolicy       string
	ExportVersion       uint64
}

// ExportRepository owns job leases, result expiry, and single-use download grants.
type ExportRepository interface {
	CreateExportJob(context.Context, CreateExportJobCommand) (ExportJob, error)
	GetExportJob(context.Context, uuid.UUID) (ExportJob, error)
	ClaimExportJob(context.Context, string, time.Duration) (ExportJob, error)
	CompleteExportJob(context.Context, CompleteExportJobCommand) (ExportJob, error)
	FailExportJob(context.Context, FailExportJobCommand) (ExportJob, error)
	ExpireExportResults(context.Context, time.Time) ([]uuid.UUID, error)
	DeleteExportResult(context.Context, DeleteExportResultCommand) (ExportJob, error)
	CreateDownloadGrant(context.Context, DownloadGrant) (DownloadGrant, error)
	ConsumeDownloadGrant(context.Context, uint32, [32]byte, uuid.UUID, uuid.UUID) (ConsumedExport, error)
}
