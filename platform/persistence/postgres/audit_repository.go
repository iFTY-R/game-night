package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"math"

	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// auditHeadConflictSQLState is raised deliberately by append_audit_event when its expected hash loses the CAS.
const auditHeadConflictSQLState = "40001"

type auditQueries interface {
	ReadAuditHead(context.Context, sqlcgen.ReadAuditHeadParams) (sqlcgen.ReadAuditHeadRow, error)
	ReadAuditAnchor(context.Context, sqlcgen.ReadAuditAnchorParams) (sqlcgen.ReadAuditAnchorRow, error)
	AppendAuditEvent(context.Context, sqlcgen.AppendAuditEventParams) (sqlcgen.AppendAuditEventRow, error)
	ListAuditEvents(context.Context, sqlcgen.ListAuditEventsParams) ([]sqlcgen.AuditEventsRedacted, error)
}

// AuditRepository maps restricted audit functions and the redacted view to signed domain values.
type AuditRepository struct {
	queries  auditQueries
	verifier audit.IntegrityVerifier
}

func newAuditRepository(queries auditQueries, verifier audit.IntegrityVerifier) *AuditRepository {
	return &AuditRepository{queries: queries, verifier: verifier}
}

// ReadHead obtains the authoritative chain position through the restricted SECURITY DEFINER function.
func (repository *AuditRepository) ReadHead(ctx context.Context, chainID audit.ChainID) (audit.Head, error) {
	if !chainID.Valid() {
		return audit.Head{}, audit.ErrInvalidInput
	}
	row, err := repository.queries.ReadAuditHead(ctx, sqlcgen.ReadAuditHeadParams{ChainID: string(chainID)})
	if err != nil {
		return audit.Head{}, mapAuditQueryError(ctx, err, audit.ErrNotFound)
	}
	if row.Sequence < 0 || !row.UpdatedAt.Valid {
		return audit.Head{}, audit.ErrIntegrity
	}
	headHash, err := audit.NewHash(row.HeadHash)
	if err != nil {
		return audit.Head{}, audit.ErrIntegrity
	}
	head, err := audit.RestoreHead(audit.HeadSnapshot{
		ChainID: chainID, Sequence: uint64(row.Sequence), Hash: headHash, UpdatedAt: row.UpdatedAt.Time,
	})
	if err != nil {
		return audit.Head{}, audit.ErrIntegrity
	}
	return head, nil
}

// AppendEvent compares the supplied trusted head, appends exactly one signed event, and validates the database result.
func (repository *AuditRepository) AppendEvent(ctx context.Context, request audit.AppendRequest) (audit.Head, error) {
	expected := request.ExpectedHead.Snapshot()
	event := request.Event.Snapshot()
	parsed, parseErr := audit.ParseSignedEvent(event.CanonicalEvent, event.EventHash.Bytes(), event.Signature)
	canonicalHash := sha256.Sum256(event.CanonicalEvent)
	if !expected.ChainID.Valid() || expected.UpdatedAt.IsZero() || expected.Sequence >= math.MaxInt64 ||
		event.Event.ChainID != expected.ChainID || event.Event.Sequence != expected.Sequence+1 ||
		event.Event.PreviousHash != expected.Hash || event.Event.OccurredAt.Before(expected.UpdatedAt) ||
		event.Event.SigningKeyVersion == 0 || event.Event.SigningKeyVersion > math.MaxInt32 {
		return audit.Head{}, audit.ErrInvalidInput
	}
	if parseErr != nil || audit.Hash(canonicalHash) != event.EventHash || !sameSignedEventSnapshot(parsed.Snapshot(), event) ||
		repository.verifier == nil || repository.verifier.Verify(request.Event) != nil {
		return audit.Head{}, audit.ErrIntegrity
	}
	row, err := repository.queries.AppendAuditEvent(ctx, sqlcgen.AppendAuditEventParams{
		ChainID:              string(expected.ChainID),
		ExpectedPreviousHash: expected.Hash.Bytes(),
		EventID:              uuidToPG(event.Event.EventID),
		CanonicalEvent:       event.CanonicalEvent,
		Signature:            event.Signature,
		SigningKeyVersion:    int32(event.Event.SigningKeyVersion),
		CreatedAt:            timeToPG(event.Event.OccurredAt),
	})
	if err != nil {
		return audit.Head{}, mapAuditQueryError(ctx, err, audit.ErrRepositoryUnavailable)
	}
	if row.AppendedSequence < 0 || uint64(row.AppendedSequence) != event.Event.Sequence ||
		!bytes.Equal(row.AppendedHash, event.EventHash.Bytes()) {
		return audit.Head{}, audit.ErrIntegrity
	}
	next, err := request.Event.NextHead()
	if err != nil {
		return audit.Head{}, audit.ErrIntegrity
	}
	return next, nil
}

