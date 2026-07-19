package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identity"
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

func TestProfileExportSourcesMapOptionalProfileAndStatusFilters(t *testing.T) {
	userID := uuid.New()
	queries := &fakeProfileQueries{sourceRows: []sqlcgen.ListProfileExportSourcesRow{{
		UserID: pgUUID(userID), Username: pgtype.Text{String: "alice", Valid: true},
	}}}
	repository := &profileRepository{queries: queries}
	sources, err := repository.ListSources(context.Background(), []uuid.UUID{userID}, []identity.UserStatus{identity.UserStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].ProfileVersion != 0 || sources[0].RealName != nil {
		t.Fatalf("unexpected source mapping: %#v", sources)
	}
	if len(queries.sourceParams.UserIds) != 1 || queries.sourceParams.Statuses[0] != string(identity.UserStatusActive) {
		t.Fatalf("unexpected source filters: %#v", queries.sourceParams)
	}
}

func TestProfileExportItemMappingRejectsPartialEncryptedPayload(t *testing.T) {
	row := sqlcgen.ProfileExportItem{
		ExportID: pgUUID(uuid.New()), Ordinal: 1, UserID: pgUUID(uuid.New()), Username: "alice",
		ProfileVersion:     pgtype.Int8{Int64: 1, Valid: true},
		RealNameKeyVersion: pgtype.Int4{Int32: 1, Valid: true}, RealNameNonce: []byte{1}, RealNameCiphertext: make([]byte, 16),
	}
	if _, err := profileExportItemFromRow(row); !errors.Is(err, profileDomain.ErrProfileIntegrity) {
		t.Fatalf("expected malformed item integrity error, got %v", err)
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
	sourceRows    []sqlcgen.ListProfileExportSourcesRow
	sourceParams  sqlcgen.ListProfileExportSourcesParams
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
func (fake *fakeProfileQueries) CreateProfileExportContext(context.Context, sqlcgen.CreateProfileExportContextParams) (sqlcgen.ProfileExportContext, error) {
	return sqlcgen.ProfileExportContext{}, nil
}
func (fake *fakeProfileQueries) CreateProfileExportItem(context.Context, sqlcgen.CreateProfileExportItemParams) (sqlcgen.ProfileExportItem, error) {
	return sqlcgen.ProfileExportItem{}, nil
}
func (fake *fakeProfileQueries) GetProfileExportContextForUpdate(context.Context, sqlcgen.GetProfileExportContextForUpdateParams) (sqlcgen.ProfileExportContext, error) {
	return sqlcgen.ProfileExportContext{}, nil
}
func (fake *fakeProfileQueries) ListProfileExportItems(context.Context, sqlcgen.ListProfileExportItemsParams) ([]sqlcgen.ProfileExportItem, error) {
	return nil, nil
}
func (fake *fakeProfileQueries) ListProfileExportSources(_ context.Context, params sqlcgen.ListProfileExportSourcesParams) ([]sqlcgen.ListProfileExportSourcesRow, error) {
	fake.sourceParams = params
	return fake.sourceRows, nil
}
func (fake *fakeProfileQueries) CompleteProfileExportContextCAS(context.Context, sqlcgen.CompleteProfileExportContextCASParams) (sqlcgen.CompleteProfileExportContextCASRow, error) {
	return sqlcgen.CompleteProfileExportContextCASRow{}, nil
}
func (fake *fakeProfileQueries) AbortProfileExportContextCAS(context.Context, sqlcgen.AbortProfileExportContextCASParams) (sqlcgen.AbortProfileExportContextCASRow, error) {
	return sqlcgen.AbortProfileExportContextCASRow{}, nil
}
func (fake *fakeProfileQueries) ExpireProfileExportContextCAS(context.Context, sqlcgen.ExpireProfileExportContextCASParams) (sqlcgen.ExpireProfileExportContextCASRow, error) {
	return sqlcgen.ExpireProfileExportContextCASRow{}, nil
}
