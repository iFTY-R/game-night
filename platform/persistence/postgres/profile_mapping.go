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

func profileExportContextFromRow(row sqlcgen.ProfileExportContext) (profileDomain.ProfileExportContext, error) {
	if !row.ExportID.Valid || !row.CreatedByAdminID.Valid || !row.CreatedAt.Valid || !row.ExpiresAt.Valid ||
		row.SchemaVersion <= 0 || row.ItemCount < 0 {
		return profileDomain.ProfileExportContext{}, profileDomain.ErrProfileIntegrity
	}
	fields := make([]profileDomain.Field, len(row.RequestedFields))
	for index, field := range row.RequestedFields {
		fields[index] = profileDomain.Field(field)
	}
	snapshot := profileDomain.ProfileExportContextSnapshot{
		ExportID:         uuid.UUID(row.ExportID.Bytes),
		CreatedByAdminID: uuid.UUID(row.CreatedByAdminID.Bytes),
		FilterDigest:     append([]byte(nil), row.FilterDigest...),
		RequestedFields:  fields,
		SchemaVersion:    uint32(row.SchemaVersion),
		ItemCount:        uint64(row.ItemCount),
		Status:           profileDomain.ExportStatus(row.Status),
		Reason:           row.Reason,
		CreatedAt:        row.CreatedAt.Time,
		ExpiresAt:        row.ExpiresAt.Time,
	}
	if row.CompletedAt.Valid {
		snapshot.CompletedAt = row.CompletedAt.Time
	}
	if row.AbortedAt.Valid {
		snapshot.AbortedAt = row.AbortedAt.Time
	}
	if row.ExpiredAt.Valid {
		snapshot.ExpiredAt = row.ExpiredAt.Time
	}
	context, err := profileDomain.RestoreProfileExportContext(snapshot)
	if err != nil {
		return profileDomain.ProfileExportContext{}, profileDomain.ErrProfileIntegrity
	}
	return context, nil
}

func profileExportItemFromRow(row sqlcgen.ProfileExportItem) (profileDomain.ProfileExportItem, error) {
	if !row.ExportID.Valid || !row.UserID.Valid || row.Ordinal <= 0 || row.Username == "" {
		return profileDomain.ProfileExportItem{}, profileDomain.ErrProfileIntegrity
	}
	snapshot := profileDomain.ProfileExportItemSnapshot{
		ExportID: uuid.UUID(row.ExportID.Bytes),
		Ordinal:  uint64(row.Ordinal),
		UserID:   uuid.UUID(row.UserID.Bytes),
		Username: row.Username,
	}
	if row.ProfileVersion.Valid {
		if row.ProfileVersion.Int64 <= 0 || row.RealNameKeyVersion.Int32 <= 0 || !row.RealNameKeyVersion.Valid ||
			len(row.RealNameCiphertext) == 0 || len(row.RealNameNonce) == 0 {
			return profileDomain.ProfileExportItem{}, profileDomain.ErrProfileIntegrity
		}
		snapshot.ProfileVersion = uint64(row.ProfileVersion.Int64)
		snapshot.RealNameCiphertext = append([]byte(nil), row.RealNameCiphertext...)
		snapshot.RealNameNonce = append([]byte(nil), row.RealNameNonce...)
		snapshot.RealNameKeyVersion = uint32(row.RealNameKeyVersion.Int32)
	} else if row.RealNameKeyVersion.Valid || len(row.RealNameCiphertext) != 0 || len(row.RealNameNonce) != 0 {
		return profileDomain.ProfileExportItem{}, profileDomain.ErrProfileIntegrity
	}
	item, err := profileDomain.RestoreProfileExportItem(snapshot)
	if err != nil {
		return profileDomain.ProfileExportItem{}, profileDomain.ErrProfileIntegrity
	}
	return item, nil
}

func profileExportSourceFromRow(row sqlcgen.ListProfileExportSourcesRow) (profileDomain.ProfileExportSource, error) {
	if !row.UserID.Valid {
		return profileDomain.ProfileExportSource{}, profileDomain.ErrProfileIntegrity
	}
	username := ""
	if row.Username.Valid {
		username = row.Username.String
	}
	source := profileDomain.ProfileExportSource{UserID: uuid.UUID(row.UserID.Bytes), Username: username}
	if row.ProfileVersion.Valid {
		if row.ProfileVersion.Int64 <= 0 || !row.RealNameKeyVersion.Valid || row.RealNameKeyVersion.Int32 <= 0 ||
			len(row.RealNameCiphertext) == 0 || len(row.RealNameNonce) == 0 {
			return profileDomain.ProfileExportSource{}, profileDomain.ErrProfileIntegrity
		}
		value := profileDomain.EncryptedValue{KeyVersion: uint32(row.RealNameKeyVersion.Int32), Nonce: append([]byte(nil), row.RealNameNonce...), Ciphertext: append([]byte(nil), row.RealNameCiphertext...)}
		source.ProfileVersion = uint64(row.ProfileVersion.Int64)
		source.RealName = &value
	} else if row.RealNameKeyVersion.Valid || len(row.RealNameCiphertext) != 0 || len(row.RealNameNonce) != 0 {
		return profileDomain.ProfileExportSource{}, profileDomain.ErrProfileIntegrity
	}
	validated, err := profileDomain.RestoreExportSource(source)
	if err != nil {
		return profileDomain.ProfileExportSource{}, profileDomain.ErrProfileIntegrity
	}
	return validated, nil
}

func profileFieldsToStrings(fields []profileDomain.Field) ([]string, error) {
	if len(fields) == 0 {
		return nil, profileDomain.ErrInvalidProfileInput
	}
	result := make([]string, len(fields))
	for index, field := range fields {
		if !field.Valid() {
			return nil, profileDomain.ErrInvalidProfileInput
		}
		result[index] = string(field)
	}
	return result, nil
}
