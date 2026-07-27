package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	goredis "github.com/redis/go-redis/v9"
)

func TestAdminPresenceProjectionValidatesClosedBoundary(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	valid := AdminPresenceConfig{KeyPrefix: "game-night:test:", Timeout: time.Second, TTL: 30 * time.Second, Clock: clock.NewFake(now)}
	if _, err := NewAdminPresenceProjection(client, valid); err != nil {
		t.Fatalf("NewAdminPresenceProjection() error = %v", err)
	}
	for _, config := range []AdminPresenceConfig{
		{KeyPrefix: "unsafe prefix", Timeout: time.Second, TTL: 30 * time.Second, Clock: clock.NewFake(now)},
		{KeyPrefix: "game-night:test:", Timeout: 0, TTL: 30 * time.Second, Clock: clock.NewFake(now)},
		{KeyPrefix: "game-night:test:", Timeout: time.Second, TTL: 0, Clock: clock.NewFake(now)},
		{KeyPrefix: "game-night:test:", Timeout: time.Second, TTL: 30 * time.Second},
	} {
		if _, err := NewAdminPresenceProjection(client, config); !errors.Is(err, ErrInvalidCoordinationConfig) {
			t.Fatalf("NewAdminPresenceProjection(%+v) error = %v", config, err)
		}
	}
}

func TestAdminPresenceProjectionRejectsMalformedConnectionsAndBoundsReads(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	projection, err := NewAdminPresenceProjection(client, AdminPresenceConfig{
		KeyPrefix: "game-night:test:", Timeout: time.Second, TTL: 30 * time.Second, Clock: clock.NewFake(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.UpsertConnection(context.Background(), AdminPresenceConnection{}); !errors.Is(err, ErrInvalidCoordinationInput) {
		t.Fatalf("UpsertConnection() error = %v", err)
	}
	if err := projection.RemoveConnection(context.Background(), uuid.Nil); !errors.Is(err, ErrInvalidCoordinationInput) {
		t.Fatalf("RemoveConnection() error = %v", err)
	}
	users := make([]uuid.UUID, MaximumAdminPresenceUsersPerRead+1)
	for index := range users {
		users[index] = uuid.New()
	}
	if _, err := projection.ReadUserPresence(context.Background(), users, now); !errors.Is(err, ErrInvalidCoordinationInput) {
		t.Fatalf("ReadUserPresence() error = %v", err)
	}
}

func TestAdminPresenceProjectionMapsDisconnectedRedis(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond})
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	projection, err := NewAdminPresenceProjection(client, AdminPresenceConfig{
		KeyPrefix: "game-night:test:", Timeout: 20 * time.Millisecond, TTL: 30 * time.Second, Clock: clock.NewFake(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	_, err = projection.CountOnlineUsers(context.Background(), now)
	if !errors.Is(err, ErrCoordinationUnavailable) {
		t.Fatalf("CountOnlineUsers() error = %v", err)
	}
}
