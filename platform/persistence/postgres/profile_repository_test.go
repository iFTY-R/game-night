package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	profileDomain "github.com/iFTY-R/game-night/platform/profile"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestProfileMappingRejectsAuthenticatedPayloadShapeViolations(t *testing.T) {
	row := validProfileRow()
	row.RealNameNonce = []byte{1}
	if _, err := profileFromRow(row); !errors.Is(err, profileDomain.ErrProfileIntegrity) {
		t.Fatalf("expected integrity error, got %v", err)
	}
}

func TestProfileRepositoryUpdateCASBindsCurrentVersionAndEncryptedPayload(t *testing.T) {
	userID, adminID := uuid.New(), uuid.New()
	current := newTestProfile(t, userID, adminID, time.Unix(100, 0))
	next, err := current.UpdateEncrypted(current.ProfileVersion(), testEncryptedValue(7), time.Unix(101, 0), adminID)
	if err != nil {
		t.Fatal(err)
	}
	queries := &fakeProfileQueries{updateProfile: validProfileRowFor(next)}
	repository := &profileRepository{queries: queries}
	updated, err := repository.UpdateCAS(context.Background(), current, next)
	if err != nil {
		t.Fatal(err)
	}
	if queries.updateParams.ExpectedProfileVersion != 1 || queries.updateParams.UserID.Bytes != userID {
		t.Fatalf("unexpected CAS binding: %#v", queries.updateParams)
	}
	if queries.updateParams.RealNameKeyVersion != 7 || string(queries.updateParams.RealNameNonce) != string(next.Snapshot().RealNameNonce) {
		t.Fatalf("encrypted payload was not forwarded: %#v", queries.updateParams)
	}
	if updated.ProfileVersion() != 2 {
		t.Fatalf("expected persisted version 2, got %d", updated.ProfileVersion())
	}
}

func TestProfileRepositoryMapsNotFoundAndCancellation(t *testing.T) {
	queries := &fakeProfileQueries{getProfileErr: pgx.ErrNoRows}
	repository := &profileRepository{queries: queries}
	if _, err := repository.GetByID(context.Background(), uuid.New()); !errors.Is(err, profileDomain.ErrProfileNotFound) {
		t.Fatalf("expected not-found mapping, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queries.getProfileErr = errors.New("driver failure")
	if _, err := repository.GetByID(ctx, uuid.New()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func newTestProfile(t *testing.T, userID, adminID uuid.UUID, at time.Time) profileDomain.UserProfile {
	t.Helper()
	value, err := profileDomain.NewUserProfile(userID, testEncryptedValue(1), at, adminID)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testEncryptedValue(keyVersion uint32) profileDomain.EncryptedValue {
	return profileDomain.EncryptedValue{KeyVersion: keyVersion, Nonce: make([]byte, profileDomain.ProfileAESNonceSize), Ciphertext: make([]byte, profileDomain.ProfileAESOverhead)}
}

func validProfileRow() sqlcgen.UserProfile {
	return sqlcgen.UserProfile{UserID: pgUUID(uuid.New()), RealNameCiphertext: make([]byte, profileDomain.ProfileAESOverhead), RealNameNonce: make([]byte, profileDomain.ProfileAESNonceSize), RealNameKeyVersion: 1, ProfileVersion: 1, RealNameUpdatedAt: pgTime(time.Unix(100, 0)), RealNameUpdatedBy: pgUUID(uuid.New())}
}

func validProfileRowFor(value profileDomain.UserProfile) sqlcgen.UserProfile {
	snapshot := value.Snapshot()
	return sqlcgen.UserProfile{UserID: pgUUID(snapshot.UserID), RealNameCiphertext: snapshot.RealNameCiphertext, RealNameNonce: snapshot.RealNameNonce, RealNameKeyVersion: int32(snapshot.RealNameKeyVersion), ProfileVersion: int64(snapshot.ProfileVersion), RealNameUpdatedAt: pgTime(snapshot.RealNameUpdatedAt), RealNameUpdatedBy: pgUUID(snapshot.RealNameUpdatedBy)}
}

func pgUUID(value uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: value, Valid: true} }
func pgTime(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

type fakeProfileQueries struct {
	getProfileErr error
	updateProfile sqlcgen.UserProfile
	updateParams  sqlcgen.UpdateUserProfileCASParams
}

func (fake *fakeProfileQueries) GetUserProfile(context.Context, sqlcgen.GetUserProfileParams) (sqlcgen.UserProfile, error) {
	return validProfileRow(), fake.getProfileErr
}
func (fake *fakeProfileQueries) GetUserProfileForUpdate(context.Context, sqlcgen.GetUserProfileForUpdateParams) (sqlcgen.UserProfile, error) {
	return validProfileRow(), fake.getProfileErr
}
func (fake *fakeProfileQueries) CreateUserProfile(context.Context, sqlcgen.CreateUserProfileParams) (sqlcgen.UserProfile, error) {
	return validProfileRow(), nil
}
func (fake *fakeProfileQueries) UpdateUserProfileCAS(_ context.Context, params sqlcgen.UpdateUserProfileCASParams) (sqlcgen.UserProfile, error) {
	fake.updateParams = params
	return fake.updateProfile, nil
}
