package postgres

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	profileDomain "github.com/iFTY-R/game-night/platform/profile"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type profileQueries interface {
	GetUserProfile(context.Context, sqlcgen.GetUserProfileParams) (sqlcgen.UserProfile, error)
	GetUserProfileForUpdate(context.Context, sqlcgen.GetUserProfileForUpdateParams) (sqlcgen.UserProfile, error)
	CreateUserProfile(context.Context, sqlcgen.CreateUserProfileParams) (sqlcgen.UserProfile, error)
	UpdateUserProfileCAS(context.Context, sqlcgen.UpdateUserProfileCASParams) (sqlcgen.UserProfile, error)
	CreateProfileExportContext(context.Context, sqlcgen.CreateProfileExportContextParams) (sqlcgen.ProfileExportContext, error)
	CreateProfileExportItem(context.Context, sqlcgen.CreateProfileExportItemParams) (sqlcgen.ProfileExportItem, error)
	GetProfileExportContextForUpdate(context.Context, sqlcgen.GetProfileExportContextForUpdateParams) (sqlcgen.ProfileExportContext, error)
	ListProfileExportItems(context.Context, sqlcgen.ListProfileExportItemsParams) ([]sqlcgen.ProfileExportItem, error)
	ListProfileExportSources(context.Context, sqlcgen.ListProfileExportSourcesParams) ([]sqlcgen.ListProfileExportSourcesRow, error)
	CompleteProfileExportContextCAS(context.Context, sqlcgen.CompleteProfileExportContextCASParams) (sqlcgen.CompleteProfileExportContextCASRow, error)
	AbortProfileExportContextCAS(context.Context, sqlcgen.AbortProfileExportContextCASParams) (sqlcgen.AbortProfileExportContextCASRow, error)
	ExpireProfileExportContextCAS(context.Context, sqlcgen.ExpireProfileExportContextCASParams) (sqlcgen.ExpireProfileExportContextCASRow, error)
}

type profileRepository struct{ queries profileQueries }

func (repository *profileRepository) GetByID(ctx context.Context, userID uuid.UUID) (profileDomain.UserProfile, error) {
	if userID == uuid.Nil {
		return profileDomain.UserProfile{}, profileDomain.ErrInvalidProfileInput
	}
	row, err := repository.queries.GetUserProfile(ctx, sqlcgen.GetUserProfileParams{UserID: uuidToPG(userID)})
	if err != nil {
		return profileDomain.UserProfile{}, mapProfileQueryError(ctx, err, profileDomain.ErrProfileNotFound)
	}
	return profileFromRow(row)
}

func (repository *profileRepository) GetForUpdate(ctx context.Context, userID uuid.UUID) (profileDomain.UserProfile, error) {
	if userID == uuid.Nil {
		return profileDomain.UserProfile{}, profileDomain.ErrInvalidProfileInput
	}
	row, err := repository.queries.GetUserProfileForUpdate(ctx, sqlcgen.GetUserProfileForUpdateParams{UserID: uuidToPG(userID)})
	if err != nil {
		return profileDomain.UserProfile{}, mapProfileQueryError(ctx, err, profileDomain.ErrProfileNotFound)
	}
	return profileFromRow(row)
}

func (repository *profileRepository) Insert(ctx context.Context, value profileDomain.UserProfile) (profileDomain.UserProfile, error) {
	snapshot := value.Snapshot()
	if snapshot.UserID == uuid.Nil || snapshot.ProfileVersion != 1 || snapshot.RealNameKeyVersion == 0 ||
		snapshot.RealNameKeyVersion > math.MaxInt32 || snapshot.RealNameUpdatedBy == uuid.Nil || snapshot.RealNameUpdatedAt.IsZero() {
		return profileDomain.UserProfile{}, profileDomain.ErrInvalidProfileInput
	}
	row, err := repository.queries.CreateUserProfile(ctx, sqlcgen.CreateUserProfileParams{
		UserID: snapshotUserID(snapshot.UserID), RealNameCiphertext: snapshot.RealNameCiphertext,
		RealNameNonce: snapshot.RealNameNonce, RealNameKeyVersion: int32(snapshot.RealNameKeyVersion),
		UpdatedAt: timeToPG(snapshot.RealNameUpdatedAt), UpdatedBy: uuidToPG(snapshot.RealNameUpdatedBy),
	})
	if err != nil {
		return profileDomain.UserProfile{}, mapProfileQueryError(ctx, err, profileDomain.ErrProfileConcurrentTransition)
	}
	return profileFromRow(row)
}

