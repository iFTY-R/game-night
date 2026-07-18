// Package secretaccess defines cryptographically authenticated authority for opening actor-owned result envelopes.
package secretaccess

import (
	"bytes"
	"encoding/binary"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

// deviceGrantDomain prevents a grant MAC from being reused as another device-authentication primitive.
const deviceGrantDomain = "game-night:device-result-grant:v1\x00"

// ErrInvalidGrant rejects malformed or unauthenticated result grants without exposing their claims.
var ErrInvalidGrant = errors.New("invalid result grant")

// DeviceGrant is a signed, exact-result capability. Private claims prevent mutation after minting.
type DeviceGrant struct {
	credentialID uuid.UUID
	generation   uint64
	actorID      uuid.UUID
	resultID     uuid.UUID
	validUntil   time.Time
	mac          security.MAC[security.DeviceHMACKeyPurpose]
}

// MintDeviceGrant signs exact credential, actor, result, generation, and expiry claims with the device keyring.
func MintDeviceGrant(
	keyring *security.HMACKeyring[security.DeviceHMACKeyPurpose],
	credentialID uuid.UUID,
	generation uint64,
	actorID, resultID uuid.UUID,
	validUntil time.Time,
) (DeviceGrant, error) {
	validUntil = canonicalGrantTime(validUntil)
	if keyring == nil || credentialID == uuid.Nil || generation == 0 || actorID == uuid.Nil ||
		resultID == uuid.Nil || validUntil.IsZero() {
		return DeviceGrant{}, ErrInvalidGrant
	}
	claims := deviceGrantClaims(credentialID, generation, actorID, resultID, validUntil)
	mac, err := keyring.Sum(claims)
	clear(claims)
	if err != nil || mac.KeyVersion == 0 || len(mac.Value) == 0 {
		return DeviceGrant{}, ErrInvalidGrant
	}
	return DeviceGrant{
		credentialID: credentialID, generation: generation, actorID: actorID,
		resultID: resultID, validUntil: validUntil,
		mac: security.MAC[security.DeviceHMACKeyPurpose]{KeyVersion: mac.KeyVersion, Value: bytes.Clone(mac.Value)},
	}, nil
}

// VerifyDeviceGrant authenticates the concrete private-state grant and applies its half-open exact binding.
func VerifyDeviceGrant(
	keyring *security.HMACKeyring[security.DeviceHMACKeyPurpose],
	grant DeviceGrant,
	resultID, actorID uuid.UUID,
	now time.Time,
) bool {
	now = canonicalGrantTime(now)
	if keyring == nil || now.IsZero() || resultID == uuid.Nil || actorID == uuid.Nil ||
		grant.credentialID == uuid.Nil || grant.generation == 0 || grant.resultID != resultID ||
		grant.actorID != actorID || !now.Before(grant.validUntil) || grant.mac.KeyVersion == 0 || len(grant.mac.Value) == 0 {
		return false
	}
	claims := deviceGrantClaims(grant.credentialID, grant.generation, grant.actorID, grant.resultID, grant.validUntil)
	matched, err := keyring.Verify(claims, grant.mac)
	clear(claims)
	return err == nil && matched
}

func deviceGrantClaims(
	credentialID uuid.UUID,
	generation uint64,
	actorID, resultID uuid.UUID,
	validUntil time.Time,
) []byte {
	claims := make([]byte, 0, len(deviceGrantDomain)+16+8+16+16+8)
	claims = append(claims, deviceGrantDomain...)
	claims = append(claims, credentialID[:]...)
	claims = binary.BigEndian.AppendUint64(claims, generation)
	claims = append(claims, actorID[:]...)
	claims = append(claims, resultID[:]...)
	return binary.BigEndian.AppendUint64(claims, uint64(canonicalGrantTime(validUntil).UnixMicro()))
}

func canonicalGrantTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}
