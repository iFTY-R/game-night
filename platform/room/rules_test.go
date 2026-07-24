package room

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryRuleRepositoryConsumePendingStartIgnoresOperationBinding(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryRuleRepository()
	roomID := uuid.New()
	start, err := repository.BeginPendingStart(t.Context(), PendingStartCreate{
		RoomID:         roomID,
		ActorUserID:    uuid.New(),
		GameID:         "dice",
		ConfigRevision: 3,
		Expected:       Version{Room: 4, Membership: 2},
		OwnershipEpoch: 5,
		OperationID:    "begin-op",
		RequestDigest:  testRuleDigest("begin"),
		Deadline:       now.Add(5 * time.Second),
		At:             now,
	})
	if err != nil {
		t.Fatal(err)
	}

	consumed, err := repository.ConsumePendingStart(
		t.Context(),
		roomID,
		start.ID,
		start.CancelToken,
		"different-consume-op",
		testRuleDigest("different-consume-digest"),
		now.Add(6*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !consumed.Consumed || consumed.ID != start.ID {
		t.Fatalf("consumed start = %+v", consumed)
	}

	replayed, err := repository.ConsumePendingStart(
		t.Context(),
		roomID,
		start.ID,
		start.CancelToken,
		"another-consume-op",
		testRuleDigest("another-consume-digest"),
		now.Add(7*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Consumed || replayed.ID != start.ID {
		t.Fatalf("replayed start = %+v", replayed)
	}
}

func testRuleDigest(seed string) [32]byte {
	var digest [32]byte
	copy(digest[:], seed)
	return digest
}
