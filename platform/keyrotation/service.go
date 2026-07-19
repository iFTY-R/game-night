package keyrotation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/profile"
)

const (
	rotationSystemActor  = "key-rotation-worker"
	failureIntegrityCode = "ciphertext_integrity"
)

// Config bounds one lease-owned pass so rotation remains resumable and does not monopolize PostgreSQL.
type Config struct {
	Owner         outbox.LeaseOwner
	LeaseDuration time.Duration
	BatchSize     uint32
}

// Service creates historical-key jobs and advances at most one small ciphertext batch per pass.
type Service struct {
	config           Config
	unitOfWork       UnitOfWork
	pii              *profile.PIIProtector
	totp             *admin.TOTPService
	audit            *audit.Service
	checkpointHealth *audit.CheckpointHealthPolicy
	clock            clock.Clock
}

// NewService validates every authority before a worker can acquire a rotation lease.
func NewService(
	config Config,
	unitOfWork UnitOfWork,
	pii *profile.PIIProtector,
	totp *admin.TOTPService,
	auditService *audit.Service,
	checkpointHealth *audit.CheckpointHealthPolicy,
	source clock.Clock,
) (*Service, error) {
	if !config.Owner.Valid() || config.LeaseDuration <= 0 || config.LeaseDuration > outbox.MaximumLeaseDuration ||
		config.BatchSize == 0 || config.BatchSize > outbox.MaximumBatchSize || unitOfWork == nil || pii == nil ||
		totp == nil || auditService == nil || checkpointHealth == nil || source == nil ||
		!validVersion(pii.ActiveKeyVersion()) || !validVersion(totp.ActiveKeyVersion()) {
		return nil, ErrInvalidInput
	}
	return &Service{
		config: config, unitOfWork: unitOfWork, pii: pii, totp: totp, audit: auditService,
		checkpointHealth: checkpointHealth, clock: source,
	}, nil
}

// RunOnce ensures pending work exists, acquires one purpose lease, and commits one resumable batch.
func (service *Service) RunOnce(ctx context.Context) (Result, error) {
	if service == nil || ctx == nil {
		return Result{}, ErrInvalidInput
	}
	result := Result{}
	if err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		for _, purpose := range []Purpose{PurposePII, PurposeTOTP} {
			created, err := service.ensureJob(ctx, transaction, purpose)
			if err != nil {
				return err
			}
			if created {
				result.Created++
			}
		}
		return nil
	}); err != nil {
		return Result{}, err
	}

	var acquired *AcquiredJob
	acquiredAt := service.clock.Now()
	if err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		var err error
		acquired, err = transaction.AcquireJob(
			ctx, service.config.Owner, acquiredAt, acquiredAt.Add(service.config.LeaseDuration),
		)
		if err != nil || acquired == nil || !acquired.StartedNow {
			return err
		}
		return service.appendAudit(ctx, transaction, acquired.Job, audit.ActionKeyRotationStarted, "rotation_started", acquiredAt,
			rotationDigest(acquired.Job, acquired.Job.Cursor, 0, 0))
	}); err != nil {
		return Result{}, err
	}
	if acquired == nil {
		result.Idle = true
		return result, nil
	}
	if !validJob(acquired.Job) {
		return Result{}, ErrIntegrity
	}

	processed, conflicts, completed, err := service.processBatch(ctx, acquired.Job)
	result.Processed = processed
	result.Conflicts = conflicts
	result.Completed = completed
	if err != nil {
		// Authentication failures are unrecoverable for this job; transient repository failures retain the lease for retry.
		if err == ErrIntegrity {
			_ = service.failJob(ctx, acquired.Job, failureIntegrityCode)
		}
		return result, err
	}
	if completed {
		return result, nil
	}
	if err := service.releaseJob(ctx, acquired.Job); err != nil {
		return result, err
	}
	return result, nil
}

func (service *Service) ensureJob(ctx context.Context, transaction Transaction, purpose Purpose) (bool, error) {
	versions, err := transaction.ListReferencedVersions(ctx, purpose)
	if err != nil {
		return false, err
	}
	targetVersion := service.activeVersion(purpose)
	for _, sourceVersion := range versions {
		if sourceVersion == targetVersion {
			continue
		}
		// The database permits one active job per purpose; a false result means an existing job owns sequencing.
		return transaction.CreateJob(ctx, CreateRequest{
			JobID: uuid.New(), Purpose: purpose, SourceVersion: sourceVersion, TargetVersion: targetVersion,
			InitialScope: initialScope(purpose), CreatedAt: service.clock.Now(),
		})
	}
	return false, nil
}

func (service *Service) activeVersion(purpose Purpose) uint32 {
	if purpose == PurposePII {
		return service.pii.ActiveKeyVersion()
	}
	return service.totp.ActiveKeyVersion()
}

