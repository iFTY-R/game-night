package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	adminroom "github.com/iFTY-R/game-night/platform/admin/room"
	goredis "github.com/redis/go-redis/v9"
)

func TestAdminRoomOwnerReaderDecodesLeaseWithoutToken(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	sessionID := uuid.MustParse("018f3f7c-296f-7a4e-8e16-0bba70f3f4aa")
	token := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	client := &memoryAdminRoomLeaseClient{
		values: map[string]string{"gn:test:game:lease:v1:" + sessionID.String(): encodeLeaseWire("rt-1", "10.0.0.1:9000", token, true, 7)},
		ttls:   map[string]time.Duration{"gn:test:game:lease:v1:" + sessionID.String(): 20 * time.Second},
	}
	reader := newTestAdminRoomOwnerReader(t, client)

	owners, err := reader.ReadOwners(context.Background(), []uuid.UUID{sessionID}, now)
	if err != nil {
		t.Fatal(err)
	}
	owner := owners[sessionID]
	if owner.Freshness != adminroom.OwnerFreshnessFresh || owner.OwnerInstance != "rt-1" || owner.OwnerAddress != "10.0.0.1:9000" || owner.OwnershipEpoch != 7 {
		t.Fatalf("owner summary = %+v", owner)
	}
	if !owner.ExpiresAt.Equal(now.Add(20 * time.Second)) {
		t.Fatalf("expires_at = %s", owner.ExpiresAt)
	}
}

func TestAdminRoomOwnerReaderMarksMissingAndExpired(t *testing.T) {
	now := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)
	missingID := uuid.New()
	expiredID := uuid.New()
	token := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	expiredKey := "gn:test:game:lease:v1:" + expiredID.String()
	client := &memoryAdminRoomLeaseClient{
		values: map[string]string{expiredKey: encodeLeaseWire("rt-1", "10.0.0.1:9000", token, true, 7)},
		ttls:   map[string]time.Duration{expiredKey: -time.Millisecond},
	}
	reader := newTestAdminRoomOwnerReader(t, client)

	owners, err := reader.ReadOwners(context.Background(), []uuid.UUID{missingID, expiredID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := owners[missingID]; exists {
		t.Fatalf("missing lease should be omitted for domain fallback: %+v", owners[missingID])
	}
	if owners[expiredID].Freshness != adminroom.OwnerFreshnessExpired {
		t.Fatalf("expired owner = %+v", owners[expiredID])
	}
}

func TestAdminRoomOwnerReaderFailsClosedOnRedisErrors(t *testing.T) {
	reader := newTestAdminRoomOwnerReader(t, &memoryAdminRoomLeaseClient{err: errors.New("redis down")})
	_, err := reader.ReadOwners(context.Background(), []uuid.UUID{uuid.New()}, time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC))
	if !errors.Is(err, ErrCoordinationUnavailable) {
		t.Fatalf("redis error = %v", err)
	}
}

type memoryAdminRoomLeaseClient struct {
	values map[string]string
	ttls   map[string]time.Duration
	err    error
}

func (client *memoryAdminRoomLeaseClient) Get(ctx context.Context, key string) *goredis.StringCmd {
	cmd := goredis.NewStringCmd(ctx, "get", key)
	if client.err != nil {
		cmd.SetErr(client.err)
		return cmd
	}
	value, ok := client.values[key]
	if !ok {
		cmd.SetErr(goredis.Nil)
		return cmd
	}
	cmd.SetVal(value)
	return cmd
}

func (client *memoryAdminRoomLeaseClient) PTTL(ctx context.Context, key string) *goredis.DurationCmd {
	cmd := goredis.NewDurationCmd(ctx, time.Millisecond, "pttl", key)
	if client.err != nil {
		cmd.SetErr(client.err)
		return cmd
	}
	cmd.SetVal(client.ttls[key])
	return cmd
}

func newTestAdminRoomOwnerReader(t testing.TB, client adminRoomLeaseClient) *AdminRoomOwnerReader {
	t.Helper()
	reader, err := NewAdminRoomOwnerReader(client, CoordinationConfig{KeyPrefix: "gn:test:", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return reader
}
