package user

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestCursorCodecBindsFilterSortAndPosition(t *testing.T) {
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "admin-cursor-keyring.json")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xA5}, 32))
	document := fmt.Sprintf(`{"active_version":1,"keys":[{"version":1,"key":%q,"not_before":"%s"}]}`,
		key, now.Add(-time.Hour).Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(document), 0o400); err != nil {
		t.Fatal(err)
	}
	keyring, err := security.LoadHMACKeyring[security.AdminCursorKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := NewCursorCodec(keyring)
	if err != nil {
		t.Fatal(err)
	}
	filter := sha256.Sum256([]byte("status=active"))
	position := UserListPosition{SortTime: now.Add(-time.Minute), UserID: uuid.New()}
	token, err := codec.Encode(filter, UserSortCreatedAt, SortDescending, now, position)
	if err != nil {
		t.Fatal(err)
	}
	sampledAt, decoded, err := codec.Decode(token, filter, UserSortCreatedAt, SortDescending)
	if err != nil || !sampledAt.Equal(now) || decoded != position {
		t.Fatalf("cursor round trip: sampled=%v position=%+v err=%v", sampledAt, decoded, err)
	}
	otherFilter := sha256.Sum256([]byte("status=suspended"))
	if _, _, err = codec.Decode(token, otherFilter, UserSortCreatedAt, SortDescending); err != ErrInvalidInput {
		t.Fatalf("cross-filter cursor error = %v", err)
	}
	if _, _, err = codec.Decode(token, filter, UserSortUsername, SortDescending); err != ErrInvalidInput {
		t.Fatalf("cross-sort cursor error = %v", err)
	}
	tamperedSuffix := "A"
	if token[len(token)-1] == 'A' {
		tamperedSuffix = "B"
	}
	tampered := token[:len(token)-1] + tamperedSuffix
	if _, _, err = codec.Decode(tampered, filter, UserSortCreatedAt, SortDescending); err != ErrInvalidInput {
		t.Fatalf("tampered cursor error = %v", err)
	}
}

func TestCursorCodecSupportsEveryStableSortPosition(t *testing.T) {
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "admin-cursor-keyring.json")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xB6}, 32))
	document := fmt.Sprintf(`{"active_version":1,"keys":[{"version":1,"key":%q,"not_before":"%s"}]}`,
		key, now.Add(-time.Hour).Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(document), 0o400); err != nil {
		t.Fatal(err)
	}
	keyring, err := security.LoadHMACKeyring[security.AdminCursorKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := NewCursorCodec(keyring)
	if err != nil {
		t.Fatal(err)
	}
	filter := sha256.Sum256([]byte("all-users"))
	userID := uuid.New()
	tests := []struct {
		name     string
		field    UserSortField
		position UserListPosition
	}{
		{name: "created", field: UserSortCreatedAt, position: UserListPosition{SortTime: now.Add(-time.Hour), UserID: userID}},
		{name: "activity", field: UserSortLastActivityAt, position: UserListPosition{SortTime: now.Add(-time.Minute), UserID: userID}},
		{name: "username", field: UserSortUsername, position: UserListPosition{SortText: "alice", UserID: userID}},
		{name: "missing username", field: UserSortUsername, position: UserListPosition{UserID: userID}},
		{name: "user id", field: UserSortUserID, position: UserListPosition{UserID: userID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token, encodeErr := codec.Encode(filter, test.field, SortAscending, now, test.position)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			_, decoded, decodeErr := codec.Decode(token, filter, test.field, SortAscending)
			if decodeErr != nil || decoded != test.position {
				t.Fatalf("position=%+v err=%v", decoded, decodeErr)
			}
		})
	}
	if _, _, err = codec.Decode(strings.Repeat("A", adminUserCursorMaximumTokenBytes+1), filter, UserSortUserID, SortAscending); err != ErrInvalidInput {
		t.Fatalf("oversized cursor error = %v", err)
	}
}
