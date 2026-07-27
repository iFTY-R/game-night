package operations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
)

const (
	// CommandPreviewTTL limits how long live operational state can authorize a sensitive command.
	CommandPreviewTTL = 5 * time.Minute
	// MaximumManualTaskRetries caps administrator-triggered retries independently of automatic worker recovery.
	MaximumManualTaskRetries uint32 = 3
)

// CommandKind is the closed persistence vocabulary for operations previews and receipts.
type CommandKind string

const (
	CommandMaintenanceChange CommandKind = "maintenance_change"
	CommandCacheRefresh      CommandKind = "cache_refresh"
	CommandTaskRetry         CommandKind = "task_retry"
)

// CommandOutcome is the durable result returned by both first execution and idempotent replay.
type CommandOutcome string

const (
	CommandOutcomeApplied  CommandOutcome = "applied"
	CommandOutcomeNoChange CommandOutcome = "no_change"
	CommandOutcomeRejected CommandOutcome = "rejected"
)

// CommandPreview is a short-lived, server-created authorization snapshot. It stores no plaintext reason.
type CommandPreview struct {
	Digest                  [sha256.Size]byte
	ActorAdminID            uuid.UUID
	Kind                    CommandKind
	ReasonDigest            [sha256.Size]byte
	ExpectedVersion         uint64
	MaintenanceEnabled      *bool
	MaintenancePlannedEndAt time.Time
	CacheNamespace          CacheNamespace
	TaskKind                RetryTaskKind
	TaskID                  uuid.UUID
	SampledAt               time.Time
	ExpiresAt               time.Time
	ConsumedAt              time.Time
	Version                 uint64
}

// CommandReceipt preserves maintenance and cache results for operation-ID replay after later state changes.
type CommandReceipt struct {
	ActorAdminID                uuid.UUID
	OperationID                 string
	RequestDigest               [sha256.Size]byte
	Kind                        CommandKind
	Target                      string
	Outcome                     CommandOutcome
	PreviousVersion             uint64
	CurrentVersion              uint64
	MaintenanceEnabled          *bool
	MaintenanceReason           string
	MaintenancePlannedEndAt     time.Time
	MaintenanceChangedByAdminID uuid.UUID
	MaintenanceChangedAt        time.Time
	AuditEventID                uuid.UUID
	CompletedAt                 time.Time
}

// RetryTask is the redacted state needed to authorize one manual retry.
type RetryTask struct {
	Kind            RetryTaskKind
	ID              uuid.UUID
	State           string
	StableErrorCode string
	Attempts        uint32
	Version         uint64
	UpdatedAt       time.Time
}

// CommandRepository is implemented by both the pool adapter and its transaction-bound form.
type CommandRepository interface {
	GetMaintenanceState(context.Context) (MaintenanceState, error)
	UpdateMaintenanceState(context.Context, MaintenanceChange) (MaintenanceState, error)
	GetOverviewCounts(context.Context, time.Time, time.Time, time.Time) (OverviewCounts, error)
	GetCacheGeneration(context.Context, CacheNamespace) (CacheGeneration, error)
	AdvanceCacheGeneration(context.Context, CacheNamespace, uint64, uuid.UUID, time.Time) (CacheGeneration, error)
	CreateCommandPreview(context.Context, CommandPreview) (CommandPreview, error)
	GetCommandPreview(context.Context, uuid.UUID, [sha256.Size]byte) (CommandPreview, error)
	ConsumeCommandPreview(context.Context, uuid.UUID, [sha256.Size]byte, uint64, time.Time) (CommandPreview, error)
	GetCommandReceipt(context.Context, uuid.UUID, string) (CommandReceipt, error)
	CreateCommandReceipt(context.Context, CommandReceipt) (CommandReceipt, error)
	GetRetryTask(context.Context, RetryTaskKind, uuid.UUID) (RetryTask, error)
	GetRetryTaskForUpdate(context.Context, RetryTaskKind, uuid.UUID) (RetryTask, error)
	CountTaskRetries(context.Context, RetryTaskKind, uuid.UUID) (uint32, error)
	RetryTask(context.Context, RetryTaskKind, uuid.UUID, uint64, time.Time) (RetryTask, error)
	GetRetryReceipt(context.Context, uuid.UUID, string) (RetryReceipt, error)
	CreateRetryReceipt(context.Context, RetryReceipt) (RetryReceipt, error)
}

