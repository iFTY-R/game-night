package operations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
)

// CacheRefreshPreview reports only a fixed namespace and real repository-owned entry estimate.
type CacheRefreshPreview struct {
	Namespace         CacheNamespace
	CurrentGeneration uint64
	EstimatedEntries  uint64
	PreviewDigest     [sha256.Size]byte
	SampledAt         time.Time
	ExpiresAt         time.Time
}

// PreviewCacheRefreshInput cannot carry a Redis key, prefix, glob, script, or command.
type PreviewCacheRefreshInput struct {
	Namespace CacheNamespace
	Reason    string
}

// ApplyCacheRefreshCommand advances one fixed projection generation after preview review.
type ApplyCacheRefreshCommand struct {
	OperationID        idempotency.OperationID
	Namespace          CacheNamespace
	Reason             string
	ExpectedGeneration uint64
	PreviewDigest      [sha256.Size]byte
}

// CacheRefreshResult is stable across operation-ID replay.
type CacheRefreshResult struct {
	Receipt            CommandReceipt
	Outcome            CommandOutcome
	Namespace          CacheNamespace
	PreviousGeneration uint64
	CurrentGeneration  uint64
}

// PreviewCacheRefresh persists a reviewed generation and obtains an exact owned-entry estimate.
func (service *CommandService) PreviewCacheRefresh(ctx context.Context, actor admin.ActorContext, input PreviewCacheRefreshInput) (CacheRefreshPreview, error) {
	if service == nil || ctx == nil || !input.Namespace.Valid() || !validCommandReason(input.Reason) {
		return CacheRefreshPreview{}, ErrInvalidInput
	}
	if err := service.authorizePreview(actor); err != nil {
		return CacheRefreshPreview{}, err
	}
	now := service.clock.Now().Round(0).UTC()
	generation, err := service.repository.GetCacheGeneration(ctx, input.Namespace)
	if err != nil {
		return CacheRefreshPreview{}, err
	}
	estimated, err := service.cacheImpact.EstimateCacheEntries(ctx, input.Namespace)
	if err != nil {
		return CacheRefreshPreview{}, ErrRepositoryUnavailable
	}
	digest, err := newPreviewDigest(actor.AdminID(), CommandCacheRefresh, input.Namespace, generation.Generation, time.Time{}, generation.Generation, input.Reason)
	if err != nil {
		return CacheRefreshPreview{}, ErrInvalidInput
	}
	preview, err := service.repository.CreateCommandPreview(ctx, CommandPreview{
		Digest: digest, ActorAdminID: actor.AdminID(), Kind: CommandCacheRefresh, ReasonDigest: reasonDigest(input.Reason),
		ExpectedVersion: generation.Generation, CacheNamespace: input.Namespace, SampledAt: now, ExpiresAt: now.Add(CommandPreviewTTL), Version: 1,
	})
	if err != nil {
		return CacheRefreshPreview{}, err
	}
	return CacheRefreshPreview{
		Namespace: input.Namespace, CurrentGeneration: generation.Generation, EstimatedEntries: estimated,
		PreviewDigest: preview.Digest, SampledAt: preview.SampledAt, ExpiresAt: preview.ExpiresAt,
	}, nil
}

