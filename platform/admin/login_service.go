package admin

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/security"
)

type LoginPasswordCommand struct {
	Credentials     challenge.Credentials
	Password        string
	OperationID     idempotency.OperationID
	RequestDigest   idempotency.Digest
	CanonicalOrigin string
	RequestFlowID   challenge.RequestFlowID
	RequestID       string
	ClientIP        string
	UserAgent       string
}

type LoginPasswordResult struct {
	Session                       IssuedSession
	RequiresInitialPasswordChange bool
	RequiresMFA                   bool
}

type VerifyTOTPCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
	Code         string
	RequestID    string
	ClientIP     string
}

type SessionResult struct {
	Session IssuedSession
}

type RecoverCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
	Code         string
	RequestID    string
	ClientIP     string
}

// GetCurrentAdminSession revalidates the live session without mutating expiry, cookies, or audit state.
func (service *Service) GetCurrentAdminSession(ctx context.Context, command CurrentSessionCommand) (CurrentSessionResult, error) {
	if service == nil || ctx == nil || service.sessions == nil || service.unitOfWork == nil || service.clock == nil {
		return CurrentSessionResult{}, ErrRepositoryUnavailable
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return CurrentSessionResult{}, err
	}
	var result CurrentSessionResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return ErrAuthentication
		}
		if !sessionMatchesAccount(command.Session, account) {
			return ErrAuthentication
		}
		view, err := service.buildSessionView(ctx, transaction, command.Session)
		if err != nil {
			return err
		}
		result = CurrentSessionResult{View: view}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// BeginAdminLogin issues a generation-bound challenge and persists only its HMAC digest.
func (service *Service) BeginAdminLogin(ctx context.Context, request AdminChallengeRequest) (IssuedChallenge, error) {
	if request.MaxAttempts == 0 {
		request.MaxAttempts = challenge.DefaultMaxAttempts
	}
	var issued IssuedChallenge
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if account.Snapshot().Status == AccountStatusBootstrapPending {
			return ErrUnavailable
		}
		issued, err = service.challenge.Issue(
			ChallengePurposeLogin,
			account.Snapshot().ID,
			account.Snapshot().AdminVersion,
			account.Snapshot().PasswordVersion,
			request.CanonicalOrigin,
			request.RequestFlowID,
			request.MaxAttempts,
		)
		if err != nil {
			return err
		}
		return transaction.Challenges().Insert(ctx, issued.Challenge)
	})
	if err != nil {
		return IssuedChallenge{}, mapAdminUoWError(err)
	}
	return issued, nil
}

