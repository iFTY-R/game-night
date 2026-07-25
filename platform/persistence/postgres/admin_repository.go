package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type adminAccountQueries interface {
	GetSingletonAdminForUpdate(context.Context) (sqlcgen.AdminAccount, error)
	BootstrapAdminPasswordCAS(context.Context, sqlcgen.BootstrapAdminPasswordCASParams) (sqlcgen.BootstrapAdminPasswordCASRow, error)
	UpdateAdminPasswordCAS(context.Context, sqlcgen.UpdateAdminPasswordCASParams) (sqlcgen.UpdateAdminPasswordCASRow, error)
	TransitionAdminStatusCAS(context.Context, sqlcgen.TransitionAdminStatusCASParams) (sqlcgen.TransitionAdminStatusCASRow, error)
	RecordAdminMFAChangeCAS(context.Context, sqlcgen.RecordAdminMFAChangeCASParams) (sqlcgen.RecordAdminMFAChangeCASRow, error)
}

type adminAccountRepository struct{ queries adminAccountQueries }

func (repository *adminAccountRepository) GetForUpdate(ctx context.Context) (adminDomain.Account, error) {
	row, err := repository.queries.GetSingletonAdminForUpdate(ctx)
	if err != nil {
		return adminDomain.Account{}, mapAdminQueryError(err, adminDomain.ErrUnavailable)
	}
	return adminAccountFromRow(row)
}

