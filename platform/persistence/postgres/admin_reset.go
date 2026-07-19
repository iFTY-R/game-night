package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
)

var ErrAdminResetUnavailable = errors.New("administrator reset unavailable")

// AdminResetRequest is the persistence-neutral payload accepted by the migration-role reset function.
type AdminResetRequest struct {
	ExpectedPreviousHash []byte
	EventID              uuid.UUID
	CanonicalEvent       []byte
	Signature            []byte
	SigningKeyVersion    int32
	CreatedAt            time.Time
	PasswordHash         string
	PasswordAlgorithm    string
	PasswordParameters   string
	CheckpointEventID    uuid.UUID
	CheckpointPayload    []byte
}

// AdminResetResult contains only the committed audit chain position.
type AdminResetResult struct {
	AppendedSequence int64
	AppendedHash     []byte
}

// ResetWork receives the restricted audit repository and reset function inside one transaction.
type ResetWork func(context.Context, audit.Repository, func(context.Context, AdminResetRequest) (AdminResetResult, error)) error

// RunAdminReset executes a read-head/sign/reset transaction. It is declared on AuditOutboxUnitOfWork below.
func (unitOfWork *AuditOutboxUnitOfWork) RunAdminReset(ctx context.Context, work ResetWork) error {
	if unitOfWork == nil || unitOfWork.runner == nil || unitOfWork.verifier == nil || work == nil {
		return ErrAdminResetUnavailable
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		repository := newAuditRepository(queries, unitOfWork.verifier)
		reset := func(ctx context.Context, request AdminResetRequest) (AdminResetResult, error) {
			if request.EventID == uuid.Nil || request.CheckpointEventID == uuid.Nil || request.SigningKeyVersion <= 0 {
				return AdminResetResult{}, ErrAdminResetUnavailable
			}
			row, err := queries.ResetAdminAccount(ctx, sqlcgen.ResetAdminAccountParams{
				ExpectedPreviousHash: request.ExpectedPreviousHash, EventID: uuidToPG(request.EventID),
				CanonicalEvent: request.CanonicalEvent, Signature: request.Signature, SigningKeyVersion: request.SigningKeyVersion,
				CreatedAt: timeToPG(request.CreatedAt), PasswordHash: request.PasswordHash, PasswordAlgorithm: request.PasswordAlgorithm,
				PasswordParameters: request.PasswordParameters, CheckpointEventID: uuidToPG(request.CheckpointEventID), CheckpointPayload: request.CheckpointPayload,
			})
			if err != nil {
				return AdminResetResult{}, ErrAdminResetUnavailable
			}
			return AdminResetResult{AppendedSequence: row.AppendedSequence, AppendedHash: append([]byte(nil), row.AppendedHash...)}, nil
		}
		return work(ctx, repository, reset)
	})
	if err != nil {
		return ErrAdminResetUnavailable
	}
	return nil
}
