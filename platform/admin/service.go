package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

// NextStep is the transport-neutral state machine result consumed by Connect adapters.
type NextStep string

const (
	NextStepChangePassword NextStep = "change_password"
	NextStepEnrollTOTP     NextStep = "enroll_totp"
	NextStepVerifyMFA      NextStep = "verify_mfa"
	NextStepRebindTOTP     NextStep = "rebind_totp"
	NextStepAuthenticated  NextStep = "authenticated"
)

type SetupState string

const (
	SetupStateBootstrapPending SetupState = "bootstrap_pending"
	SetupStateSetupRequired    SetupState = "setup_required"
	SetupStateActive           SetupState = "active"
)

const (
	adminSecretResultVersion uint32 = 1
	adminSecretResultTTL            = 10 * time.Minute
)

// OperationResult identifies one committed secret envelope and whether the response was replayed.
type OperationResult struct {
	OperationID     idempotency.OperationID
	ResultID        uuid.UUID
	SecretExpiresAt time.Time
	Replayed        bool
}

// ServiceDependencies makes security wiring explicit and prevents accidental use of process globals.
type ServiceDependencies struct {
	Challenge      *ChallengeService
	Passwords      PasswordHasher
	PasswordPolicy PasswordPolicy
	TOTP           *TOTPService
	Sessions       *SessionService
	RecoveryCodes  *RecoveryCodeService
	Results        *secretresult.Service
	Clock          clock.Clock
	UnitOfWork     UnitOfWork
	Limiter        ratelimit.RateLimiter
}

// Service coordinates administrator authentication workflows while repositories own durable CAS.
type Service struct {
	challenge      *ChallengeService
	passwords      PasswordHasher
	passwordPolicy PasswordPolicy
	totp           *TOTPService
	sessions       *SessionService
	recoveryCodes  *RecoveryCodeService
	results        *secretresult.Service
	clock          clock.Clock
	unitOfWork     UnitOfWork
	limiter        ratelimit.RateLimiter
}

func NewService(deps ServiceDependencies) (*Service, error) {
	if deps.Challenge == nil || deps.Passwords == nil || deps.TOTP == nil || deps.Sessions == nil || deps.RecoveryCodes == nil || deps.Results == nil ||
		deps.Clock == nil || deps.UnitOfWork == nil || deps.Limiter == nil {
		return nil, ErrInvalidInput
	}
	if deps.PasswordPolicy.MinimumRunes == 0 {
		deps.PasswordPolicy = DefaultPasswordPolicy()
	}
	return &Service{
		challenge: deps.Challenge, passwords: deps.Passwords, passwordPolicy: deps.PasswordPolicy, totp: deps.TOTP,
		sessions: deps.Sessions, recoveryCodes: deps.RecoveryCodes, results: deps.Results, clock: deps.Clock, unitOfWork: deps.UnitOfWork, limiter: deps.Limiter,
	}, nil
}

// GetSetupState reads the singleton without exposing password or generation metadata.
func (service *Service) GetSetupState(ctx context.Context) (SetupState, error) {
	if service == nil || ctx == nil || service.unitOfWork == nil {
		return "", ErrRepositoryUnavailable
	}
	var state SetupState
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		switch account.Snapshot().Status {
		case AccountStatusBootstrapPending:
			state = SetupStateBootstrapPending
		case AccountStatusSetupRequired:
			state = SetupStateSetupRequired
		case AccountStatusActive, AccountStatusRecoveryPending:
			state = SetupStateActive
		default:
			return ErrIntegrity
		}
		return nil
	})
	return state, mapAdminUoWError(err)
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
		snapshot := account.Snapshot()
		if snapshot.Status != AccountStatusSetupRequired && snapshot.Status != AccountStatusActive {
			return ErrUnavailable
		}
		issued, err = service.challenge.Issue(ChallengePurposeLogin, snapshot.ID, snapshot.AdminVersion, snapshot.PasswordVersion, request.CanonicalOrigin, request.RequestFlowID, request.MaxAttempts)
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

type LoginPasswordCommand struct {
	Credentials     challenge.Credentials
	Password        string
	OperationID     idempotency.OperationID
	RequestDigest   idempotency.Digest
	CanonicalOrigin string
	RequestFlowID   challenge.RequestFlowID
	ClientIP        string
}

type LoginPasswordResult struct {
	NextStep  NextStep
	Session   IssuedSession
	ExpiresAt time.Time
}