// LoginPassword verifies the first factor and issues either a setup, MFA-pending, or full session.
func (service *Service) LoginPassword(ctx context.Context, command LoginPasswordCommand) (LoginPasswordResult, error) {
	if !command.OperationID.Valid() || strings.TrimSpace(command.ClientIP) == "" {
		return LoginPasswordResult{}, ErrInvalidInput
	}
	account, err := service.readAccount(ctx)
	if err != nil {
		return LoginPasswordResult{}, err
	}
	if err = service.consumePasswordLimit(ctx, command.ClientIP, account.Snapshot().ID.String()); err != nil {
		return LoginPasswordResult{}, err
	}
	matched, needsUpgrade, verifyErr := VerifyPassword(ctx, service.passwords, PasswordRecord{
		Hash: account.Snapshot().PasswordHash, Algorithm: account.Snapshot().PasswordAlgorithm, Parameters: account.Snapshot().PasswordParameters,
	}, command.Password)
	if verifyErr != nil {
		return LoginPasswordResult{}, verifyErr
	}

	var result LoginPasswordResult
	challengeUOW := adminChallengeUnitOfWork{parent: service.unitOfWork}
	credentials := command.Credentials
	if !matched {
		credentials.BodyProof = ""
	}
	_, err = service.challenge.AuthorizePersistent(
		ctx,
		challengeUOW,
		ChallengePurposeLogin,
		account.Snapshot().ID,
		account.Snapshot().AdminVersion,
		account.Snapshot().PasswordVersion,
		command.CanonicalOrigin,
		command.RequestFlowID,
		credentials,
		command.OperationID,
		command.RequestDigest,
		func(ctx context.Context, transaction ChallengeTransaction, _ Challenge, _ challenge.Authorization) (AuthorizedChallengeCompletion, error) {
			adminTransaction, ok := transaction.(Transaction)
			if !ok {
				return AuthorizedChallengeCompletion{}, ErrRepositoryUnavailable
			}
			if !matched {
				return AuthorizedChallengeCompletion{}, ErrAuthentication
			}
			currentAccount := account
			if needsUpgrade {
				upgraded, hashErr := HashPassword(ctx, service.passwords, service.passwordPolicy, account.Snapshot().Username, command.Password)
				if hashErr != nil {
					return AuthorizedChallengeCompletion{}, hashErr
				}
				currentAccount, hashErr = adminTransaction.Accounts().UpdatePasswordCAS(
					ctx, currentAccount, upgraded.Hash, upgraded.Algorithm, upgraded.Parameters, service.clock.Now(),
				)
				if hashErr != nil {
					return AuthorizedChallengeCompletion{}, hashErr
				}
			}
			activeEnrollment, enrollmentErr := service.loadActiveEnrollment(ctx, adminTransaction, currentAccount.Snapshot().ID)
			if enrollmentErr != nil {
				return AuthorizedChallengeCompletion{}, enrollmentErr
			}
			kind, stateErr := PasswordLoginSessionKind(currentAccount.Snapshot().Status, activeEnrollment != nil)
			if stateErr != nil {
				return AuthorizedChallengeCompletion{}, stateErr
			}
			issued, issueErr := service.sessions.IssueWithClient(
				currentAccount.Snapshot().ID,
				kind,
				currentAccount.Snapshot().AdminVersion,
				currentAccount.Snapshot().PasswordVersion,
				SessionClientMetadata{ClientIP: command.ClientIP, UserAgent: command.UserAgent},
				service.clock.Now(),
			)
			if issueErr != nil {
				return AuthorizedChallengeCompletion{}, issueErr
			}
			if insertErr := adminTransaction.Sessions().Insert(ctx, issued.Session); insertErr != nil {
				return AuthorizedChallengeCompletion{}, insertErr
			}
			switch kind {
			case SessionKindSetupPasswordPending:
				result.RequiresInitialPasswordChange = true
			case SessionKindMFAPending:
				result.RequiresMFA = true
			}
			result.Session = issued
			currentSnapshot := currentAccount.Snapshot()
			if kind != SessionKindMFAPending {
				if _, appendErr := service.appendAdminAudit(
					ctx,
					adminTransaction,
					currentSnapshot.ID,
					command.RequestID,
					audit.TargetAdmin,
					currentSnapshot.ID.String(),
					audit.ActionAdminLoginSucceeded,
					"password_verified",
					digestAdminRequest("admin.login.success", kind.String()).Bytes(),
				); appendErr != nil {
					return AuthorizedChallengeCompletion{}, appendErr
				}
			}
			return NoReplayCompletionAtGeneration(challenge.SubjectBinding{
				ID: currentSnapshot.ID, Version: currentSnapshot.AdminVersion, CredentialVersion: currentSnapshot.PasswordVersion,
			})
		},
	)
	if err != nil {
		if errors.Is(normalizeAuthError(err), ErrAuthentication) {
			_ = service.auditLoginFailure(ctx, account.Snapshot().ID, command.RequestID, "login_failed")
		}
		return LoginPasswordResult{}, normalizeAuthError(err)
	}
	return result, nil
}

