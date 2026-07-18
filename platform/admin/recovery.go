package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	AdminRecoveryCodeCount     = 10
	adminRecoveryCodeVersion   = "v1"
	adminRecoverySelectorBytes = 16
	adminRecoverySecretBytes   = 24
)

// RecoverySecretHasher is implemented by the same bounded Argon2 worker used for passwords.
type RecoverySecretHasher interface {
	Hash(context.Context, []byte) (string, error)
	VerifyOrDummy(context.Context, string, []byte) (bool, bool, error)
}

type IssuedRecoveryCode struct {
	Code   RecoveryCode
	Secret string
}

// RecoveryCodeService creates and verifies ten independently hashed one-time codes per set.
type RecoveryCodeService struct {
	hasher RecoverySecretHasher
}

func NewRecoveryCodeService(hasher RecoverySecretHasher) (*RecoveryCodeService, error) {
	if hasher == nil {
		return nil, ErrInvalidInput
	}
	return &RecoveryCodeService{hasher: hasher}, nil
}

func (service *RecoveryCodeService) IssueSet(ctx context.Context, adminID uuid.UUID, setVersion int64, at time.Time) ([]IssuedRecoveryCode, error) {
	if service == nil || service.hasher == nil || ctx == nil || adminID == uuid.Nil || setVersion <= 0 {
		return nil, ErrInvalidInput
	}
	issued := make([]IssuedRecoveryCode, 0, AdminRecoveryCodeCount)
	for index := 0; index < AdminRecoveryCodeCount; index++ {
		selectorEntropy, err := security.RandomBytes(adminRecoverySelectorBytes)
		if err != nil {
			return nil, err
		}
		selector, err := identifier.NewSelector(selectorEntropy)
		clearRecoveryBytes(selectorEntropy)
		if err != nil {
			return nil, ErrInvalidInput
		}
		secret, err := security.RandomBytes(adminRecoverySecretBytes)
		if err != nil {
			return nil, err
		}
		secretText := base64.RawURLEncoding.EncodeToString(secret)
		hashInput := recoveryHashInput(selector.Value(), secret)
		clearRecoveryBytes(secret)
		hash, err := service.hasher.Hash(ctx, hashInput)
		clearRecoveryBytes(hashInput)
		if err != nil {
			return nil, err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		code, err := RestoreRecoveryCode(RecoveryCodeSnapshot{ID: id, AdminID: adminID, Selector: selector.Value(), SecretHash: hash, SetVersion: setVersion, Status: RecoveryCodeStatusActive, CreatedAt: at})
		if err != nil {
			return nil, err
		}
		issued = append(issued, IssuedRecoveryCode{Code: code, Secret: fmt.Sprintf("%s.%s.%s", adminRecoveryCodeVersion, selector.Value(), secretText)})
	}
	return issued, nil
}

func (service *RecoveryCodeService) Verify(ctx context.Context, code RecoveryCode, submitted string) error {
	if service == nil || service.hasher == nil || ctx == nil {
		return ErrRecoveryInvalid
	}
	selector, secret, err := parseRecoveryCode(submitted)
	if err != nil {
		selector = "dummy"
		secret = make([]byte, adminRecoverySecretBytes)
	}
	defer clearRecoveryBytes(secret)
	snapshot := code.Snapshot()
	validSelector := snapshot.Status == RecoveryCodeStatusActive && snapshot.Selector == selector
	input := recoveryHashInput(selector, secret)
	defer clearRecoveryBytes(input)
	matched, _, verifyErr := service.hasher.VerifyOrDummy(ctx, valueIf(validSelector, snapshot.SecretHash), input)
	if verifyErr != nil || !validSelector || !matched {
		return ErrRecoveryInvalid
	}
	return nil
}

func parseRecoveryCode(value string) (string, []byte, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != adminRecoveryCodeVersion {
		return "", nil, ErrRecoveryInvalid
	}
	selector, err := identifier.ParseSelector(parts[1])
	if err != nil || selector.ByteLength() != adminRecoverySelectorBytes {
		return "", nil, ErrRecoveryInvalid
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(secret) != adminRecoverySecretBytes || base64.RawURLEncoding.EncodeToString(secret) != parts[2] {
		clearRecoveryBytes(secret)
		return "", nil, ErrRecoveryInvalid
	}
	return selector.Value(), secret, nil
}

func recoveryHashInput(selector string, secret []byte) []byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte("game-night/admin-recovery/v1/"))
	_, _ = digest.Write([]byte(selector))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(secret)
	return digest.Sum(nil)
}

func valueIf(condition bool, value string) string {
	if condition {
		return value
	}
	return ""
}

func clearRecoveryBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
