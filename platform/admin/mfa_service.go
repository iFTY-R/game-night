package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

type BeginEnrollmentCommand struct {
	Session         Session
	SessionToken    string
	CSRFToken       string
	OperationID     idempotency.OperationID
	CurrentPassword string
	RequestID       string
}

type EnrollmentResult struct {
	Enrollment Enrollment
	Operation  OperationResult
	Secret     string
	URI        string
}

type CompleteEnrollmentCommand struct {
	Session                  Session
	SessionToken             string
	CSRFToken                string
	EnrollmentOperationID    string
	RecoveryCodesOperationID idempotency.OperationID
	TOTPPasscode             string
	RequestID                string
}

type CompleteEnrollmentResult struct {
	Operation       OperationResult
	Session         IssuedSession
	RecoveryCodes   []string
	RevokedSessions int64
}

type DisableTotpCommand struct {
	Session                   Session
	SessionToken              string
	CSRFToken                 string
	OperationID               idempotency.OperationID
	Reason                    string
	RequestID                 string
	ExpectedEnrollmentVersion int64
}

type DisableTotpResult struct {
	Session         IssuedSession
	RevokedSessions int64
	AlreadyDisabled bool
}

type RegenerateRecoveryCodesCommand struct {
	Session                      Session
	SessionToken                 string
	CSRFToken                    string
	OperationID                  idempotency.OperationID
	RequestID                    string
	ExpectedRecoveryCodesVersion int64
}

type RegenerateRecoveryCodesResult struct {
	Operation     OperationResult
	Session       Session
	RecoveryCodes []string
}

// BeginTotpEnrollment creates the one pending encrypted seed per account and replays the same result for duplicate operations.
func (service *Service) BeginTotpEnrollment(ctx context.Context, command BeginEnrollmentCommand) (EnrollmentResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || !command.OperationID.Valid() {
		return EnrollmentResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return EnrollmentResult{}, err
	}
	var result EnrollmentResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) || account.Snapshot().Status != AccountStatusActive {
			return ErrAuthentication
		}
		matched, _, verifyErr := VerifyPassword(ctx, service.passwords, PasswordRecord{
			Hash: account.Snapshot().PasswordHash, Algorithm: account.Snapshot().PasswordAlgorithm, Parameters: account.Snapshot().PasswordParameters,
		}, command.CurrentPassword)
		if verifyErr != nil || !matched {
			return ErrAuthentication
		}
		if active, activeErr := service.loadActiveEnrollment(ctx, transaction, account.Snapshot().ID); activeErr != nil {
			return activeErr
		} else if active != nil {
			return ErrMFAStateConflict
		}
		binding := adminResultBinding(
			secretresult.ScopeAdminTOTPEnrollment,
			account.Snapshot().ID,
			command.OperationID,
			digestAdminRequest("admin.totp_enrollment", command.CurrentPassword),
			secretresult.ResultTypeAdminTOTPEnrollment,
		)
		existing, getErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
		if getErr == nil {
			replayed, replayErr := service.replayEnrollmentResult(command.Session, command.OperationID, binding, existing)
			if replayErr != nil {
				return replayErr
			}
			result = replayed
			_, _ = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminSecretResultOpened, "totp_enrollment_replayed", digestAdminRequest("admin.secret.opened", existing.Snapshot().ID.String()).Bytes())
			return nil
		}
		if !errors.Is(getErr, secretresult.ErrNotFound) {
			return getErr
		}
		if pending, pendingErr := transaction.Enrollments().GetPendingForUpdate(ctx, account.Snapshot().ID); pendingErr == nil {
			pendingOperationID, parseErr := idempotency.ParseOperationID(pending.Snapshot().OperationID)
			if parseErr != nil {
				return ErrIntegrity
			}
			pendingBinding := adminResultBinding(
				secretresult.ScopeAdminTOTPEnrollment,
				account.Snapshot().ID,
				pendingOperationID,
				digestAdminRequest("admin.totp_enrollment", command.CurrentPassword),
				secretresult.ResultTypeAdminTOTPEnrollment,
			)
			pendingResult, resultErr := transaction.SecretResults().GetByOperationForUpdate(ctx, pendingBinding.Key)
			if resultErr != nil {
				return resultErr
			}
			replayed, replayErr := service.replayEnrollmentResult(command.Session, pendingOperationID, pendingBinding, pendingResult)
			if replayErr != nil {
				return replayErr
			}
			replayed.Enrollment = pending
			result = replayed
			return nil
		} else if !errors.Is(pendingErr, ErrNotFound) {
			return pendingErr
		}

		enrollmentID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		secret, uri, encrypted, err := service.totp.NewEnrollmentSecret(
			uuidToArray(account.Snapshot().ID),
			uuidToArray(enrollmentID),
			"Game Night",
			account.Snapshot().Username,
		)
		if err != nil {
			return err
		}
		enrollment, err := RestoreEnrollment(EnrollmentSnapshot{
			ID: enrollmentID, AdminID: account.Snapshot().ID, Ciphertext: encrypted.Ciphertext, Nonce: encrypted.Nonce,
			KeyVersion: encrypted.KeyVersion, Status: EnrollmentStatusPending, AdminVersion: account.Snapshot().AdminVersion,
			EnrollmentVersion: 1, OperationID: command.OperationID.Value(), CreatedAt: service.clock.Now(), ExpiresAt: service.clock.Now().Add(AdminSetupSessionTTL),
		})
		if err != nil {
			return err
		}
		stored, err := transaction.Enrollments().CreatePending(ctx, enrollment)
		if err != nil {
			return err
		}
		plaintext, err := json.Marshal(totpEnrollmentEnvelope{Secret: secret, URI: uri})
		if err != nil {
			return ErrIntegrity
		}
		defer clear(plaintext)
		resultID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		prepared, err := service.results.PrepareAvailable(resultID, binding, plaintext, adminSecretResultTTL)
		if err != nil {
			return err
		}
		storedResult, err := transaction.SecretResults().InsertAvailable(ctx, prepared)
		if err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminSecretResultOpened, "totp_enrollment_started", digestAdminRequest("admin.secret.opened", storedResult.Snapshot().ID.String()).Bytes()); err != nil {
			return err
		}
		result = EnrollmentResult{Enrollment: stored, Operation: adminOperationResult(command.OperationID, storedResult, false), Secret: secret, URI: uri}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// CompleteTotpEnrollment verifies the first TOTP code, activates the seed, rotates recovery codes, and replaces older sessions.
