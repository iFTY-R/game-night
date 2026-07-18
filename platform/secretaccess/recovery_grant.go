package secretaccess

import (
	"bytes"
	"encoding/binary"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

// recoveryGrantDomain prevents short-lived recovery authority from crossing into challenge authentication.
const recoveryGrantDomain = "game-night:recovery-result-grant:v1\x00"

// RecoveryGrant is a signed exact-result capability minted only after a recovery attempt is authenticated.
type RecoveryGrant struct {
	attemptID  uuid.UUID
	actorID    uuid.UUID
	resultID   uuid.UUID
	validUntil time.Time
	mac        security.MAC[security.UserChallengeKeyPurpose]
}

// MintRecoveryGrant signs attempt, actor, result, and expiry claims with the user challenge keyring.
func MintRecoveryGrant(
	keyring *security.HMACKeyring[security.UserChallengeKeyPurpose],
	attemptID, actorID, resultID uuid.UUID,
	validUntil time.Time,
) (RecoveryGrant, error) {
	validUntil = canonicalGrantTime(validUntil)
	if keyring == nil || attemptID == uuid.Nil || actorID == uuid.Nil || resultID == uuid.Nil || validUntil.IsZero() {
		return RecoveryGrant{}, ErrInvalidGrant
	}
	claims := recoveryGrantClaims(attemptID, actorID, resultID, validUntil)
	mac, err := keyring.Sum(claims)
	clear(claims)
	if err != nil || mac.KeyVersion == 0 || len(mac.Value) == 0 {
		return RecoveryGrant{}, ErrInvalidGrant
	}
	return RecoveryGrant{
		attemptID: attemptID, actorID: actorID, resultID: resultID, validUntil: validUntil,
		mac: security.MAC[security.UserChallengeKeyPurpose]{KeyVersion: mac.KeyVersion, Value: bytes.Clone(mac.Value)},
	}, nil
}

// VerifyRecoveryGrant accepts only the concrete immutable capability and exact result/actor binding.
func VerifyRecoveryGrant(
	keyring *security.HMACKeyring[security.UserChallengeKeyPurpose],
	grant RecoveryGrant,
	resultID, actorID uuid.UUID,
	now time.Time,
) bool {
	now = canonicalGrantTime(now)
	if keyring == nil || now.IsZero() || resultID == uuid.Nil || actorID == uuid.Nil ||
		grant.attemptID == uuid.Nil || grant.resultID != resultID || grant.actorID != actorID ||
		!now.Before(grant.validUntil) || grant.mac.KeyVersion == 0 || len(grant.mac.Value) == 0 {
		return false
	}
	claims := recoveryGrantClaims(grant.attemptID, grant.actorID, grant.resultID, grant.validUntil)
	matched, err := keyring.Verify(claims, grant.mac)
	clear(claims)
	return err == nil && matched
}

func recoveryGrantClaims(attemptID, actorID, resultID uuid.UUID, validUntil time.Time) []byte {
	claims := make([]byte, 0, len(recoveryGrantDomain)+16+16+16+8)
	claims = append(claims, recoveryGrantDomain...)
	claims = append(claims, attemptID[:]...)
	claims = append(claims, actorID[:]...)
	claims = append(claims, resultID[:]...)
	return binary.BigEndian.AppendUint64(claims, uint64(canonicalGrantTime(validUntil).UnixMicro()))
}
