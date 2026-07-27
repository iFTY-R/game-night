package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	adminaudit "github.com/iFTY-R/game-night/platform/admin/auditlog"
	"github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminAuditReadRepository exposes audit reads with per-event verification outcomes to the
// management query service. Structural corruption still fails the read; a signature that does not
// validate against the current keyring is reported per event instead of failing the whole page,
// because one unverifiable historical event must not make the entire audit view unusable.
type AdminAuditReadRepository struct {
	repository *AuditRepository
	queries    *sqlcgen.Queries
	verifier   audit.IntegrityVerifier
}

// NewAdminAuditReadRepository builds a read-only adapter around the existing signed audit repository.
func NewAdminAuditReadRepository(pool *pgxpool.Pool, verifier audit.IntegrityVerifier) *AdminAuditReadRepository {
	if pool == nil || verifier == nil {
		return nil
	}
	queries := sqlcgen.New(pool)
	return &AdminAuditReadRepository{repository: newAuditRepository(queries, verifier), queries: queries, verifier: verifier}
}

// ReadHead returns the restricted, trusted chain head without exposing append operations to the admin audit domain.
func (repository *AdminAuditReadRepository) ReadHead(ctx context.Context, chainID audit.ChainID) (audit.Head, error) {
	if repository == nil || repository.repository == nil {
		return audit.Head{}, audit.ErrRepositoryUnavailable
	}
	return repository.repository.ReadHead(ctx, chainID)
}

// List restores every stored event, enforces structural/column consistency, and attaches the
// signature verification outcome so the domain can surface unverifiable ranges explicitly.
func (repository *AdminAuditReadRepository) List(ctx context.Context, request audit.ListRequest) ([]adminaudit.ReadEvent, error) {
	if repository == nil || repository.queries == nil || repository.verifier == nil {
		return nil, audit.ErrRepositoryUnavailable
	}
	validated, err := audit.NewListRequest(request.ChainID, request.AfterSequence, request.PageSize)
	if err != nil {
		return nil, err
	}
	rows, err := repository.queries.ListAuditEvents(ctx, sqlcgen.ListAuditEventsParams{
		ChainID: string(validated.ChainID), AfterSequence: int64(validated.AfterSequence), PageSize: int32(validated.PageSize),
	})
	if err != nil {
		return nil, mapAuditQueryError(ctx, err, audit.ErrRepositoryUnavailable)
	}
	events := make([]adminaudit.ReadEvent, 0, len(rows))
	for _, row := range rows {
		event, parseErr := auditEventFromRow(row)
		if parseErr != nil {
			return nil, audit.ErrIntegrity
		}
		events = append(events, adminaudit.ReadEvent{Event: event, Verified: repository.verifier.Verify(event) == nil})
	}
	return events, nil
}

// ListRecentHighRiskOperations scans a bounded tail of the signed chain and returns only verified administrator actions.
func (repository *AdminAuditReadRepository) ListRecentHighRiskOperations(ctx context.Context, start, end time.Time, limit uint32) ([]operations.RiskOperation, error) {
	if repository == nil || repository.repository == nil || repository.verifier == nil || ctx == nil || start.IsZero() || !end.After(start) ||
		limit == 0 || limit > operations.MaximumOverviewRiskOperations {
		return nil, operations.ErrInvalidInput
	}
	head, err := repository.ReadHead(ctx, audit.ChainAdmin)
	if err != nil {
		return nil, operations.ErrRepositoryUnavailable
	}
	after := uint64(0)
	if head.Sequence() > uint64(adminaudit.MaximumScanEvents) {
		after = head.Sequence() - uint64(adminaudit.MaximumScanEvents)
	}
	request, err := audit.NewListRequest(audit.ChainAdmin, after, adminaudit.MaximumScanEvents)
	if err != nil {
		return nil, operations.ErrInvalidInput
	}
	events, err := repository.List(ctx, request)
	if err != nil {
		return nil, operations.ErrRepositoryUnavailable
	}
	return recentHighRiskOperations(events, start, end, limit)
}

func recentHighRiskOperations(events []adminaudit.ReadEvent, start, end time.Time, limit uint32) ([]operations.RiskOperation, error) {
	if start.IsZero() || !end.After(start) || limit == 0 || limit > operations.MaximumOverviewRiskOperations {
		return nil, operations.ErrInvalidInput
	}
	result := make([]operations.RiskOperation, 0, limit)
	for index := len(events) - 1; index >= 0 && len(result) < int(limit); index-- {
		read := events[index]
		snapshot := read.Event.Snapshot().Event
		if !read.Verified || snapshot.Actor.Type() != audit.ActorAdmin || !highRiskOverviewAction(snapshot.Action) ||
			snapshot.OccurredAt.Before(start) || !snapshot.OccurredAt.Before(end) {
			continue
		}
		actorID, parseErr := uuid.Parse(snapshot.Actor.ID())
		if parseErr != nil || actorID == uuid.Nil || snapshot.EventID == uuid.Nil || snapshot.Target.ID() == "" {
			return nil, operations.ErrIntegrity
		}
		result = append(result, operations.RiskOperation{
			AuditEventID: snapshot.EventID, Action: snapshot.Action, ActorAdminID: actorID,
			TargetID: snapshot.Target.ID(), Verified: true, OccurredAt: snapshot.OccurredAt,
		})
	}
	return result, nil
}

func highRiskOverviewAction(action audit.Action) bool {
	switch action {
	case audit.ActionUserSuspended, audit.ActionUserUnsuspended, audit.ActionUserDeleted,
		audit.ActionRealNameRead, audit.ActionRealNameUpdated, audit.ActionAdminPasswordChanged,
		audit.ActionAdminTOTPRebound, audit.ActionAdminSessionsRevoked, audit.ActionAdminOfflineReset,
		audit.ActionKeyRotationStarted, audit.ActionKeyRotationCompleted, audit.ActionAdminMFAEnabled,
		audit.ActionAdminMFADisabled, audit.ActionAdminRecoveryCodesRegenerated,
		audit.ActionAdminMaintenanceChanged, audit.ActionAdminCacheRefreshed, audit.ActionAdminTaskRetried:
		return true
	default:
		return false
	}
}

var _ adminaudit.Reader = (*AdminAuditReadRepository)(nil)

var _ operations.OverviewAuditReader = (*AdminAuditReadRepository)(nil)