// ApplyCacheRefresh advances PostgreSQL first and emits a committed generation event; it never mutates Redis directly.
func (service *CommandService) ApplyCacheRefresh(ctx context.Context, actor admin.ActorContext, command ApplyCacheRefreshCommand) (CacheRefreshResult, error) {
	if service == nil || ctx == nil || !command.OperationID.Valid() || !command.Namespace.Valid() || command.ExpectedGeneration == 0 ||
		!validCommandReason(command.Reason) || isZeroDigest(command.PreviewDigest) {
		return CacheRefreshResult{}, ErrInvalidInput
	}
	if err := service.authorizeApply(actor); err != nil {
		return CacheRefreshResult{}, err
	}
	requestDigest := digestCanonical(actor.AdminID(), command.OperationID.Value(), command)
	var result CacheRefreshResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction CommandTransaction) error {
		repository := transaction.Operations()
		if receipt, err := repository.GetCommandReceipt(ctx, actor.AdminID(), command.OperationID.Value()); err == nil {
			if receipt.RequestDigest != requestDigest || receipt.Kind != CommandCacheRefresh {
				return ErrIdempotencyConflict
			}
			result = cacheResultFromReceipt(receipt)
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := service.clock.Now().Round(0).UTC()
		preview, err := repository.GetCommandPreview(ctx, actor.AdminID(), command.PreviewDigest)
		if err != nil {
			return err
		}
		if preview.Kind != CommandCacheRefresh || preview.CacheNamespace != command.Namespace || preview.ExpectedVersion != command.ExpectedGeneration || preview.ReasonDigest != reasonDigest(command.Reason) {
			return ErrConflict
		}
		if err = validateLivePreview(preview, now); err != nil {
			return err
		}
		if _, err = repository.ConsumeCommandPreview(ctx, actor.AdminID(), preview.Digest, preview.Version, now); err != nil {
			return ErrPreviewExpired
		}
		current, err := repository.GetCacheGeneration(ctx, command.Namespace)
		if err != nil {
			return err
		}
		if current.Generation != command.ExpectedGeneration {
			return ErrConflict
		}
		next, err := repository.AdvanceCacheGeneration(ctx, command.Namespace, current.Generation, actor.AdminID(), now)
		if err != nil {
			return err
		}
		auditID, err := service.appendAudit(ctx, transaction, actor, audit.ActionAdminCacheRefreshed, "cache_refreshed", "cache."+string(command.Namespace), command.Reason,
			digestCanonical(command.Namespace, current.Generation, next.Generation))
		if err != nil {
			return err
		}
		if err = insertOperationsEvent(ctx, transaction.OutboxEvents(), outbox.EventTypeAdminCacheGenerationAdvanced, outbox.AggregateTypeAdminProjection,
			stableOperationsAggregateID("cache", string(command.Namespace)), now, struct {
				Namespace  CacheNamespace `json:"namespace"`
				Generation uint64         `json:"generation"`
			}{Namespace: command.Namespace, Generation: next.Generation}); err != nil {
			return err
		}
		receipt, err := repository.CreateCommandReceipt(ctx, CommandReceipt{
			ActorAdminID: actor.AdminID(), OperationID: command.OperationID.Value(), RequestDigest: requestDigest,
			Kind: CommandCacheRefresh, Target: string(command.Namespace), Outcome: CommandOutcomeApplied,
			PreviousVersion: current.Generation, CurrentVersion: next.Generation, AuditEventID: auditID, CompletedAt: now,
		})
		if err != nil {
			return err
		}
		result = cacheResultFromReceipt(receipt)
		return nil
	})
	if err != nil {
		return CacheRefreshResult{}, mapCommandError(err)
	}
	return result, nil
}

// TaskRetryPreview reports the failed task state without exposing payload or raw error text.
type TaskRetryPreview struct {
	Task             RetryTask
	ManualRetryCount uint32
	RetryAllowed     bool
	PreviewDigest    [sha256.Size]byte
	SampledAt        time.Time
	ExpiresAt        time.Time
}

// PreviewTaskRetryInput accepts only the two reviewed durable administrator job families.
type PreviewTaskRetryInput struct {
	TaskKind RetryTaskKind
	TaskID   uuid.UUID
	Reason   string
}

// ApplyTaskRetryCommand binds retry to one exact failed-task version and reviewed preview.
type ApplyTaskRetryCommand struct {
	OperationID         idempotency.OperationID
	TaskKind            RetryTaskKind
	TaskID              uuid.UUID
	Reason              string
	ExpectedTaskVersion uint64
	PreviewDigest       [sha256.Size]byte
}

// TaskRetryResult is stable across operation-ID replay.
type TaskRetryResult struct {
	Receipt          RetryReceipt
	Outcome          CommandOutcome
	Task             RetryTask
	ManualRetryCount uint32
}

