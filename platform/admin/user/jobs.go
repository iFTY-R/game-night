package user

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// BatchTarget freezes one target and its user-version guard at preview time.
type BatchTarget struct {
	ItemID              uuid.UUID
	UserID              uuid.UUID
	ExpectedUserVersion uint64
	RequestDigest       [32]byte
}

// BatchPreview is a short-lived, versioned selection snapshot.
type BatchPreview struct {
	ID                     uuid.UUID
	ActorAdminID           uuid.UUID
	Command                string
	SelectionSchemaVersion uint32
	SelectionSnapshot      []byte
	SelectionDigest        [32]byte
	PreviewDigest          [32]byte
	TargetCount            int64
	ExecutableCount        int64
	BlockedCount           int64
	SampledAt              time.Time
	ExpiresAt              time.Time
	ConsumedAt             time.Time
	Version                uint64
}

// CreateBatchPreviewCommand persists the exact selection and bounded impact counts shown to the administrator.
type CreateBatchPreviewCommand struct {
	Preview BatchPreview
}

// StartBatchJobCommand converts one live preview into an idempotent durable item set.
type StartBatchJobCommand struct {
	BatchJobID             uuid.UUID
	ActorAdminID           uuid.UUID
	OperationID            string
	RequestDigest          [32]byte
	PreviewID              uuid.UUID
	PreviewDigest          [32]byte
	ExpectedPreviewVersion uint64
	Reason                 string
	Targets                []BatchTarget
	CreatedAt              time.Time
}

// BatchJob is the aggregate progress row recovered after refresh.
type BatchJob struct {
	ID                     uuid.UUID
	ActorAdminID           uuid.UUID
	OperationID            string
	RequestDigest          [32]byte
	PreviewID              uuid.UUID
	Command                string
	SelectionSchemaVersion uint32
	SelectionSnapshot      []byte
	SelectionDigest        [32]byte
	Reason                 string
	State                  string
	TargetCount            int64
	QueuedCount            int64
	RunningCount           int64
	SucceededCount         int64
	FailedCount            int64
	SkippedCount           int64
	CanceledCount          int64
	ErrorMessageKey        string
	Version                uint64
	CreatedAt              time.Time
	StartedAt              time.Time
	CompletedAt            time.Time
	UpdatedAt              time.Time
}

// BatchItem is one independently leased and idempotent target.
type BatchItem struct {
	ID                  uuid.UUID
	BatchJobID          uuid.UUID
	UserID              uuid.UUID
	ExpectedUserVersion uint64
	RequestDigest       [32]byte
	State               string
	AttemptCount        uint32
	LeaseOwner          string
	LeaseUntil          time.Time
	ErrorMessageKey     string
	AuditEventID        uuid.UUID
	StartedAt           time.Time
	CompletedAt         time.Time
	Version             uint64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ErasureJob tracks the durable deletion workflow without a free-form state document.
type ErasureJob struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	ActorAdminID    uuid.UUID
	OperationID     string
	RequestDigest   [32]byte
	State           string
	Step            string
	Reason          string
	AttemptCount    uint32
	LeaseOwner      string
	LeaseUntil      time.Time
	ErrorMessageKey string
	Version         uint64
	CreatedAt       time.Time
	StartedAt       time.Time
	CompletedAt     time.Time
	UpdatedAt       time.Time
}

// CreateErasureJobCommand binds one user deletion to an idempotency digest.
type CreateErasureJobCommand struct {
	Job ErasureJob
}

// JobRepository owns preview consumption, item leases, and erasure state transitions.
type JobRepository interface {
	CreateBatchPreview(context.Context, CreateBatchPreviewCommand) (BatchPreview, error)
	StartBatchJob(context.Context, StartBatchJobCommand) (BatchJob, error)
	ClaimBatchItem(context.Context, uuid.UUID, string, time.Duration) (BatchItem, error)
	ClaimNextBatchItem(context.Context, string, time.Duration) (BatchItem, error)
	CompleteBatchItem(context.Context, BatchItem, string, string, uuid.UUID, time.Time) (BatchItem, error)
	CreateErasureJob(context.Context, CreateErasureJobCommand) (ErasureJob, error)
	ClaimErasureJob(context.Context, string, time.Duration) (ErasureJob, error)
	AdvanceErasureJob(context.Context, ErasureJob, string, time.Time) (ErasureJob, error)
	CompleteErasureJob(context.Context, ErasureJob, string, string, time.Time) (ErasureJob, error)
}

// BatchRepository is the complete durable boundary used by both HTTP orchestration and worker execution.
// Keeping reads and leases together prevents the domain service from relying on unchecked type assertions.
type BatchRepository interface {
	JobRepository
	GetBatchPreview(context.Context, uuid.UUID, uuid.UUID) (BatchPreview, error)
	GetBatchJob(context.Context, uuid.UUID) (BatchJob, error)
	ListBatchJobs(context.Context, BatchJobListQuery) ([]BatchJob, error)
	ListBatchItems(context.Context, BatchItemListQuery) ([]BatchItem, error)
	CancelBatchJob(context.Context, uuid.UUID, uint64, time.Time) (BatchJob, error)
	RetryBatchJob(context.Context, uuid.UUID, []uuid.UUID, uint64, time.Time) (BatchJob, int64, error)
}
