package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgtype"
)

type adminAccountQueries interface {
	GetSingletonAdminForUpdate(context.Context) (sqlcgen.AdminAccount, error)
	BootstrapAdminPasswordCAS(context.Context, sqlcgen.BootstrapAdminPasswordCASParams) (sqlcgen.BootstrapAdminPasswordCASRow, error)
	UpdateAdminPasswordCAS(context.Context, sqlcgen.UpdateAdminPasswordCASParams) (sqlcgen.UpdateAdminPasswordCASRow, error)
	TransitionAdminStatusCAS(context.Context, sqlcgen.TransitionAdminStatusCASParams) (sqlcgen.TransitionAdminStatusCASRow, error)
	AcceptAdminTotpStepCAS(context.Context, sqlcgen.AcceptAdminTotpStepCASParams) (sqlcgen.AcceptAdminTotpStepCASRow, error)
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

func (repository *adminAccountRepository) AcceptTOTPStepCAS(ctx context.Context, current adminDomain.Account, step int64, at time.Time) (adminDomain.Account, error) {
	if current.Snapshot().Status != adminDomain.AccountStatusActive {
		return adminDomain.Account{}, adminDomain.ErrConcurrentTransition
	}
	row, err := repository.queries.AcceptAdminTotpStepCAS(ctx, sqlcgen.AcceptAdminTotpStepCASParams{
		TotpStep: pgtype.Int8{Int64: step, Valid: true}, AcceptedAt: timeToPG(at), AdminID: uuidToPG(current.Snapshot().ID), ExpectedAdminVersion: current.Snapshot().AdminVersion,
	})
	if err != nil {
		return adminDomain.Account{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	updated, err := accountAfterCAS(current, string(current.Snapshot().Status), current.Snapshot().PasswordVersion, row.AdminVersion, row.UpdatedAt.Time, nil)
	if err != nil {
		return adminDomain.Account{}, err
	}
	accepted := row.LastAcceptedTotpStep.Int64
	snapshot := updated.Snapshot()
	snapshot.LastAcceptedTOTPStep = &accepted
	return adminDomain.RestoreAccount(snapshot)
}

type adminEnrollmentQueries interface {
	CreatePendingAdminTotpEnrollment(context.Context, sqlcgen.CreatePendingAdminTotpEnrollmentParams) (sqlcgen.AdminTotpEnrollment, error)
	GetPendingAdminTotpEnrollmentForUpdate(context.Context, sqlcgen.GetPendingAdminTotpEnrollmentForUpdateParams) (sqlcgen.AdminTotpEnrollment, error)
	GetActiveAdminTotpEnrollmentForUpdate(context.Context, sqlcgen.GetActiveAdminTotpEnrollmentForUpdateParams) (sqlcgen.AdminTotpEnrollment, error)
	ActivatePendingAdminTotpEnrollmentCAS(context.Context, sqlcgen.ActivatePendingAdminTotpEnrollmentCASParams) (sqlcgen.AdminTotpEnrollment, error)
	DisableActiveAdminTotpEnrollmentCAS(context.Context, sqlcgen.DisableActiveAdminTotpEnrollmentCASParams) (sqlcgen.DisableActiveAdminTotpEnrollmentCASRow, error)
}

type adminEnrollmentRepository struct{ queries adminEnrollmentQueries }

func (repository *adminEnrollmentRepository) CreatePending(ctx context.Context, enrollment adminDomain.Enrollment) (adminDomain.Enrollment, error) {
	snapshot := enrollment.Snapshot()
	row, err := repository.queries.CreatePendingAdminTotpEnrollment(ctx, sqlcgen.CreatePendingAdminTotpEnrollmentParams{
		EnrollmentID: uuidToPG(snapshot.ID), AdminID: uuidToPG(snapshot.AdminID), Ciphertext: snapshot.Ciphertext, Nonce: snapshot.Nonce, KeyVersion: int32(snapshot.KeyVersion), AdminVersion: snapshot.AdminVersion,
		OperationID: snapshot.OperationID, CreatedAt: timeToPG(snapshot.CreatedAt), ExpiresAt: timeToPG(snapshot.ExpiresAt),
	})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return adminEnrollmentFromRow(row)
}

func (repository *adminEnrollmentRepository) GetPendingForUpdate(ctx context.Context, adminID uuid.UUID) (adminDomain.Enrollment, error) {
	row, err := repository.queries.GetPendingAdminTotpEnrollmentForUpdate(ctx, sqlcgen.GetPendingAdminTotpEnrollmentForUpdateParams{AdminID: uuidToPG(adminID)})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrNotFound)
	}
	return adminEnrollmentFromRow(row)
}

func (repository *adminEnrollmentRepository) GetActiveForUpdate(ctx context.Context, adminID uuid.UUID) (adminDomain.Enrollment, error) {
	row, err := repository.queries.GetActiveAdminTotpEnrollmentForUpdate(ctx, sqlcgen.GetActiveAdminTotpEnrollmentForUpdateParams{AdminID: uuidToPG(adminID)})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrNotFound)
	}
	return adminEnrollmentFromRow(row)
}

