package admin

import (
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"regexp"
	"time"

	"github.com/iFTY-R/game-night/platform/security"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

const (
	TOTPPeriod      uint = 30
	TOTPSkew        uint = 1
	TOTPSecretBytes      = 20
)

var totpCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// TOTPService generates and verifies seeds while keeping ciphertext AAD bound to one admin enrollment.
type TOTPService struct {
	keyring *security.AESKeyring[security.TOTPKeyPurpose]
}

func NewTOTPService(keyring *security.AESKeyring[security.TOTPKeyPurpose]) (*TOTPService, error) {
	if keyring == nil {
		return nil, ErrInvalidInput
	}
	return &TOTPService{keyring: keyring}, nil
}

// ActiveKeyVersion returns the version used for new enrollments and rotation targets.
func (service *TOTPService) ActiveKeyVersion() uint32 {
	if service == nil || service.keyring == nil {
		return 0
	}
	return service.keyring.ActiveVersion()
}

// NewEnrollmentSecret creates a seed and encrypted storage payload; plaintext is returned only to the caller.
func (service *TOTPService) NewEnrollmentSecret(adminID, enrollmentID [16]byte, issuer, account string) (secret, uri string, encrypted security.Encrypted[security.TOTPKeyPurpose], err error) {
	if service == nil || service.keyring == nil || issuer == "" || account == "" {
		return "", "", security.Encrypted[security.TOTPKeyPurpose]{}, ErrInvalidInput
	}
	seed, err := security.RandomBytes(TOTPSecretBytes)
	if err != nil {
		return "", "", security.Encrypted[security.TOTPKeyPurpose]{}, err
	}
	defer clearBytes(seed)
	secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
	uri = fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30", issuer, account, secret, issuer)
	aad := totpAAD(adminID, enrollmentID, service.keyring.ActiveVersion())
	encrypted, err = service.keyring.Encrypt([]byte(secret), aad)
	if err != nil {
		return "", "", security.Encrypted[security.TOTPKeyPurpose]{}, err
	}
	return secret, uri, encrypted, nil
}

func (service *TOTPService) DecryptSeed(adminID, enrollmentID [16]byte, encrypted security.Encrypted[security.TOTPKeyPurpose]) (string, error) {
	if service == nil || service.keyring == nil {
		return "", ErrInvalidInput
	}
	plaintext, err := service.keyring.Decrypt(encrypted, totpAAD(adminID, enrollmentID, encrypted.KeyVersion))
	if err != nil {
		return "", ErrTOTPInvalid
	}
	defer clearBytes(plaintext)
	secret := string(plaintext)
	if !validTOTPSecret(secret) {
		return "", ErrIntegrity
	}
	return secret, nil
}

// ReencryptSeed authenticates the enrollment-specific old AAD before sealing with active-version AAD.
// Plaintext seed bytes remain local to this method and are cleared before returning to the worker.
func (service *TOTPService) ReencryptSeed(
	adminID, enrollmentID [16]byte,
	encrypted security.Encrypted[security.TOTPKeyPurpose],
) (security.Encrypted[security.TOTPKeyPurpose], error) {
	if service == nil || service.keyring == nil || encrypted.KeyVersion == 0 {
		return security.Encrypted[security.TOTPKeyPurpose]{}, ErrInvalidInput
	}
	plaintext, err := service.keyring.Decrypt(encrypted, totpAAD(adminID, enrollmentID, encrypted.KeyVersion))
	if err != nil {
		return security.Encrypted[security.TOTPKeyPurpose]{}, ErrTOTPInvalid
	}
	defer clearBytes(plaintext)
	if !validTOTPSecret(string(plaintext)) {
		return security.Encrypted[security.TOTPKeyPurpose]{}, ErrIntegrity
	}
	targetVersion := service.keyring.ActiveVersion()
	rotated, err := service.keyring.Encrypt(plaintext, totpAAD(adminID, enrollmentID, targetVersion))
	if err != nil || rotated.KeyVersion != targetVersion {
		return security.Encrypted[security.TOTPKeyPurpose]{}, ErrIntegrity
	}
	return rotated, nil
}

// VerifyCode returns the accepted moving-factor step, allowing PostgreSQL to enforce monotonic CAS.
func VerifyTOTPCode(secret, code string, now time.Time) (int64, error) {
	if !validTOTPSecret(secret) || !totpCodePattern.MatchString(code) {
		return 0, ErrTOTPInvalid
	}
	now = now.Round(0).UTC()
	current := now.Unix() / int64(TOTPPeriod)
	for offset := int64(-1); offset <= 1; offset++ {
		step := current + offset
		candidate, err := hotp.GenerateCodeCustom(secret, uint64(step), hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return 0, ErrTOTPInvalid
		}
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return step, nil
		}
	}
	return 0, ErrTOTPInvalid
}

// GenerateTOTPCode is kept for deterministic tests and local admin tooling.
func GenerateTOTPCode(secret string, at time.Time) (string, error) {
	if !validTOTPSecret(secret) {
		return "", ErrTOTPInvalid
	}
	return totp.GenerateCodeCustom(secret, at.Round(0).UTC(), totp.ValidateOpts{Period: TOTPPeriod, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
}

func validTOTPSecret(secret string) bool {
	if len(secret) < 16 || len(secret) > 128 {
		return false
	}
	_, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	return err == nil
}

func totpAAD(adminID, enrollmentID [16]byte, keyVersion uint32) []byte {
	return []byte(fmt.Sprintf("game-night/admin-totp/v1/%x/%x/%d", adminID, enrollmentID, keyVersion))
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