func (service *Service) CompleteTotpEnrollment(ctx context.Context, command CompleteEnrollmentCommand) (CompleteEnrollmentResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || command.EnrollmentOperationID == "" || !command.RecoveryCodesOperationID.Valid() {
		return CompleteEnrollmentResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return CompleteEnrollmentResult{}, err
	}
	var result CompleteEnrollmentResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) || account.Snapshot().Status != AccountStatusActive {
			return ErrAuthentication
		}
		binding := adminResultBinding(
			secretresult.ScopeAdminInitialRecoveryCodes,
			account.Snapshot().ID,
			command.RecoveryCodesOperationID,
			digestAdminRequest("admin.recovery_codes", command.EnrollmentOperationID),
			secretresult.ResultTypeAdminRecoveryCodes,
		)
		existing, getErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
		if getErr == nil {
			if _, resolveErr := existing.Resolve(binding, service.clock.Now()); resolveErr != nil {
				return resolveErr
			}
			grant, grantErr := service.sessions.ResultGrant(command.Session, existing.Snapshot().ID, service.clock.Now())
			if grantErr != nil {
				return grantErr
			}
			plaintext, openErr := service.results.OpenAdminAuthorizedResult(existing, binding, grant)
			if openErr != nil {
				return openErr
			}
			defer clear(plaintext)
			envelope, decodeErr := decodeAdminRecoveryBundle(plaintext)
			if decodeErr != nil {
				return decodeErr
			}
			selector, secret, parseErr := parseSessionToken(envelope.SessionToken)
			clearSessionBytes(secret)
			if parseErr != nil {
				return ErrIntegrity
			}
			storedSession, sessionErr := transaction.Sessions().GetForUpdate(ctx, selector)
			if sessionErr != nil {
				return sessionErr
			}
			result = CompleteEnrollmentResult{
				Operation:     adminOperationResult(command.RecoveryCodesOperationID, existing, true),
				Session:       IssuedSession{Session: storedSession, Token: envelope.SessionToken, CSRFToken: envelope.CSRFToken},
				RecoveryCodes: envelope.RecoveryCodes,
			}
			return nil
		}
		if !errors.Is(getErr, secretresult.ErrNotFound) {
			return getErr
		}
		enrollment, err := transaction.Enrollments().GetPendingForUpdate(ctx, account.Snapshot().ID)
		if err != nil || enrollment.Snapshot().OperationID != command.EnrollmentOperationID {
			return ErrMFAStateConflict
		}
		es := enrollment.Snapshot()
		secret, err := service.totp.DecryptSeed(
			uuidToArray(account.Snapshot().ID),
			uuidToArray(es.ID),
			security.Encrypted[security.TOTPKeyPurpose]{KeyVersion: es.KeyVersion, Nonce: es.Nonce, Ciphertext: es.Ciphertext},
		)
		if err != nil {
			return err
		}
		step, err := VerifyTOTPCode(secret, command.TOTPPasscode, service.clock.Now())
		if err != nil {
			return err
		}
		updatedAccount, err := transaction.Accounts().RecordMFAChangeCAS(ctx, account, service.clock.Now())
		if err != nil {
			return err
		}
		if _, err = transaction.Enrollments().ActivateCAS(ctx, enrollment, step, updatedAccount.Snapshot().AdminVersion, service.clock.Now()); err != nil {
			return err
		}
		recoveryState, err := transaction.RecoveryCodes().GetActiveSetState(ctx, account.Snapshot().ID)
		if err != nil {
			return err
		}
		nextRecoverySetVersion := recoveryState.SetVersion + 1
		if nextRecoverySetVersion <= 0 {
			return ErrIntegrity
		}
		if _, err = transaction.RecoveryCodes().RevokeAllSets(ctx, account.Snapshot().ID, service.clock.Now()); err != nil {
			return err
		}
		issuedCodes, err := service.recoveryCodes.IssueSet(ctx, account.Snapshot().ID, nextRecoverySetVersion, service.clock.Now())
		if err != nil {
			return err
		}
		codes := make([]string, 0, len(issuedCodes))
		for _, issued := range issuedCodes {
			if err = transaction.RecoveryCodes().Insert(ctx, issued.Code); err != nil {
				return err
			}
			codes = append(codes, issued.Secret)
		}
		issued, err := service.sessions.IssueWithClient(
			updatedAccount.Snapshot().ID,
			SessionKindFull,
			updatedAccount.Snapshot().AdminVersion,
			updatedAccount.Snapshot().PasswordVersion,
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
			updatedAccount.Snapshot().ID,
			issued.Session.Snapshot().ID,
			updatedAccount.Snapshot().AdminVersion,
			issued.Session.Snapshot().SessionVersion,
			"mfa_enabled",
			service.clock.Now(),
		)
		if err != nil {
			return err
		}
		plaintext, err := json.Marshal(adminRecoveryBundle{RecoveryCodes: codes, SessionToken: issued.Token, CSRFToken: issued.CSRFToken})
		if err != nil {
			return ErrIntegrity
		}
		defer clear(plaintext)
		resultID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		prepared, err := service.results.PrepareAvailable(resultID, binding, plaintext, adminSecretResultTTL)
		if err != nil {
			return err
		}
		storedResult, err := transaction.SecretResults().InsertAvailable(ctx, prepared)
		if err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(ctx, transaction, updatedAccount.Snapshot().ID, command.RequestID, audit.TargetAdmin, updatedAccount.Snapshot().ID.String(), audit.ActionAdminMFAEnabled, "totp_enabled", digestAdminRequest("admin.mfa.enabled", strconv.FormatInt(updatedAccount.Snapshot().AdminVersion, 10)).Bytes()); err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(ctx, transaction, updatedAccount.Snapshot().ID, command.RequestID, audit.TargetAdmin, updatedAccount.Snapshot().ID.String(), audit.ActionAdminSecretResultOpened, "initial_recovery_codes", digestAdminRequest("admin.secret.opened", storedResult.Snapshot().ID.String()).Bytes()); err != nil {
			return err
		}
		result = CompleteEnrollmentResult{
			Operation:       adminOperationResult(command.RecoveryCodesOperationID, storedResult, false),
			Session:         issued,
			RecoveryCodes:   codes,
			RevokedSessions: int64(len(revoked)),
		}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// DisableTotp validates a current elevation grant, disables MFA, revokes every older session, and leaves the caller signed in on a fresh session.
