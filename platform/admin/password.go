package admin

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/iFTY-R/game-night/platform/security"
)

const (
	PasswordAlgorithmArgon2id = "argon2id"
	MinimumPasswordRunes      = 12
)

// PasswordHasher is the bounded Argon2 worker port; services never call x/crypto directly.
type PasswordHasher interface {
	Hash(context.Context, []byte) (string, error)
	VerifyOrDummy(context.Context, string, []byte) (bool, bool, error)
}

// PasswordPolicy centralizes length, username, and common-leak checks for every password mutation.
type PasswordPolicy struct {
	MinimumRunes uint
	Leaked       map[string]struct{}
}

func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{MinimumRunes: MinimumPasswordRunes, Leaked: map[string]struct{}{
		"password123": {}, "password1234": {}, "123456789012": {}, "qwertyuiop12": {}, "adminadmin12": {},
	}}
}

func (policy PasswordPolicy) Validate(username, password string) error {
	if policy.MinimumRunes == 0 {
		policy.MinimumRunes = MinimumPasswordRunes
	}
	if password == "" || !utf8.ValidString(password) || utf8.RuneCountInString(password) < int(policy.MinimumRunes) || utf8.RuneCountInString(password) > 1024 {
		return ErrPasswordPolicy
	}
	if strings.EqualFold(strings.TrimSpace(password), strings.TrimSpace(username)) {
		return ErrPasswordPolicy
	}
	if _, leaked := policy.Leaked[strings.ToLower(password)]; leaked {
		return ErrPasswordPolicy
	}
	return nil
}

// PasswordRecord stores the algorithm and encoded parameters needed for future rehash decisions.
type PasswordRecord struct {
	Hash       string
	Algorithm  string
	Parameters string
}

func NewPasswordRecord(hash string, params security.Argon2Params) (PasswordRecord, error) {
	if hash == "" || security.ValidateArgon2Hash(hash) != nil {
		return PasswordRecord{}, ErrIntegrity
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return PasswordRecord{}, ErrIntegrity
	}
	return PasswordRecord{Hash: hash, Algorithm: PasswordAlgorithmArgon2id, Parameters: string(encoded)}, nil
}

func ParsePasswordParameters(encoded string) (security.Argon2Params, error) {
	var params security.Argon2Params
	if encoded == "" || json.Unmarshal([]byte(encoded), &params) != nil || params.Validate() != nil {
		return security.Argon2Params{}, ErrIntegrity
	}
	return params, nil
}

func HashPassword(ctx context.Context, hasher PasswordHasher, policy PasswordPolicy, username, password string) (PasswordRecord, error) {
	if hasher == nil || ctx == nil || policy.Validate(username, password) != nil {
		return PasswordRecord{}, ErrPasswordPolicy
	}
	hash, err := hasher.Hash(ctx, []byte(password))
	if err != nil {
		return PasswordRecord{}, err
	}
	return NewPasswordRecord(hash, security.DefaultArgon2Params())
}

// VerifyPassword performs one Argon2 operation and reports whether a successful login should upgrade the hash.
func VerifyPassword(ctx context.Context, hasher PasswordHasher, stored PasswordRecord, password string) (matched, needsUpgrade bool, err error) {
	if hasher == nil || ctx == nil || stored.Algorithm != PasswordAlgorithmArgon2id {
		return false, false, ErrAuthentication
	}
	return hasher.VerifyOrDummy(ctx, stored.Hash, []byte(password))
}
