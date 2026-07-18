package admin

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

// AccountStatus is the closed administrator lifecycle persisted by the singleton row.
type AccountStatus string

const (
	AccountStatusBootstrapPending AccountStatus = "bootstrap_pending"
	AccountStatusSetupRequired    AccountStatus = "setup_required"
	AccountStatusActive           AccountStatus = "active"
	AccountStatusRecoveryPending  AccountStatus = "recovery_pending"
)

func (status AccountStatus) Valid() bool {
	return status == AccountStatusBootstrapPending || status == AccountStatusSetupRequired ||
		status == AccountStatusActive || status == AccountStatusRecoveryPending
}

// AccountSnapshot is the persistence-neutral administrator singleton representation.
type AccountSnapshot struct {
	ID                   uuid.UUID
	Username             string
	Status               AccountStatus
	PasswordHash         string
	PasswordAlgorithm    string
	PasswordParameters   string
	PasswordVersion      int64
	AdminVersion         int64
	LastAcceptedTOTPStep *int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Account is immutable; every security mutation returns a value that must be persisted by CAS.
type Account struct{ snapshot AccountSnapshot }

// NewBootstrapAccount creates the only legal pre-password account state.
func NewBootstrapAccount(id uuid.UUID, at time.Time) (Account, error) {
	return RestoreAccount(AccountSnapshot{
		ID: id, Username: "admin", Status: AccountStatusBootstrapPending,
		AdminVersion: 1, CreatedAt: at, UpdatedAt: at,
	})
}

// RestoreAccount validates singleton and generation invariants before authentication consumes them.
func RestoreAccount(snapshot AccountSnapshot) (Account, error) {
	snapshot.Username = strings.TrimSpace(snapshot.Username)
	snapshot.CreatedAt = canonicalAdminTime(snapshot.CreatedAt)
	snapshot.UpdatedAt = canonicalAdminTime(snapshot.UpdatedAt)
	if snapshot.ID == uuid.Nil || snapshot.Username != "admin" || !snapshot.Status.Valid() ||
		snapshot.AdminVersion <= 0 || snapshot.PasswordVersion < 0 || snapshot.CreatedAt.IsZero() ||
		snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return Account{}, ErrIntegrity
	}
	if snapshot.Status == AccountStatusBootstrapPending {
		if snapshot.PasswordHash != "" || snapshot.PasswordAlgorithm != "" || snapshot.PasswordParameters != "" || snapshot.PasswordVersion != 0 {
			return Account{}, ErrIntegrity
		}
	} else if snapshot.PasswordHash == "" || snapshot.PasswordAlgorithm == "" || snapshot.PasswordParameters == "" ||
		snapshot.PasswordVersion <= 0 {
		return Account{}, ErrIntegrity
	}
	if snapshot.PasswordAlgorithm != "" && snapshot.PasswordAlgorithm != PasswordAlgorithmArgon2id {
		return Account{}, ErrIntegrity
	}
	if snapshot.Status != AccountStatusBootstrapPending && security.ValidateArgon2Hash(snapshot.PasswordHash) != nil {
		return Account{}, ErrIntegrity
	}
	if snapshot.LastAcceptedTOTPStep != nil && *snapshot.LastAcceptedTOTPStep < 0 {
		return Account{}, ErrIntegrity
	}
	return Account{snapshot: snapshot}, nil
}

func (account Account) Snapshot() AccountSnapshot { return account.snapshot }

func (account Account) IsBootstrapPending() bool {
	return account.snapshot.Status == AccountStatusBootstrapPending
}

// WithPassword applies a validated hash while preserving the account status and incrementing generations.
func (account Account) WithPassword(hash, algorithm, parameters string, at time.Time) (Account, error) {
	if hash == "" || algorithm != PasswordAlgorithmArgon2id || parameters == "" {
		return Account{}, ErrInvalidInput
	}
	snapshot := account.Snapshot()
	// The first bootstrap password write is also the irreversible transition into setup_required.
	if snapshot.Status == AccountStatusBootstrapPending {
		snapshot.Status = AccountStatusSetupRequired
	}
	snapshot.PasswordHash, snapshot.PasswordAlgorithm, snapshot.PasswordParameters = hash, algorithm, parameters
	snapshot.PasswordVersion++
	snapshot.AdminVersion++
	snapshot.UpdatedAt = canonicalAdminTime(at)
	if snapshot.PasswordVersion <= 0 || snapshot.AdminVersion <= 0 || snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return Account{}, ErrConcurrentTransition
	}
	return RestoreAccount(snapshot)
}