// LoginPassword verifies the first factor and creates either a setup or MFA-pending session.
func (service *Service) LoginPassword(ctx context.Context, command LoginPasswordCommand) (LoginPasswordResult, error) {
	if !command.OperationID.Valid() || command.ClientIP == "" {
		return LoginPasswordResult{}, ErrInvalidInput
	}
	account, err := service.readAccount(ctx)
	if err != nil {
		return LoginPasswordResult{}, err
	}
	if err := service.consumePasswordLimit(ctx, command.ClientIP, account.Snapshot().ID.String()); err != nil {
		return LoginPasswordResult{}, err
	}
	// Verify the expensive first factor before entering the challenge transaction. A mismatch is
	// deliberately re-submitted as an invalid proof below so the challenge attempt CAS commits.
	matched, needsUpgrade, verifyErr := VerifyPassword(ctx, service.passwords, PasswordRecord{Hash: account.Snapshot().PasswordHash, Algorithm: account.Snapshot().PasswordAlgorithm, Parameters: account.Snapshot().PasswordParameters}, command.Password)
	if verifyErr != nil {
		return LoginPasswordResult{}, verifyErr
	}
	var result LoginPasswordResult
	challengeUOW := adminChallengeUnitOfWork{parent: service.unitOfWork}
	credentials := command.Credentials
	if !matched {
		credentials.BodyProof = ""
	}
	_, err = service.challenge.AuthorizePersistent(ctx, challengeUOW, ChallengePurposeLogin, account.Snapshot().ID, account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion, command.CanonicalOrigin, command.RequestFlowID, credentials, command.OperationID, command.RequestDigest,
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
				currentAccount, hashErr = adminTransaction.Accounts().UpdatePasswordCAS(ctx, currentAccount, upgraded.Hash, upgraded.Algorithm, upgraded.Parameters, service.clock.Now())
				if hashErr != nil {
					return AuthorizedChallengeCompletion{}, hashErr
				}
			}
			kind := SessionKindMFAPending
			result.NextStep = NextStepVerifyMFA
			if currentAccount.Snapshot().Status == AccountStatusSetupRequired {
				kind, result.NextStep = SessionKindSetupPasswordPending, NextStepChangePassword
			}
			issued, issueErr := service.sessions.Issue(currentAccount.Snapshot().ID, kind, currentAccount.Snapshot().AdminVersion, currentAccount.Snapshot().PasswordVersion, service.clock.Now())
			if issueErr != nil {
				return AuthorizedChallengeCompletion{}, issueErr
			}
			if insertErr := adminTransaction.Sessions().Insert(ctx, issued.Session); insertErr != nil {
				return AuthorizedChallengeCompletion{}, insertErr
			}
			result.Session, result.ExpiresAt = issued, issued.Session.Snapshot().AbsoluteExpiresAt
			return NoReplayCompletion(), nil
		})
	if err != nil {
		return LoginPasswordResult{}, normalizeAuthError(err)
	}
	return result, nil
}

type VerifyTOTPCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
	Code         string
	ClientIP     string
}

type SessionResult struct {
	Session IssuedSession
}

// VerifyTotp atomically consumes the accepted moving-factor step and replaces MFA-pending with full access.
func (service *Service) VerifyTotp(ctx context.Context, command VerifyTOTPCommand) (SessionResult, error) {
	if command.ClientIP == "" || command.Session.Snapshot().Kind != SessionKindMFAPending {
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
		secret, err := service.totp.DecryptSeed(uuidToArray(account.Snapshot().ID), uuidToArray(es.ID), security.Encrypted[security.TOTPKeyPurpose]{KeyVersion: es.KeyVersion, Nonce: es.Nonce, Ciphertext: es.Ciphertext})
		if err != nil {
			return ErrTOTPInvalid
		}
		step, err := VerifyTOTPCode(secret, command.Code, service.clock.Now())
		if err != nil {
			return err
		}
		if _, err = transaction.Accounts().AcceptTOTPStepCAS(ctx, account, step, service.clock.Now()); err != nil {
			return err
		}
		issued, err := service.sessions.Issue(account.Snapshot().ID, SessionKindFull, account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion, service.clock.Now())
		if err != nil {
			return err
		}
		if err = transaction.Sessions().Insert(ctx, issued.Session); err != nil {
			return err
		}
		if _, err = transaction.Sessions().RevokeCAS(ctx, command.Session, "mfa_completed", service.clock.Now()); err != nil {
			return err
		}
		result.Session = issued
		return nil
	})
	return result, mapAdminUoWError(err)
}

type ChangePasswordCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
	Current      string
	New          string
	ClientIP     string
}

// ChangeInitialPassword replaces the bootstrap password and narrows the session to TOTP enrollment.
func (service *Service) ChangeInitialPassword(ctx context.Context, command ChangePasswordCommand) (SessionResult, error) {
	return service.changePassword(ctx, command, true)
}

// ChangeAdminPassword is available in full and recovery-pending sessions, with recovery remaining restricted.
func (service *Service) ChangeAdminPassword(ctx context.Context, command ChangePasswordCommand) (SessionResult, error) {
	return service.changePassword(ctx, command, false)
}

func (service *Service) changePassword(ctx context.Context, command ChangePasswordCommand, initial bool) (SessionResult, error) {
	kind := command.Session.Snapshot().Kind
	if initial && kind != SessionKindSetupPasswordPending || !initial && kind != SessionKindRecoveryPending && kind != SessionKindFull {
		return SessionResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return SessionResult{}, err
	}
	if command.ClientIP == "" {
		return SessionResult{}, ErrInvalidInput
	}
	if err := service.consumePasswordLimit(ctx, command.ClientIP, command.Session.Snapshot().AdminID.String()); err != nil {
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
		if command.Current != "" {
			matched, _, verifyErr := VerifyPassword(ctx, service.passwords, PasswordRecord{Hash: account.Snapshot().PasswordHash, Algorithm: account.Snapshot().PasswordAlgorithm, Parameters: account.Snapshot().PasswordParameters}, command.Current)
			if verifyErr != nil || !matched {
				return ErrAuthentication
			}
		}
		record, err := HashPassword(ctx, service.passwords, service.passwordPolicy, account.Snapshot().Username, command.New)
		if err != nil {
			return err
		}
		updated, err := transaction.Accounts().UpdatePasswordCAS(ctx, account, record.Hash, record.Algorithm, record.Parameters, service.clock.Now())
		if err != nil {
			return err
		}
		nextKind := SessionKindTOTPEnrollmentPending
		if !initial {
			nextKind = SessionKindRecoveryPending
		}
		issued, err := service.sessions.Issue(updated.Snapshot().ID, nextKind, updated.Snapshot().AdminVersion, updated.Snapshot().PasswordVersion, service.clock.Now())
		if err != nil {
			return err
		}
		if err = transaction.Sessions().Insert(ctx, issued.Session); err != nil {
			return err
		}
		_, err = transaction.Sessions().RevokeCAS(ctx, command.Session, "password_changed", service.clock.Now())
		result.Session = issued
		return err
	})
	return result, mapAdminUoWError(err)
}

type BeginEnrollmentCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
	OperationID  idempotency.OperationID
}

type EnrollmentResult struct {
	Enrollment Enrollment
	Operation  OperationResult
	Secret     string
	URI        string
}

// BeginTotpEnrollment creates the one pending encrypted seed per account.
func (service *Service) BeginTotpEnrollment(ctx context.Context, command BeginEnrollmentCommand) (EnrollmentResult, error) {
	if !command.OperationID.Valid() || command.Session.Snapshot().Kind != SessionKindTOTPEnrollmentPending && command.Session.Snapshot().Kind != SessionKindRecoveryPending && command.Session.Snapshot().Kind != SessionKindFull {
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
		scope := secretresult.ScopeAdminTOTPRebind
		if command.Session.Snapshot().Kind == SessionKindTOTPEnrollmentPending {
			scope = secretresult.ScopeAdminTOTPEnrollment
		}
		binding := adminResultBinding(scope, account.Snapshot().ID, command.OperationID, digestAdminRequest("admin.totp_enrollment", string(command.Session.Snapshot().Kind)), secretresult.ResultTypeAdminTOTPEnrollment)
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
			envelope, decodeErr := decodeTOTPEnrollmentEnvelope(plaintext)
			if decodeErr != nil {
				return decodeErr
			}
			result = EnrollmentResult{Operation: adminOperationResult(command.OperationID, existing, true), Secret: envelope.Secret, URI: envelope.URI}
			return nil
		}
		if !errors.Is(getErr, secretresult.ErrNotFound) {
			return getErr
		}
		if !sessionMatchesAccount(command.Session, account) {
			return ErrAuthentication
		}
		enrollmentID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		secret, uri, encrypted, err := service.totp.NewEnrollmentSecret(uuidToArray(account.Snapshot().ID), uuidToArray(enrollmentID), "Game Night", account.Snapshot().Username)
		if err != nil {
			return err
		}
		enrollment, err := RestoreEnrollment(EnrollmentSnapshot{ID: enrollmentID, AdminID: account.Snapshot().ID, Ciphertext: encrypted.Ciphertext, Nonce: encrypted.Nonce, KeyVersion: encrypted.KeyVersion, Status: EnrollmentStatusPending, AdminVersion: account.Snapshot().AdminVersion, OperationID: command.OperationID.Value(), CreatedAt: service.clock.Now(), ExpiresAt: service.clock.Now().Add(AdminSetupSessionTTL)})
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
		result = EnrollmentResult{Enrollment: stored, Operation: adminOperationResult(command.OperationID, storedResult, false), Secret: secret, URI: uri}
		return nil
	})
	return result, mapAdminUoWError(err)
}

