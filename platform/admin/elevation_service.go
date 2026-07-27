package admin

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/security"
)

type ElevateSessionCommand struct {
	Session         Session
	SessionToken    string
	CSRFToken       string
	OperationID     idempotency.OperationID
	Scope           ElevationScope
	CurrentPassword string
	TOTPCode        string
	RecoveryCode    string
	RequestID       string
	ClientIP        string
}

type ElevateSessionResult struct {
	Elevation        Elevation
	Session          Session
	UsedSecondFactor bool
	UsedRecoveryCode bool
}

type RevokeCurrentElevationCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
	Scope        ElevationScope
	RequestID    string
}

type RevokeCurrentElevationResult struct {
	Revoked bool
	Session Session
}

// ElevateAdminSession verifies the password and, when MFA is enabled, the required second factor before issuing a short-lived grant.
func (service *Service) ElevateAdminSession(ctx context.Context, command ElevateSessionCommand) (ElevateSessionResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || !command.OperationID.Valid() || !command.Scope.Valid() || strings.TrimSpace(command.ClientIP) == "" {
		return ElevateSessionResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return ElevateSessionResult{}, err
	}
	if err := service.consumeSecondFactorLimit(ctx, command.ClientIP, command.Session.Snapshot().AdminID.String(), "elevation:"+string(command.Scope)); err != nil {
		return ElevateSessionResult{}, err
	}
	var result ElevateSessionResult
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
			_, _ = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminElevationDenied, "password_invalid", digestAdminRequest("admin.elevation.denied", string(command.Scope)).Bytes())
			return ErrAuthentication
		}
		enrollment, err := service.loadActiveEnrollment(ctx, transaction, account.Snapshot().ID)
		if err != nil {
			return err
		}
		enrollmentVersion := enrollmentVersionOf(enrollment)
		usedRecoveryCode := false
		totpProvided := strings.TrimSpace(command.TOTPCode) != ""
		recoveryProvided := strings.TrimSpace(command.RecoveryCode) != ""
		if totpProvided && recoveryProvided {
			return ErrInvalidInput
		}
		switch {
		case enrollment == nil && (totpProvided || recoveryProvided):
			return ErrMFAStateConflict
		case enrollment == nil:
			// Password-only step-up is authoritative while the account has no active MFA enrollment.
		case strings.TrimSpace(command.TOTPCode) != "":
			secret, secretErr := service.totp.DecryptSeed(
				uuidToArray(account.Snapshot().ID),
				uuidToArray(enrollment.Snapshot().ID),
				security.Encrypted[security.TOTPKeyPurpose]{KeyVersion: enrollment.Snapshot().KeyVersion, Nonce: enrollment.Snapshot().Nonce, Ciphertext: enrollment.Snapshot().Ciphertext},
			)
			if secretErr != nil {
				return secretErr
			}
			step, codeErr := VerifyTOTPCode(secret, command.TOTPCode, service.clock.Now())
			if codeErr != nil {
				_, _ = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminElevationDenied, "totp_invalid", digestAdminRequest("admin.elevation.denied", string(command.Scope)).Bytes())
				return codeErr
			}
			nextEnrollment, acceptErr := transaction.Enrollments().AcceptTOTPCAS(ctx, *enrollment, step, service.clock.Now())
			if acceptErr != nil {
				return acceptErr
			}
			enrollmentVersion = nextEnrollment.Snapshot().EnrollmentVersion
		case strings.TrimSpace(command.RecoveryCode) != "":
			if !command.Scope.AllowsRecoveryCodeSubstitution() {
				_, _ = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminElevationDenied, "recovery_not_allowed", digestAdminRequest("admin.elevation.denied", string(command.Scope)).Bytes())
				return ErrElevationDenied
			}
			recoveryState, stateErr := transaction.RecoveryCodes().GetActiveSetState(ctx, account.Snapshot().ID)
			if stateErr != nil {
				return stateErr
			}
			if recoveryState.RemainingActive == 0 {
				return ErrRecoveryCodeExhausted
			}
			selector, parsedSecret, parseErr := parseRecoveryCode(command.RecoveryCode)
			clearRecoveryBytes(parsedSecret)
			if parseErr != nil {
				return ErrRecoveryInvalid
			}
			code, getErr := transaction.RecoveryCodes().FindActiveBySelector(ctx, selector)
			if getErr != nil {
				return ErrRecoveryInvalid
			}
			if err = service.recoveryCodes.Verify(ctx, code, command.RecoveryCode); err != nil {
				return err
			}
			if _, err = transaction.RecoveryCodes().ConsumeCAS(ctx, code, service.clock.Now()); err != nil {
				return err
			}
			if _, err = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminRecoveryUsed, "elevation", digestAdminRequest("admin.recovery.used", string(command.Scope)).Bytes()); err != nil {
				return err
			}
			usedRecoveryCode = true
		default:
			return ErrMFAStateConflict
		}
		elevation, err := NewElevation(command.Session, enrollmentVersion, command.Scope, service.clock.Now(), service.clock.Now().Add(AdminElevationMaxTTL))
		if err != nil {
			return err
		}
		stored, err := transaction.Elevations().UpsertLive(ctx, elevation)
		if err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminSessionElevated, "elevation_granted", digestAdminRequest("admin.elevation.granted", string(command.Scope), strconv.FormatInt(enrollmentVersion, 10)).Bytes()); err != nil {
			return err
		}
		result = ElevateSessionResult{
			Elevation:        stored,
			Session:          command.Session,
			UsedSecondFactor: enrollment != nil,
			UsedRecoveryCode: usedRecoveryCode,
		}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// RevokeCurrentAdminElevation revokes one current-session scope grant if it still exists.
