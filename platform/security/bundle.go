package security

import (
	"crypto/sha256"
	"time"
)

// KeyringPaths names all independent secret files required by identity and administration processes.
type KeyringPaths struct {
	PII            string
	TOTP           string
	ResultEnvelope string
	Device         string
	RateLimit      string
	UserChallenge  string
	AdminChallenge string
	AdminSession   string
	Audit          string
}

// Keyrings preserves purpose types after startup validation so callers cannot cross encryption domains.
type Keyrings struct {
	PII            *AESKeyring[PIIKeyPurpose]
	TOTP           *AESKeyring[TOTPKeyPurpose]
	ResultEnvelope *AESKeyring[ResultEnvelopeKeyPurpose]
	Device         *HMACKeyring[DeviceHMACKeyPurpose]
	RateLimit      *HMACKeyring[RateLimitHMACKeyPurpose]
	UserChallenge  *HMACKeyring[UserChallengeKeyPurpose]
	AdminChallenge *HMACKeyring[AdminChallengeKeyPurpose]
	AdminSession   *HMACKeyring[AdminSessionKeyPurpose]
	Audit          *AuditKeyring
}

// LoadKeyrings loads every purpose before use and rejects key material reused across security domains.
func LoadKeyrings(paths KeyringPaths, now time.Time) (Keyrings, error) {
	pii, err := LoadAESKeyring[PIIKeyPurpose](paths.PII, now)
	if err != nil {
		return Keyrings{}, err
	}
	totp, err := LoadAESKeyring[TOTPKeyPurpose](paths.TOTP, now)
	if err != nil {
		return Keyrings{}, err
	}
	resultEnvelope, err := LoadAESKeyring[ResultEnvelopeKeyPurpose](paths.ResultEnvelope, now)
	if err != nil {
		return Keyrings{}, err
	}
	device, err := LoadHMACKeyring[DeviceHMACKeyPurpose](paths.Device, now)
	if err != nil {
		return Keyrings{}, err
	}
	rateLimit, err := LoadHMACKeyring[RateLimitHMACKeyPurpose](paths.RateLimit, now)
	if err != nil {
		return Keyrings{}, err
	}
	userChallenge, err := LoadHMACKeyring[UserChallengeKeyPurpose](paths.UserChallenge, now)
	if err != nil {
		return Keyrings{}, err
	}
	adminChallenge, err := LoadHMACKeyring[AdminChallengeKeyPurpose](paths.AdminChallenge, now)
	if err != nil {
		return Keyrings{}, err
	}
	adminSession, err := LoadHMACKeyring[AdminSessionKeyPurpose](paths.AdminSession, now)
	if err != nil {
		return Keyrings{}, err
	}
	audit, err := LoadAuditKeyring(paths.Audit, now)
	if err != nil {
		return Keyrings{}, err
	}

	seen := make(map[[sha256.Size]byte]struct{})
	for _, fingerprints := range [][][sha256.Size]byte{
		pii.keys.fingerprints(),
		totp.keys.fingerprints(),
		resultEnvelope.keys.fingerprints(),
		device.keys.fingerprints(),
		rateLimit.keys.fingerprints(),
		userChallenge.keys.fingerprints(),
		adminChallenge.keys.fingerprints(),
		adminSession.keys.fingerprints(),
		audit.fingerprints(),
	} {
		for _, fingerprint := range fingerprints {
			if _, duplicate := seen[fingerprint]; duplicate {
				return Keyrings{}, ErrInvalidKeyring
			}
			seen[fingerprint] = struct{}{}
		}
	}
	return Keyrings{
		PII:            pii,
		TOTP:           totp,
		ResultEnvelope: resultEnvelope,
		Device:         device,
		RateLimit:      rateLimit,
		UserChallenge:  userChallenge,
		AdminChallenge: adminChallenge,
		AdminSession:   adminSession,
		Audit:          audit,
	}, nil
}
