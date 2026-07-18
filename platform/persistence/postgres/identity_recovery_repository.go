package postgres

import (
	"context"
	"math"

	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
)

type identityRecoveryQueries interface {
	CreateUserRecoveryCredential(context.Context, sqlcgen.CreateUserRecoveryCredentialParams) (sqlcgen.UserRecoveryCredential, error)
}

type identityRecoveryRepository struct{ queries identityRecoveryQueries }

func (repository *identityRecoveryRepository) Insert(
	ctx context.Context,
	credential identityDomain.RecoveryCredential,
) (identityDomain.RecoveryCredential, error) {
	snapshot := credential.Snapshot()
	if snapshot.Status != identityDomain.RecoveryCredentialActive || snapshot.Version == 0 || snapshot.Version > math.MaxInt64 {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrInvalidRecoveryCredential
	}
	row, err := repository.queries.CreateUserRecoveryCredential(ctx, sqlcgen.CreateUserRecoveryCredentialParams{
		RecoveryCredentialID: uuidToPG(snapshot.ID), UserID: uuidToPG(snapshot.UserID),
		Selector: snapshot.Selector.Value(), SecretHash: snapshot.SecretHash,
		Version: int64(snapshot.Version), CreatedAt: timeToPG(snapshot.CreatedAt),
	})
	if err != nil {
		return identityDomain.RecoveryCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	return identityRecoveryFromRow(row)
}

var _ identityDomain.RecoveryCredentialRepository = (*identityRecoveryRepository)(nil)