func (repository *profileRepository) UpdateCAS(ctx context.Context, current, next profileDomain.UserProfile) (profileDomain.UserProfile, error) {
	before, after := current.Snapshot(), next.Snapshot()
	if before.UserID == uuid.Nil || before.UserID != after.UserID || before.ProfileVersion == 0 || after.ProfileVersion != before.ProfileVersion+1 {
		return profileDomain.UserProfile{}, profileDomain.ErrInvalidProfileInput
	}
	validated, err := current.UpdateEncrypted(before.ProfileVersion, next.EncryptedRealName(), after.RealNameUpdatedAt, after.RealNameUpdatedBy)
	if err != nil {
		return profileDomain.UserProfile{}, err
	}
	if validated.Snapshot().ProfileVersion != after.ProfileVersion {
		return profileDomain.UserProfile{}, profileDomain.ErrInvalidProfileInput
	}
	row, err := repository.queries.UpdateUserProfileCAS(ctx, sqlcgen.UpdateUserProfileCASParams{
		RealNameCiphertext: after.RealNameCiphertext, RealNameNonce: after.RealNameNonce,
		RealNameKeyVersion: int32(after.RealNameKeyVersion), UpdatedAt: timeToPG(after.RealNameUpdatedAt),
		UpdatedBy: uuidToPG(after.RealNameUpdatedBy), UserID: uuidToPG(before.UserID),
		ExpectedProfileVersion: int64(before.ProfileVersion),
	})
	if err != nil {
		return profileDomain.UserProfile{}, mapProfileQueryError(ctx, err, profileDomain.ErrProfileConcurrentTransition)
	}
	return profileFromRow(row)
}

func (repository *profileRepository) InsertContext(ctx context.Context, value profileDomain.ProfileExportContext) (profileDomain.ProfileExportContext, error) {
	snapshot := value.Snapshot()
	fields, err := profileFieldsToStrings(snapshot.RequestedFields)
	if err != nil || snapshot.Status != profileDomain.ExportStatusActive || !snapshot.CompletedAt.IsZero() ||
		!snapshot.AbortedAt.IsZero() || !snapshot.ExpiredAt.IsZero() || snapshot.ItemCount > math.MaxInt64 || snapshot.SchemaVersion > math.MaxInt32 {
		return profileDomain.ProfileExportContext{}, profileDomain.ErrInvalidProfileInput
	}
	row, err := repository.queries.CreateProfileExportContext(ctx, sqlcgen.CreateProfileExportContextParams{
		ExportID: uuidToPG(snapshot.ExportID), CreatedByAdminID: uuidToPG(snapshot.CreatedByAdminID),
		FilterDigest: snapshot.FilterDigest, RequestedFields: fields, SchemaVersion: int32(snapshot.SchemaVersion),
		ItemCount: int64(snapshot.ItemCount), Reason: snapshot.Reason, CreatedAt: timeToPG(snapshot.CreatedAt), ExpiresAt: timeToPG(snapshot.ExpiresAt),
	})
	if err != nil {
		return profileDomain.ProfileExportContext{}, mapProfileQueryError(ctx, err, profileDomain.ErrProfileConcurrentTransition)
	}
	return profileExportContextFromRow(row)
}