func sameSignedEventSnapshot(left, right audit.SignedEventSnapshot) bool {
	return left.Event.SchemaVersion == right.Event.SchemaVersion && left.Event.ChainID == right.Event.ChainID &&
		left.Event.EventID == right.Event.EventID && left.Event.Sequence == right.Event.Sequence &&
		left.Event.PreviousHash == right.Event.PreviousHash && left.Event.RequestID == right.Event.RequestID &&
		left.Event.OccurredAt.Equal(right.Event.OccurredAt) && left.Event.Actor == right.Event.Actor &&
		left.Event.Target == right.Event.Target && left.Event.Action == right.Event.Action &&
		left.Event.ReasonCode == right.Event.ReasonCode && bytes.Equal(left.Event.DetailDigest, right.Event.DetailDigest) &&
		left.Event.SigningKeyVersion == right.Event.SigningKeyVersion && bytes.Equal(left.CanonicalEvent, right.CanonicalEvent) &&
		left.EventHash == right.EventHash && bytes.Equal(left.Signature, right.Signature)
}

// List restores deterministic canonical envelopes and rejects disagreement with redundant database columns.
func (repository *AuditRepository) List(ctx context.Context, request audit.ListRequest) ([]audit.SignedEvent, error) {
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
	events := make([]audit.SignedEvent, 0, len(rows))
	for _, row := range rows {
		event, parseErr := auditEventFromRow(row)
		if parseErr != nil || repository.verifier == nil || repository.verifier.Verify(event) != nil {
			return nil, audit.ErrIntegrity
		}
		events = append(events, event)
	}
	return events, nil
}

func auditEventFromRow(row sqlcgen.AuditEventsRedacted) (audit.SignedEvent, error) {
	if row.Sequence <= 0 || !row.EventID.Valid || row.SigningKeyVersion <= 0 || !row.CreatedAt.Valid {
		return audit.SignedEvent{}, audit.ErrIntegrity
	}
	event, err := audit.ParseSignedEvent(row.CanonicalEvent, row.EventHash, row.Signature)
	if err != nil {
		return audit.SignedEvent{}, audit.ErrIntegrity
	}
	snapshot := event.Snapshot()
	if string(snapshot.Event.ChainID) != row.ChainID || snapshot.Event.Sequence != uint64(row.Sequence) ||
		snapshot.Event.EventID != row.EventID.Bytes || !bytes.Equal(snapshot.Event.PreviousHash.Bytes(), row.PreviousHash) ||
		snapshot.Event.SigningKeyVersion != uint32(row.SigningKeyVersion) ||
		!snapshot.Event.OccurredAt.Equal(row.CreatedAt.Time.UTC()) {
		return audit.SignedEvent{}, audit.ErrIntegrity
	}
	return event, nil
}

func mapAuditQueryError(ctx context.Context, err, noRowsError error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == auditHeadConflictSQLState {
		return audit.ErrHeadConflict
	}
	return audit.ErrRepositoryUnavailable
}

var _ audit.Repository = (*AuditRepository)(nil)
