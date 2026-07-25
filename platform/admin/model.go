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
	// AccountStatusBootstrapPending means the singleton account exists but no initial password has been written.
	AccountStatusBootstrapPending AccountStatus = "bootstrap_pending"
	// AccountStatusSetupRequired means the deployment password exists and the first browser login must replace it.
	AccountStatusSetupRequired AccountStatus = "setup_required"
	// AccountStatusActive means the administrator may log in normally; MFA is derived from active enrollment state instead.
	AccountStatusActive AccountStatus = "active"
)

// Valid reports whether the status belongs to the rebuilt admin console state machine.
func (status AccountStatus) Valid() bool {
	return status == AccountStatusBootstrapPending || status == AccountStatusSetupRequired || status == AccountStatusActive
}

// AccountSnapshot is the persistence-neutral administrator singleton representation.
type AccountSnapshot struct {
	ID                 uuid.UUID
	Username           string
	Status             AccountStatus
	PasswordHash       string
	PasswordAlgorithm  string
	PasswordParameters string
	PasswordVersion    int64
	AdminVersion       int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
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
	return Account{snapshot: snapshot}, nil
}

// Snapshot returns the persistence-ready immutable state.
func (account Account) Snapshot() AccountSnapshot { return account.snapshot }

// IsBootstrapPending reports whether the singleton account still lacks its initial password.
func (account Account) IsBootstrapPending() bool {
	return account.snapshot.Status == AccountStatusBootstrapPending
}

// WithPassword applies a validated hash while preserving MFA state outside the account aggregate.
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

// RecordMFAChange advances the account-wide security generation without coupling MFA state to the account row.
func (account Account) RecordMFAChange(at time.Time) (Account, error) {
	if account.snapshot.Status != AccountStatusActive {
		return Account{}, ErrConcurrentTransition
	}
	now := canonicalAdminTime(at)
	if now.IsZero() || now.Before(account.snapshot.UpdatedAt) {
		return Account{}, ErrConcurrentTransition
	}
	snapshot := account.Snapshot()
	snapshot.AdminVersion++
	snapshot.UpdatedAt = now
	return RestoreAccount(snapshot)
}

// Transition changes only the reviewed lifecycle states and always advances the admin generation.
func (account Account) Transition(next AccountStatus, at time.Time) (Account, error) {
	current := account.snapshot.Status
	valid := (current == AccountStatusBootstrapPending && next == AccountStatusSetupRequired) ||
		(current == AccountStatusSetupRequired && next == AccountStatusActive)
	if !valid {
		return Account{}, ErrConcurrentTransition
	}
	snapshot := account.Snapshot()
	snapshot.Status = next
	snapshot.AdminVersion++
	snapshot.UpdatedAt = canonicalAdminTime(at)
	return RestoreAccount(snapshot)
}

// SessionKind controls both TTL and authorization; only full sessions may execute business commands.
type SessionKind string

const (
	// SessionKindSetupPasswordPending can only complete the one-time initial password change flow.
	SessionKindSetupPasswordPending SessionKind = "setup_password_pending"
	// SessionKindMFAPending can only prove the second factor before a full session is issued.
	SessionKindMFAPending SessionKind = "mfa_pending"
	// SessionKindFull can execute authenticated admin commands according to granted permissions and elevations.
	SessionKindFull SessionKind = "full"
)

