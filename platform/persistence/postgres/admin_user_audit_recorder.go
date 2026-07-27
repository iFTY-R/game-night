package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminUserAuditRecorder keeps user-center audit writes available to both API handlers and durable workers.
// It deliberately lives beside the PostgreSQL unit of work because audit append and checkpoint scheduling share one transaction.
type AdminUserAuditRecorder struct {
	service          *audit.Service
	unitOfWork       audit.UnitOfWork
	checkpointHealth *audit.CheckpointHealthPolicy
	clock            clock.Clock
}

// NewAdminUserAuditRecorder creates the reusable audit adapter for a worker or HTTP composition root.
func NewAdminUserAuditRecorder(
	pool *pgxpool.Pool,
	service *audit.Service,
	checkpointHealth *audit.CheckpointHealthPolicy,
	source clock.Clock,
) *AdminUserAuditRecorder {
	return &AdminUserAuditRecorder{
		service: service, unitOfWork: NewAuditOutboxUnitOfWork(pool, service), checkpointHealth: checkpointHealth, clock: source,
	}
}

// RecordPIIRead records an authorized PII access without retaining plaintext values in the audit stream.
func (recorder *AdminUserAuditRecorder) RecordPIIRead(ctx context.Context, event adminuser.PIIAuditEvent) (uuid.UUID, error) {
	return recorder.append(ctx, event.ActorAdminID, event.UserID, event.RequestID, audit.ActionRealNameRead, "admin_user_pii_read", event.ReasonDigest[:], event.OccurredAt)
}

// RecordAnnotationWrite records user governance and annotation intent with only a digest of mutable content.
func (recorder *AdminUserAuditRecorder) RecordAnnotationWrite(ctx context.Context, event adminuser.AnnotationAuditEvent) (uuid.UUID, error) {
	action := audit.ActionRealNameUpdated
	targetID := event.UserID
	if targetID == uuid.Nil {
		targetID = event.ActorAdminID
		action = audit.ActionAuditEventsRead
	}
	return recorder.append(ctx, event.ActorAdminID, targetID, event.RequestID, action, event.Action, event.DetailDigest[:], event.OccurredAt)
}

func (recorder *AdminUserAuditRecorder) append(
	ctx context.Context,
	adminID uuid.UUID,
	targetID uuid.UUID,
	requestID string,
	action audit.Action,
	reasonCode string,
	detailDigest []byte,
	occurredAt time.Time,
) (uuid.UUID, error) {
	if recorder == nil || recorder.service == nil || recorder.unitOfWork == nil || recorder.checkpointHealth == nil || recorder.clock == nil ||
		ctx == nil || adminID == uuid.Nil || targetID == uuid.Nil {
		return uuid.Nil, audit.ErrInvalidInput
	}
	if occurredAt.IsZero() {
		occurredAt = recorder.clock.Now()
	}
	var eventID uuid.UUID
	err := recorder.unitOfWork.Run(ctx, func(ctx context.Context, transaction audit.Transaction) error {
		head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		progress, err := transaction.Checkpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		health, err := recorder.checkpointHealth.Evaluate(ctx, head.Sequence(), progress, recorder.clock.Now())
		if err != nil {
			return err
		}
		if !health.AllowsSensitiveWrites() {
			return audit.ErrSensitiveWriteBlocked
		}
		actor, err := audit.NewActor(audit.ActorAdmin, adminID.String())
		if err != nil {
			return err
		}
		target, err := audit.NewTarget(audit.TargetUser, targetID.String())
		if err != nil {
			return err
		}
		if strings.TrimSpace(requestID) == "" {
			requestID = fmt.Sprintf("admin-user:%d:%s", action, adminID.String())
		}
		eventID, err = uuid.NewV7()
		if err != nil {
			return err
		}
		signed, err := recorder.service.Prepare(head, audit.EventInput{
			EventID: eventID, RequestID: requestID, OccurredAt: occurredAt,
			Actor: actor, Target: target, Action: action, ReasonCode: reasonCode, DetailDigest: detailDigest,
		})
		if err != nil {
			return err
		}
		next, err := transaction.Audit().AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: signed})
		if err != nil {
			return err
		}
		// Checkpoint scheduling must re-read progress after the append. Reusing the pre-append
		// snapshot is inconsistent with the new head when the chain was fully checkpointed at
		// transaction start (positive lag with a zero UncheckpointedSince), which fails health
		// evaluation and would block every sensitive write on an otherwise healthy chain.
		progress, err = transaction.Checkpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		health, err = recorder.checkpointHealth.Evaluate(ctx, next.Sequence(), progress, recorder.clock.Now())
		if err != nil || !health.CheckpointDue() {
			return err
		}
		checkpoint, err := recorder.service.PrepareCheckpoint(next, recorder.clock.Now())
		if err != nil {
			return err
		}
		return transaction.Checkpoints().AppendPendingCheckpoint(ctx, checkpoint)
	})
	if err != nil {
		return uuid.Nil, err
	}
	return eventID, nil
}

var _ adminuser.AuditRecorder = (*AdminUserAuditRecorder)(nil)