func (service *Service) DisableTotp(ctx context.Context, command DisableTotpCommand) (DisableTotpResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || !command.OperationID.Valid() {
		return DisableTotpResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return DisableTotpResult{}, err
	}
	reason, err := normalizeSecurityReason(command.Reason)
	if err != nil {
		return DisableTotpResult{}, err
	}
	var result DisableTotpResult
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) || account.Snapshot().Status != AccountStatusActive {
			return ErrAuthentication
		}
		enrollment, err := service.loadActiveEnrollment(ctx, transaction, account.Snapshot().ID)
		if err != nil {
			return err
		}
		if err = service.requireElevation(ctx, transaction, command.Session, enrollmentVersionOf(enrollment), ElevationScopeSecurityDisableMFA, command.RequestID); err != nil {
			return err
		}
		replayed, replayErr := service.replayDisableTotpReceipt(ctx, transaction, account.Snapshot().ID, command)
		if replayErr != nil {
			return replayErr
		}
		if replayed != nil {
			result = *replayed
			return nil
		}
		if enrollment == nil {
			result = DisableTotpResult{Session: IssuedSession{Session: command.Session}, AlreadyDisabled: true}
			auditEventID := uuid.Must(uuid.NewV7())
			if err = service.saveCommandReceipt(ctx, transaction, account.Snapshot().ID, command.OperationID, digestAdminRequest("admin.disable_totp", reason, strconv.FormatInt(command.ExpectedEnrollmentVersion, 10)), "disable_totp", encodeReceiptTarget(command.Session.Snapshot().ID.String(), "0", "true"), account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion, command.Session.Snapshot().SessionVersion, 0, auditEventID); err != nil {
				return err
			}
			return nil
		}
		if command.ExpectedEnrollmentVersion > 0 && enrollment.Snapshot().EnrollmentVersion != command.ExpectedEnrollmentVersion {
			return ErrConcurrentTransition
		}
		updatedAccount, err := transaction.Accounts().RecordMFAChangeCAS(ctx, account, service.clock.Now())
		if err != nil {
			return err
		}
		if _, err = transaction.Enrollments().DisableCAS(ctx, *enrollment, updatedAccount.Snapshot().AdminVersion, service.clock.Now()); err != nil {
			return err
		}
		if _, err = transaction.RecoveryCodes().RevokeAllSets(ctx, account.Snapshot().ID, service.clock.Now()); err != nil {
			return err
		}
		issued, err := service.sessions.IssueWithClient(updatedAccount.Snapshot().ID, SessionKindFull, updatedAccount.Snapshot().AdminVersion, updatedAccount.Snapshot().PasswordVersion, SessionClientMetadata{ClientIP: command.Session.Snapshot().ClientIP, UserAgent: command.Session.Snapshot().UserAgent}, service.clock.Now())
		if err != nil {
			return err
		}
		if err = transaction.Sessions().Insert(ctx, issued.Session); err != nil {
			return err
		}
		revoked, err := transaction.Sessions().RevokeOtherActiveCAS(ctx, updatedAccount.Snapshot().ID, issued.Session.Snapshot().ID, updatedAccount.Snapshot().AdminVersion, issued.Session.Snapshot().SessionVersion, "mfa_disabled", service.clock.Now())
		if err != nil {
			return err
		}
		auditEventID, err := service.appendAdminAudit(ctx, transaction, updatedAccount.Snapshot().ID, command.RequestID, audit.TargetAdmin, updatedAccount.Snapshot().ID.String(), audit.ActionAdminMFADisabled, "mfa_disabled", digestAdminRequest("admin.mfa.disabled", reason, strconv.FormatInt(updatedAccount.Snapshot().AdminVersion, 10)).Bytes())
		if err != nil {
			return err
		}
		result = DisableTotpResult{Session: issued, RevokedSessions: int64(len(revoked))}
		if err = service.saveCommandReceipt(ctx, transaction, account.Snapshot().ID, command.OperationID, digestAdminRequest("admin.disable_totp", reason, strconv.FormatInt(command.ExpectedEnrollmentVersion, 10)), "disable_totp", encodeReceiptTarget(issued.Session.Snapshot().ID.String(), strconv.FormatInt(result.RevokedSessions, 10), "false"), updatedAccount.Snapshot().AdminVersion, updatedAccount.Snapshot().PasswordVersion, issued.Session.Snapshot().SessionVersion, 0, auditEventID); err != nil {
			return err
		}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// RegenerateAdminRecoveryCodes rotates the current recovery-code set after a validated elevation grant.