// Valid reports whether the session kind belongs to the rebuilt admin console state machine.
func (kind SessionKind) Valid() bool {
	return kind == SessionKindSetupPasswordPending || kind == SessionKindMFAPending || kind == SessionKindFull
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
	SessionVersion    int64
	ClientIP          string
	UserAgent         string
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

// RestoreSession validates one persisted session row before any transport or repository uses it.
func RestoreSession(snapshot SessionSnapshot) (Session, error) {
	snapshot.ClientIP = strings.TrimSpace(snapshot.ClientIP)
	snapshot.UserAgent = strings.TrimSpace(snapshot.UserAgent)
	snapshot.CreatedAt = canonicalAdminTime(snapshot.CreatedAt)
	snapshot.LastSeenAt = canonicalAdminTime(snapshot.LastSeenAt)
	snapshot.IdleExpiresAt = canonicalAdminTime(snapshot.IdleExpiresAt)
	snapshot.AbsoluteExpiresAt = canonicalAdminTime(snapshot.AbsoluteExpiresAt)
	snapshot.RevokedAt = canonicalAdminTime(snapshot.RevokedAt)
	selector, selectorErr := identifier.ParseSelector(snapshot.Selector)
	if snapshot.ID == uuid.Nil || snapshot.AdminID == uuid.Nil || selectorErr != nil || selector.ByteLength() != adminSessionSelectorBytes ||
		len(snapshot.SecretMAC.Value) != 32 || snapshot.SecretMAC.KeyVersion == 0 || len(snapshot.CSRFHash.Value) != 32 ||
		snapshot.CSRFHash.KeyVersion == 0 || !snapshot.Kind.Valid() || snapshot.AdminVersion <= 0 || snapshot.PasswordVersion < 0 ||
		snapshot.SessionVersion <= 0 || snapshot.MaxAttempts == 0 || snapshot.AttemptCount > snapshot.MaxAttempts ||
		snapshot.CreatedAt.IsZero() || snapshot.LastSeenAt.Before(snapshot.CreatedAt) ||
		!snapshot.IdleExpiresAt.After(snapshot.LastSeenAt) || !snapshot.AbsoluteExpiresAt.After(snapshot.CreatedAt) ||
		snapshot.IdleExpiresAt.After(snapshot.AbsoluteExpiresAt) {
		return Session{}, ErrIntegrity
	}
	if snapshot.RevokedAt.IsZero() != (snapshot.RevokeReason == "") {
		return Session{}, ErrIntegrity
	}
	return Session{snapshot: snapshot}, nil
}

// Snapshot returns the persistence-ready state without exposing bearer-proof backing storage.
func (session Session) Snapshot() SessionSnapshot {
	snapshot := session.snapshot
	snapshot.SecretMAC.Value = append([]byte(nil), snapshot.SecretMAC.Value...)
	snapshot.CSRFHash.Value = append([]byte(nil), snapshot.CSRFHash.Value...)
	return snapshot
}

// Active reports whether the session remains usable at the supplied application time.
func (session Session) Active(at time.Time) bool {
	now := canonicalAdminTime(at)
	return session.snapshot.RevokedAt.IsZero() && now.Before(session.snapshot.IdleExpiresAt) && now.Before(session.snapshot.AbsoluteExpiresAt)
}

// Touch advances the session version and last-seen fields without exceeding the absolute expiry boundary.
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
	snapshot.SessionVersion++
	return RestoreSession(snapshot)
}

// Revoke closes the session exactly once and advances the session version for later CAS writes.
func (session Session) Revoke(reason string, at time.Time) (Session, error) {
	if strings.TrimSpace(reason) == "" || session.snapshot.RevokedAt.IsZero() == false {
		return Session{}, ErrConcurrentTransition
	}
	snapshot := session.Snapshot()
	snapshot.RevokedAt, snapshot.RevokeReason = canonicalAdminTime(at), reason
	snapshot.SessionVersion++
	return RestoreSession(snapshot)
}

// EnrollmentStatus is the lifecycle of one encrypted TOTP seed.
type EnrollmentStatus string

const (
	// EnrollmentStatusPending means the seed exists only for enrollment confirmation until the short setup TTL ends.
	EnrollmentStatusPending EnrollmentStatus = "pending"
	// EnrollmentStatusActive means the seed may authenticate login and elevation TOTP challenges.
	EnrollmentStatusActive EnrollmentStatus = "active"
	// EnrollmentStatusDisabled means the seed has been destroyed and no longer enables MFA.
	EnrollmentStatusDisabled EnrollmentStatus = "disabled"
)

// Valid reports whether the status belongs to the rebuilt MFA enrollment state machine.
func (status EnrollmentStatus) Valid() bool {
	return status == EnrollmentStatusPending || status == EnrollmentStatusActive || status == EnrollmentStatusDisabled
}

// EnrollmentSnapshot is the persistence-neutral state of one TOTP enrollment operation.
type EnrollmentSnapshot struct {
	ID                uuid.UUID
	AdminID           uuid.UUID
	Ciphertext        []byte
	Nonce             []byte
	KeyVersion        uint32
	Status            EnrollmentStatus
	AdminVersion      int64
	EnrollmentVersion int64
	ReplayFloor       *int64
	OperationID       string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	ActivatedAt       time.Time
	DisabledAt        time.Time
}