type CompleteEnrollmentCommand struct {
	Session                  Session
	SessionToken             string
	CSRFToken                string
	EnrollmentOperationID    string
	RecoveryCodesOperationID idempotency.OperationID
	TOTPPasscode             string
}

type CompleteEnrollmentResult struct {
	Operation     OperationResult
	Session       IssuedSession
	RecoveryCodes []string
}

// CompleteTotpEnrollment verifies the first code, atomically activates the seed, and rotates recovery codes.
func (service *Service) CompleteTotpEnrollment(ctx context.Context, command CompleteEnrollmentCommand) (CompleteEnrollmentResult, error) {
	if command.EnrollmentOperationID == "" || !command.RecoveryCodesOperationID.Valid() || command.Session.Snapshot().Kind != SessionKindTOTPEnrollmentPending && command.Session.Snapshot().Kind != SessionKindRecoveryPending && command.Session.Snapshot().Kind != SessionKindFull {
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
		scope := secretresult.ScopeAdminTOTPRebind
		if command.Session.Snapshot().Kind == SessionKindTOTPEnrollmentPending {
			scope = secretresult.ScopeAdminInitialRecoveryCodes
		}
		binding := adminResultBinding(scope, account.Snapshot().ID, command.RecoveryCodesOperationID, digestAdminRequest("admin.recovery_codes", command.EnrollmentOperationID, string(command.Session.Snapshot().Kind)), secretresult.ResultTypeAdminRecoveryCodes)
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
			clear(secret)
			if parseErr != nil {
				return ErrIntegrity
			}
			storedSession, sessionErr := transaction.Sessions().GetForUpdate(ctx, selector)
			if sessionErr != nil {
				return sessionErr
			}
			result = CompleteEnrollmentResult{Operation: adminOperationResult(command.RecoveryCodesOperationID, existing, true), Session: IssuedSession{Session: storedSession, Token: envelope.SessionToken, CSRFToken: envelope.CSRFToken}, RecoveryCodes: envelope.RecoveryCodes}
			return nil
		}
		if !errors.Is(getErr, secretresult.ErrNotFound) {
			return getErr
		}
		if !sessionMatchesAccount(command.Session, account) {
			return ErrAuthentication
		}
		enrollment, err := transaction.Enrollments().GetPendingForUpdate(ctx, account.Snapshot().ID)
		if err != nil || enrollment.Snapshot().OperationID != command.EnrollmentOperationID {
			return ErrTOTPInvalid
		}
		es := enrollment.Snapshot()
		secret, err := service.totp.DecryptSeed(uuidToArray(account.Snapshot().ID), uuidToArray(es.ID), security.Encrypted[security.TOTPKeyPurpose]{KeyVersion: es.KeyVersion, Nonce: es.Nonce, Ciphertext: es.Ciphertext})
		if err != nil {
			return err
		}
		// Rebinding from a full session must retire the old seed before the partial unique index can activate the new one.
		if command.Session.Snapshot().Kind == SessionKindFull {
			if active, activeErr := transaction.Enrollments().GetActiveForUpdate(ctx, account.Snapshot().ID); activeErr == nil {
				if _, activeErr = transaction.Enrollments().DisableCAS(ctx, active, service.clock.Now()); activeErr != nil {
					return activeErr
				}
			}
		}
		step, err := VerifyTOTPCode(secret, command.TOTPPasscode, service.clock.Now())
		if err != nil {
			return err
		}
		if _, err = transaction.Accounts().AcceptTOTPStepCAS(ctx, account, step, service.clock.Now()); err != nil {
			return err
		}
		if _, err = transaction.Enrollments().ActivateCAS(ctx, enrollment, service.clock.Now()); err != nil {
			return err
		}
		setVersion := account.Snapshot().PasswordVersion
		if _, err = transaction.RecoveryCodes().RevokeAllSets(ctx, account.Snapshot().ID, service.clock.Now()); err != nil {
			return err
		}
		issuedCodes, err := service.recoveryCodes.IssueSet(ctx, account.Snapshot().ID, setVersion, service.clock.Now())
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
		updated, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if updated.Snapshot().Status != AccountStatusActive {
			updated, err = updated.Transition(AccountStatusActive, service.clock.Now())
			if err != nil {
				return err
			}
			updated, err = transaction.Accounts().TransitionStatusCAS(ctx, account, AccountStatusActive, service.clock.Now())
			if err != nil {
				return err
			}
		}
		issued, err := service.sessions.Issue(updated.Snapshot().ID, SessionKindFull, updated.Snapshot().AdminVersion, updated.Snapshot().PasswordVersion, service.clock.Now())
		if err != nil {
			return err
		}
		if err = transaction.Sessions().Insert(ctx, issued.Session); err != nil {
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
		result.Operation, result.Session, result.RecoveryCodes = adminOperationResult(command.RecoveryCodesOperationID, storedResult, false), issued, codes
		return nil
	})
	return result, mapAdminUoWError(err)
}

type RecoverCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
	Code         string
	ClientIP     string
}

// RecoverAdmin consumes a recovery code only inside a password-authenticated MFA-pending session.
func (service *Service) RecoverAdmin(ctx context.Context, command RecoverCommand) (SessionResult, error) {
	if command.Session.Snapshot().Kind != SessionKindMFAPending || command.ClientIP == "" {
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
		selector, parsedSecret, parseErr := parseRecoveryCode(command.Code)
		clear(parsedSecret)
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
		if active, activeErr := transaction.Enrollments().GetActiveForUpdate(ctx, account.Snapshot().ID); activeErr == nil {
			if _, activeErr = transaction.Enrollments().DisableCAS(ctx, active, service.clock.Now()); activeErr != nil {
				return activeErr
			}
		}
		updated, err := transaction.Accounts().TransitionStatusCAS(ctx, account, AccountStatusRecoveryPending, service.clock.Now())
		if err != nil {
			return err
		}
		if _, err = transaction.Sessions().RevokeAll(ctx, account.Snapshot().ID, "admin_recovery", service.clock.Now()); err != nil {
			return err
		}
		issued, err := service.sessions.Issue(updated.Snapshot().ID, SessionKindRecoveryPending, updated.Snapshot().AdminVersion, updated.Snapshot().PasswordVersion, service.clock.Now())
		if err != nil {
			return err
		}
		if err = transaction.Sessions().Insert(ctx, issued.Session); err != nil {
			return err
		}
		result.Session = issued
		return nil
	})
	return result, mapAdminUoWError(err)
}

type RegenerateRecoveryCodesCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
	OperationID  idempotency.OperationID
	TOTPPasscode string
	ClientIP     string
}

type RegenerateRecoveryCodesResult struct {
	Operation     OperationResult
	RecoveryCodes []string
}