func (repository *profileRepository) InsertItem(ctx context.Context, value profileDomain.ProfileExportItem) (profileDomain.ProfileExportItem, error) {
	snapshot := value.Snapshot()
	if snapshot.Ordinal > math.MaxInt64 {
		return profileDomain.ProfileExportItem{}, profileDomain.ErrInvalidProfileInput
	}
	var version pgtype.Int8
	var ciphertext, nonce []byte
	var keyVersion pgtype.Int4
	if snapshot.ProfileVersion != 0 {
		if snapshot.ProfileVersion > math.MaxInt64 || snapshot.RealNameKeyVersion == 0 || snapshot.RealNameKeyVersion > math.MaxInt32 {
			return profileDomain.ProfileExportItem{}, profileDomain.ErrInvalidProfileInput
		}
		version = pgtype.Int8{Int64: int64(snapshot.ProfileVersion), Valid: true}
		ciphertext, nonce = snapshot.RealNameCiphertext, snapshot.RealNameNonce
		keyVersion = pgtype.Int4{Int32: int32(snapshot.RealNameKeyVersion), Valid: true}
	}
	row, err := repository.queries.CreateProfileExportItem(ctx, sqlcgen.CreateProfileExportItemParams{
		ExportID: uuidToPG(snapshot.ExportID), Ordinal: int64(snapshot.Ordinal), UserID: uuidToPG(snapshot.UserID),
		Username: snapshot.Username, ProfileVersion: version, RealNameCiphertext: ciphertext,
		RealNameNonce: nonce, RealNameKeyVersion: keyVersion,
	})
	if err != nil {
		return profileDomain.ProfileExportItem{}, mapProfileQueryError(ctx, err, profileDomain.ErrProfileConcurrentTransition)
	}
	return profileExportItemFromRow(row)
}

func (repository *profileRepository) GetContextForUpdate(ctx context.Context, exportID uuid.UUID) (profileDomain.ProfileExportContext, error) {
	if exportID == uuid.Nil {
		return profileDomain.ProfileExportContext{}, profileDomain.ErrInvalidProfileInput
	}
	row, err := repository.queries.GetProfileExportContextForUpdate(ctx, sqlcgen.GetProfileExportContextForUpdateParams{ExportID: uuidToPG(exportID)})
	if err != nil {
		return profileDomain.ProfileExportContext{}, mapProfileQueryError(ctx, err, profileDomain.ErrProfileNotFound)
	}
	return profileExportContextFromRow(row)
}

func (repository *profileRepository) ListItems(ctx context.Context, exportID uuid.UUID, afterOrdinal int64, pageSize int32) ([]profileDomain.ProfileExportItem, error) {
	if exportID == uuid.Nil || afterOrdinal < 0 || pageSize <= 0 || pageSize > profileDomain.MaximumExportPageSize {
		return nil, profileDomain.ErrInvalidProfileInput
	}
	rows, err := repository.queries.ListProfileExportItems(ctx, sqlcgen.ListProfileExportItemsParams{ExportID: uuidToPG(exportID), AfterOrdinal: afterOrdinal, PageSize: pageSize})
	if err != nil {
		return nil, mapProfileQueryError(ctx, err, profileDomain.ErrProfileNotFound)
	}
	items := make([]profileDomain.ProfileExportItem, 0, len(rows))
	for _, row := range rows {
		item, mapErr := profileExportItemFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		items = append(items, item)
	}
	return items, nil
}