// CommandTransaction exposes every participant bound to one authoritative PostgreSQL transaction.
type CommandTransaction interface {
	Operations() CommandRepository
	Audit() audit.Repository
	Checkpoints() audit.CheckpointRepository
	OutboxEvents() outbox.EventRepository
}

// CommandTransactionWork must not retain transaction-bound repositories after returning.
type CommandTransactionWork func(context.Context, CommandTransaction) error

// CommandUnitOfWork commits state CAS, signed audit, receipt, and outbox atomically.
type CommandUnitOfWork interface {
	Run(context.Context, CommandTransactionWork) error
}

// CacheImpactReader counts only keys owned by one fixed projection implementation.
type CacheImpactReader interface {
	EstimateCacheEntries(context.Context, CacheNamespace) (uint64, error)
}

// CommandServiceConfig supplies the complete fail-closed graph for sensitive operations commands.
type CommandServiceConfig struct {
	Repository       CommandRepository
	UnitOfWork       CommandUnitOfWork
	Audit            *audit.Service
	CheckpointHealth *audit.CheckpointHealthPolicy
	CacheImpact      CacheImpactReader
	Clock            clock.Clock
}

// CommandService owns preview binding, authorization, idempotency, and atomic command execution.
type CommandService struct {
	repository       CommandRepository
	unitOfWork       CommandUnitOfWork
	audit            *audit.Service
	checkpointHealth *audit.CheckpointHealthPolicy
	cacheImpact      CacheImpactReader
	clock            clock.Clock
}

// NewCommandService rejects partial composition so audit or transaction failures cannot silently bypass controls.
func NewCommandService(config CommandServiceConfig) (*CommandService, error) {
	if config.Repository == nil || config.UnitOfWork == nil || config.Audit == nil || config.CheckpointHealth == nil || config.CacheImpact == nil || config.Clock == nil {
		return nil, ErrInvalidInput
	}
	return &CommandService{
		repository: config.Repository, unitOfWork: config.UnitOfWork, audit: config.Audit,
		checkpointHealth: config.CheckpointHealth, cacheImpact: config.CacheImpact, clock: config.Clock,
	}, nil
}

// MaintenancePreview freezes the exact state and impact reviewed before changing mutation admission.
type MaintenancePreview struct {
	Current            MaintenanceState
	Target             MaintenanceState
	ActiveRooms        uint64
	ActiveGames        uint64
	RejectedProcedures []string
	PreviewDigest      [sha256.Size]byte
	SampledAt          time.Time
	ExpiresAt          time.Time
}

// PreviewMaintenanceChangeInput contains only the closed maintenance transition exposed by the wire contract.
type PreviewMaintenanceChangeInput struct {
	Enabled      bool
	Scope        MaintenanceScope
	Reason       string
	PlannedEndAt time.Time
}

// ApplyMaintenanceChangeCommand binds execution to the exact preview and current singleton version.
type ApplyMaintenanceChangeCommand struct {
	OperationID     idempotency.OperationID
	Enabled         bool
	Scope           MaintenanceScope
	Reason          string
	PlannedEndAt    time.Time
	ExpectedVersion uint64
	PreviewDigest   [sha256.Size]byte
}

// MaintenanceChangeResult is stable across operation-ID replay.
type MaintenanceChangeResult struct {
	Receipt     CommandReceipt
	Outcome     CommandOutcome
	Maintenance MaintenanceState
}