// Transition changes only reviewed lifecycle states and always advances the account generation.
func (account Account) Transition(next AccountStatus, at time.Time) (Account, error) {
	current := account.snapshot.Status
	valid := (current == AccountStatusBootstrapPending && next == AccountStatusSetupRequired) ||
		(current == AccountStatusSetupRequired && next == AccountStatusActive) ||
		(current == AccountStatusActive && next == AccountStatusRecoveryPending) ||
		(current == AccountStatusRecoveryPending && next == AccountStatusActive)
	if !valid {
		return Account{}, ErrConcurrentTransition
	}
	snapshot := account.Snapshot()
	snapshot.Status = next
	snapshot.AdminVersion++
	snapshot.UpdatedAt = canonicalAdminTime(at)
	return RestoreAccount(snapshot)
}

// AcceptTOTPStep advances the replay floor without changing the account generation.
func (account Account) AcceptTOTPStep(step int64, at time.Time) (Account, error) {
	if step < 0 {
		return Account{}, ErrTOTPInvalid
	}
	if account.snapshot.LastAcceptedTOTPStep != nil && step <= *account.snapshot.LastAcceptedTOTPStep {
		return Account{}, ErrConcurrentTransition
	}
	snapshot := account.Snapshot()
	snapshot.LastAcceptedTOTPStep = &step
	snapshot.UpdatedAt = canonicalAdminTime(at)
	return RestoreAccount(snapshot)
}

// SessionKind controls both TTL and authorization; pending kinds never inherit full permissions.
type SessionKind string

const (
	SessionKindSetupPasswordPending  SessionKind = "setup_password_pending"
	SessionKindTOTPEnrollmentPending SessionKind = "totp_enrollment_pending"
	SessionKindMFAPending            SessionKind = "mfa_pending"
	SessionKindRecoveryPending       SessionKind = "recovery_pending"
	SessionKindFull                  SessionKind = "full"
)

func (kind SessionKind) Valid() bool {
	return kind == SessionKindSetupPasswordPending || kind == SessionKindTOTPEnrollmentPending ||
		kind == SessionKindMFAPending || kind == SessionKindRecoveryPending || kind == SessionKindFull
}

// SessionSnapshot is the persistence-neutral bearer and CSRF session representation.
type SessionSnapshot struct {
	ID                uuid.UUID
	AdminID           uuid.UUID
	Selector          string
	SecretMAC         security.MAC[security.AdminSessionKeyPurpose]
	CSRFHash          security.MAC[security.AdminSessionKeyPurpose]
	Kind              SessionKind
	AdminVersion      int64
	PasswordVersion   int64
	AttemptCount      uint32
	MaxAttempts       uint32
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         time.Time
	RevokeReason      string
}

// Session is immutable and must be touched through repository CAS when used by a request.
type Session struct{ snapshot SessionSnapshot }

func RestoreSession(snapshot SessionSnapshot) (Session, error) {
	snapshot.CreatedAt = canonicalAdminTime(snapshot.CreatedAt)
	snapshot.LastSeenAt = canonicalAdminTime(snapshot.LastSeenAt)
	snapshot.IdleExpiresAt = canonicalAdminTime(snapshot.IdleExpiresAt)
	snapshot.AbsoluteExpiresAt = canonicalAdminTime(snapshot.AbsoluteExpiresAt)
	snapshot.RevokedAt = canonicalAdminTime(snapshot.RevokedAt)
	selector, selectorErr := identifier.ParseSelector(snapshot.Selector)
	if snapshot.ID == uuid.Nil || snapshot.AdminID == uuid.Nil || selectorErr != nil || selector.ByteLength() != adminSessionSelectorBytes ||
		len(snapshot.SecretMAC.Value) != 32 || snapshot.SecretMAC.KeyVersion == 0 || len(snapshot.CSRFHash.Value) != 32 ||
		snapshot.CSRFHash.KeyVersion == 0 || !snapshot.Kind.Valid() || snapshot.AdminVersion <= 0 || snapshot.PasswordVersion < 0 ||
		snapshot.MaxAttempts == 0 || snapshot.AttemptCount > snapshot.MaxAttempts || snapshot.CreatedAt.IsZero() ||
		snapshot.LastSeenAt.Before(snapshot.CreatedAt) || !snapshot.IdleExpiresAt.After(snapshot.LastSeenAt) ||
		!snapshot.AbsoluteExpiresAt.After(snapshot.CreatedAt) || snapshot.IdleExpiresAt.After(snapshot.AbsoluteExpiresAt) {
		return Session{}, ErrIntegrity
	}
	if snapshot.RevokedAt.IsZero() != (snapshot.RevokeReason == "") {
		return Session{}, ErrIntegrity
	}
	return Session{snapshot: snapshot}, nil
}