// RegenerateAdminRecoveryCodes requires a fresh monotonic TOTP step and atomically revokes all previous sets.
func (service *Service) RegenerateAdminRecoveryCodes(ctx context.Context, command RegenerateRecoveryCodesCommand) (RegenerateRecoveryCodesResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || !command.OperationID.Valid() || command.ClientIP == "" {
		return RegenerateRecoveryCodesResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return RegenerateRecoveryCodesResult{}, err
	}
	if err := service.consumeSecondFactorLimit(ctx, command.ClientIP, command.Session.Snapshot().AdminID.String(), "regenerate_recovery_codes"); err != nil {
		return RegenerateRecoveryCodesResult{}, err
	}
	var result RegenerateRecoveryCodesResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) {
			return ErrAuthentication
		}
		binding := adminResultBinding(secretresult.ScopeAdminRegenerateRecoveryCodes, account.Snapshot().ID, command.OperationID, digestAdminRequest("admin.regenerate_recovery_codes", command.TOTPPasscode), secretresult.ResultTypeAdminRecoveryCodes)
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
			result = RegenerateRecoveryCodesResult{Operation: adminOperationResult(command.OperationID, existing, true), RecoveryCodes: envelope.RecoveryCodes}
			return nil
		}
		if !errors.Is(getErr, secretresult.ErrNotFound) {
			return getErr
		}
		enrollment, err := transaction.Enrollments().GetActiveForUpdate(ctx, account.Snapshot().ID)
		if err != nil {
			return err
		}
		es := enrollment.Snapshot()
		seed, err := service.totp.DecryptSeed(uuidToArray(account.Snapshot().ID), uuidToArray(es.ID), security.Encrypted[security.TOTPKeyPurpose]{KeyVersion: es.KeyVersion, Nonce: es.Nonce, Ciphertext: es.Ciphertext})
		if err != nil {
			return err
		}
		step, err := VerifyTOTPCode(seed, command.TOTPPasscode, service.clock.Now())
		if err != nil {
			return err
		}
		if _, err = transaction.Accounts().AcceptTOTPStepCAS(ctx, account, step, service.clock.Now()); err != nil {
			return err
		}
		if _, err = transaction.RecoveryCodes().RevokeAllSets(ctx, account.Snapshot().ID, service.clock.Now()); err != nil {
			return err
		}
		issuedCodes, err := service.recoveryCodes.IssueSet(ctx, account.Snapshot().ID, account.Snapshot().PasswordVersion, service.clock.Now())
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
		result = RegenerateRecoveryCodesResult{Operation: adminOperationResult(command.OperationID, stored, false), RecoveryCodes: codes}
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
		confirmed = updated.Snapshot().Status == secretresult.StatusConfirmed
		return nil
	})
	return confirmed, mapAdminUoWError(err)
}

// LogoutAdmin revokes exactly one authenticated session.
func (service *Service) LogoutAdmin(ctx context.Context, session Session, token, csrfToken string) error {
	if err := service.sessions.Authenticate(session, token, csrfToken, service.clock.Now()); err != nil {
		return err
	}
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		_, err := transaction.Sessions().RevokeCAS(ctx, session, "logout", service.clock.Now())
		return err
	})
	return mapAdminUoWError(err)
}

// LogoutAllAdminSessions revokes every session after authenticating the caller.
func (service *Service) LogoutAllAdminSessions(ctx context.Context, session Session, token, csrfToken string) (int64, error) {
	if err := service.sessions.Authenticate(session, token, csrfToken, service.clock.Now()); err != nil {
		return 0, err
	}
	var count int64
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		var err error
		count, err = transaction.Sessions().RevokeAll(ctx, session.Snapshot().AdminID, "logout_all", service.clock.Now())
		return err
	})
	return count, mapAdminUoWError(err)
}

func (service *Service) readAccount(ctx context.Context) (Account, error) {
	var account Account
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		var err error
		account, err = transaction.Accounts().GetForUpdate(ctx)
		return err
	})
	return account, mapAdminUoWError(err)
}

func (service *Service) consumePasswordLimit(ctx context.Context, clientIP, adminID string) error {
	ip, err := ratelimit.NewBucketValue(clientIP)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	account, err := ratelimit.NewBucketValue(adminID)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	ipKey, err := ratelimit.NewBucketKey(ratelimit.DimensionIP, ip)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	accountKey, err := ratelimit.NewBucketKey(ratelimit.DimensionAdminAccount, account)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	policy, _ := ratelimit.PolicyFor(ratelimit.OperationAdminPasswordLogin)
	return policy.Consume(ctx, service.limiter, ipKey, accountKey)
}

func (service *Service) consumeSecondFactorLimit(ctx context.Context, clientIP, adminID, purpose string) error {
	ip, err := ratelimit.NewBucketValue(clientIP)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	account, err := ratelimit.NewBucketValue(adminID)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	flow, err := ratelimit.NewBucketValue(purpose)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	ipKey, _ := ratelimit.NewBucketKey(ratelimit.DimensionIP, ip)
	accountKey, _ := ratelimit.NewBucketKey(ratelimit.DimensionAdminAccount, account)
	flowKey, _ := ratelimit.NewBucketKey(ratelimit.DimensionFlowPurpose, flow)
	policy, _ := ratelimit.PolicyFor(ratelimit.OperationAdminSecondFactor)
	return policy.Consume(ctx, service.limiter, ipKey, accountKey, flowKey)
}