func (service *Service) RevokeCurrentAdminElevation(ctx context.Context, command RevokeCurrentElevationCommand) (RevokeCurrentElevationResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || !command.Scope.Valid() {
		return RevokeCurrentElevationResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return RevokeCurrentElevationResult{}, err
	}
	result := RevokeCurrentElevationResult{Session: command.Session}
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
		elevation, err := transaction.Elevations().GetForSessionScope(ctx, command.Session.Snapshot().ID, command.Scope, service.clock.Now())
		if errors.Is(err, ErrElevationDenied) || errors.Is(err, ErrElevationExpired) || errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if validateErr := elevation.Validate(command.Session, enrollmentVersionOf(enrollment), command.Scope, service.clock.Now()); validateErr != nil {
			if errors.Is(validateErr, ErrElevationDenied) || errors.Is(validateErr, ErrElevationExpired) {
				return nil
			}
			return validateErr
		}
		if _, err = transaction.Elevations().RevokeCAS(ctx, elevation, service.clock.Now()); err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminElevationRevoked, "elevation_revoked", digestAdminRequest("admin.elevation.revoked", string(command.Scope)).Bytes()); err != nil {
			return err
		}
		result.Revoked = true
		return nil
	})
	return result, mapAdminUoWError(err)
}

func (service *Service) requireElevation(ctx context.Context, transaction Transaction, session Session, enrollmentVersion int64, scope ElevationScope, requestID string) error {
	elevation, err := transaction.Elevations().GetForSessionScope(ctx, session.Snapshot().ID, scope, service.clock.Now())
	if err != nil {
		if errors.Is(err, ErrElevationExpired) {
			_, _ = service.appendAdminAudit(ctx, transaction, session.Snapshot().AdminID, requestID, audit.TargetAdmin, session.Snapshot().AdminID.String(), audit.ActionAdminElevationExpired, "elevation_expired", digestAdminRequest("admin.elevation.expired", string(scope)).Bytes())
		}
		return err
	}
	if err = elevation.Validate(session, enrollmentVersion, scope, service.clock.Now()); err != nil {
		if errors.Is(err, ErrElevationExpired) {
			_, _ = service.appendAdminAudit(ctx, transaction, session.Snapshot().AdminID, requestID, audit.TargetAdmin, session.Snapshot().AdminID.String(), audit.ActionAdminElevationExpired, "elevation_expired", digestAdminRequest("admin.elevation.expired", string(scope)).Bytes())
		}
		return err
	}
	return nil
}