func (repository *adminEnrollmentRepository) ActivateCAS(ctx context.Context, current adminDomain.Enrollment, at time.Time) (adminDomain.Enrollment, error) {
	snapshot := current.Snapshot()
	row, err := repository.queries.ActivatePendingAdminTotpEnrollmentCAS(ctx, sqlcgen.ActivatePendingAdminTotpEnrollmentCASParams{ActivatedAt: timeToPG(at), AdminID: uuidToPG(snapshot.AdminID), EnrollmentID: uuidToPG(snapshot.ID), ExpectedAdminVersion: snapshot.AdminVersion})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	return adminEnrollmentFromRow(row)
}

func (repository *adminEnrollmentRepository) DisableCAS(ctx context.Context, current adminDomain.Enrollment, at time.Time) (adminDomain.Enrollment, error) {
	snapshot := current.Snapshot()
	row, err := repository.queries.DisableActiveAdminTotpEnrollmentCAS(ctx, sqlcgen.DisableActiveAdminTotpEnrollmentCASParams{DisabledAt: timeToPG(at), AdminID: uuidToPG(snapshot.AdminID), EnrollmentID: uuidToPG(snapshot.ID), ExpectedAdminVersion: snapshot.AdminVersion})
	if err != nil {
		return adminDomain.Enrollment{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	disabled, err := current.Disable(at)
	if err != nil || !row.EnrollmentID.Valid {
		return adminDomain.Enrollment{}, adminDomain.ErrIntegrity
	}
	return disabled, nil
}

type adminSessionQueries interface {
	CreateAdminSession(context.Context, sqlcgen.CreateAdminSessionParams) (sqlcgen.AdminSession, error)
	GetAdminSessionForUpdate(context.Context, sqlcgen.GetAdminSessionForUpdateParams) (sqlcgen.AdminSession, error)
	TouchAdminSessionCAS(context.Context, sqlcgen.TouchAdminSessionCASParams) (sqlcgen.TouchAdminSessionCASRow, error)
	RevokeAdminSessionCAS(context.Context, sqlcgen.RevokeAdminSessionCASParams) (sqlcgen.RevokeAdminSessionCASRow, error)
	RevokeAllAdminSessions(context.Context, sqlcgen.RevokeAllAdminSessionsParams) (int64, error)
}

type adminSessionRepository struct{ queries adminSessionQueries }

func (repository *adminSessionRepository) Insert(ctx context.Context, session adminDomain.Session) error {
	snapshot := session.Snapshot()
	if snapshot.SecretMAC.KeyVersion > 1<<31-1 {
		return adminDomain.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminSession(ctx, sqlcgen.CreateAdminSessionParams{SessionID: uuidToPG(snapshot.ID), AdminID: uuidToPG(snapshot.AdminID), Selector: snapshot.Selector, SecretHash: snapshot.SecretMAC.Value, SecretKeyVersion: int32(snapshot.SecretMAC.KeyVersion), CsrfHash: snapshot.CSRFHash.Value, Kind: string(snapshot.Kind), AdminVersion: snapshot.AdminVersion, PasswordVersion: snapshot.PasswordVersion, MaxAttempts: int32(snapshot.MaxAttempts), CreatedAt: timeToPG(snapshot.CreatedAt), IdleExpiresAt: timeToPG(snapshot.IdleExpiresAt), AbsoluteExpiresAt: timeToPG(snapshot.AbsoluteExpiresAt)})
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
	return adminSessionFromRow(row)
}

func (repository *adminSessionRepository) TouchCAS(ctx context.Context, current adminDomain.Session, at time.Time, idleTTL time.Duration) (adminDomain.Session, error) {
	snapshot := current.Snapshot()
	row, err := repository.queries.TouchAdminSessionCAS(ctx, sqlcgen.TouchAdminSessionCASParams{SeenAt: timeToPG(at), IdleExpiresAt: timeToPG(at.Add(idleTTL)), SessionID: uuidToPG(snapshot.ID), ExpectedAdminVersion: snapshot.AdminVersion, ExpectedPasswordVersion: snapshot.PasswordVersion})
	if err != nil {
		return adminDomain.Session{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	updated := snapshot
	updated.LastSeenAt, updated.IdleExpiresAt, updated.AbsoluteExpiresAt = row.LastSeenAt.Time, row.IdleExpiresAt.Time, row.AbsoluteExpiresAt.Time
	return adminDomain.RestoreSession(updated)
}

func (repository *adminSessionRepository) RevokeCAS(ctx context.Context, current adminDomain.Session, reason string, at time.Time) (adminDomain.Session, error) {
	row, err := repository.queries.RevokeAdminSessionCAS(ctx, sqlcgen.RevokeAdminSessionCASParams{RevokedAt: timeToPG(at), RevokeReason: pgtype.Text{String: reason, Valid: true}, SessionID: uuidToPG(current.Snapshot().ID)})
	if err != nil {
		return adminDomain.Session{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	updated := current.Snapshot()
	updated.RevokedAt, updated.RevokeReason = row.RevokedAt.Time, row.RevokeReason.String
	return adminDomain.RestoreSession(updated)
}

func (repository *adminSessionRepository) RevokeAll(ctx context.Context, adminID uuid.UUID, reason string, at time.Time) (int64, error) {
	count, err := repository.queries.RevokeAllAdminSessions(ctx, sqlcgen.RevokeAllAdminSessionsParams{RevokedAt: timeToPG(at), RevokeReason: pgtype.Text{String: reason, Valid: true}, AdminID: uuidToPG(adminID)})
	if err != nil {
		return 0, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	return count, nil
}

type adminRecoveryCodeQueries interface {
	CreateAdminRecoveryCode(context.Context, sqlcgen.CreateAdminRecoveryCodeParams) (sqlcgen.AdminRecoveryCode, error)
	GetAdminRecoveryCodeForUpdate(context.Context, sqlcgen.GetAdminRecoveryCodeForUpdateParams) (sqlcgen.AdminRecoveryCode, error)
	ConsumeAdminRecoveryCodeCAS(context.Context, sqlcgen.ConsumeAdminRecoveryCodeCASParams) (sqlcgen.ConsumeAdminRecoveryCodeCASRow, error)
	RevokeAdminRecoveryCodeSet(context.Context, sqlcgen.RevokeAdminRecoveryCodeSetParams) (int64, error)
	RevokeAllAdminRecoveryCodeSets(context.Context, sqlcgen.RevokeAllAdminRecoveryCodeSetsParams) (int64, error)
}

type adminRecoveryCodeRepository struct{ queries adminRecoveryCodeQueries }

func (repository *adminRecoveryCodeRepository) Insert(ctx context.Context, code adminDomain.RecoveryCode) error {
	snapshot := code.Snapshot()
	row, err := repository.queries.CreateAdminRecoveryCode(ctx, sqlcgen.CreateAdminRecoveryCodeParams{RecoveryCodeID: uuidToPG(snapshot.ID), AdminID: uuidToPG(snapshot.AdminID), Selector: snapshot.Selector, SecretHash: snapshot.SecretHash, SetVersion: snapshot.SetVersion, CreatedAt: timeToPG(snapshot.CreatedAt)})
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

func (repository *adminRecoveryCodeRepository) ConsumeCAS(ctx context.Context, current adminDomain.RecoveryCode, at time.Time) (adminDomain.RecoveryCode, error) {
	snapshot := current.Snapshot()
	row, err := repository.queries.ConsumeAdminRecoveryCodeCAS(ctx, sqlcgen.ConsumeAdminRecoveryCodeCASParams{ConsumedAt: timeToPG(at), RecoveryCodeID: uuidToPG(snapshot.ID), AdminID: uuidToPG(snapshot.AdminID), ExpectedSetVersion: snapshot.SetVersion})
	if err != nil {
		return adminDomain.RecoveryCode{}, mapAdminQueryError(err, adminDomain.ErrConcurrentTransition)
	}
	updated := snapshot
	updated.Status, updated.ConsumedAt = adminDomain.RecoveryCodeStatus(row.Status), row.ConsumedAt.Time
	return adminDomain.RestoreRecoveryCode(updated)
}

func (repository *adminRecoveryCodeRepository) RevokeSet(ctx context.Context, adminID uuid.UUID, setVersion int64, at time.Time) (int64, error) {
	count, err := repository.queries.RevokeAdminRecoveryCodeSet(ctx, sqlcgen.RevokeAdminRecoveryCodeSetParams{RevokedAt: timeToPG(at), AdminID: uuidToPG(adminID), SetVersion: setVersion})
	if err != nil {
		return 0, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	return count, nil
}

func (repository *adminRecoveryCodeRepository) RevokeAllSets(ctx context.Context, adminID uuid.UUID, at time.Time) (int64, error) {
	count, err := repository.queries.RevokeAllAdminRecoveryCodeSets(ctx, sqlcgen.RevokeAllAdminRecoveryCodeSetsParams{RevokedAt: timeToPG(at), AdminID: uuidToPG(adminID)})
	if err != nil {
		return 0, mapAdminQueryError(err, adminDomain.ErrRepositoryUnavailable)
	}
	return count, nil
}

var _ adminDomain.AccountRepository = (*adminAccountRepository)(nil)
var _ adminDomain.EnrollmentRepository = (*adminEnrollmentRepository)(nil)
var _ adminDomain.SessionRepository = (*adminSessionRepository)(nil)
var _ adminDomain.RecoveryCodeRepository = (*adminRecoveryCodeRepository)(nil)