func initialScope(purpose Purpose) Scope {
	if purpose == PurposePII {
		return ScopeUserProfiles
	}
	return ScopeAdminTOTPEnrollments
}

func (service *Service) processBatch(ctx context.Context, job Job) (processed uint32, conflicts uint32, completed bool, err error) {
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		switch job.Cursor.Scope {
		case ScopeUserProfiles:
			return service.processUserProfiles(ctx, transaction, job, &processed, &conflicts)
		case ScopeProfileExportItems:
			return service.processProfileExportItems(ctx, transaction, job, &processed, &conflicts, &completed)
		case ScopeAdminTOTPEnrollments:
			return service.processTOTPEnrollments(ctx, transaction, job, &processed, &conflicts, &completed)
		default:
			return ErrIntegrity
		}
	})
	return processed, conflicts, completed, err
}

func (service *Service) processUserProfiles(
	ctx context.Context,
	transaction Transaction,
	job Job,
	processed, conflicts *uint32,
) error {
	rows, err := transaction.ListUserProfiles(ctx, job.SourceVersion, job.Cursor.ID, service.config.BatchSize)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return service.advanceAndAudit(ctx, transaction, job, Cursor{Scope: ScopeProfileExportItems}, 0, 0)
	}
	for _, row := range rows {
		rotated, rotateErr := service.pii.Reencrypt(row.UserID, profile.FieldRealName, row.Encrypted)
		if rotateErr != nil || rotated.KeyVersion != job.TargetVersion {
			return ErrIntegrity
		}
		updated, updateErr := transaction.RotateUserProfile(ctx, row, rotated, job.SourceVersion)
		if updateErr != nil {
			return updateErr
		}
		if updated {
			(*processed)++
		} else {
			(*conflicts)++
		}
	}
	next := Cursor{Scope: ScopeUserProfiles, ID: rows[len(rows)-1].UserID}
	return service.advanceAndAudit(ctx, transaction, job, next, int64(*processed), int64(*conflicts))
}

func (service *Service) processProfileExportItems(
	ctx context.Context,
	transaction Transaction,
	job Job,
	processed, conflicts *uint32,
	completed *bool,
) error {
	rows, err := transaction.ListProfileExportItems(
		ctx, job.SourceVersion, job.Cursor.ID, job.Cursor.Ordinal, service.config.BatchSize,
	)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return service.finishOrRestart(ctx, transaction, job, processed, conflicts, completed)
	}
	for _, row := range rows {
		rotated, rotateErr := service.pii.Reencrypt(row.UserID, profile.FieldRealName, row.Encrypted)
		if rotateErr != nil || rotated.KeyVersion != job.TargetVersion {
			return ErrIntegrity
		}
		updated, updateErr := transaction.RotateProfileExportItem(ctx, row, rotated, job.SourceVersion)
		if updateErr != nil {
			return updateErr
		}
		if updated {
			(*processed)++
		} else {
			(*conflicts)++
		}
	}
	last := rows[len(rows)-1]
	next := Cursor{Scope: ScopeProfileExportItems, ID: last.ExportID, Ordinal: last.Ordinal}
	return service.advanceAndAudit(ctx, transaction, job, next, int64(*processed), int64(*conflicts))
}

func (service *Service) processTOTPEnrollments(
	ctx context.Context,
	transaction Transaction,
	job Job,
	processed, conflicts *uint32,
	completed *bool,
) error {
	rows, err := transaction.ListTOTPEnrollments(ctx, job.SourceVersion, job.Cursor.ID, service.config.BatchSize)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return service.finishOrRestart(ctx, transaction, job, processed, conflicts, completed)
	}
	for _, row := range rows {
		rotated, rotateErr := service.totp.ReencryptSeed(row.AdminID, row.EnrollmentID, row.Encrypted)
		if rotateErr != nil || rotated.KeyVersion != job.TargetVersion {
			return ErrIntegrity
		}
		updated, updateErr := transaction.RotateTOTPEnrollment(ctx, row, rotated, job.SourceVersion)
		if updateErr != nil {
			return updateErr
		}
		if updated {
			(*processed)++
		} else {
			(*conflicts)++
		}
	}
	next := Cursor{Scope: ScopeAdminTOTPEnrollments, ID: rows[len(rows)-1].EnrollmentID}
	return service.advanceAndAudit(ctx, transaction, job, next, int64(*processed), int64(*conflicts))
}

