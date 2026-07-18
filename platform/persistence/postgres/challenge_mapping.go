package postgres

import (
	"math"

	"github.com/google/uuid"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5/pgtype"
)

func identityChallengeFromRow(row sqlcgen.AnonymousChallenge) (identityDomain.Challenge, error) {
	if !row.ChallengeID.Valid || row.SecretKeyVersion <= 0 || row.AttemptCount < 0 || row.MaxAttempts <= 0 ||
		!row.CreatedAt.Valid || !row.ExpiresAt.Valid {
		return identityDomain.Challenge{}, challenge.ErrIntegrity
	}
	selector, origin, replay, err := challengeRowValues(
		row.Selector, row.OriginHash, row.OperationID, row.RequestDigest, row.ResultID, row.ReplayUntil,
	)
	if err != nil {
		return identityDomain.Challenge{}, err
	}
	snapshot := identityDomain.ChallengeSnapshot{
		ID:       uuid.UUID(row.ChallengeID.Bytes),
		Selector: selector,
		SecretMAC: security.MAC[security.UserChallengeKeyPurpose]{
			KeyVersion: uint32(row.SecretKeyVersion), Value: row.SecretHash,
		},
		Binding: challenge.Binding{
			Purpose: challenge.Purpose(row.Purpose), Audience: challenge.Audience(row.Audience),
			Origin: origin, RequestFlowID: challenge.RequestFlowID(row.RequestFlowID),
		},
		AttemptCount: uint32(row.AttemptCount), MaxAttempts: uint32(row.MaxAttempts),
		CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time, Replay: replay,
	}
	if row.ConsumedAt.Valid {
		snapshot.ConsumedAt = row.ConsumedAt.Time
	}
	restored, err := identityDomain.RestoreChallenge(snapshot)
	if err != nil {
		return identityDomain.Challenge{}, challenge.ErrIntegrity
	}
	return restored, nil
}

func adminChallengeFromRow(row sqlcgen.AdminChallenge) (adminDomain.Challenge, error) {
	if !row.ChallengeID.Valid || !row.AdminID.Valid || row.SecretKeyVersion <= 0 || row.AdminVersion <= 0 ||
		row.PasswordVersion < 0 || row.AttemptCount < 0 || row.MaxAttempts <= 0 || !row.CreatedAt.Valid || !row.ExpiresAt.Valid ||
		!validAdminChallengeTerminalShape(row) {
		return adminDomain.Challenge{}, challenge.ErrIntegrity
	}
	selector, origin, replay, err := challengeRowValues(
		row.Selector, row.OriginHash, row.OperationID, row.RequestDigest, row.ResultID, row.ReplayUntil,
	)
	if err != nil {
		return adminDomain.Challenge{}, err
	}
	snapshot := adminDomain.ChallengeSnapshot{
		ID:       uuid.UUID(row.ChallengeID.Bytes),
		Selector: selector,
		SecretMAC: security.MAC[security.AdminChallengeKeyPurpose]{
			KeyVersion: uint32(row.SecretKeyVersion), Value: row.SecretHash,
		},
		Binding: challenge.Binding{
			Purpose: challenge.Purpose(row.Purpose), Audience: challenge.Audience(row.Audience),
			Origin: origin, RequestFlowID: challenge.RequestFlowID(row.RequestFlowID),
			Subject: challenge.SubjectBinding{
				ID: uuid.UUID(row.AdminID.Bytes), Version: row.AdminVersion, CredentialVersion: row.PasswordVersion,
			},
		},
		AttemptCount: uint32(row.AttemptCount), MaxAttempts: uint32(row.MaxAttempts),
		CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time, Replay: replay,
	}
	if row.ConsumedAt.Valid {
		snapshot.ConsumedAt = row.ConsumedAt.Time
	}
	if row.RevokedAt.Valid {
		snapshot.RevokedAt = row.RevokedAt.Time
	}
	if row.Status == "expired" {
		snapshot.PersistedState = challenge.StateExpired
	}
	restored, err := adminDomain.RestoreChallenge(snapshot)
	if err != nil {
		return adminDomain.Challenge{}, challenge.ErrIntegrity
	}
	return restored, nil
}

func challengeRowValues(
	selectorValue string,
	originValue []byte,
	operationValue pgtype.Text,
	digestValue []byte,
	resultValue pgtype.UUID,
	replayUntilValue pgtype.Timestamptz,
) (identifier.Selector, challenge.OriginDigest, *challenge.ReplayAuthorization, error) {
	selector, err := identifier.ParseSelector(selectorValue)
	if err != nil || selector.ByteLength() != challenge.SelectorBytes {
		return identifier.Selector{}, challenge.OriginDigest{}, nil, challenge.ErrIntegrity
	}
	origin, err := challenge.NewOriginDigest(originValue)
	if err != nil {
		return identifier.Selector{}, challenge.OriginDigest{}, nil, challenge.ErrIntegrity
	}
	present := 0
	for _, valid := range []bool{operationValue.Valid, len(digestValue) > 0, resultValue.Valid, replayUntilValue.Valid} {
		if valid {
			present++
		}
	}
	if present == 0 {
		return selector, origin, nil, nil
	}
	if present != 4 {
		return identifier.Selector{}, challenge.OriginDigest{}, nil, challenge.ErrIntegrity
	}
	operationID, err := idempotency.ParseOperationID(operationValue.String)
	if err != nil {
		return identifier.Selector{}, challenge.OriginDigest{}, nil, challenge.ErrIntegrity
	}
	digest, err := idempotency.NewDigest(digestValue)
	if err != nil {
		return identifier.Selector{}, challenge.OriginDigest{}, nil, challenge.ErrIntegrity
	}
	return selector, origin, &challenge.ReplayAuthorization{
		OperationID: operationID, RequestDigest: digest, ResultID: uuid.UUID(resultValue.Bytes), ReplayUntil: replayUntilValue.Time,
	}, nil
}

func validAdminChallengeTerminalShape(row sqlcgen.AdminChallenge) bool {
	switch row.Status {
	case "active", "expired":
		return !row.ConsumedAt.Valid && !row.RevokedAt.Valid
	case "consumed":
		return row.ConsumedAt.Valid && !row.RevokedAt.Valid
	case "revoked":
		return !row.ConsumedAt.Valid && row.RevokedAt.Valid
	default:
		return false
	}
}

func validateChallengeCounterRange(attemptCount, maxAttempts uint32) error {
	if attemptCount > math.MaxInt32 || maxAttempts == 0 || maxAttempts > math.MaxInt32 {
		return challenge.ErrInvalidInput
	}
	return nil
}
