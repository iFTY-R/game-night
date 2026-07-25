package admin

import (
	"context"
	"strconv"
	"strings"

	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/idempotency"
)

type ChangePasswordCommand struct {
	Session                 Session
	SessionToken            string
	CSRFToken               string
	Current                 string
	New                     string
	RequestID               string
	ClientIP                string
	OperationID             idempotency.OperationID
	ExpectedPasswordVersion int64
}

type ChangePasswordResult struct {
	Session         IssuedSession
	RevokedSessions int64
}

// BootstrapPassword performs the one-winner bootstrap CAS. A losing instance verifies that its mounted
// secret matches the committed bootstrap password; later active states reject a still-mounted secret.
func (service *Service) BootstrapPassword(ctx context.Context, bootstrapSecret string) error {
	if service == nil || ctx == nil || service.unitOfWork == nil || service.passwords == nil || service.clock == nil ||
		strings.TrimSpace(bootstrapSecret) == "" {
		return ErrBootstrapSecretMismatch
	}
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		snapshot := account.Snapshot()
		if snapshot.Status == AccountStatusSetupRequired {
			matched, _, verifyErr := VerifyPassword(ctx, service.passwords, PasswordRecord{
				Hash: snapshot.PasswordHash, Algorithm: snapshot.PasswordAlgorithm, Parameters: snapshot.PasswordParameters,
			}, bootstrapSecret)
			if verifyErr != nil || !matched {
				return ErrBootstrapSecretMismatch
			}
			return nil
		}
		if !account.IsBootstrapPending() {
			return ErrBootstrapSecretMismatch
		}
		record, err := HashPassword(ctx, service.passwords, service.passwordPolicy, account.Snapshot().Username, bootstrapSecret)
		if err != nil {
			return ErrBootstrapSecretMismatch
		}
		_, err = transaction.Accounts().BootstrapPasswordCAS(ctx, account, record.Hash, record.Algorithm, record.Parameters, service.clock.Now())
		return err
	})
	return mapAdminUoWError(err)
}

// BootstrapReadyWithoutSecret confirms that startup no longer depends on the one-time secret mount.
func (service *Service) BootstrapReadyWithoutSecret(ctx context.Context) error {
	state, err := service.GetSetupState(ctx)
	if err != nil {
		return err
	}
	if state == SetupStateBootstrapPending {
		return ErrBootstrapSecretMismatch
	}
	return nil
}

// ChangeInitialPassword replaces the bootstrap password and transitions the singleton account into its active state.
func (service *Service) ChangeInitialPassword(ctx context.Context, command ChangePasswordCommand) (SessionResult, error) {
	if command.Session.Snapshot().Kind != SessionKindSetupPasswordPending {
		return SessionResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return SessionResult{}, err
	}
	var result SessionResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) || account.Snapshot().Status != AccountStatusSetupRequired {
			return ErrAuthentication
		}
		record, err := HashPassword(ctx, service.passwords, service.passwordPolicy, account.Snapshot().Username, command.New)
		if err != nil {
			return err
		}
		updated, err := transaction.Accounts().UpdatePasswordCAS(ctx, account, record.Hash, record.Algorithm, record.Parameters, service.clock.Now())
		if err != nil {
			return err
		}
		if updated.Snapshot().Status != AccountStatusActive {
			updated, err = transaction.Accounts().TransitionStatusCAS(ctx, updated, AccountStatusActive, service.clock.Now())
			if err != nil {
				return err
			}
		}
		enrollment, err := service.loadActiveEnrollment(ctx, transaction, updated.Snapshot().ID)
		if err != nil {
			return err
		}
		nextKind, err := PasswordLoginSessionKind(updated.Snapshot().Status, enrollment != nil)
		if err != nil {
			return err
		}
		issued, err := service.sessions.IssueWithClient(
			updated.Snapshot().ID,
			nextKind,
			updated.Snapshot().AdminVersion,
			updated.Snapshot().PasswordVersion,
			SessionClientMetadata{ClientIP: command.Session.Snapshot().ClientIP, UserAgent: command.Session.Snapshot().UserAgent},
			service.clock.Now(),
		)
		if err != nil {
			return err
		}
		if err = transaction.Sessions().Insert(ctx, issued.Session); err != nil {
			return err
		}
		if _, err = transaction.Sessions().RevokeCAS(ctx, command.Session, "initial_password_changed", service.clock.Now()); err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(
			ctx,
			transaction,
			updated.Snapshot().ID,
			command.RequestID,
			audit.TargetAdmin,
			updated.Snapshot().ID.String(),
			audit.ActionAdminPasswordChanged,
			"initial_password_changed",
			digestAdminRequest("admin.password.initial", strconv.FormatInt(updated.Snapshot().PasswordVersion, 10)).Bytes(),
		); err != nil {
			return err
		}
		result = SessionResult{Session: issued}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// ChangeAdminPassword rotates the password, preserves MFA state, and revokes every older session.