func (service *Service) finishOrRestart(
	ctx context.Context,
	transaction Transaction,
	job Job,
	processed, conflicts *uint32,
	completed *bool,
) error {
	references, err := transaction.CountReferences(ctx, job.Purpose, job.SourceVersion)
	if err != nil {
		return err
	}
	if references > 0 {
		// A concurrent authoritative write may have lost the row CAS; rescan from the beginning before retirement.
		return service.advanceAndAudit(ctx, transaction, job, Cursor{Scope: initialScope(job.Purpose)}, 0, 0)
	}
	completedAt := service.clock.Now()
	finished, err := transaction.CompleteJob(ctx, job, service.config.Owner, completedAt)
	if err != nil {
		return err
	}
	if err = service.appendAudit(ctx, transaction, finished, audit.ActionKeyRotationCompleted, "rotation_completed", completedAt,
		rotationDigest(finished, finished.Cursor, 0, 0)); err != nil {
		return err
	}
	*completed = true
	return nil
}

func (service *Service) advanceAndAudit(
	ctx context.Context,
	transaction Transaction,
	job Job,
	next Cursor,
	processed, conflicts int64,
) error {
	advancedAt := service.clock.Now()
	if err := transaction.AdvanceCursor(ctx, AdvanceRequest{
		JobID: job.ID, Owner: service.config.Owner, ExpectedCursor: job.Cursor, NextCursor: next,
		ProcessedDelta: processed, ConflictDelta: conflicts, AdvancedAt: advancedAt,
	}); err != nil {
		return err
	}
	return service.appendAudit(ctx, transaction, job, audit.ActionKeyRotationBatchCompleted, "rotation_batch", advancedAt,
		rotationDigest(job, next, processed, conflicts))
}

func (service *Service) appendAudit(
	ctx context.Context,
	transaction Transaction,
	job Job,
	action audit.Action,
	reason string,
	occurredAt time.Time,
	detailDigest []byte,
) error {
	head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
	if err != nil {
		return err
	}
	actor, err := audit.NewActor(audit.ActorSystem, rotationSystemActor)
	if err != nil {
		return ErrIntegrity
	}
	target, err := audit.NewTarget(audit.TargetSystem, job.ID.String())
	if err != nil {
		return ErrIntegrity
	}
	eventID := uuid.New()
	event, err := service.audit.Prepare(head, audit.EventInput{
		EventID: eventID, RequestID: "key-rotation:" + eventID.String(), OccurredAt: occurredAt,
		Actor: actor, Target: target, Action: action, ReasonCode: reason, DetailDigest: detailDigest,
	})
	if err != nil {
		return err
	}
	next, err := transaction.Audit().AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: event})
	if err != nil {
		return err
	}
	progress, err := transaction.Checkpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
	if err != nil {
		return err
	}
	health, err := service.checkpointHealth.Evaluate(ctx, next.Sequence(), progress, occurredAt)
	if err != nil || !health.CheckpointDue() {
		return err
	}
	checkpoint, err := service.audit.PrepareCheckpoint(next, occurredAt)
	if err != nil {
		return err
	}
	return transaction.Checkpoints().AppendPendingCheckpoint(ctx, checkpoint)
}

func (service *Service) failJob(ctx context.Context, job Job, code string) error {
	return service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		return transaction.FailJob(ctx, job.ID, service.config.Owner, code, service.clock.Now())
	})
}

func (service *Service) releaseJob(ctx context.Context, job Job) error {
	return service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		return transaction.ReleaseJob(ctx, job.ID, service.config.Owner, service.clock.Now())
	})
}

func rotationDigest(job Job, cursor Cursor, processed, conflicts int64) []byte {
	// This canonical text contains only identifiers, versions, cursor position, and counts; no protected data enters audit.
	value := fmt.Sprintf("v1\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s\x00%d\x00%d\x00%d",
		job.ID, job.Purpose, job.SourceVersion, job.TargetVersion, cursor.Scope, cursor.ID, cursor.Ordinal, processed, conflicts)
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func validVersion(version uint32) bool { return version > 0 && version <= math.MaxInt32 }

func validJob(job Job) bool {
	if job.ID == uuid.Nil || !job.Purpose.valid() || !validVersion(job.SourceVersion) || !validVersion(job.TargetVersion) ||
		job.SourceVersion == job.TargetVersion || job.ProcessedCount < 0 || job.ConflictCount < 0 || job.StartedAt.IsZero() {
		return false
	}
	switch job.Cursor.Scope {
	case ScopeUserProfiles:
		return job.Purpose == PurposePII && job.Cursor.Ordinal == 0
	case ScopeProfileExportItems:
		return job.Purpose == PurposePII &&
			((job.Cursor.ID == uuid.Nil && job.Cursor.Ordinal == 0) || (job.Cursor.ID != uuid.Nil && job.Cursor.Ordinal > 0))
	case ScopeAdminTOTPEnrollments:
		return job.Purpose == PurposeTOTP && job.Cursor.Ordinal == 0
	default:
		return false
	}
}