func (session Session) Snapshot() SessionSnapshot { return session.snapshot }

func (session Session) Active(at time.Time) bool {
	now := canonicalAdminTime(at)
	return session.snapshot.RevokedAt.IsZero() && now.Before(session.snapshot.IdleExpiresAt) && now.Before(session.snapshot.AbsoluteExpiresAt)
}

func (session Session) Touch(at time.Time, idleTTL time.Duration) (Session, error) {
	now := canonicalAdminTime(at)
	if idleTTL <= 0 || !session.Active(now) {
		return Session{}, ErrSessionExpired
	}
	snapshot := session.Snapshot()
	snapshot.LastSeenAt = now
	snapshot.IdleExpiresAt = now.Add(idleTTL)
	if snapshot.IdleExpiresAt.After(snapshot.AbsoluteExpiresAt) {
		snapshot.IdleExpiresAt = snapshot.AbsoluteExpiresAt
	}
	return RestoreSession(snapshot)
}

func (session Session) Revoke(reason string, at time.Time) (Session, error) {
	if strings.TrimSpace(reason) == "" || session.snapshot.RevokedAt.IsZero() == false {
		return Session{}, ErrConcurrentTransition
	}
	snapshot := session.Snapshot()
	snapshot.RevokedAt, snapshot.RevokeReason = canonicalAdminTime(at), reason
	return RestoreSession(snapshot)
}

// EnrollmentStatus is the lifecycle of an encrypted TOTP seed.
type EnrollmentStatus string

const (
	EnrollmentStatusPending  EnrollmentStatus = "pending"
	EnrollmentStatusActive   EnrollmentStatus = "active"
	EnrollmentStatusDisabled EnrollmentStatus = "disabled"
	EnrollmentStatusExpired  EnrollmentStatus = "expired"
)

type EnrollmentSnapshot struct {
	ID           uuid.UUID
	AdminID      uuid.UUID
	Ciphertext   []byte
	Nonce        []byte
	KeyVersion   uint32
	Status       EnrollmentStatus
	AdminVersion int64
	OperationID  string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	ActivatedAt  time.Time
	DisabledAt   time.Time
}

type Enrollment struct{ snapshot EnrollmentSnapshot }

func RestoreEnrollment(snapshot EnrollmentSnapshot) (Enrollment, error) {
	snapshot.CreatedAt, snapshot.ExpiresAt = canonicalAdminTime(snapshot.CreatedAt), canonicalAdminTime(snapshot.ExpiresAt)
	snapshot.ActivatedAt, snapshot.DisabledAt = canonicalAdminTime(snapshot.ActivatedAt), canonicalAdminTime(snapshot.DisabledAt)
	if snapshot.ID == uuid.Nil || snapshot.AdminID == uuid.Nil || snapshot.KeyVersion == 0 || snapshot.AdminVersion <= 0 ||
		snapshot.OperationID == "" || snapshot.CreatedAt.IsZero() {
		return Enrollment{}, ErrIntegrity
	}
	switch snapshot.Status {
	case EnrollmentStatusPending:
		if len(snapshot.Ciphertext) == 0 || len(snapshot.Nonce) == 0 || !snapshot.ExpiresAt.After(snapshot.CreatedAt) || !snapshot.ActivatedAt.IsZero() || !snapshot.DisabledAt.IsZero() {
			return Enrollment{}, ErrIntegrity
		}
	case EnrollmentStatusActive:
		if len(snapshot.Ciphertext) == 0 || len(snapshot.Nonce) == 0 || snapshot.ExpiresAt != (time.Time{}) || snapshot.ActivatedAt.Before(snapshot.CreatedAt) || !snapshot.DisabledAt.IsZero() {
			return Enrollment{}, ErrIntegrity
		}
	case EnrollmentStatusDisabled, EnrollmentStatusExpired:
		if len(snapshot.Ciphertext) != 0 || len(snapshot.Nonce) != 0 ||
			(snapshot.Status == EnrollmentStatusDisabled && snapshot.DisabledAt.IsZero()) ||
			(snapshot.Status == EnrollmentStatusExpired && (snapshot.ExpiresAt.IsZero() || !snapshot.DisabledAt.IsZero())) {
			return Enrollment{}, ErrIntegrity
		}
	default:
		return Enrollment{}, ErrIntegrity
	}
	return Enrollment{snapshot: snapshot}, nil
}

func (enrollment Enrollment) Snapshot() EnrollmentSnapshot { return enrollment.snapshot }