func (service *Service) ChangeAdminPassword(ctx context.Context, command ChangePasswordCommand) (ChangePasswordResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || !command.OperationID.Valid() || command.ExpectedPasswordVersion <= 0 {
		return ChangePasswordResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return ChangePasswordResult{}, err
	}
	if strings.TrimSpace(command.ClientIP) == "" {
		return ChangePasswordResult{}, ErrInvalidInput
	}
	if err := service.consumePasswordLimit(ctx, command.ClientIP, command.Session.Snapshot().AdminID.String()); err != nil {
		return ChangePasswordResult{}, err
	}
	var result ChangePasswordResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) || account.Snapshot().Status != AccountStatusActive {
			return ErrAuthentication
		}
		replayed, replayErr := service.replayPasswordChangeReceipt(ctx, transaction, account.Snapshot().ID, command)
		if replayErr != nil {
			return replayErr
		}
		if replayed != nil {
			result = *replayed
			return nil
		}
		if account.Snapshot().PasswordVersion != command.ExpectedPasswordVersion {
			return ErrConcurrentTransition
		}
		matched, _, verifyErr := VerifyPassword(ctx, service.passwords, PasswordRecord{
			Hash: account.Snapshot().PasswordHash, Algorithm: account.Snapshot().PasswordAlgorithm, Parameters: account.Snapshot().PasswordParameters,
		}, command.Current)
		if verifyErr != nil || !matched {
			return ErrAuthentication
		}
		record, err := HashPassword(ctx, service.passwords, service.passwordPolicy, account.Snapshot().Username, command.New)
		if err != nil {
			return err
		}
		updated, err := transaction.Accounts().UpdatePasswordCAS(ctx, account, record.Hash, record.Algorithm, record.Parameters, service.clock.Now())
		if err != nil {
			return err
		}
		issued, err := service.sessions.IssueWithClient(
			updated.Snapshot().ID,
			SessionKindFull,
			updated.Snapshot().AdminVersion,
			updated.Snapshot().PasswordVersion,
			SessionClientMetadata{ClientIP: command.Session.Snapshot().ClientIP, UserAgent: command.Session.Snapshot().UserAgent},
			service.clock.Now(),
		)
		if err != nil {
			return err
		}
		if err = transaction.Sessions().Insert(ctx, issued.Session); err != nil {
			return err
		}
		revoked, err := transaction.Sessions().RevokeOtherActiveCAS(
			ctx,
			updated.Snapshot().ID,
			issued.Session.Snapshot().ID,
			updated.Snapshot().AdminVersion,
			issued.Session.Snapshot().SessionVersion,
			"password_changed",
			service.clock.Now(),
		)
		if err != nil {
			return err
		}
		auditEventID, err := service.appendAdminAudit(
			ctx,
			transaction,
			updated.Snapshot().ID,
			command.RequestID,
			audit.TargetAdmin,
			updated.Snapshot().ID.String(),
			audit.ActionAdminPasswordChanged,
			"password_changed",
			digestAdminRequest("admin.password.changed", strconv.FormatInt(int64(len(revoked)), 10), strconv.FormatInt(updated.Snapshot().PasswordVersion, 10)).Bytes(),
		)
		if err != nil {
			return err
		}
		result = ChangePasswordResult{Session: issued, RevokedSessions: int64(len(revoked))}
		if err = service.saveCommandReceipt(
			ctx,
			transaction,
			account.Snapshot().ID,
			command.OperationID,
			digestAdminRequest("admin.change_password", command.Current, command.New, strconv.FormatInt(command.ExpectedPasswordVersion, 10)),
			"change_admin_password",
			encodeReceiptTarget(issued.Session.Snapshot().ID.String(), strconv.FormatInt(result.RevokedSessions, 10)),
			updated.Snapshot().AdminVersion,
			updated.Snapshot().PasswordVersion,
			issued.Session.Snapshot().SessionVersion,
			enrollmentVersionOf(nil),
			auditEventID,
		); err != nil {
			return err
		}
		return nil
	})
	return result, mapAdminUoWError(err)
}
