// Package dice contains deterministic, IO-free primitives shared by dice game engines.
package dice

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	// MaxDicePerRoll is the defensive upper bound for one module-owned roll.
	MaxDicePerRoll uint32 = 128
	// faceRejectionLimit keeps modulo reduction unbiased for six equally likely faces.
	faceRejectionLimit byte = 252
	// maxSamplingAttempts prevents malformed inputs from creating an unbounded loop.
	maxSamplingAttempts uint32 = 1024
)

var (
	// ErrZeroSeed prevents callers from accidentally treating an uninitialized seed as entropy.
	ErrZeroSeed = errors.New("dice seed is empty")
	// ErrInvalidCount rejects zero or defensively oversized rolls.
	ErrInvalidCount = errors.New("dice count is invalid")
)

// Face is one canonical six-sided die value.
type Face uint8

// Valid reports whether the face can occur on a standard six-sided die.
func (face Face) Valid() bool { return face >= 1 && face <= 6 }

// Roll deterministically generates count faces from seed and stream without process-local randomness.
// Each die has an independent SHA-256 derivation, so extending a roll preserves its existing prefix.
func Roll(seed [32]byte, stream uint64, count uint32) ([]Face, error) {
	if isZeroSeed(seed) {
		return nil, ErrZeroSeed
	}
	if count == 0 || count > MaxDicePerRoll {
		return nil, ErrInvalidCount
	}

	faces := make([]Face, count)
	for dieIndex := uint32(0); dieIndex < count; dieIndex++ {
		face, ok := sampleFace(seed, stream, dieIndex)
		if !ok {
			return nil, ErrInvalidCount
		}
		faces[dieIndex] = face
	}
	return faces, nil
}

func sampleFace(seed [32]byte, stream uint64, dieIndex uint32) (Face, bool) {
	for attempt := uint32(0); attempt < maxSamplingAttempts; attempt++ {
		var input [32 + 8 + 4 + 4]byte
		copy(input[:32], seed[:])
		binary.LittleEndian.PutUint64(input[32:40], stream)
		binary.LittleEndian.PutUint32(input[40:44], dieIndex)
		binary.LittleEndian.PutUint32(input[44:48], attempt)
		digest := sha256.Sum256(input[:])
		for _, value := range digest {
			if value < faceRejectionLimit {
				return Face(value%6 + 1), true
			}
		}
	}
	return 0, false
}

func isZeroSeed(seed [32]byte) bool {
	for _, value := range seed {
		if value != 0 {
			return false
		}
	}
	return true
}