// PreviewMaintenanceChange persists a short-lived snapshot and returns real current room/game counts.
func (service *CommandService) PreviewMaintenanceChange(ctx context.Context, actor admin.ActorContext, input PreviewMaintenanceChangeInput) (MaintenancePreview, error) {
	if service == nil || ctx == nil || input.Scope != MaintenanceUserMutations || !validCommandReason(input.Reason) {
		return MaintenancePreview{}, ErrInvalidInput
	}
	if err := service.authorizePreview(actor); err != nil {
		return MaintenancePreview{}, err
	}
	now := service.clock.Now().Round(0).UTC()
	if !input.PlannedEndAt.IsZero() && !input.PlannedEndAt.After(now) {
		return MaintenancePreview{}, ErrInvalidInput
	}
	current, err := service.repository.GetMaintenanceState(ctx)
	if err != nil {
		return MaintenancePreview{}, err
	}
	counts, err := service.repository.GetOverviewCounts(ctx, now.Add(-24*time.Hour), now, now)
	if err != nil {
		return MaintenancePreview{}, err
	}
	digest, err := newPreviewDigest(actor.AdminID(), CommandMaintenanceChange, string(input.Scope), input.Enabled, input.PlannedEndAt, current.Version, input.Reason)
	if err != nil {
		return MaintenancePreview{}, ErrInvalidInput
	}
	target := current
	target.Enabled, target.Scope, target.Reason = input.Enabled, input.Scope, input.Reason
	target.PlannedEndAt, target.Version, target.ChangedByAdminID, target.ChangedAt = canonicalCommandTime(input.PlannedEndAt), current.Version+1, actor.AdminID(), now
	preview, err := service.repository.CreateCommandPreview(ctx, CommandPreview{
		Digest: digest, ActorAdminID: actor.AdminID(), Kind: CommandMaintenanceChange, ReasonDigest: reasonDigest(input.Reason),
		ExpectedVersion: current.Version, MaintenanceEnabled: boolPointer(input.Enabled), MaintenancePlannedEndAt: canonicalCommandTime(input.PlannedEndAt),
		SampledAt: now, ExpiresAt: now.Add(CommandPreviewTTL), Version: 1,
	})
	if err != nil {
		return MaintenancePreview{}, err
	}
	return MaintenancePreview{
		Current: current, Target: target, ActiveRooms: counts.ActiveRooms, ActiveGames: counts.RunningGames,
		RejectedProcedures: []string{"user.mutation", "room.mutation", "game.action", "game.admission"},
		PreviewDigest:      preview.Digest, SampledAt: preview.SampledAt, ExpiresAt: preview.ExpiresAt,
	}, nil
}