// VerifyAdminTotp atomically consumes one TOTP step and replaces the MFA-pending session with a full session.
func (service *Service) VerifyAdminTotp(ctx context.Context, command VerifyTOTPCommand) (SessionResult, error) {
	if strings.TrimSpace(command.ClientIP) == "" || command.Session.Snapshot().Kind != SessionKindMFAPending {
		return SessionResult{}, ErrAuthentication
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return SessionResult{}, err
	}
	if err := service.consumeSecondFactorLimit(ctx, command.ClientIP, command.Session.Snapshot().AdminID.String(), "totp"); err != nil {
		return SessionResult{}, err
	}
	var result SessionResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) {
			return ErrAuthentication
		}
		enrollment, err := transaction.Enrollments().GetActiveForUpdate(ctx, account.Snapshot().ID)
		if err != nil {
			return err
		}
		es := enrollment.Snapshot()
		secret, err := service.totp.DecryptSeed(
			uuidToArray(account.Snapshot().ID),
			uuidToArray(es.ID),
			security.Encrypted[security.TOTPKeyPurpose]{KeyVersion: es.KeyVersion, Nonce: es.Nonce, Ciphertext: es.Ciphertext},
		)
		if err != nil {
			return ErrTOTPInvalid
		}
		step, err := VerifyTOTPCode(secret, command.Code, service.clock.Now())
		if err != nil {
			return err
		}
		accepted, err := transaction.Enrollments().AcceptTOTPCAS(ctx, enrollment, step, service.clock.Now())
		if err != nil {
			return err
		}
		issued, err := service.sessions.IssueWithClient(
			account.Snapshot().ID,
			SessionKindFull,
			account.Snapshot().AdminVersion,
			account.Snapshot().PasswordVersion,
			SessionClientMetadata{ClientIP: command.Session.Snapshot().ClientIP, UserAgent: command.Session.Snapshot().UserAgent},
			service.clock.Now(),
		)
		if err != nil {
			return err
		}
		if err = transaction.Sessions().Insert(ctx, issued.Session); err != nil {
			return err
		}
		if _, err = transaction.Sessions().RevokeCAS(ctx, command.Session, "mfa_completed", service.clock.Now()); err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(
			ctx,
			transaction,
			account.Snapshot().ID,
			command.RequestID,
			audit.TargetAdmin,
			account.Snapshot().ID.String(),
			audit.ActionAdminLoginSucceeded,
			"totp_verified",
			digestAdminRequest("admin.login.success", accepted.Snapshot().OperationID).Bytes(),
		); err != nil {
			return err
		}
		result = SessionResult{Session: issued}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// VerifyAdminRecoveryCode consumes one active recovery code and completes the MFA login flow.
func (service *Service) VerifyAdminRecoveryCode(ctx context.Context, command RecoverCommand) (SessionResult, error) {
	if command.Session.Snapshot().Kind != SessionKindMFAPending || strings.TrimSpace(command.ClientIP) == "" {
		return SessionResult{}, ErrAuthentication
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return SessionResult{}, err
	}
	if err := service.consumeSecondFactorLimit(ctx, command.ClientIP, command.Session.Snapshot().AdminID.String(), "recovery"); err != nil {
		return SessionResult{}, err
	}
	var result SessionResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) {
			return ErrAuthentication
		}
		recoveryState, stateErr := transaction.RecoveryCodes().GetActiveSetState(ctx, account.Snapshot().ID)
		if stateErr != nil {
			return stateErr
		}
		if recoveryState.RemainingActive == 0 {
			return ErrRecoveryCodeExhausted
		}
		selector, parsedSecret, parseErr := parseRecoveryCode(command.Code)
		clearRecoveryBytes(parsedSecret)
		if parseErr != nil {
			return ErrRecoveryInvalid
		}
		code, err := transaction.RecoveryCodes().FindActiveBySelector(ctx, selector)
		if err != nil {
			return ErrRecoveryInvalid
		}
		if err = service.recoveryCodes.Verify(ctx, code, command.Code); err != nil {
			return err
		}
		if _, err = transaction.RecoveryCodes().ConsumeCAS(ctx, code, service.clock.Now()); err != nil {
			return err
		}
		issued, err := service.sessions.IssueWithClient(
			account.Snapshot().ID,
			SessionKindFull,
			account.Snapshot().AdminVersion,
			account.Snapshot().PasswordVersion,
			SessionClientMetadata{ClientIP: command.Session.Snapshot().ClientIP, UserAgent: command.Session.Snapshot().UserAgent},
			service.clock.Now(),
		)
		if err != nil {
			return err
		}
		if err = transaction.Sessions().Insert(ctx, issued.Session); err != nil {
			return err
		}
		if _, err = transaction.Sessions().RevokeCAS(ctx, command.Session, "mfa_completed", service.clock.Now()); err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(
			ctx,
			transaction,
			account.Snapshot().ID,
			command.RequestID,
			audit.TargetAdmin,
			account.Snapshot().ID.String(),
			audit.ActionAdminRecoveryUsed,
			"mfa_login",
			digestAdminRequest("admin.recovery.used", code.Snapshot().ID.String()).Bytes(),
		); err != nil {
			return err
		}
		if _, err = service.appendAdminAudit(
			ctx,
			transaction,
			account.Snapshot().ID,
			command.RequestID,
			audit.TargetAdmin,
			account.Snapshot().ID.String(),
			audit.ActionAdminLoginSucceeded,
			"recovery_code_verified",
			digestAdminRequest("admin.login.success", "recovery").Bytes(),
		); err != nil {
			return err
		}
		result = SessionResult{Session: issued}
		return nil
	})
	return result, mapAdminUoWError(err)
}

