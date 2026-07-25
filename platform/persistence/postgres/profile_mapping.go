package postgres

import (
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	profileDomain "github.com/iFTY-R/game-night/platform/profile"
)

// profileFromRow validates every persisted invariant before exposing the
// encrypted payload to the profile service. A malformed row is an integrity
// failure, never an ordinary not-found result.
func profileFromRow(row sqlcgen.UserProfile) (profileDomain.UserProfile, error) {
	if !row.UserID.Valid || !row.RealNameUpdatedAt.Valid || !row.RealNameUpdatedBy.Valid ||
		row.RealNameKeyVersion <= 0 || row.ProfileVersion <= 0 {
		return profileDomain.UserProfile{}, profileDomain.ErrProfileIntegrity
	}
	profile, err := profileDomain.RestoreUserProfile(profileDomain.UserProfileSnapshot{
		UserID:             uuid.UUID(row.UserID.Bytes),
		RealNameCiphertext: append([]byte(nil), row.RealNameCiphertext...),
		RealNameNonce:      append([]byte(nil), row.RealNameNonce...),
		RealNameKeyVersion: uint32(row.RealNameKeyVersion),
		ProfileVersion:     uint64(row.ProfileVersion),
		RealNameUpdatedAt:  row.RealNameUpdatedAt.Time,
		RealNameUpdatedBy:  uuid.UUID(row.RealNameUpdatedBy.Bytes),
	})
	if err != nil {
		return profileDomain.UserProfile{}, profileDomain.ErrProfileIntegrity
	}
	return profile, nil
}