// Enrollment is immutable; replay-floor advancement and state transitions must round-trip through CAS.
type Enrollment struct{ snapshot EnrollmentSnapshot }

// RestoreEnrollment validates one persisted enrollment row before any MFA flow consumes it.
func RestoreEnrollment(snapshot EnrollmentSnapshot) (Enrollment, error) {
	snapshot.Ciphertext = append([]byte(nil), snapshot.Ciphertext...)
	snapshot.Nonce = append([]byte(nil), snapshot.Nonce...)
	snapshot.OperationID = strings.TrimSpace(snapshot.OperationID)
	snapshot.CreatedAt = canonicalAdminTime(snapshot.CreatedAt)
	snapshot.ExpiresAt = canonicalAdminTime(snapshot.ExpiresAt)
	snapshot.ActivatedAt = canonicalAdminTime(snapshot.ActivatedAt)
	snapshot.DisabledAt = canonicalAdminTime(snapshot.DisabledAt)
	if snapshot.ID == uuid.Nil || snapshot.AdminID == uuid.Nil || snapshot.KeyVersion == 0 || !snapshot.Status.Valid() ||
		snapshot.AdminVersion <= 0 || snapshot.EnrollmentVersion <= 0 || snapshot.OperationID == "" || snapshot.CreatedAt.IsZero() {
		return Enrollment{}, ErrIntegrity
	}
	if snapshot.ReplayFloor != nil && *snapshot.ReplayFloor < 0 {
		return Enrollment{}, ErrIntegrity
	}
	switch snapshot.Status {
	case EnrollmentStatusPending:
		if len(snapshot.Ciphertext) == 0 || len(snapshot.Nonce) == 0 || !snapshot.ExpiresAt.After(snapshot.CreatedAt) ||
			!snapshot.ActivatedAt.IsZero() || !snapshot.DisabledAt.IsZero() || snapshot.ReplayFloor != nil {
			return Enrollment{}, ErrIntegrity
		}
	case EnrollmentStatusActive:
		if len(snapshot.Ciphertext) == 0 || len(snapshot.Nonce) == 0 || snapshot.ExpiresAt != (time.Time{}) ||
			snapshot.ActivatedAt.Before(snapshot.CreatedAt) || !snapshot.DisabledAt.IsZero() || snapshot.ReplayFloor == nil {
			return Enrollment{}, ErrIntegrity
		}
	case EnrollmentStatusDisabled:
		if len(snapshot.Ciphertext) != 0 || len(snapshot.Nonce) != 0 || snapshot.DisabledAt.IsZero() || snapshot.DisabledAt.Before(snapshot.CreatedAt) {
			return Enrollment{}, ErrIntegrity
		}
	default:
		return Enrollment{}, ErrIntegrity
	}
	return Enrollment{snapshot: snapshot}, nil
}

// Snapshot returns the persistence-ready state without exposing encrypted-seed backing storage.
func (enrollment Enrollment) Snapshot() EnrollmentSnapshot {
	snapshot := enrollment.snapshot
	snapshot.Ciphertext = append([]byte(nil), snapshot.Ciphertext...)
	snapshot.Nonce = append([]byte(nil), snapshot.Nonce...)
	return snapshot
}

// Active reports whether this enrollment currently enables MFA.
func (enrollment Enrollment) Active() bool {
	return enrollment.snapshot.Status == EnrollmentStatusActive
}

// Activate confirms the pending enrollment and binds it to the account generation produced by the MFA change.
func (enrollment Enrollment) Activate(step, adminVersion int64, at time.Time) (Enrollment, error) {
	now := canonicalAdminTime(at)
	if step < 0 || adminVersion != enrollment.snapshot.AdminVersion+1 || enrollment.snapshot.Status != EnrollmentStatusPending ||
		!now.Before(enrollment.snapshot.ExpiresAt) {
		return Enrollment{}, ErrConcurrentTransition
	}
	snapshot := enrollment.Snapshot()
	snapshot.Status = EnrollmentStatusActive
	snapshot.AdminVersion = adminVersion
	snapshot.ExpiresAt = time.Time{}
	snapshot.ActivatedAt = now
	snapshot.ReplayFloor = &step
	snapshot.EnrollmentVersion++
	return RestoreEnrollment(snapshot)
}

