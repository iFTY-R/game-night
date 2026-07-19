package postgres

import (
	"context"
	"errors"

	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrMissingReferencedKey fails closed when a mounted keyring cannot decrypt persisted material.
	ErrMissingReferencedKey = errors.New("keyring is missing a referenced historical version")
	// ErrKeyringReferenceUnavailable hides database details from readiness and startup callers.
	ErrKeyringReferenceUnavailable = errors.New("keyring reference check unavailable")
)

// KeyringReferenceChecker verifies every active PII/TOTP ciphertext version against mounted historical keyrings.
type KeyringReferenceChecker struct {
	queries *sqlcgen.Queries
	pii     versionKeyring
	totp    versionKeyring
}

type versionKeyring interface {
	Versions() []uint32
}

// NewKeyringReferenceChecker binds a read-only query surface to one loaded keyring bundle.
func NewKeyringReferenceChecker(pool *pgxpool.Pool, keys security.Keyrings) *KeyringReferenceChecker {
	if pool == nil {
		return nil
	}
	return &KeyringReferenceChecker{queries: sqlcgen.New(pool), pii: keys.PII, totp: keys.TOTP}
}

// NewOperationsKeyringReferenceChecker applies the same fail-closed startup check to the worker's reduced key bundle.
func NewOperationsKeyringReferenceChecker(pool *pgxpool.Pool, keys security.OperationsKeyrings) *KeyringReferenceChecker {
	if pool == nil {
		return nil
	}
	return &KeyringReferenceChecker{queries: sqlcgen.New(pool), pii: keys.PII, totp: keys.TOTP}
}

// Check rejects startup if any database ciphertext references a version absent from its domain keyring.
func (checker *KeyringReferenceChecker) Check(ctx context.Context) error {
	if checker == nil || checker.queries == nil || checker.pii == nil || checker.totp == nil {
		return ErrKeyringReferenceUnavailable
	}
	piiVersions, err := checker.queries.ListPIIKeyVersionsWithReferences(ctx)
	if err != nil {
		return ErrKeyringReferenceUnavailable
	}
	if !containsAll(checker.pii.Versions(), piiVersions) {
		return ErrMissingReferencedKey
	}
	totpVersions, err := checker.queries.ListTotpKeyVersionsWithReferences(ctx)
	if err != nil {
		return ErrKeyringReferenceUnavailable
	}
	if !containsAll(checker.totp.Versions(), totpVersions) {
		return ErrMissingReferencedKey
	}
	return nil
}

func containsAll(available []uint32, references []int32) bool {
	set := make(map[uint32]struct{}, len(available))
	for _, version := range available {
		set[version] = struct{}{}
	}
	for _, reference := range references {
		if reference <= 0 {
			return false
		}
		if _, exists := set[uint32(reference)]; !exists {
			return false
		}
	}
	return true
}