// ApplyMaintenanceChange consumes the reviewed snapshot and commits state, signed audit, and receipt atomically.
func (service *CommandService) ApplyMaintenanceChange(ctx context.Context, actor admin.ActorContext, command ApplyMaintenanceChangeCommand) (MaintenanceChangeResult, error) {
	if service == nil || ctx == nil || !command.OperationID.Valid() || command.Scope != MaintenanceUserMutations || command.ExpectedVersion == 0 ||
		!validCommandReason(command.Reason) || isZeroDigest(command.PreviewDigest) {
		return MaintenanceChangeResult{}, ErrInvalidInput
	}
	if err := service.authorizeApply(actor); err != nil {
		return MaintenanceChangeResult{}, err
	}
	requestDigest := digestCanonical(actor.AdminID(), command.OperationID.Value(), command)
	var result MaintenanceChangeResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction CommandTransaction) error {
		repository := transaction.Operations()
		if receipt, err := repository.GetCommandReceipt(ctx, actor.AdminID(), command.OperationID.Value()); err == nil {
			if receipt.RequestDigest != requestDigest || receipt.Kind != CommandMaintenanceChange {
				return ErrIdempotencyConflict
			}
			result = maintenanceResultFromReceipt(receipt)
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := service.clock.Now().Round(0).UTC()
		preview, err := repository.GetCommandPreview(ctx, actor.AdminID(), command.PreviewDigest)
		if err != nil {
			return err
		}
		if err = validateMaintenancePreview(preview, command, now); err != nil {
			return err
		}
		if _, err = repository.ConsumeCommandPreview(ctx, actor.AdminID(), preview.Digest, preview.Version, now); err != nil {
			return ErrPreviewExpired
		}
		current, err := repository.GetMaintenanceState(ctx)
		if err != nil {
			return err
		}
		if current.Version != command.ExpectedVersion {
			return ErrConflict
		}
		next := current
		outcome := CommandOutcomeNoChange
		if current.Enabled != command.Enabled || current.Scope != command.Scope || current.Reason != command.Reason || !current.PlannedEndAt.Equal(canonicalCommandTime(command.PlannedEndAt)) {
			next, err = repository.UpdateMaintenanceState(ctx, MaintenanceChange{
				Enabled: command.Enabled, Scope: command.Scope, Reason: command.Reason, PlannedEndAt: canonicalCommandTime(command.PlannedEndAt),
				ExpectedVersion: command.ExpectedVersion, ChangedByAdminID: actor.AdminID(), ChangedAt: now,
			})
			if err != nil {
				return err
			}
			outcome = CommandOutcomeApplied
		}
		auditID, err := service.appendAudit(ctx, transaction, actor, audit.ActionAdminMaintenanceChanged, "maintenance_changed", "maintenance.user_mutations", command.Reason,
			digestCanonical(current, next, outcome))
		if err != nil {
			return err
		}
		receipt, err := repository.CreateCommandReceipt(ctx, CommandReceipt{
			ActorAdminID: actor.AdminID(), OperationID: command.OperationID.Value(), RequestDigest: requestDigest,
			Kind: CommandMaintenanceChange, Target: string(command.Scope), Outcome: outcome, PreviousVersion: current.Version, CurrentVersion: next.Version,
			MaintenanceEnabled: boolPointer(next.Enabled), MaintenanceReason: next.Reason, MaintenancePlannedEndAt: next.PlannedEndAt,
			MaintenanceChangedByAdminID: next.ChangedByAdminID, MaintenanceChangedAt: next.ChangedAt,
			AuditEventID: auditID, CompletedAt: now,
		})
		if err != nil {
			return err
		}
		result = maintenanceResultFromReceipt(receipt)
		return nil
	})
	if err != nil {
		return MaintenanceChangeResult{}, mapCommandError(err)
	}
	return result, nil
}

// authorizePreview permits impact review before step-up so the exact server preview can survive elevation.
func (service *CommandService) authorizePreview(actor admin.ActorContext) error {
	if err := actor.Require(admin.PermissionOperationsMaintain); err != nil {
		return ErrPermissionDenied
	}
	return nil
}

// authorizeApply requires the same permission plus a live operations-maintenance elevation grant.
func (service *CommandService) authorizeApply(actor admin.ActorContext) error {
	if err := service.authorizePreview(actor); err != nil {
		return err
	}
	if err := actor.RequireElevation(admin.ElevationScopeOperationsMaintenance, service.clock.Now()); err != nil {
		return ErrElevationRequired
	}
	return nil
}

