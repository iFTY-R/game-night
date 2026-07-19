// Package keyrotation coordinates resumable encryption-key rotation without depending on a database driver.
package keyrotation

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/security"
)

var (
	// ErrInvalidInput rejects malformed jobs, cursors, ciphertext metadata, or service configuration.
	ErrInvalidInput = errors.New("invalid key rotation input")
	// ErrRepositoryUnavailable hides database and transaction lifecycle details from the worker runtime.
	ErrRepositoryUnavailable = errors.New("key rotation repository unavailable")
	// ErrConcurrentTransition reports a lost lease, cursor, or row compare-and-swap.
	ErrConcurrentTransition = errors.New("key rotation concurrent transition")
	// ErrIntegrity reports ciphertext that cannot be authenticated or restored safely.
	ErrIntegrity = errors.New("key rotation integrity failure")
)

// Purpose selects the independent keyring and set of ciphertext tables covered by one job.
type Purpose string

const (
	PurposePII  Purpose = "pii"
	PurposeTOTP Purpose = "totp"
)

func (purpose Purpose) valid() bool { return purpose == PurposePII || purpose == PurposeTOTP }

// Scope selects one stable cursor ordering within a purpose.
type Scope string

const (
	ScopeUserProfiles         Scope = "user_profiles"
	ScopeProfileExportItems   Scope = "profile_export_items"
	ScopeAdminTOTPEnrollments Scope = "admin_totp_enrollments"
)

// Cursor is the last examined row; a nil ID starts the named scope from its beginning.
type Cursor struct {
	Scope   Scope
	ID      uuid.UUID
	Ordinal int64
}

// Job is the persistence-neutral snapshot used for lease-bound batch processing.
type Job struct {
	ID             uuid.UUID
	Purpose        Purpose
	SourceVersion  uint32
	TargetVersion  uint32
	Cursor         Cursor
	ProcessedCount int64
	ConflictCount  int64
	StartedAt      time.Time
}

// AcquiredJob reports whether this lease transition started a pending job and therefore requires start audit.
type AcquiredJob struct {
	Job        Job
	StartedNow bool
}

// UserProfileCiphertext binds one profile payload to the version used by its row-level CAS.
type UserProfileCiphertext struct {
	UserID         uuid.UUID
	ProfileVersion int64
	Encrypted      profile.EncryptedValue
}

// ProfileExportCiphertext binds immutable export ciphertext to its composite stable cursor.
type ProfileExportCiphertext struct {
	ExportID  uuid.UUID
	Ordinal   int64
	UserID    uuid.UUID
	Encrypted profile.EncryptedValue
}

// TOTPEnrollmentCiphertext carries only encrypted enrollment material and its row-level CAS version.
type TOTPEnrollmentCiphertext struct {
	EnrollmentID uuid.UUID
	AdminID      uuid.UUID
	AdminVersion int64
	Encrypted    security.Encrypted[security.TOTPKeyPurpose]
}

// CreateRequest defines a pending job for one historical source version and the current active target.
type CreateRequest struct {
	JobID         uuid.UUID
	Purpose       Purpose
	SourceVersion uint32
	TargetVersion uint32
	InitialScope  Scope
	CreatedAt     time.Time
}

// AdvanceRequest moves a lease-owned stable cursor and records successful/conflicting row counts atomically.
type AdvanceRequest struct {
	JobID          uuid.UUID
	Owner          outbox.LeaseOwner
	ExpectedCursor Cursor
	NextCursor     Cursor
	ProcessedDelta int64
	ConflictDelta  int64
	AdvancedAt     time.Time
}

// Transaction is valid only for the callback lifetime and combines rotation state with signed audit participants.
type Transaction interface {
	ListReferencedVersions(context.Context, Purpose) ([]uint32, error)
	CreateJob(context.Context, CreateRequest) (bool, error)
	AcquireJob(context.Context, outbox.LeaseOwner, time.Time, time.Time) (*AcquiredJob, error)
	ListUserProfiles(context.Context, uint32, uuid.UUID, uint32) ([]UserProfileCiphertext, error)
	RotateUserProfile(context.Context, UserProfileCiphertext, profile.EncryptedValue, uint32) (bool, error)
	ListProfileExportItems(context.Context, uint32, uuid.UUID, int64, uint32) ([]ProfileExportCiphertext, error)
	RotateProfileExportItem(context.Context, ProfileExportCiphertext, profile.EncryptedValue, uint32) (bool, error)
	ListTOTPEnrollments(context.Context, uint32, uuid.UUID, uint32) ([]TOTPEnrollmentCiphertext, error)
	RotateTOTPEnrollment(context.Context, TOTPEnrollmentCiphertext, security.Encrypted[security.TOTPKeyPurpose], uint32) (bool, error)
	AdvanceCursor(context.Context, AdvanceRequest) error
	CountReferences(context.Context, Purpose, uint32) (int64, error)
	CompleteJob(context.Context, Job, outbox.LeaseOwner, time.Time) (Job, error)
	FailJob(context.Context, uuid.UUID, outbox.LeaseOwner, string, time.Time) error
	ReleaseJob(context.Context, uuid.UUID, outbox.LeaseOwner, time.Time) error
	Audit() audit.Repository
	Checkpoints() audit.CheckpointRepository
}

// TransactionWork must not retain transaction-bound repositories after returning.
type TransactionWork func(context.Context, Transaction) error

// UnitOfWork commits job state, ciphertext CAS updates, audit append, and checkpoint outbox writes atomically.
type UnitOfWork interface {
	Run(context.Context, TransactionWork) error
}

// Result is safe for structured logs because it contains counts and state only, never ciphertext or plaintext.
type Result struct {
	Created   uint32
	Processed uint32
	Conflicts uint32
	Completed bool
	Idle      bool
}