func (repository *profileRepository) ListSources(ctx context.Context, userIDs []uuid.UUID, statuses []identity.UserStatus) ([]profileDomain.ExportSource, error) {
	ids := make([]pgtype.UUID, len(userIDs))
	for index, userID := range userIDs {
		if userID == uuid.Nil {
			return nil, profileDomain.ErrInvalidProfileInput
		}
		ids[index] = uuidToPG(userID)
	}
	statusValues := make([]string, len(statuses))
	for index, status := range statuses {
		if status == "" {
			return nil, profileDomain.ErrInvalidProfileInput
		}
		statusValues[index] = string(status)
	}
	rows, err := repository.queries.ListProfileExportSources(ctx, sqlcgen.ListProfileExportSourcesParams{UserIds: ids, Statuses: statusValues})
	if err != nil {
		return nil, mapProfileQueryError(ctx, err, profileDomain.ErrProfileNotFound)
	}
	sources := make([]profileDomain.ExportSource, 0, len(rows))
	for _, row := range rows {
		source, mapErr := profileExportSourceFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func (repository *profileRepository) CompleteCAS(ctx context.Context, exportID, adminID uuid.UUID, at time.Time) (profileDomain.ProfileExportContext, error) {
	if exportID == uuid.Nil || adminID == uuid.Nil || at.IsZero() {
		return profileDomain.ProfileExportContext{}, profileDomain.ErrInvalidProfileInput
	}
	row, err := repository.queries.CompleteProfileExportContextCAS(ctx, sqlcgen.CompleteProfileExportContextCASParams{CompletedAt: timeToPG(at), ExportID: uuidToPG(exportID), CreatedByAdminID: uuidToPG(adminID)})
	return repository.reloadExportAfterCAS(ctx, row.ExportID, row.Status, row.CompletedAt, err, profileDomain.ExportStatusCompleted)
}

func (repository *profileRepository) AbortCAS(ctx context.Context, exportID, adminID uuid.UUID, at time.Time) (profileDomain.ProfileExportContext, error) {
	if exportID == uuid.Nil || adminID == uuid.Nil || at.IsZero() {
		return profileDomain.ProfileExportContext{}, profileDomain.ErrInvalidProfileInput
	}
	row, err := repository.queries.AbortProfileExportContextCAS(ctx, sqlcgen.AbortProfileExportContextCASParams{AbortedAt: timeToPG(at), ExportID: uuidToPG(exportID), CreatedByAdminID: uuidToPG(adminID)})
	return repository.reloadExportAfterCAS(ctx, row.ExportID, row.Status, row.AbortedAt, err, profileDomain.ExportStatusAborted)
}

func (repository *profileRepository) ExpireCAS(ctx context.Context, exportID uuid.UUID, at time.Time) (profileDomain.ProfileExportContext, error) {
	if exportID == uuid.Nil || at.IsZero() {
		return profileDomain.ProfileExportContext{}, profileDomain.ErrInvalidProfileInput
	}
	row, err := repository.queries.ExpireProfileExportContextCAS(ctx, sqlcgen.ExpireProfileExportContextCASParams{ExpiredAt: timeToPG(at), ExportID: uuidToPG(exportID)})
	return repository.reloadExportAfterCAS(ctx, row.ExportID, row.Status, row.ExpiredAt, err, profileDomain.ExportStatusExpired)
}

func (repository *profileRepository) reloadExportAfterCAS(ctx context.Context, exportID pgtype.UUID, status string, at pgtype.Timestamptz, queryErr error, expected profileDomain.ExportStatus) (profileDomain.ProfileExportContext, error) {
	if queryErr != nil {
		return profileDomain.ProfileExportContext{}, mapProfileQueryError(ctx, queryErr, profileDomain.ErrProfileConcurrentTransition)
	}
	if !exportID.Valid || !at.Valid || profileDomain.ExportStatus(status) != expected {
		return profileDomain.ProfileExportContext{}, profileDomain.ErrProfileIntegrity
	}
	row, err := repository.queries.GetProfileExportContextForUpdate(ctx, sqlcgen.GetProfileExportContextForUpdateParams{ExportID: exportID})
	if err != nil {
		return profileDomain.ProfileExportContext{}, mapProfileQueryError(ctx, err, profileDomain.ErrProfileRepositoryUnavailable)
	}
	return profileExportContextFromRow(row)
}

func mapProfileQueryError(ctx context.Context, err, noRows error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return noRows
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return profileDomain.ErrProfileRepositoryUnavailable
}

func snapshotUserID(value uuid.UUID) pgtype.UUID { return uuidToPG(value) }

var _ profileDomain.Repository = (*profileRepository)(nil)
var _ profileDomain.ExportRepository = (*profileRepository)(nil)