func (service *Service) RegenerateAdminRecoveryCodes(ctx context.Context, command RegenerateRecoveryCodesCommand) (RegenerateRecoveryCodesResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || !command.OperationID.Valid() {
		return RegenerateRecoveryCodesResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return RegenerateRecoveryCodesResult{}, err
	}
	var result RegenerateRecoveryCodesResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) || account.Snapshot().Status != AccountStatusActive {
			return ErrAuthentication
		}
		enrollment, err := service.loadActiveEnrollment(ctx, transaction, account.Snapshot().ID)
		if err != nil {
			return err
		}
		if enrollment == nil {
			return ErrMFAStateConflict
		}
		if err = service.requireElevation(ctx, transaction, command.Session, enrollment.Snapshot().EnrollmentVersion, ElevationScopeSecurityRegenerateRecoveryCodes, command.RequestID); err != nil {
			return err
		}
		binding := adminResultBinding(
			secretresult.ScopeAdminRegenerateRecoveryCodes,
			account.Snapshot().ID,
			command.OperationID,
			digestAdminRequest("admin.regenerate_recovery_codes", strconv.FormatInt(command.ExpectedRecoveryCodesVersion, 10)),
			secretresult.ResultTypeAdminRecoveryCodes,
		)
		existing, getErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
		if getErr == nil {
			if _, resolveErr := existing.Resolve(binding, service.clock.Now()); resolveErr != nil {
				return resolveErr
			}
			grant, grantErr := service.sessions.ResultGrant(command.Session, existing.Snapshot().ID, service.clock.Now())
			if grantErr != nil {
				return grantErr
			}
			plaintext, openErr := service.results.OpenAdminAuthorizedResult(existing, binding, grant)
			if openErr != nil {
				return openErr
			}
			defer clear(plaintext)
			envelope, decodeErr := decodeAdminRecoveryCodesEnvelope(plaintext)
			if decodeErr != nil {
				return decodeErr
			}
			result = RegenerateRecoveryCodesResult{Operation: adminOperationResult(command.OperationID, existing, true), Session: command.Session, RecoveryCodes: envelope.RecoveryCodes}
			return nil
		}
		if !errors.Is(getErr, secretresult.ErrNotFound) {
			return getErr
		}
		recoveryState, err := transaction.RecoveryCodes().GetActiveSetState(ctx, account.Snapshot().ID)
		if err != nil {
			return err
		}
		if recoveryState.SetVersion <= 0 || command.ExpectedRecoveryCodesVersion != recoveryState.SetVersion {
			return ErrConcurrentTransition
		}
		nextRecoverySetVersion := recoveryState.SetVersion + 1
		if _, err = transaction.RecoveryCodes().RevokeAllSets(ctx, account.Snapshot().ID, service.clock.Now()); err != nil {
			return err
		}
		issuedCodes, err := service.recoveryCodes.IssueSet(ctx, account.Snapshot().ID, nextRecoverySetVersion, service.clock.Now())
		if err != nil {
			return err
		}
		codes := make([]string, 0, len(issuedCodes))
		for _, issued := range issuedCodes {
			if err = transaction.RecoveryCodes().Insert(ctx, issued.Code); err != nil {
				return err
			}
			codes = append(codes, issued.Secret)
		}
		plaintext, err := json.Marshal(adminRecoveryCodesEnvelope{RecoveryCodes: codes})
		if err != nil {
			return ErrIntegrity
		}
		defer clear(plaintext)
		resultID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		prepared, err := service.results.PrepareAvailable(resultID, binding, plaintext, adminSecretResultTTL)
		if err != nil {
			return err
		}
		stored, err := transaction.SecretResults().InsertAvailable(ctx, prepared)
		if err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminRecoveryCodesRegenerated, "recovery_codes_regenerated", digestAdminRequest("admin.recovery_codes.regenerated", strconv.FormatInt(nextRecoverySetVersion, 10)).Bytes()); err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminSecretResultOpened, "recovery_codes_opened", digestAdminRequest("admin.secret.opened", stored.Snapshot().ID.String()).Bytes()); err != nil {
			return err
		}
		result = RegenerateRecoveryCodesResult{Operation: adminOperationResult(command.OperationID, stored, false), Session: command.Session, RecoveryCodes: codes}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// ConfirmAdminSecretReceipt erases a result only after the exact operation and live session are revalidated.