// PreviewTaskRetry persists the exact failed task version and retry-count evidence.
func (service *CommandService) PreviewTaskRetry(ctx context.Context, actor admin.ActorContext, input PreviewTaskRetryInput) (TaskRetryPreview, error) {
	if service == nil || ctx == nil || !input.TaskKind.Valid() || input.TaskID == uuid.Nil || !validCommandReason(input.Reason) {
		return TaskRetryPreview{}, ErrInvalidInput
	}
	if err := service.authorizePreview(actor); err != nil {
		return TaskRetryPreview{}, err
	}
	now := service.clock.Now().Round(0).UTC()
	task, err := service.repository.GetRetryTask(ctx, input.TaskKind, input.TaskID)
	if err != nil {
		return TaskRetryPreview{}, err
	}
	count, err := service.repository.CountTaskRetries(ctx, input.TaskKind, input.TaskID)
	if err != nil {
		return TaskRetryPreview{}, err
	}
	digest, err := newPreviewDigest(actor.AdminID(), CommandTaskRetry, input.TaskKind, input.TaskID, time.Time{}, task.Version, input.Reason)
	if err != nil {
		return TaskRetryPreview{}, ErrInvalidInput
	}
	preview, err := service.repository.CreateCommandPreview(ctx, CommandPreview{
		Digest: digest, ActorAdminID: actor.AdminID(), Kind: CommandTaskRetry, ReasonDigest: reasonDigest(input.Reason),
		ExpectedVersion: task.Version, TaskKind: input.TaskKind, TaskID: input.TaskID, SampledAt: now, ExpiresAt: now.Add(CommandPreviewTTL), Version: 1,
	})
	if err != nil {
		return TaskRetryPreview{}, err
	}
	return TaskRetryPreview{
		Task: task, ManualRetryCount: count, RetryAllowed: task.State == "failed" && count < MaximumManualTaskRetries,
		PreviewDigest: preview.Digest, SampledAt: preview.SampledAt, ExpiresAt: preview.ExpiresAt,
	}, nil
}

