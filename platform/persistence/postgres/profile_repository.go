package postgres

import (
	"context"
	"errors"
	"math"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	profileDomain "github.com/iFTY-R/game-night/platform/profile"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type profileQueries interface {
	GetUserProfile(context.Context, sqlcgen.GetUserProfileParams) (sqlcgen.UserProfile, error)
	GetUserProfileForUpdate(context.Context, sqlcgen.GetUserProfileForUpdateParams) (sqlcgen.UserProfile, error)
	CreateUserProfile(context.Context, sqlcgen.CreateUserProfileParams) (sqlcgen.UserProfile, error)
	UpdateUserProfileCAS(context.Context, sqlcgen.UpdateUserProfileCASParams) (sqlcgen.UserProfile, error)
}

type profileRepository struct{ queries profileQueries }

// NewProfileRepository exposes the profile repository behind the domain interface for read-only admin PII availability checks.
func NewProfileRepository(pool *pgxpool.Pool) profileDomain.Repository {
	return &profileRepository{queries: sqlcgen.New(pool)}
}

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