func (service *Service) ConfirmAdminSecretReceipt(ctx context.Context, session Session, token, csrfToken string, scope secretresult.Scope, operationID idempotency.OperationID, resultID uuid.UUID) (bool, error) {
	if !scope.IsAdmin() || !operationID.Valid() || resultID == uuid.Nil {
		return false, ErrInvalidInput
	}
	if err := service.sessions.Authenticate(session, token, csrfToken, service.clock.Now()); err != nil {
		return false, err
	}
	confirmed := false
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(session, account) {
			return ErrAuthentication
		}
		stored, err := transaction.SecretResults().GetByIDForUpdate(ctx, resultID)
		if err != nil {
			return err
		}
		snapshot := stored.Snapshot()
		if snapshot.Binding.Key.Scope != scope || snapshot.Binding.Key.ActorID != account.Snapshot().ID || snapshot.Binding.Key.OperationID != operationID || snapshot.ID != resultID {
			return secretresult.ErrReplayUnauthorized
		}
		grant, err := service.sessions.ResultGrant(session, resultID, service.clock.Now())
		if err != nil {
			return err
		}
		updated, err := service.results.ConfirmAdminAuthorizedResult(ctx, transaction.SecretResults(), stored, snapshot.Binding, grant)
		if err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, operationID.Value(), audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminSecretResultConfirmed, "secret_confirmed", digestAdminRequest("admin.secret.confirmed", resultID.String()).Bytes()); err != nil {
			return err
		}
		confirmed = updated.Snapshot().Status == secretresult.StatusConfirmed
		return nil
	})
	return confirmed, mapAdminUoWError(err)
}

func (service *Service) replayEnrollmentResult(
	session Session,
	operationID idempotency.OperationID,
	binding secretresult.Binding,
	result secretresult.Result,
) (EnrollmentResult, error) {
	if _, err := result.Resolve(binding, service.clock.Now()); err != nil {
		return EnrollmentResult{}, err
	}
	grant, err := service.sessions.ResultGrant(session, result.Snapshot().ID, service.clock.Now())
	if err != nil {
		return EnrollmentResult{}, err
	}
	plaintext, err := service.results.OpenAdminAuthorizedResult(result, binding, grant)
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer clear(plaintext)
	envelope, err := decodeTOTPEnrollmentEnvelope(plaintext)
	if err != nil {
		return EnrollmentResult{}, err
	}
	return EnrollmentResult{
		Operation: adminOperationResult(operationID, result, true),
		Secret:    envelope.Secret,
		URI:       envelope.URI,
	}, nil
}