// ApplyTaskRetry requeues the original failed task without creating a second logical job.
func (service *CommandService) ApplyTaskRetry(ctx context.Context, actor admin.ActorContext, command ApplyTaskRetryCommand) (TaskRetryResult, error) {
	if service == nil || ctx == nil || !command.OperationID.Valid() || !command.TaskKind.Valid() || command.TaskID == uuid.Nil ||
		command.ExpectedTaskVersion == 0 || !validCommandReason(command.Reason) || isZeroDigest(command.PreviewDigest) {
		return TaskRetryResult{}, ErrInvalidInput
	}
	if err := service.authorizeApply(actor); err != nil {
		return TaskRetryResult{}, err
	}
	requestDigest := digestCanonical(actor.AdminID(), command.OperationID.Value(), command)
	var result TaskRetryResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction CommandTransaction) error {
		repository := transaction.Operations()
		if receipt, err := repository.GetRetryReceipt(ctx, actor.AdminID(), command.OperationID.Value()); err == nil {
			if receipt.RequestDigest != requestDigest || receipt.TaskKind != command.TaskKind || receipt.TaskID != command.TaskID {
				return ErrIdempotencyConflict
			}
			result = retryResultFromReceipt(receipt)
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := service.clock.Now().Round(0).UTC()
		preview, err := repository.GetCommandPreview(ctx, actor.AdminID(), command.PreviewDigest)
		if err != nil {
			return err
		}
		if preview.Kind != CommandTaskRetry || preview.TaskKind != command.TaskKind || preview.TaskID != command.TaskID ||
			preview.ExpectedVersion != command.ExpectedTaskVersion || preview.ReasonDigest != reasonDigest(command.Reason) {
			return ErrConflict
		}
		if err = validateLivePreview(preview, now); err != nil {
			return err
		}
		if _, err = repository.ConsumeCommandPreview(ctx, actor.AdminID(), preview.Digest, preview.Version, now); err != nil {
			return ErrPreviewExpired
		}
		task, err := repository.GetRetryTaskForUpdate(ctx, command.TaskKind, command.TaskID)
		if err != nil {
			return err
		}
		if task.Version != command.ExpectedTaskVersion || task.State != "failed" || task.StableErrorCode == "" {
			return ErrConflict
		}
		count, err := repository.CountTaskRetries(ctx, command.TaskKind, command.TaskID)
		if err != nil {
			return err
		}
		if count >= MaximumManualTaskRetries {
			return ErrRetryLimit
		}
		updated, err := repository.RetryTask(ctx, command.TaskKind, command.TaskID, task.Version, now)
		if err != nil {
			return err
		}
		auditID, err := service.appendAudit(ctx, transaction, actor, audit.ActionAdminTaskRetried, "task_retried", "task."+string(command.TaskKind), command.Reason,
			digestCanonical(command.TaskKind, command.TaskID, task.Version, updated.Version, task.StableErrorCode, count+1))
		if err != nil {
			return err
		}
		if err = insertOperationsEvent(ctx, transaction.OutboxEvents(), outbox.EventTypeAdminTaskRetried, outbox.AggregateTypeAdminTask,
			command.TaskID, now, struct {
				TaskKind RetryTaskKind `json:"task_kind"`
				TaskID   uuid.UUID     `json:"task_id"`
				Version  uint64        `json:"version"`
			}{TaskKind: command.TaskKind, TaskID: command.TaskID, Version: updated.Version}); err != nil {
			return err
		}
		receipt, err := repository.CreateRetryReceipt(ctx, RetryReceipt{
			ActorAdminID: actor.AdminID(), OperationID: command.OperationID.Value(), RequestDigest: requestDigest,
			TaskKind: command.TaskKind, TaskID: command.TaskID, ExpectedTaskVersion: task.Version, Outcome: string(CommandOutcomeApplied),
			TaskVersion: updated.Version, ManualRetryCount: count + 1, TaskState: updated.State, OriginalErrorCode: task.StableErrorCode,
			AuditEventID: auditID, CompletedAt: now,
		})
		if err != nil {
			return err
		}
		result = retryResultFromReceipt(receipt)
		return nil
	})
	if err != nil {
		return TaskRetryResult{}, mapCommandError(err)
	}
	return result, nil
}

func cacheResultFromReceipt(receipt CommandReceipt) CacheRefreshResult {
	return CacheRefreshResult{
		Receipt: receipt, Outcome: receipt.Outcome, Namespace: CacheNamespace(receipt.Target),
		PreviousGeneration: receipt.PreviousVersion, CurrentGeneration: receipt.CurrentVersion,
	}
}

func retryResultFromReceipt(receipt RetryReceipt) TaskRetryResult {
	return TaskRetryResult{
		Receipt: receipt, Outcome: CommandOutcome(receipt.Outcome), ManualRetryCount: receipt.ManualRetryCount,
		Task: RetryTask{Kind: receipt.TaskKind, ID: receipt.TaskID, State: receipt.TaskState, StableErrorCode: receipt.OriginalErrorCode, Version: receipt.TaskVersion},
	}
}

func insertOperationsEvent(ctx context.Context, repository outbox.EventRepository, eventType outbox.EventType, aggregateType outbox.AggregateType, aggregateID uuid.UUID, at time.Time, payload any) error {
	if repository == nil || aggregateID == uuid.Nil {
		return ErrRepositoryUnavailable
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ErrInvalidInput
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return ErrRepositoryUnavailable
	}
	event, err := outbox.NewEvent(eventID, eventType, aggregateType, aggregateID, encoded, at, at)
	if err != nil {
		return ErrInvalidInput
	}
	if _, err = repository.Insert(ctx, event); err != nil {
		return ErrRepositoryUnavailable
	}
	return nil
}

func stableOperationsAggregateID(kind, value string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("game-night/admin-operations/%s/%s", kind, value)))
}