func (service *CommandService) appendAudit(ctx context.Context, transaction CommandTransaction, actor admin.ActorContext, action audit.Action, reasonCode, targetID, commandReason string, detail [sha256.Size]byte) (uuid.UUID, error) {
	if transaction == nil || transaction.Audit() == nil || transaction.Checkpoints() == nil || service.audit == nil || service.checkpointHealth == nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	progress, err := transaction.Checkpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	health, err := service.checkpointHealth.Evaluate(ctx, head.Sequence(), progress, service.clock.Now())
	if err != nil || !health.AllowsSensitiveWrites() {
		return uuid.Nil, ErrAuditUnavailable
	}
	auditActor, err := audit.NewActor(audit.ActorAdmin, actor.AdminID().String())
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	target, err := audit.NewTarget(audit.TargetSystem, targetID)
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	requestID := strings.TrimSpace(actor.RequestID())
	if requestID == "" {
		requestID = fmt.Sprintf("admin-operations:%d:%s", action, eventID.String())
	}
	signed, err := service.audit.Prepare(head, audit.EventInput{
		EventID: eventID, RequestID: requestID, OccurredAt: service.clock.Now(), Actor: auditActor, Target: target,
		Action: action, ReasonCode: reasonCode, DetailDigest: digestCanonical(reasonDigest(commandReason), detail).Bytes(),
	})
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	next, err := transaction.Audit().AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: signed})
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	// Progress is re-read after append because the pre-append snapshot is invalid when the chain began fully acknowledged.
	progress, err = transaction.Checkpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	health, err = service.checkpointHealth.Evaluate(ctx, next.Sequence(), progress, service.clock.Now())
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	if health.CheckpointDue() {
		checkpoint, prepareErr := service.audit.PrepareCheckpoint(next, service.clock.Now())
		if prepareErr != nil || transaction.Checkpoints().AppendPendingCheckpoint(ctx, checkpoint) != nil {
			return uuid.Nil, ErrAuditUnavailable
		}
	}
	return eventID, nil
}

func validateMaintenancePreview(preview CommandPreview, command ApplyMaintenanceChangeCommand, now time.Time) error {
	if preview.Kind != CommandMaintenanceChange || preview.MaintenanceEnabled == nil || *preview.MaintenanceEnabled != command.Enabled ||
		preview.ExpectedVersion != command.ExpectedVersion || preview.ReasonDigest != reasonDigest(command.Reason) ||
		!preview.MaintenancePlannedEndAt.Equal(canonicalCommandTime(command.PlannedEndAt)) {
		return ErrConflict
	}
	return validateLivePreview(preview, now)
}

func validateLivePreview(preview CommandPreview, now time.Time) error {
	if preview.Version == 0 || !preview.ConsumedAt.IsZero() || !now.Before(preview.ExpiresAt) || now.Before(preview.SampledAt) {
		return ErrPreviewExpired
	}
	return nil
}

func maintenanceResultFromReceipt(receipt CommandReceipt) MaintenanceChangeResult {
	state := MaintenanceState{
		Enabled: receipt.MaintenanceEnabled != nil && *receipt.MaintenanceEnabled, Scope: MaintenanceUserMutations,
		Reason: receipt.MaintenanceReason, PlannedEndAt: receipt.MaintenancePlannedEndAt, Version: receipt.CurrentVersion,
		ChangedByAdminID: receipt.MaintenanceChangedByAdminID, ChangedAt: receipt.MaintenanceChangedAt,
	}
	return MaintenanceChangeResult{Receipt: receipt, Outcome: receipt.Outcome, Maintenance: state}
}

func validCommandReason(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 512
}

func reasonDigest(value string) [sha256.Size]byte { return sha256.Sum256([]byte(value)) }

func newPreviewDigest(actorID uuid.UUID, kind CommandKind, target any, input any, plannedAt time.Time, expected uint64, reason string) ([sha256.Size]byte, error) {
	nonce, err := uuid.NewV7()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return digestCanonical(actorID, kind, target, input, canonicalCommandTime(plannedAt), expected, reasonDigest(reason), nonce), nil
}

type commandDigest [sha256.Size]byte

func (digest commandDigest) Bytes() []byte { return digest[:] }

func digestCanonical(values ...any) commandDigest {
	encoded, _ := json.Marshal(values)
	return commandDigest(sha256.Sum256(encoded))
}

func isZeroDigest(value [sha256.Size]byte) bool { return value == [sha256.Size]byte{} }

func boolPointer(value bool) *bool { return &value }

func canonicalCommandTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.Round(0).UTC()
}

func mapCommandError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict), errors.Is(err, ErrIntegrity),
		errors.Is(err, ErrRepositoryUnavailable), errors.Is(err, ErrPermissionDenied), errors.Is(err, ErrElevationRequired),
		errors.Is(err, ErrPreviewExpired), errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrAuditUnavailable), errors.Is(err, ErrRetryLimit):
		return err
	default:
		return ErrRepositoryUnavailable
	}
}
