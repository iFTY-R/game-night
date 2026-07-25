package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
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

// SessionView keeps auth-session introspection transport-neutral while preserving the domain aggregates.
type SessionView struct {
	Session       Session
	Permissions   PermissionSet
	Enrollment    *Enrollment
	Elevations    ElevationSet
	RecoveryCodes RecoveryCodeSetState
}

// ServiceDependencies makes security wiring explicit and prevents accidental use of process globals.
type ServiceDependencies struct {
	Challenge        *ChallengeService
	Passwords        PasswordHasher
	PasswordPolicy   PasswordPolicy
	TOTP             *TOTPService
	Sessions         *SessionService
	RecoveryCodes    *RecoveryCodeService
	Results          *secretresult.Service
	Clock            clock.Clock
	UnitOfWork       UnitOfWork
	Limiter          ratelimit.RateLimiter
	Audit            *audit.Service
	CheckpointHealth *audit.CheckpointHealthPolicy
}

// Service coordinates administrator authentication workflows while repositories own durable CAS.
type Service struct {
	challenge        *ChallengeService
	passwords        PasswordHasher
	passwordPolicy   PasswordPolicy
	totp             *TOTPService
	sessions         *SessionService
	recoveryCodes    *RecoveryCodeService
	results          *secretresult.Service
	clock            clock.Clock
	unitOfWork       UnitOfWork
	limiter          ratelimit.RateLimiter
	audit            *audit.Service
	checkpointHealth *audit.CheckpointHealthPolicy
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
		sessions: deps.Sessions, recoveryCodes: deps.RecoveryCodes, results: deps.Results, clock: deps.Clock,
		unitOfWork: deps.UnitOfWork, limiter: deps.Limiter, audit: deps.Audit, checkpointHealth: deps.CheckpointHealth,
	}, nil
}

type CurrentSessionCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
}

type CurrentSessionResult struct {
	View SessionView
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
		case AccountStatusActive:
			state = SetupStateActive
		default:
			return ErrIntegrity
		}
		return nil
	})
	return state, mapAdminUoWError(err)
}

func (service *Service) readAccount(ctx context.Context) (Account, error) {
	var account Account
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		var runErr error
		account, runErr = transaction.Accounts().GetForUpdate(ctx)
		return runErr
	})
	return account, mapAdminUoWError(err)
}

// ResolveSession loads one persisted session from the transport bearer without exposing token parsing internals.
func (service *Service) ResolveSession(ctx context.Context, token string) (Session, error) {
	if service == nil || ctx == nil || service.unitOfWork == nil {
		return Session{}, ErrRepositoryUnavailable
	}
	selector, secret, err := parseSessionToken(token)
	clearSessionBytes(secret)
	if err != nil {
		return Session{}, ErrAuthentication
	}
	var session Session
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		repository := transaction.Sessions()
		if repository == nil {
			return ErrRepositoryUnavailable
		}
		var runErr error
		session, runErr = repository.GetForUpdate(ctx, selector)
		return runErr
	})
	if errors.Is(err, ErrNotFound) {
		return Session{}, ErrAuthentication
	}
	return session, mapAdminUoWError(err)
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
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrPasswordPolicy) ||
		errors.Is(err, ErrTOTPInvalid) || errors.Is(err, ErrRecoveryInvalid) || errors.Is(err, ErrPermissionDenied) ||
		errors.Is(err, ErrElevationDenied) || errors.Is(err, ErrElevationExpired) || errors.Is(err, ErrIdempotencyConflict) ||
		errors.Is(err, ErrRecoveryCodeExhausted) || errors.Is(err, ErrMFAStateConflict) ||
		errors.Is(err, ErrUnavailable) || errors.Is(err, ErrConcurrentTransition) || errors.Is(err, audit.ErrSensitiveWriteBlocked) ||
		errors.Is(err, secretresult.ErrSecretNoLongerAvailable) || errors.Is(err, secretresult.ErrReplayUnauthorized) ||
		errors.Is(err, secretresult.ErrIdempotencyConflict) {
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

func normalizeSecurityReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", ErrInvalidInput
	}
	if strings.ContainsAny(reason, "\r\n\t") {
		return "", ErrInvalidInput
	}
	return reason, nil
}

func sessionMatchesAccount(session Session, account Account) bool {
	sessionSnapshot, accountSnapshot := session.Snapshot(), account.Snapshot()
	return sessionSnapshot.AdminID == accountSnapshot.ID &&
		sessionSnapshot.AdminVersion == accountSnapshot.AdminVersion &&
		sessionSnapshot.PasswordVersion == accountSnapshot.PasswordVersion
}

func adminResultBinding(scope secretresult.Scope, actorID uuid.UUID, operationID idempotency.OperationID, digest idempotency.Digest, resultType secretresult.ResultType) secretresult.Binding {
	return secretresult.Binding{
		Key:           secretresult.Key{Scope: scope, ActorID: actorID, OperationID: operationID},
		RequestDigest: digest,
		ResultType:    resultType,
		ResultVersion: adminSecretResultVersion,
	}
}

func adminOperationResult(operationID idempotency.OperationID, result secretresult.Result, replayed bool) OperationResult {
	snapshot := result.Snapshot()
	return OperationResult{
		OperationID:     operationID,
		ResultID:        snapshot.ID,
		SecretExpiresAt: snapshot.SecretExpiresAt,
		Replayed:        replayed,
	}
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
	if err := decodeAdminEnvelope(plaintext, &envelope); err != nil ||
		len(envelope.RecoveryCodes) != AdminRecoveryCodeCount || envelope.SessionToken == "" || envelope.CSRFToken == "" {
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

func encodeReceiptTarget(parts ...string) string {
	return strings.Join(parts, "|")
}

func decodeReceiptTarget(value string, expectedParts int) ([]string, error) {
	parts := strings.Split(value, "|")
	if len(parts) != expectedParts {
		return nil, ErrIntegrity
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return nil, ErrIntegrity
		}
	}
	return parts, nil
}

func parseReceiptUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil {
		return uuid.UUID{}, ErrIntegrity
	}
	return parsed, nil
}

func parseReceiptInt64(value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, ErrIntegrity
	}
	return parsed, nil
}

func parseReceiptBool(value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, ErrIntegrity
	}
}

func formatSessionViewResult(view SessionView) string {
	return fmt.Sprintf("%s/%d/%d", view.Session.Snapshot().ID, view.Session.Snapshot().AdminVersion, view.Session.Snapshot().SessionVersion)
}

type adminChallengeUnitOfWork struct{ parent UnitOfWork }

func (unitOfWork adminChallengeUnitOfWork) Run(ctx context.Context, work ChallengeTransactionWork) error {
	return unitOfWork.parent.Run(ctx, func(ctx context.Context, transaction Transaction) error { return work(ctx, transaction) })
}

var _ ChallengeUnitOfWork = adminChallengeUnitOfWork{}
