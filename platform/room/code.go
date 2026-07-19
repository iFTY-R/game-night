package room

import (
	"crypto/rand"
	"math/big"
)

const (
	// generatedRoomCodeLength keeps invitation codes short enough to enter manually while retaining collision retries.
	generatedRoomCodeLength = 6
	// roomCodeAlphabet excludes visually ambiguous characters used by zero/one and their letter counterparts.
	roomCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
)

// CodeGenerator supplies server-owned invitation codes and permits deterministic service tests.
type CodeGenerator interface {
	Generate() (string, error)
}

// SecureCodeGenerator reads operating-system entropy for production invitation codes.
type SecureCodeGenerator struct{}

// NewSecureCodeGenerator creates a stateless production generator.
func NewSecureCodeGenerator() *SecureCodeGenerator { return &SecureCodeGenerator{} }

// Generate samples each character without modulo bias.
func (*SecureCodeGenerator) Generate() (string, error) {
	result := make([]byte, generatedRoomCodeLength)
	limit := big.NewInt(int64(len(roomCodeAlphabet)))
	for index := range result {
		value, err := rand.Int(rand.Reader, limit)
		if err != nil {
			clear(result)
			return "", ErrInvalidRoomInput
		}
		result[index] = roomCodeAlphabet[value.Int64()]
	}
	return string(result), nil
}

var _ CodeGenerator = (*SecureCodeGenerator)(nil)