func (repository *adminAccountRepository) BootstrapPasswordCAS(ctx context.Context, current adminDomain.Account, hash, algorithm, parameters string, at time.Time) (adminDomain.Account, error) {
	if hash == "" || algorithm == "" || parameters == "" {
		return adminDomain.Account{}, adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.BootstrapAdminPasswordCAS(ctx, sqlcgen.BootstrapAdminPasswordCASParams{
		PasswordHash: pgtype.Text{String: hash, Valid: true}, PasswordAlgorithm: pgtype.Text{String: algorithm, Valid: true}, PasswordParameters: pgtype.Text{String: parameters, Valid: true},
		ChangedAt: timeToPG(at), ExpectedAdminVersion: current.Snapshot().AdminVersion,
	})
	if err != nil {
		return adminDomain.Account{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return accountAfterCAS(current, row.Status, row.PasswordVersion, row.AdminVersion, row.UpdatedAt.Time, &adminDomain.PasswordRecord{Hash: hash, Algorithm: algorithm, Parameters: parameters})
}

func (repository *adminAccountRepository) UpdatePasswordCAS(ctx context.Context, current adminDomain.Account, hash, algorithm, parameters string, at time.Time) (adminDomain.Account, error) {
	if hash == "" || algorithm == "" || parameters == "" {
		return adminDomain.Account{}, adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.UpdateAdminPasswordCAS(ctx, sqlcgen.UpdateAdminPasswordCASParams{
		PasswordHash: pgtype.Text{String: hash, Valid: true}, PasswordAlgorithm: pgtype.Text{String: algorithm, Valid: true}, PasswordParameters: pgtype.Text{String: parameters, Valid: true},
		ChangedAt: timeToPG(at), AdminID: uuidToPG(current.Snapshot().ID), ExpectedStatus: string(current.Snapshot().Status), ExpectedPasswordVersion: current.Snapshot().PasswordVersion, ExpectedAdminVersion: current.Snapshot().AdminVersion,
	})
	if err != nil {
		return adminDomain.Account{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return accountAfterCAS(current, row.Status, row.PasswordVersion, row.AdminVersion, row.UpdatedAt.Time, &adminDomain.PasswordRecord{Hash: hash, Algorithm: algorithm, Parameters: parameters})
}

func (repository *adminAccountRepository) TransitionStatusCAS(ctx context.Context, current adminDomain.Account, next adminDomain.AccountStatus, at time.Time) (adminDomain.Account, error) {
	if _, err := current.Transition(next, at); err != nil {
		return adminDomain.Account{}, err
	}
	row, err := repository.queries.TransitionAdminStatusCAS(ctx, sqlcgen.TransitionAdminStatusCASParams{
		NextStatus: string(next), ChangedAt: timeToPG(at), AdminID: uuidToPG(current.Snapshot().ID), ExpectedStatus: string(current.Snapshot().Status), ExpectedAdminVersion: current.Snapshot().AdminVersion,
	})
	if err != nil {
		return adminDomain.Account{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return accountAfterCAS(current, row.Status, row.PasswordVersion, row.AdminVersion, row.UpdatedAt.Time, nil)
}

func (repository *adminAccountRepository) RecordMFAChangeCAS(ctx context.Context, current adminDomain.Account, at time.Time) (adminDomain.Account, error) {
	next, err := current.RecordMFAChange(at)
	if err != nil {
		return adminDomain.Account{}, err
	}
	row, err := repository.queries.RecordAdminMFAChangeCAS(ctx, sqlcgen.RecordAdminMFAChangeCASParams{
		ChangedAt: timeToPG(next.Snapshot().UpdatedAt), AdminID: uuidToPG(current.Snapshot().ID), ExpectedAdminVersion: current.Snapshot().AdminVersion,
	})
	if err != nil {
		return adminDomain.Account{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return accountAfterCAS(current, row.Status, row.PasswordVersion, row.AdminVersion, row.UpdatedAt.Time, nil)
}

type adminEnrollmentQueries interface {
	CreatePendingAdminTotpEnrollment(context.Context, sqlcgen.CreatePendingAdminTotpEnrollmentParams) (sqlcgen.CreatePendingAdminTotpEnrollmentRow, error)
	GetPendingAdminTotpEnrollmentForUpdate(context.Context, sqlcgen.GetPendingAdminTotpEnrollmentForUpdateParams) (sqlcgen.GetPendingAdminTotpEnrollmentForUpdateRow, error)
	GetActiveAdminTotpEnrollmentForUpdate(context.Context, sqlcgen.GetActiveAdminTotpEnrollmentForUpdateParams) (sqlcgen.GetActiveAdminTotpEnrollmentForUpdateRow, error)
	ActivatePendingAdminTotpEnrollmentCAS(context.Context, sqlcgen.ActivatePendingAdminTotpEnrollmentCASParams) (sqlcgen.ActivatePendingAdminTotpEnrollmentCASRow, error)
	AcceptAdminTotpReplayCAS(context.Context, sqlcgen.AcceptAdminTotpReplayCASParams) (sqlcgen.AcceptAdminTotpReplayCASRow, error)
	DisableActiveAdminTotpEnrollmentCAS(context.Context, sqlcgen.DisableActiveAdminTotpEnrollmentCASParams) (sqlcgen.DisableActiveAdminTotpEnrollmentCASRow, error)
}

// adminEnrollmentQueryRow normalizes the identical column lists emitted by the enrollment queries before domain mapping.
type adminEnrollmentQueryRow struct {
	EnrollmentID      pgtype.UUID        `json:"enrollment_id"`
	AdminID           pgtype.UUID        `json:"admin_id"`
	Ciphertext        []byte             `json:"ciphertext"`
	Nonce             []byte             `json:"nonce"`
	KeyVersion        int32              `json:"key_version"`
	Status            string             `json:"status"`
	AdminVersion      int64              `json:"admin_version"`
	EnrollmentVersion int64              `json:"enrollment_version"`
	ReplayFloor       pgtype.Int8        `json:"replay_floor"`
	OperationID       string             `json:"operation_id"`
	CreatedAt         pgtype.Timestamptz `json:"created_at"`
	ExpiresAt         pgtype.Timestamptz `json:"expires_at"`
	ActivatedAt       pgtype.Timestamptz `json:"activated_at"`
	DisabledAt        pgtype.Timestamptz `json:"disabled_at"`
}

type adminEnrollmentRepository struct{ queries adminEnrollmentQueries }

func (repository *adminEnrollmentRepository) CreatePending(ctx context.Context, enrollment adminDomain.Enrollment) (adminDomain.Enrollment, error) {
	snapshot := enrollment.Snapshot()
	row, err := repository.queries.CreatePendingAdminTotpEnrollment(ctx, sqlcgen.CreatePendingAdminTotpEnrollmentParams{
		EnrollmentID: uuidToPG(snapshot.ID), AdminID: uuidToPG(snapshot.AdminID), Ciphertext: snapshot.Ciphertext, Nonce: snapshot.Nonce,
		KeyVersion: int32(snapshot.KeyVersion), AdminVersion: snapshot.AdminVersion, OperationID: snapshot.OperationID, CreatedAt: timeToPG(snapshot.CreatedAt), ExpiresAt: timeToPG(snapshot.ExpiresAt),
	})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return adminEnrollmentFromQueryRow(adminEnrollmentQueryRow(row))
}

func (repository *adminEnrollmentRepository) GetPendingForUpdate(ctx context.Context, adminID uuid.UUID) (adminDomain.Enrollment, error) {
	row, err := repository.queries.GetPendingAdminTotpEnrollmentForUpdate(ctx, sqlcgen.GetPendingAdminTotpEnrollmentForUpdateParams{AdminID: uuidToPG(adminID)})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrNotFound)
	}
	return adminEnrollmentFromQueryRow(adminEnrollmentQueryRow(row))
}

func (repository *adminEnrollmentRepository) GetActiveForUpdate(ctx context.Context, adminID uuid.UUID) (adminDomain.Enrollment, error) {
	row, err := repository.queries.GetActiveAdminTotpEnrollmentForUpdate(ctx, sqlcgen.GetActiveAdminTotpEnrollmentForUpdateParams{AdminID: uuidToPG(adminID)})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrNotFound)
	}
	return adminEnrollmentFromQueryRow(adminEnrollmentQueryRow(row))
}

func (repository *adminEnrollmentRepository) ActivateCAS(ctx context.Context, current adminDomain.Enrollment, step, nextAdminVersion int64, at time.Time) (adminDomain.Enrollment, error) {
	next, err := current.Activate(step, nextAdminVersion, at)
	if err != nil {
		return adminDomain.Enrollment{}, err
	}
	snapshot := current.Snapshot()
	row, err := repository.queries.ActivatePendingAdminTotpEnrollmentCAS(ctx, sqlcgen.ActivatePendingAdminTotpEnrollmentCASParams{
		ActivatedAt: timeToPG(next.Snapshot().ActivatedAt), ReplayFloor: pgtype.Int8{Int64: step, Valid: true}, NextAdminVersion: nextAdminVersion,
		AdminID: uuidToPG(snapshot.AdminID), EnrollmentID: uuidToPG(snapshot.ID), ExpectedAdminVersion: snapshot.AdminVersion, ExpectedEnrollmentVersion: snapshot.EnrollmentVersion,
	})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return adminEnrollmentFromQueryRow(adminEnrollmentQueryRow(row))
}

func (repository *adminEnrollmentRepository) AcceptTOTPCAS(ctx context.Context, current adminDomain.Enrollment, step int64, at time.Time) (adminDomain.Enrollment, error) {
	_, err := current.AcceptTOTP(step, at)
	if err != nil {
		return adminDomain.Enrollment{}, err
	}
	snapshot := current.Snapshot()
	row, err := repository.queries.AcceptAdminTotpReplayCAS(ctx, sqlcgen.AcceptAdminTotpReplayCASParams{
		ReplayFloor: pgtype.Int8{Int64: step, Valid: true}, AdminID: uuidToPG(snapshot.AdminID), EnrollmentID: uuidToPG(snapshot.ID),
		ExpectedAdminVersion: snapshot.AdminVersion, ExpectedEnrollmentVersion: snapshot.EnrollmentVersion,
	})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return adminEnrollmentFromQueryRow(adminEnrollmentQueryRow(row))
}

func (repository *adminEnrollmentRepository) DisableCAS(ctx context.Context, current adminDomain.Enrollment, nextAdminVersion int64, at time.Time) (adminDomain.Enrollment, error) {
	next, err := current.Disable(nextAdminVersion, at)
	if err != nil {
		return adminDomain.Enrollment{}, err
	}
	snapshot := current.Snapshot()
	row, err := repository.queries.DisableActiveAdminTotpEnrollmentCAS(ctx, sqlcgen.DisableActiveAdminTotpEnrollmentCASParams{
		DisabledAt: timeToPG(next.Snapshot().DisabledAt), NextAdminVersion: nextAdminVersion, AdminID: uuidToPG(snapshot.AdminID),
		EnrollmentID: uuidToPG(snapshot.ID), ExpectedAdminVersion: snapshot.AdminVersion, ExpectedEnrollmentVersion: snapshot.EnrollmentVersion,
	})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return adminEnrollmentFromQueryRow(adminEnrollmentQueryRow(row))
}

type adminSessionQueries interface {
	CreateAdminSession(context.Context, sqlcgen.CreateAdminSessionParams) (sqlcgen.CreateAdminSessionRow, error)
	GetAdminSessionForUpdate(context.Context, sqlcgen.GetAdminSessionForUpdateParams) (sqlcgen.GetAdminSessionForUpdateRow, error)
	GetAdminSessionByIDForUpdate(context.Context, sqlcgen.GetAdminSessionByIDForUpdateParams) (sqlcgen.GetAdminSessionByIDForUpdateRow, error)
	ListActiveAdminSessions(context.Context, sqlcgen.ListActiveAdminSessionsParams) ([]sqlcgen.ListActiveAdminSessionsRow, error)
	TouchAdminSessionCAS(context.Context, sqlcgen.TouchAdminSessionCASParams) (sqlcgen.TouchAdminSessionCASRow, error)
	RevokeAdminSessionCAS(context.Context, sqlcgen.RevokeAdminSessionCASParams) (sqlcgen.RevokeAdminSessionCASRow, error)
	RevokeOtherActiveAdminSessionsCAS(context.Context, sqlcgen.RevokeOtherActiveAdminSessionsCASParams) ([]sqlcgen.RevokeOtherActiveAdminSessionsCASRow, error)
}

type adminSessionRepository struct{ queries adminSessionQueries }

func (repository *adminSessionRepository) Insert(ctx context.Context, session adminDomain.Session) error {
	snapshot := session.Snapshot()
	if snapshot.SecretMAC.KeyVersion > 1<<31-1 {
		return adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminSession(ctx, sqlcgen.CreateAdminSessionParams{
		SessionID: uuidToPG(snapshot.ID), AdminID: uuidToPG(snapshot.AdminID), Selector: snapshot.Selector, SecretHash: snapshot.SecretMAC.Value,
		SecretKeyVersion: int32(snapshot.SecretMAC.KeyVersion), CsrfHash: snapshot.CSRFHash.Value, Kind: string(snapshot.Kind), AdminVersion: snapshot.AdminVersion,
		PasswordVersion: snapshot.PasswordVersion, SessionVersion: snapshot.SessionVersion, ClientIp: snapshot.ClientIP, UserAgent: snapshot.UserAgent,
		MaxAttempts: int32(snapshot.MaxAttempts), CreatedAt: timeToPG(snapshot.CreatedAt), IdleExpiresAt: timeToPG(snapshot.IdleExpiresAt), AbsoluteExpiresAt: timeToPG(snapshot.AbsoluteExpiresAt),
	})
	if err != nil {
		return mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	if !row.SessionID.Valid {
		return adminDomain.ErrIntegrity
	}
	return nil
}

func (repository *adminSessionRepository) GetForUpdate(ctx context.Context, selector string) (adminDomain.Session, error) {
	if selector == "" {
		return adminDomain.Session{}, adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminSessionForUpdate(ctx, sqlcgen.GetAdminSessionForUpdateParams{Selector: selector})
	if err != nil {
		return adminDomain.Session{}, mapAdminQueryError(err, adminDomain.ErrNotFound)
	}
	return adminSessionFromGetForUpdateRow(row)
}

func (repository *adminSessionRepository) GetByIDForUpdate(ctx context.Context, sessionID uuid.UUID) (adminDomain.Session, error) {
	if sessionID == uuid.Nil {
		return adminDomain.Session{}, adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminSessionByIDForUpdate(ctx, sqlcgen.GetAdminSessionByIDForUpdateParams{SessionID: uuidToPG(sessionID)})
	if err != nil {
		return adminDomain.Session{}, mapAdminQueryError(err, adminDomain.ErrNotFound)
	}
	return adminSessionFromByIDRow(row)
}

func (repository *adminSessionRepository) ListActiveForAdmin(ctx context.Context, adminID uuid.UUID, at time.Time) ([]adminDomain.Session, error) {
	if adminID == uuid.Nil {
		return nil, adminDomain.ErrInvalidInput
	}
	rows, err := repository.queries.ListActiveAdminSessions(ctx, sqlcgen.ListActiveAdminSessionsParams{AdminID: uuidToPG(adminID), ActiveAt: timeToPG(at)})
	if err != nil {
		return nil, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	sessions := make([]adminDomain.Session, 0, len(rows))
	for _, row := range rows {
		session, mapErr := adminSessionFromListRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (repository *adminSessionRepository) TouchCAS(ctx context.Context, current adminDomain.Session, at time.Time, idleTTL time.Duration) (adminDomain.Session, error) {
	next, err := current.Touch(at, idleTTL)
	if err != nil {
		return adminDomain.Session{}, err
	}
	snapshot := current.Snapshot()
	row, err := repository.queries.TouchAdminSessionCAS(ctx, sqlcgen.TouchAdminSessionCASParams{
		SeenAt: timeToPG(next.Snapshot().LastSeenAt), IdleExpiresAt: timeToPG(next.Snapshot().IdleExpiresAt), SessionID: uuidToPG(snapshot.ID),
		ExpectedAdminVersion: snapshot.AdminVersion, ExpectedPasswordVersion: snapshot.PasswordVersion, ExpectedSessionVersion: snapshot.SessionVersion,
	})
	if err != nil {
		return adminDomain.Session{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	updated := snapshot
	updated.SessionVersion = row.SessionVersion
	updated.LastSeenAt, updated.IdleExpiresAt, updated.AbsoluteExpiresAt = row.LastSeenAt.Time, row.IdleExpiresAt.Time, row.AbsoluteExpiresAt.Time
	return adminDomain.RestoreSession(updated)
}

func (repository *adminSessionRepository) RevokeCAS(ctx context.Context, current adminDomain.Session, reason string, at time.Time) (adminDomain.Session, error) {
	next, err := current.Revoke(reason, at)
	if err != nil {
		return adminDomain.Session{}, err
	}
	row, err := repository.queries.RevokeAdminSessionCAS(ctx, sqlcgen.RevokeAdminSessionCASParams{
		RevokedAt: timeToPG(next.Snapshot().RevokedAt), RevokeReason: pgtype.Text{String: next.Snapshot().RevokeReason, Valid: true},
		SessionID: uuidToPG(current.Snapshot().ID), AdminID: uuidToPG(current.Snapshot().AdminID), ExpectedSessionVersion: current.Snapshot().SessionVersion,
	})
	if err != nil {
		return adminDomain.Session{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	updated := current.Snapshot()
	updated.SessionVersion, updated.RevokedAt, updated.RevokeReason = row.SessionVersion, row.RevokedAt.Time, row.RevokeReason.String
	return adminDomain.RestoreSession(updated)
}

func (repository *adminSessionRepository) RevokeOtherActiveCAS(ctx context.Context, adminID, preservedSessionID uuid.UUID, expectedAdminVersion, expectedPreservedSessionVersion int64, reason string, at time.Time) ([]adminDomain.Session, error) {
	if adminID == uuid.Nil || preservedSessionID == uuid.Nil || expectedAdminVersion <= 0 || expectedPreservedSessionVersion <= 0 || strings.TrimSpace(reason) == "" {
		return nil, adminDomain.ErrInvalidInput
	}
	rows, err := repository.queries.RevokeOtherActiveAdminSessionsCAS(ctx, sqlcgen.RevokeOtherActiveAdminSessionsCASParams{
		RevokedAt: timeToPG(at), RevokeReason: pgtype.Text{String: strings.TrimSpace(reason), Valid: true}, AdminID: uuidToPG(adminID),
		PreservedSessionID: uuidToPG(preservedSessionID), ExpectedAdminVersion: expectedAdminVersion, ExpectedPreservedSessionVersion: expectedPreservedSessionVersion,
	})
	if err != nil {
		return nil, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	revoked := make([]adminDomain.Session, 0, len(rows))
	for _, row := range rows {
		session, mapErr := adminSessionFromRevokeOtherRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		revoked = append(revoked, session)
	}
	return revoked, nil
}

type adminElevationQueries interface {
	UpsertAdminElevationGrant(context.Context, sqlcgen.UpsertAdminElevationGrantParams) (sqlcgen.AdminElevationGrant, error)
	GetAdminElevationGrantForSessionScope(context.Context, sqlcgen.GetAdminElevationGrantForSessionScopeParams) (sqlcgen.AdminElevationGrant, error)
	ListLiveAdminElevationGrantsForSessions(context.Context, sqlcgen.ListLiveAdminElevationGrantsForSessionsParams) ([]sqlcgen.AdminElevationGrant, error)
	RevokeAdminElevationGrantCAS(context.Context, sqlcgen.RevokeAdminElevationGrantCASParams) (sqlcgen.AdminElevationGrant, error)
}

type adminElevationRepository struct{ queries adminElevationQueries }

func (repository *adminElevationRepository) UpsertLive(ctx context.Context, elevation adminDomain.Elevation) (adminDomain.Elevation, error) {
	snapshot := elevation.Snapshot()
	row, err := repository.queries.UpsertAdminElevationGrant(ctx, sqlcgen.UpsertAdminElevationGrantParams{
		AdminID: uuidToPG(snapshot.AdminID), SessionID: uuidToPG(snapshot.SessionID), Scope: string(snapshot.Scope),
		AdminVersion: snapshot.AdminVersion, PasswordVersion: snapshot.PasswordVersion, SessionVersion: snapshot.SessionVersion,
		EnrollmentVersion: snapshot.EnrollmentVersion, GrantedAt: timeToPG(snapshot.GrantedAt), ExpiresAt: timeToPG(snapshot.ExpiresAt),
		RevokedAt: timeToPGNullable(snapshot.RevokedAt),
	})
	if err != nil {
		return adminDomain.Elevation{}, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	return adminElevationFromRow(row)
}

func (repository *adminElevationRepository) GetForSessionScope(ctx context.Context, sessionID uuid.UUID, scope adminDomain.ElevationScope, at time.Time) (adminDomain.Elevation, error) {
	if sessionID == uuid.Nil || !scope.Valid() {
		return adminDomain.Elevation{}, adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminElevationGrantForSessionScope(ctx, sqlcgen.GetAdminElevationGrantForSessionScopeParams{
		SessionID: uuidToPG(sessionID), Scope: string(scope),
	})
	if err != nil {
		return adminDomain.Elevation{}, mapAdminQueryError(err, adminDomain.ErrElevationDenied)
	}
	elevation, mapErr := adminElevationFromRow(row)
	if mapErr != nil {
		return adminDomain.Elevation{}, mapErr
	}
	snapshot := elevation.Snapshot()
	now := at.UTC()
	if !snapshot.RevokedAt.IsZero() {
		return adminDomain.Elevation{}, adminDomain.ErrElevationDenied
	}
	if !now.IsZero() && !now.Before(snapshot.ExpiresAt) {
		return adminDomain.Elevation{}, adminDomain.ErrElevationExpired
	}
	return elevation, nil
}

func (repository *adminElevationRepository) ListLiveForSessions(ctx context.Context, adminID uuid.UUID, sessionIDs []uuid.UUID, at time.Time) ([]adminDomain.Elevation, error) {
	if adminID == uuid.Nil || len(sessionIDs) == 0 || at.IsZero() {
		return nil, adminDomain.ErrInvalidInput
	}
	ids := make([]pgtype.UUID, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		if sessionID == uuid.Nil {
			return nil, adminDomain.ErrInvalidInput
		}
		ids[index] = uuidToPG(sessionID)
	}
	rows, err := repository.queries.ListLiveAdminElevationGrantsForSessions(ctx, sqlcgen.ListLiveAdminElevationGrantsForSessionsParams{
		AdminID: uuidToPG(adminID), SessionIds: ids, ActiveAt: timeToPG(at),
	})
	if err != nil {
		return nil, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	elevations := make([]adminDomain.Elevation, 0, len(rows))
	for _, row := range rows {
		elevation, mapErr := adminElevationFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		elevations = append(elevations, elevation)
	}
	return elevations, nil
}

func (repository *adminElevationRepository) RevokeCAS(ctx context.Context, current adminDomain.Elevation, at time.Time) (adminDomain.Elevation, error) {
	next, err := current.Revoke(at)
	if err != nil {
		return adminDomain.Elevation{}, err
	}
	snapshot := current.Snapshot()
	row, err := repository.queries.RevokeAdminElevationGrantCAS(ctx, sqlcgen.RevokeAdminElevationGrantCASParams{
		RevokedAt: timeToPG(next.Snapshot().RevokedAt), SessionID: uuidToPG(snapshot.SessionID), Scope: string(snapshot.Scope),
		ExpectedAdminVersion: snapshot.AdminVersion, ExpectedPasswordVersion: snapshot.PasswordVersion,
		ExpectedSessionVersion: snapshot.SessionVersion, ExpectedEnrollmentVersion: snapshot.EnrollmentVersion,
	})
	if err != nil {
		return adminDomain.Elevation{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return adminElevationFromRow(row)
}

type adminCommandReceiptQueries interface {
	CreateAdminCommandReceipt(context.Context, sqlcgen.CreateAdminCommandReceiptParams) (sqlcgen.AdminCommandReceipt, error)
	GetAdminCommandReceipt(context.Context, sqlcgen.GetAdminCommandReceiptParams) (sqlcgen.AdminCommandReceipt, error)
}

type adminCommandReceiptRepository struct{ queries adminCommandReceiptQueries }

func (repository *adminCommandReceiptRepository) Save(ctx context.Context, receipt adminDomain.CommandReceipt) (adminDomain.CommandReceipt, error) {
	if !receipt.OperationID.Valid() || receipt.AdminID == uuid.Nil || receipt.Command == "" || strings.TrimSpace(receipt.TargetType) == "" ||
		strings.TrimSpace(receipt.TargetID) == "" || receipt.ResultAdminVersion <= 0 || receipt.ResultPasswordVersion < 0 ||
		receipt.ResultSessionVersion < 0 || receipt.ResultEnrollmentVersion < 0 || receipt.AuditEventID == uuid.Nil || receipt.CreatedAt.IsZero() {
		return adminDomain.CommandReceipt{}, adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminCommandReceipt(ctx, sqlcgen.CreateAdminCommandReceiptParams{
		AdminID: uuidToPG(receipt.AdminID), OperationID: receipt.OperationID.Value(), RequestDigest: receipt.RequestDigest.Bytes(), Command: receipt.Command,
		TargetType: strings.TrimSpace(receipt.TargetType), TargetID: strings.TrimSpace(receipt.TargetID), ResultAdminVersion: receipt.ResultAdminVersion,
		ResultPasswordVersion: receipt.ResultPasswordVersion, ResultSessionVersion: receipt.ResultSessionVersion,
		ResultEnrollmentVersion: receipt.ResultEnrollmentVersion, AuditEventID: uuidToPG(receipt.AuditEventID), CreatedAt: timeToPG(receipt.CreatedAt),
	})
	if err == nil {
		return adminCommandReceiptFromRow(row)
	}
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "23505" {
		return adminDomain.CommandReceipt{}, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	existing, getErr := repository.Get(ctx, receipt.AdminID, receipt.OperationID)
	if getErr != nil {
		return adminDomain.CommandReceipt{}, getErr
	}
	if existing.RequestDigest != receipt.RequestDigest || existing.Command != receipt.Command ||
		existing.TargetType != strings.TrimSpace(receipt.TargetType) || existing.TargetID != strings.TrimSpace(receipt.TargetID) {
		return adminDomain.CommandReceipt{}, adminDomain.ErrIdempotencyConflict
	}
	return existing, nil
}

func (repository *adminCommandReceiptRepository) Get(ctx context.Context, adminID uuid.UUID, operationID idempotency.OperationID) (adminDomain.CommandReceipt, error) {
	if adminID == uuid.Nil || !operationID.Valid() {
		return adminDomain.CommandReceipt{}, adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminCommandReceipt(ctx, sqlcgen.GetAdminCommandReceiptParams{
		AdminID: uuidToPG(adminID), OperationID: operationID.Value(),
	})
	if err != nil {
		return adminDomain.CommandReceipt{}, mapAdminQueryError(err, adminDomain.ErrNotFound)
	}
	return adminCommandReceiptFromRow(row)
}

type adminRecoveryCodeQueries interface {
	CreateAdminRecoveryCode(context.Context, sqlcgen.CreateAdminRecoveryCodeParams) (sqlcgen.AdminRecoveryCode, error)
	GetAdminRecoveryCodeForUpdate(context.Context, sqlcgen.GetAdminRecoveryCodeForUpdateParams) (sqlcgen.AdminRecoveryCode, error)
	GetAdminRecoveryCodeSetState(context.Context, sqlcgen.GetAdminRecoveryCodeSetStateParams) (sqlcgen.GetAdminRecoveryCodeSetStateRow, error)
	ConsumeAdminRecoveryCodeCAS(context.Context, sqlcgen.ConsumeAdminRecoveryCodeCASParams) (sqlcgen.ConsumeAdminRecoveryCodeCASRow, error)
	RevokeAdminRecoveryCodeSet(context.Context, sqlcgen.RevokeAdminRecoveryCodeSetParams) (int64, error)
	RevokeAllAdminRecoveryCodeSets(context.Context, sqlcgen.RevokeAllAdminRecoveryCodeSetsParams) (int64, error)
}

type adminRecoveryCodeRepository struct{ queries adminRecoveryCodeQueries }

func (repository *adminRecoveryCodeRepository) Insert(ctx context.Context, code adminDomain.RecoveryCode) error {
	snapshot := code.Snapshot()
	row, err := repository.queries.CreateAdminRecoveryCode(ctx, sqlcgen.CreateAdminRecoveryCodeParams{
		RecoveryCodeID: uuidToPG(snapshot.ID), AdminID: uuidToPG(snapshot.AdminID), Selector: snapshot.Selector, SecretHash: snapshot.SecretHash,
		SetVersion: snapshot.SetVersion, CreatedAt: timeToPG(snapshot.CreatedAt),
	})
	if err != nil {
		return mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	if !row.RecoveryCodeID.Valid {
		return adminDomain.ErrIntegrity
	}
	return nil
}

func (repository *adminRecoveryCodeRepository) FindActiveBySelector(ctx context.Context, selector string) (adminDomain.RecoveryCode, error) {
	row, err := repository.queries.GetAdminRecoveryCodeForUpdate(ctx, sqlcgen.GetAdminRecoveryCodeForUpdateParams{Selector: selector})
	if err != nil {
		return adminDomain.RecoveryCode{}, mapAdminQueryError(err, adminDomain.ErrNotFound)
	}
	return adminRecoveryCodeFromRow(row)
}

func (repository *adminRecoveryCodeRepository) GetActiveSetState(ctx context.Context, adminID uuid.UUID) (adminDomain.RecoveryCodeSetState, error) {
	if adminID == uuid.Nil {
		return adminDomain.RecoveryCodeSetState{}, adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminRecoveryCodeSetState(ctx, sqlcgen.GetAdminRecoveryCodeSetStateParams{AdminID: uuidToPG(adminID)})
	if err != nil {
		return adminDomain.RecoveryCodeSetState{}, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	if row.SetVersion < 0 || row.RemainingActive < 0 || row.RemainingActive > adminDomain.AdminRecoveryCodeCount ||
		(row.SetVersion == 0 && row.RemainingActive != 0) {
		return adminDomain.RecoveryCodeSetState{}, adminDomain.ErrIntegrity
	}
	return adminDomain.RecoveryCodeSetState{SetVersion: row.SetVersion, RemainingActive: row.RemainingActive}, nil
}

func (repository *adminRecoveryCodeRepository) ConsumeCAS(ctx context.Context, current adminDomain.RecoveryCode, at time.Time) (adminDomain.RecoveryCode, error) {
	snapshot := current.Snapshot()
	row, err := repository.queries.ConsumeAdminRecoveryCodeCAS(ctx, sqlcgen.ConsumeAdminRecoveryCodeCASParams{
		ConsumedAt: timeToPG(at), RecoveryCodeID: uuidToPG(snapshot.ID), AdminID: uuidToPG(snapshot.AdminID), ExpectedSetVersion: snapshot.SetVersion,
	})
	if err != nil {
		return adminDomain.RecoveryCode{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	updated := snapshot
	updated.Status, updated.ConsumedAt = adminDomain.RecoveryCodeStatus(row.Status), row.ConsumedAt.Time
	return adminDomain.RestoreRecoveryCode(updated)
}

func (repository *adminRecoveryCodeRepository) RevokeSet(ctx context.Context, adminID uuid.UUID, setVersion int64, at time.Time) (int64, error) {
	count, err := repository.queries.RevokeAdminRecoveryCodeSet(ctx, sqlcgen.RevokeAdminRecoveryCodeSetParams{
		RevokedAt: timeToPG(at), AdminID: uuidToPG(adminID), SetVersion: setVersion,
	})
	if err != nil {
		return 0, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	return count, nil
}

func (repository *adminRecoveryCodeRepository) RevokeAllSets(ctx context.Context, adminID uuid.UUID, at time.Time) (int64, error) {
	count, err := repository.queries.RevokeAllAdminRecoveryCodeSets(ctx, sqlcgen.RevokeAllAdminRecoveryCodeSetsParams{
		RevokedAt: timeToPG(at), AdminID: uuidToPG(adminID),
	})
	if err != nil {
		return 0, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	return count, nil
}

func adminEnrollmentFromQueryRow(row adminEnrollmentQueryRow) (adminDomain.Enrollment, error) {
	return adminEnrollmentFromRow(sqlcgen.AdminTotpEnrollment{
		EnrollmentID:      row.EnrollmentID,
		AdminID:           row.AdminID,
		Ciphertext:        row.Ciphertext,
		Nonce:             row.Nonce,
		KeyVersion:        row.KeyVersion,
		Status:            row.Status,
		AdminVersion:      row.AdminVersion,
		OperationID:       row.OperationID,
		CreatedAt:         row.CreatedAt,
		ExpiresAt:         row.ExpiresAt,
		ActivatedAt:       row.ActivatedAt,
		DisabledAt:        row.DisabledAt,
		EnrollmentVersion: row.EnrollmentVersion,
		ReplayFloor:       row.ReplayFloor,
	})
}

func adminSessionFromGetForUpdateRow(row sqlcgen.GetAdminSessionForUpdateRow) (adminDomain.Session, error) {
	return adminSessionFromRow(sqlcgen.AdminSession{
		SessionID:         row.SessionID,
		AdminID:           row.AdminID,
		Selector:          row.Selector,
		SecretHash:        row.SecretHash,
		SecretKeyVersion:  row.SecretKeyVersion,
		CsrfHash:          row.CsrfHash,
		Kind:              row.Kind,
		AdminVersion:      row.AdminVersion,
		PasswordVersion:   row.PasswordVersion,
		AttemptCount:      row.AttemptCount,
		MaxAttempts:       row.MaxAttempts,
		CreatedAt:         row.CreatedAt,
		LastSeenAt:        row.LastSeenAt,
		IdleExpiresAt:     row.IdleExpiresAt,
		AbsoluteExpiresAt: row.AbsoluteExpiresAt,
		RevokedAt:         row.RevokedAt,
		RevokeReason:      row.RevokeReason,
		SessionVersion:    row.SessionVersion,
		ClientIp:          row.ClientIp,
		UserAgent:         row.UserAgent,
	})
}

func adminSessionFromByIDRow(row sqlcgen.GetAdminSessionByIDForUpdateRow) (adminDomain.Session, error) {
	return adminSessionFromRow(sqlcgen.AdminSession{
		SessionID:         row.SessionID,
		AdminID:           row.AdminID,
		Selector:          row.Selector,
		SecretHash:        row.SecretHash,
		SecretKeyVersion:  row.SecretKeyVersion,
		CsrfHash:          row.CsrfHash,
		Kind:              row.Kind,
		AdminVersion:      row.AdminVersion,
		PasswordVersion:   row.PasswordVersion,
		AttemptCount:      row.AttemptCount,
		MaxAttempts:       row.MaxAttempts,
		CreatedAt:         row.CreatedAt,
		LastSeenAt:        row.LastSeenAt,
		IdleExpiresAt:     row.IdleExpiresAt,
		AbsoluteExpiresAt: row.AbsoluteExpiresAt,
		RevokedAt:         row.RevokedAt,
		RevokeReason:      row.RevokeReason,
		SessionVersion:    row.SessionVersion,
		ClientIp:          row.ClientIp,
		UserAgent:         row.UserAgent,
	})
}

func adminSessionFromListRow(row sqlcgen.ListActiveAdminSessionsRow) (adminDomain.Session, error) {
	return adminSessionFromRow(sqlcgen.AdminSession{
		SessionID:         row.SessionID,
		AdminID:           row.AdminID,
		Selector:          row.Selector,
		SecretHash:        row.SecretHash,
		SecretKeyVersion:  row.SecretKeyVersion,
		CsrfHash:          row.CsrfHash,
		Kind:              row.Kind,
		AdminVersion:      row.AdminVersion,
		PasswordVersion:   row.PasswordVersion,
		AttemptCount:      row.AttemptCount,
		MaxAttempts:       row.MaxAttempts,
		CreatedAt:         row.CreatedAt,
		LastSeenAt:        row.LastSeenAt,
		IdleExpiresAt:     row.IdleExpiresAt,
		AbsoluteExpiresAt: row.AbsoluteExpiresAt,
		RevokedAt:         row.RevokedAt,
		RevokeReason:      row.RevokeReason,
		SessionVersion:    row.SessionVersion,
		ClientIp:          row.ClientIp,
		UserAgent:         row.UserAgent,
	})
}

func adminSessionFromRevokeOtherRow(row sqlcgen.RevokeOtherActiveAdminSessionsCASRow) (adminDomain.Session, error) {
	return adminSessionFromRow(sqlcgen.AdminSession{
		SessionID:         row.SessionID,
		AdminID:           row.AdminID,
		Selector:          row.Selector,
		SecretHash:        row.SecretHash,
		SecretKeyVersion:  row.SecretKeyVersion,
		CsrfHash:          row.CsrfHash,
		Kind:              row.Kind,
		AdminVersion:      row.AdminVersion,
		PasswordVersion:   row.PasswordVersion,
		AttemptCount:      row.AttemptCount,
		MaxAttempts:       row.MaxAttempts,
		CreatedAt:         row.CreatedAt,
		LastSeenAt:        row.LastSeenAt,
		IdleExpiresAt:     row.IdleExpiresAt,
		AbsoluteExpiresAt: row.AbsoluteExpiresAt,
		RevokedAt:         row.RevokedAt,
		RevokeReason:      row.RevokeReason,
		SessionVersion:    row.SessionVersion,
		ClientIp:          row.ClientIp,
		UserAgent:         row.UserAgent,
	})
}

func timeToPGNullable(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return timeToPG(value)
}

var _ adminDomain.AccountRepository = (*adminAccountRepository)(nil)
var _ adminDomain.EnrollmentRepository = (*adminEnrollmentRepository)(nil)
var _ adminDomain.SessionRepository = (*adminSessionRepository)(nil)
var _ adminDomain.ElevationRepository = (*adminElevationRepository)(nil)
var _ adminDomain.CommandReceiptRepository = (*adminCommandReceiptRepository)(nil)
var _ adminDomain.RecoveryCodeRepository = (*adminRecoveryCodeRepository)(nil)