// AcceptTOTP advances the replay floor so future logins cannot reuse the same or older code window.
func (enrollment Enrollment) AcceptTOTP(step int64, at time.Time) (Enrollment, error) {
	now := canonicalAdminTime(at)
	if step < 0 || now.IsZero() || enrollment.snapshot.Status != EnrollmentStatusActive ||
		enrollment.snapshot.ReplayFloor == nil || step <= *enrollment.snapshot.ReplayFloor {
		return Enrollment{}, ErrConcurrentTransition
	}
	snapshot := enrollment.Snapshot()
	snapshot.ReplayFloor = &step
	snapshot.EnrollmentVersion++
	return RestoreEnrollment(snapshot)
}

// Disable destroys the active seed and binds the tombstone to the account generation produced by the MFA change.
func (enrollment Enrollment) Disable(adminVersion int64, at time.Time) (Enrollment, error) {
	if enrollment.snapshot.Status != EnrollmentStatusActive || adminVersion != enrollment.snapshot.AdminVersion+1 {
		return Enrollment{}, ErrConcurrentTransition
	}
	snapshot := enrollment.Snapshot()
	snapshot.Status = EnrollmentStatusDisabled
	snapshot.AdminVersion = adminVersion
	snapshot.Ciphertext = nil
	snapshot.Nonce = nil
	snapshot.DisabledAt = canonicalAdminTime(at)
	snapshot.EnrollmentVersion++
	return RestoreEnrollment(snapshot)
}

// RecoveryCodeStatus stores the one-time lifecycle of a recovery code within one rotated set.
type RecoveryCodeStatus string

const (
	// RecoveryCodeStatusActive means the code may still substitute for one allowed TOTP challenge.
	RecoveryCodeStatusActive RecoveryCodeStatus = "active"
	// RecoveryCodeStatusConsumed means the code has already been used and may never verify again.
	RecoveryCodeStatusConsumed RecoveryCodeStatus = "consumed"
	// RecoveryCodeStatusRevoked means a later regeneration or MFA disable operation invalidated the code.
	RecoveryCodeStatusRevoked RecoveryCodeStatus = "revoked"
)

// RecoveryCodeSnapshot stores one independently hashed code in a versioned set.
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

// RecoveryCode is immutable; every consume or revoke operation returns a new value for CAS persistence.
type RecoveryCode struct{ snapshot RecoveryCodeSnapshot }

// RestoreRecoveryCode validates one persisted recovery code row.
func RestoreRecoveryCode(snapshot RecoveryCodeSnapshot) (RecoveryCode, error) {
	snapshot.CreatedAt = canonicalAdminTime(snapshot.CreatedAt)
	snapshot.ConsumedAt = canonicalAdminTime(snapshot.ConsumedAt)
	snapshot.RevokedAt = canonicalAdminTime(snapshot.RevokedAt)
	selector, selectorErr := identifier.ParseSelector(snapshot.Selector)
	if snapshot.ID == uuid.Nil || snapshot.AdminID == uuid.Nil || selectorErr != nil ||
		selector.ByteLength() != adminRecoverySelectorBytes || snapshot.SecretHash == "" || snapshot.SetVersion <= 0 ||
		snapshot.CreatedAt.IsZero() {
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

// Snapshot returns the persistence-ready immutable state.
func (code RecoveryCode) Snapshot() RecoveryCodeSnapshot { return code.snapshot }

// Consume marks the code spent so it may never substitute for TOTP again.
func (code RecoveryCode) Consume(at time.Time) (RecoveryCode, error) {
	if code.snapshot.Status != RecoveryCodeStatusActive {
		return RecoveryCode{}, ErrConcurrentTransition
	}
	snapshot := code.Snapshot()
	snapshot.Status = RecoveryCodeStatusConsumed
	snapshot.ConsumedAt = canonicalAdminTime(at)
	return RestoreRecoveryCode(snapshot)
}

// Revoke invalidates the code because MFA was disabled or a new set replaced the old one.
func (code RecoveryCode) Revoke(at time.Time) (RecoveryCode, error) {
	if code.snapshot.Status != RecoveryCodeStatusActive {
		return RecoveryCode{}, ErrConcurrentTransition
	}
	snapshot := code.Snapshot()
	snapshot.Status = RecoveryCodeStatusRevoked
	snapshot.RevokedAt = canonicalAdminTime(at)
	return RestoreRecoveryCode(snapshot)
}

func canonicalAdminTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.Round(0).UTC().Truncate(time.Microsecond)
}