func mapAdminUoWError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrPasswordPolicy) || errors.Is(err, ErrTOTPInvalid) || errors.Is(err, ErrRecoveryInvalid) || errors.Is(err, ErrPermissionDenied) {
		return err
	}
	return err
}

func normalizeAuthError(err error) error {
	if errors.Is(err, challenge.ErrAuthentication) || errors.Is(err, ErrAuthentication) {
		return ErrAuthentication
	}
	return err
}

func sessionMatchesAccount(session Session, account Account) bool {
	sessionSnapshot, accountSnapshot := session.Snapshot(), account.Snapshot()
	return sessionSnapshot.AdminID == accountSnapshot.ID && sessionSnapshot.AdminVersion == accountSnapshot.AdminVersion && sessionSnapshot.PasswordVersion == accountSnapshot.PasswordVersion
}

func adminResultBinding(scope secretresult.Scope, actorID uuid.UUID, operationID idempotency.OperationID, digest idempotency.Digest, resultType secretresult.ResultType) secretresult.Binding {
	return secretresult.Binding{Key: secretresult.Key{Scope: scope, ActorID: actorID, OperationID: operationID}, RequestDigest: digest, ResultType: resultType, ResultVersion: adminSecretResultVersion}
}

func adminOperationResult(operationID idempotency.OperationID, result secretresult.Result, replayed bool) OperationResult {
	snapshot := result.Snapshot()
	return OperationResult{OperationID: operationID, ResultID: snapshot.ID, SecretExpiresAt: snapshot.SecretExpiresAt, Replayed: replayed}
}

func digestAdminRequest(domain string, fields ...string) idempotency.Digest {
	hash := sha256.New()
	appendAdminDigestField(hash, domain)
	for _, field := range fields {
		appendAdminDigestField(hash, field)
	}
	var digest idempotency.Digest
	copy(digest[:], hash.Sum(nil))
	return digest
}

type adminDigestWriter interface{ Write([]byte) (int, error) }

func appendAdminDigestField(writer adminDigestWriter, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}

type totpEnrollmentEnvelope struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

type adminRecoveryBundle struct {
	RecoveryCodes []string `json:"recovery_codes"`
	SessionToken  string   `json:"session_token"`
	CSRFToken     string   `json:"csrf_token"`
}

type adminRecoveryCodesEnvelope struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func decodeTOTPEnrollmentEnvelope(plaintext []byte) (totpEnrollmentEnvelope, error) {
	var envelope totpEnrollmentEnvelope
	if err := decodeAdminEnvelope(plaintext, &envelope); err != nil || envelope.Secret == "" || envelope.URI == "" {
		return totpEnrollmentEnvelope{}, ErrIntegrity
	}
	return envelope, nil
}

func decodeAdminRecoveryBundle(plaintext []byte) (adminRecoveryBundle, error) {
	var envelope adminRecoveryBundle
	if err := decodeAdminEnvelope(plaintext, &envelope); err != nil || len(envelope.RecoveryCodes) != AdminRecoveryCodeCount || envelope.SessionToken == "" || envelope.CSRFToken == "" {
		return adminRecoveryBundle{}, ErrIntegrity
	}
	return envelope, nil
}

func decodeAdminRecoveryCodesEnvelope(plaintext []byte) (adminRecoveryCodesEnvelope, error) {
	var envelope adminRecoveryCodesEnvelope
	if err := decodeAdminEnvelope(plaintext, &envelope); err != nil || len(envelope.RecoveryCodes) != AdminRecoveryCodeCount {
		return adminRecoveryCodesEnvelope{}, ErrIntegrity
	}
	return envelope, nil
}

func decodeAdminEnvelope(plaintext []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrIntegrity
	}
	return nil
}

func uuidToArray(value uuid.UUID) [16]byte { return [16]byte(value) }

type adminChallengeUnitOfWork struct{ parent UnitOfWork }

func (unitOfWork adminChallengeUnitOfWork) Run(ctx context.Context, work ChallengeTransactionWork) error {
	return unitOfWork.parent.Run(ctx, func(ctx context.Context, transaction Transaction) error { return work(ctx, transaction) })
}

var _ ChallengeUnitOfWork = adminChallengeUnitOfWork{}