func (enrollment Enrollment) Activate(at time.Time) (Enrollment, error) {
	if enrollment.snapshot.Status != EnrollmentStatusPending || !canonicalAdminTime(at).Before(enrollment.snapshot.ExpiresAt) {
		return Enrollment{}, ErrConcurrentTransition
	}
	snapshot := enrollment.Snapshot()
	snapshot.Status, snapshot.ExpiresAt, snapshot.ActivatedAt = EnrollmentStatusActive, time.Time{}, canonicalAdminTime(at)
	return RestoreEnrollment(snapshot)
}

func (enrollment Enrollment) Disable(at time.Time) (Enrollment, error) {
	if enrollment.snapshot.Status != EnrollmentStatusActive {
		return Enrollment{}, ErrConcurrentTransition
	}
	snapshot := enrollment.Snapshot()
	snapshot.Status, snapshot.Ciphertext, snapshot.Nonce, snapshot.DisabledAt = EnrollmentStatusDisabled, nil, nil, canonicalAdminTime(at)
	return RestoreEnrollment(snapshot)
}

// RecoveryCodeSnapshot stores one independently hashed code in a versioned set.
type RecoveryCodeStatus string

const (
	RecoveryCodeStatusActive   RecoveryCodeStatus = "active"
	RecoveryCodeStatusConsumed RecoveryCodeStatus = "consumed"
	RecoveryCodeStatusRevoked  RecoveryCodeStatus = "revoked"
)

type RecoveryCodeSnapshot struct {
	ID         uuid.UUID
	AdminID    uuid.UUID
	Selector   string
	SecretHash string
	SetVersion int64
	Status     RecoveryCodeStatus
	CreatedAt  time.Time
	ConsumedAt time.Time
	RevokedAt  time.Time
}

type RecoveryCode struct{ snapshot RecoveryCodeSnapshot }

func RestoreRecoveryCode(snapshot RecoveryCodeSnapshot) (RecoveryCode, error) {
	snapshot.CreatedAt, snapshot.ConsumedAt, snapshot.RevokedAt = canonicalAdminTime(snapshot.CreatedAt), canonicalAdminTime(snapshot.ConsumedAt), canonicalAdminTime(snapshot.RevokedAt)
	selector, selectorErr := identifier.ParseSelector(snapshot.Selector)
	if snapshot.ID == uuid.Nil || snapshot.AdminID == uuid.Nil || selectorErr != nil || selector.ByteLength() != adminRecoverySelectorBytes || snapshot.SecretHash == "" || snapshot.SetVersion <= 0 || snapshot.CreatedAt.IsZero() {
		return RecoveryCode{}, ErrIntegrity
	}
	switch snapshot.Status {
	case RecoveryCodeStatusActive:
		if security.ValidateArgon2Hash(snapshot.SecretHash) != nil || !snapshot.ConsumedAt.IsZero() || !snapshot.RevokedAt.IsZero() {
			return RecoveryCode{}, ErrIntegrity
		}
	case RecoveryCodeStatusConsumed:
		if security.ValidateArgon2Hash(snapshot.SecretHash) != nil || snapshot.ConsumedAt.Before(snapshot.CreatedAt) || !snapshot.RevokedAt.IsZero() {
			return RecoveryCode{}, ErrIntegrity
		}
	case RecoveryCodeStatusRevoked:
		if security.ValidateArgon2Hash(snapshot.SecretHash) != nil || snapshot.RevokedAt.Before(snapshot.CreatedAt) || !snapshot.ConsumedAt.IsZero() {
			return RecoveryCode{}, ErrIntegrity
		}
	default:
		return RecoveryCode{}, ErrIntegrity
	}
	return RecoveryCode{snapshot: snapshot}, nil
}

func (code RecoveryCode) Snapshot() RecoveryCodeSnapshot { return code.snapshot }

func (code RecoveryCode) Consume(at time.Time) (RecoveryCode, error) {
	if code.snapshot.Status != RecoveryCodeStatusActive {
		return RecoveryCode{}, ErrConcurrentTransition
	}
	snapshot := code.Snapshot()
	snapshot.Status, snapshot.ConsumedAt = RecoveryCodeStatusConsumed, canonicalAdminTime(at)
	return RestoreRecoveryCode(snapshot)
}

func (code RecoveryCode) Revoke(at time.Time) (RecoveryCode, error) {
	if code.snapshot.Status != RecoveryCodeStatusActive {
		return RecoveryCode{}, ErrConcurrentTransition
	}
	snapshot := code.Snapshot()
	snapshot.Status, snapshot.RevokedAt = RecoveryCodeStatusRevoked, canonicalAdminTime(at)
	return RestoreRecoveryCode(snapshot)
}

func canonicalAdminTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.Round(0).UTC().Truncate(time.Microsecond)
}
