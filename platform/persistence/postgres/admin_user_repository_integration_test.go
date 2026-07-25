package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
)

func TestAdminUserRepositoryTagCASNotesAndStableUserCursor(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, _ := seedAdminUserCenterPrincipal(t, ctx, fixture, now)

	userIDs := []uuid.UUID{
		uuid.MustParse("10000000-0000-0000-0000-000000000003"),
		uuid.MustParse("10000000-0000-0000-0000-000000000001"),
		uuid.MustParse("10000000-0000-0000-0000-000000000002"),
	}
	createdAt := now.Add(-time.Minute)
	for index, userID := range userIDs {
		createRoomTestUser(t, ctx, fixture, userID, "U"+string(rune('a'+index))+"1", createdAt)
	}

	repository := NewAdminUserRepository(fixture.Pool)
	created, err := repository.CreateTag(ctx, adminuser.CreateTagCommand{
		TagID: uuid.New(), Name: "VIP", Color: "#12ABEF", ActorAdminID: adminID,
		Reason: "seed reviewed tag", ExpectedCatalogVersion: 1, CreatedAt: now,
	})
	if err != nil || created.CatalogVersion != 2 || created.Tag.Version != 1 {
		t.Fatalf("create tag: mutation=%+v err=%v", created, err)
	}
	if _, err = repository.CreateTag(ctx, adminuser.CreateTagCommand{
		TagID: uuid.New(), Name: "vip", Color: "#FFFFFF", ActorAdminID: adminID,
		Reason: "duplicate normalized tag", ExpectedCatalogVersion: 2, CreatedAt: now.Add(time.Second),
	}); !errors.Is(err, adminuser.ErrConflict) {
		t.Fatalf("duplicate normalized tag error = %v", err)
	}
	updated, err := repository.UpdateTag(ctx, adminuser.UpdateTagCommand{
		TagID: created.Tag.ID, Name: "Priority", Color: "#00AA11", ActorAdminID: adminID,
		Reason: "rename reviewed tag", ExpectedVersion: created.Tag.Version, UpdatedAt: now.Add(2 * time.Second),
	})
	if err != nil || updated.Version != 2 || updated.NormalizedName != "priority" {
		t.Fatalf("update tag: tag=%+v err=%v", updated, err)
	}
	if _, err = repository.UpdateTag(ctx, adminuser.UpdateTagCommand{
		TagID: created.Tag.ID, Name: "Stale", Color: "#00AA11", ActorAdminID: adminID,
		Reason: "stale tag update", ExpectedVersion: 1, UpdatedAt: now.Add(3 * time.Second),
	}); !errors.Is(err, adminuser.ErrConflict) {
		t.Fatalf("stale tag update error = %v", err)
	}
	tagsAfterUpdate, err := repository.ListTags(ctx, adminuser.TagPageQuery{PageSize: 10})
	if err != nil || tagsAfterUpdate.CatalogVersion != 3 || len(tagsAfterUpdate.Tags) != 1 {
		t.Fatalf("tags after update: page=%+v err=%v", tagsAfterUpdate, err)
	}
	alpha, err := repository.CreateTag(ctx, adminuser.CreateTagCommand{
		TagID: uuid.New(), Name: "Alpha", Color: "#AA0000", ActorAdminID: adminID,
		Reason: "add pagination tag", ExpectedCatalogVersion: tagsAfterUpdate.CatalogVersion, CreatedAt: now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.CreateTag(ctx, adminuser.CreateTagCommand{
		TagID: uuid.New(), Name: "Beta", Color: "#00AA00", ActorAdminID: adminID,
		Reason: "add second pagination tag", ExpectedCatalogVersion: alpha.CatalogVersion, CreatedAt: now.Add(4 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	firstTagPage, err := repository.ListTags(ctx, adminuser.TagPageQuery{NamePrefix: "Ａ", PageSize: 1})
	if err != nil || len(firstTagPage.Tags) != 1 || firstTagPage.Tags[0].Name != "Alpha" {
		t.Fatalf("normalized tag prefix: page=%+v err=%v", firstTagPage, err)
	}
	allTagPage, err := repository.ListTags(ctx, adminuser.TagPageQuery{PageSize: 2})
	if err != nil || len(allTagPage.Tags) != 2 || allTagPage.Tags[0].Name != "Alpha" || allTagPage.Tags[1].Name != "Beta" {
		t.Fatalf("first tag page: page=%+v err=%v", allTagPage, err)
	}
	tagTail, err := repository.ListTags(ctx, adminuser.TagPageQuery{
		PageSize: 2, AfterNormalizedName: allTagPage.Tags[1].NormalizedName, AfterTagID: allTagPage.Tags[1].ID,
	})
	if err != nil || len(tagTail.Tags) != 1 || tagTail.Tags[0].Name != "Priority" || tagTail.CatalogVersion != allTagPage.CatalogVersion {
		t.Fatalf("tag tail page: page=%+v err=%v", tagTail, err)
	}

	nextVersion, err := repository.SetTags(ctx, adminuser.SetTagsCommand{
		UserID: userIDs[0], TagIDs: []uuid.UUID{created.Tag.ID}, ActorAdminID: adminID,
		Reason: "assign reviewed tag", ExpectedVersion: 1, ChangedAt: now.Add(4 * time.Second),
	})
	if err != nil || nextVersion != 2 {
		t.Fatalf("set tags: version=%d err=%v", nextVersion, err)
	}
	if _, err = repository.SetTags(ctx, adminuser.SetTagsCommand{
		UserID: userIDs[0], ActorAdminID: adminID, Reason: "stale tag replacement",
		ExpectedVersion: 1, ChangedAt: now.Add(5 * time.Second),
	}); !errors.Is(err, adminuser.ErrConflict) {
		t.Fatalf("stale set tags error = %v", err)
	}

	firstNote, err := repository.AppendNote(ctx, adminuser.AppendNoteCommand{
		NoteID: uuid.New(), UserID: userIDs[0], AuthorAdminID: adminID,
		Body: "first immutable note", Reason: "review evidence", CreatedAt: now.Add(6 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.AppendNote(ctx, adminuser.AppendNoteCommand{
		NoteID: uuid.New(), UserID: userIDs[0], AuthorAdminID: adminID,
		Body: "second immutable note", Reason: "follow-up evidence", CreatedAt: now.Add(7 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Pool.Exec(ctx, "UPDATE admin_user_notes SET body = 'mutated' WHERE note_id = $1", firstNote.ID); err == nil {
		t.Fatal("append-only note accepted an update")
	}
	notes, err := repository.ListNotes(ctx, adminuser.NotePageQuery{UserID: userIDs[0], PageSize: 10})
	if err != nil || len(notes) != 2 || notes[1].ID != firstNote.ID || notes[1].Body != firstNote.Body {
		t.Fatalf("note history changed: notes=%+v err=%v", notes, err)
	}

	firstPage, err := repository.ListUsers(ctx, adminuser.UserListQuery{
		Statuses: []string{"active"}, PageSize: 2, SampledAt: now.Add(time.Minute),
	})
	if err != nil || len(firstPage) != 2 {
		t.Fatalf("first user page: users=%+v err=%v", firstPage, err)
	}
	secondPage, err := repository.ListUsers(ctx, adminuser.UserListQuery{
		Statuses: []string{"active"}, PageSize: 2, SampledAt: now.Add(time.Minute),
		After: adminuser.UserListPosition{SortTime: firstPage[1].CreatedAt, UserID: firstPage[1].ID},
	})
	if err != nil || len(secondPage) != 1 || secondPage[0].ID == firstPage[0].ID || secondPage[0].ID == firstPage[1].ID {
		t.Fatalf("second user page: users=%+v err=%v", secondPage, err)
	}

	taggedUsers, err := repository.ListUsers(ctx, adminuser.UserListQuery{
		TagIDs: []uuid.UUID{created.Tag.ID}, PageSize: 10, SampledAt: now.Add(time.Minute),
	})
	if err != nil || len(taggedUsers) != 1 || taggedUsers[0].ID != userIDs[0] ||
		len(taggedUsers[0].Tags) != 1 || taggedUsers[0].Tags[0].Name != "Priority" {
		t.Fatalf("tag-filtered users: users=%+v err=%v", taggedUsers, err)
	}
	var versionBeforeRejectedDelete int64
	if err = fixture.Pool.QueryRow(ctx, "SELECT account_version FROM users WHERE user_id = $1", userIDs[0]).Scan(&versionBeforeRejectedDelete); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.DeleteTag(ctx, adminuser.DeleteTagCommand{
		TagID: updated.ID, ExpectedVersion: updated.Version, DeletedAt: now.Add(8 * time.Second),
	}); !errors.Is(err, adminuser.ErrConflict) {
		t.Fatalf("delete assigned tag error = %v", err)
	}
	var versionAfterRejectedDelete int64
	if err = fixture.Pool.QueryRow(ctx, "SELECT account_version FROM users WHERE user_id = $1", userIDs[0]).Scan(&versionAfterRejectedDelete); err != nil {
		t.Fatal(err)
	}
	if versionAfterRejectedDelete != versionBeforeRejectedDelete || versionAfterRejectedDelete != int64(nextVersion) {
		t.Fatalf("assigned tag deletion changed user version: before=%d after=%d", versionBeforeRejectedDelete, versionAfterRejectedDelete)
	}
	detachedVersion, err := repository.SetTags(ctx, adminuser.SetTagsCommand{
		UserID: userIDs[0], ActorAdminID: adminID, Reason: "detach tag before definition deletion",
		ExpectedVersion: nextVersion, ChangedAt: now.Add(9 * time.Second),
	})
	if err != nil || detachedVersion != nextVersion+1 {
		t.Fatalf("detach tag: version=%d err=%v", detachedVersion, err)
	}
	deletedCatalogVersion, err := repository.DeleteTag(ctx, adminuser.DeleteTagCommand{
		TagID: updated.ID, ExpectedVersion: updated.Version, DeletedAt: now.Add(10 * time.Second),
	})
	if err != nil || deletedCatalogVersion != tagTail.CatalogVersion+1 {
		t.Fatalf("delete detached tag: catalog_version=%d err=%v", deletedCatalogVersion, err)
	}

	if _, err = fixture.Pool.Exec(ctx, "UPDATE users SET updated_at = $2 WHERE user_id = $1", userIDs[1], now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	activityPage, err := repository.ListUsers(ctx, adminuser.UserListQuery{
		Statuses: []string{"active"}, LastActivityFrom: now, PageSize: 10, SampledAt: now.Add(time.Minute),
		SortField: adminuser.UserSortLastActivityAt, Direction: adminuser.SortDescending,
	})
	if err != nil || len(activityPage) != 2 || activityPage[0].ID != userIDs[1] || !activityPage[0].LastActivityAt.Equal(now.Add(20*time.Second)) {
		t.Fatalf("activity-sorted users: users=%+v err=%v", activityPage, err)
	}

	usernamePage, err := repository.ListUsers(ctx, adminuser.UserListQuery{
		UsernamePrefix: "Ｕ", PageSize: 2, SampledAt: now.Add(time.Minute),
		SortField: adminuser.UserSortUsername, Direction: adminuser.SortAscending,
	})
	if err != nil || len(usernamePage) != 2 || usernamePage[0].ID != userIDs[0] || usernamePage[1].ID != userIDs[1] {
		t.Fatalf("username first page: users=%+v err=%v", usernamePage, err)
	}
	usernameTail, err := repository.ListUsers(ctx, adminuser.UserListQuery{
		UsernamePrefix: "Ｕ", PageSize: 2, SampledAt: now.Add(time.Minute),
		SortField: adminuser.UserSortUsername, Direction: adminuser.SortAscending,
		After: adminuser.UserListPosition{SortText: usernamePage[1].CurrentUsernameKey, UserID: usernamePage[1].ID},
	})
	if err != nil || len(usernameTail) != 1 || usernameTail[0].ID != userIDs[2] {
		t.Fatalf("username tail page: users=%+v err=%v", usernameTail, err)
	}

	exactUser, err := repository.ListUsers(ctx, adminuser.UserListQuery{
		UserID: userIDs[2], PageSize: 10, SampledAt: now.Add(time.Minute),
		SortField: adminuser.UserSortUserID, Direction: adminuser.SortAscending,
	})
	if err != nil || len(exactUser) != 1 || exactUser[0].ID != userIDs[2] {
		t.Fatalf("exact user filter: users=%+v err=%v", exactUser, err)
	}
}

func seedAdminUserCenterPrincipal(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	now time.Time,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	unitOfWork := NewAdminUnitOfWork(fixture.Pool)
	var adminID, sessionID uuid.UUID
	if err := unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.Transaction) error {
		account, err := seedActiveAdminAccount(ctx, transaction.Accounts(), now.Add(-10*time.Minute))
		if err != nil {
			return err
		}
		adminID = account.Snapshot().ID
		session := mustRestoreSession(t, adminDomain.SessionSnapshot{
			ID: uuid.New(), AdminID: adminID, Selector: mustSelector(t, 0xD1), SecretMAC: mustAdminSessionMAC(0xD2),
			CSRFHash: mustAdminSessionMAC(0xD3), Kind: adminDomain.SessionKindFull,
			AdminVersion: account.Snapshot().AdminVersion, PasswordVersion: account.Snapshot().PasswordVersion,
			SessionVersion: 1, ClientIP: "203.0.113.90", UserAgent: "admin-user-center-test", MaxAttempts: 5,
			CreatedAt: now.Add(-5 * time.Minute), LastSeenAt: now.Add(-time.Minute),
			IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(12 * time.Hour),
		})
		sessionID = session.Snapshot().ID
		return transaction.Sessions().Insert(ctx, session)
	}); err != nil {
		t.Fatal(err)
	}
	return adminID, sessionID
}
