package secretaccess

import (
	"bytes"
	"encoding/binary"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

const adminGrantDomain = "game-night:admin-result-grant:v1\x00"

// AdminGrant is an exact-result capability minted only after an administrator session is authenticated.
type AdminGrant struct {
	sessionID  uuid.UUID
	actorID    uuid.UUID
	resultID   uuid.UUID
	validUntil time.Time
	mac        security.MAC[security.AdminSessionKeyPurpose]
}

func MintAdminGrant(keyring *security.HMACKeyring[security.AdminSessionKeyPurpose], sessionID, actorID, resultID uuid.UUID, validUntil time.Time) (AdminGrant, error) {
	validUntil = canonicalGrantTime(validUntil)
	if keyring == nil || sessionID == uuid.Nil || actorID == uuid.Nil || resultID == uuid.Nil || validUntil.IsZero() {
		return AdminGrant{}, ErrInvalidGrant
	}
	claims := adminGrantClaims(sessionID, actorID, resultID, validUntil)
	mac, err := keyring.Sum(claims)
	clear(claims)
	if err != nil || mac.KeyVersion == 0 || len(mac.Value) == 0 {
		return AdminGrant{}, ErrInvalidGrant
	}
	return AdminGrant{sessionID: sessionID, actorID: actorID, resultID: resultID, validUntil: validUntil, mac: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: mac.KeyVersion, Value: bytes.Clone(mac.Value)}}, nil
}

func VerifyAdminGrant(keyring *security.HMACKeyring[security.AdminSessionKeyPurpose], grant AdminGrant, resultID, actorID uuid.UUID, now time.Time) bool {
	now = canonicalGrantTime(now)
	if keyring == nil || now.IsZero() || resultID == uuid.Nil || actorID == uuid.Nil || grant.sessionID == uuid.Nil ||
		grant.resultID != resultID || grant.actorID != actorID || !now.Before(grant.validUntil) || grant.mac.KeyVersion == 0 || len(grant.mac.Value) == 0 {
		return false
	}
	claims := adminGrantClaims(grant.sessionID, grant.actorID, grant.resultID, grant.validUntil)
	matched, err := keyring.Verify(claims, grant.mac)
	clear(claims)
	return err == nil && matched
}

func adminGrantClaims(sessionID, actorID, resultID uuid.UUID, validUntil time.Time) []byte {
	claims := make([]byte, 0, len(adminGrantDomain)+16+16+16+8)
	claims = append(claims, adminGrantDomain...)
	claims = append(claims, sessionID[:]...)
	claims = append(claims, actorID[:]...)
	claims = append(claims, resultID[:]...)
	return binary.BigEndian.AppendUint64(claims, uint64(canonicalGrantTime(validUntil).UnixMicro()))
}