func (service *Service) buildSessionView(ctx context.Context, transaction Transaction, session Session) (SessionView, error) {
	if service == nil || transaction == nil {
		return SessionView{}, ErrRepositoryUnavailable
	}
	enrollment, err := service.loadActiveEnrollment(ctx, transaction, session.Snapshot().AdminID)
	if err != nil {
		return SessionView{}, err
	}
	recoveryCodes := RecoveryCodeSetState{}
	if enrollment != nil {
		repository := transaction.RecoveryCodes()
		if repository == nil {
			return SessionView{}, ErrRepositoryUnavailable
		}
		recoveryCodes, err = repository.GetActiveSetState(ctx, session.Snapshot().AdminID)
		if err != nil {
			return SessionView{}, err
		}
	}
	elevations, err := service.loadElevationSet(ctx, transaction, session, enrollmentVersionOf(enrollment))
	if err != nil {
		return SessionView{}, err
	}
	permissions := PermissionSet{}
	if session.Snapshot().Kind == SessionKindFull {
		permissions = ActiveAdminPermissionSet()
	}
	return SessionView{
		Session:       session,
		Permissions:   permissions,
		Enrollment:    enrollment,
		Elevations:    elevations,
		RecoveryCodes: recoveryCodes,
	}, nil
}

func (service *Service) loadActiveEnrollment(ctx context.Context, transaction Transaction, adminID uuid.UUID) (*Enrollment, error) {
	repository := transaction.Enrollments()
	if repository == nil {
		return nil, nil
	}
	enrollment, err := repository.GetActiveForUpdate(ctx, adminID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &enrollment, nil
}

func (service *Service) loadElevationSet(ctx context.Context, transaction Transaction, session Session, enrollmentVersion int64) (ElevationSet, error) {
	repository := transaction.Elevations()
	if repository == nil || session.Snapshot().Kind != SessionKindFull {
		return ElevationSet{}, nil
	}
	elevations, err := repository.ListLiveForSessions(ctx, session.Snapshot().AdminID, []uuid.UUID{session.Snapshot().ID}, service.clock.Now())
	if err != nil {
		return ElevationSet{}, err
	}
	validated := make([]Elevation, 0, len(elevations))
	for _, elevation := range elevations {
		scope := elevation.Snapshot().Scope
		if validateErr := elevation.Validate(session, enrollmentVersion, scope, service.clock.Now()); validateErr != nil {
			if errors.Is(validateErr, ErrElevationDenied) || errors.Is(validateErr, ErrElevationExpired) {
				continue
			}
			return ElevationSet{}, validateErr
		}
		validated = append(validated, elevation)
	}
	return NewElevationSet(validated...)
}

func enrollmentVersionOf(enrollment *Enrollment) int64 {
	if enrollment == nil {
		return 0
	}
	return enrollment.Snapshot().EnrollmentVersion
}

func (kind SessionKind) String() string { return string(kind) }
