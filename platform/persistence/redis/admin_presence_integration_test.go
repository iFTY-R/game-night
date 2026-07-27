package redis

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
)

func TestAdminPresenceProjectionConnectionLifecycle(t *testing.T) {
	client := openIntegrationRedis(t)
	prefix := randomIntegrationPrefix(t)
	t.Cleanup(func() { deleteIntegrationKeys(t, client, prefix) })
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	source := clock.NewFake(now)
	projection, err := NewAdminPresenceProjection(client, AdminPresenceConfig{KeyPrefix: prefix, Timeout: time.Second, TTL: 10 * time.Second, Clock: source})
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	first := AdminPresenceConnection{ConnectionID: uuid.New(), UserID: userID, RoomID: uuid.New(), SessionID: uuid.New()}
	second := AdminPresenceConnection{ConnectionID: uuid.New(), UserID: userID, RoomID: uuid.New(), SessionID: uuid.New()}
	for _, connection := range []AdminPresenceConnection{first, second, first} {
		if err := projection.UpsertConnection(context.Background(), connection); err != nil {
			t.Fatal(err)
		}
	}
	count, err := projection.CountOnlineUsers(context.Background(), now)
	if err != nil || count != 1 {
		t.Fatalf("online users = %d, error = %v", count, err)
	}
	summary, err := projection.ReadPresenceSummary(context.Background())
	if err != nil || summary.OnlineUsers != 1 || summary.ActiveConnections != 2 {
		t.Fatalf("presence summary = %+v, error = %v", summary, err)
	}
	users, err := projection.ReadUserPresence(context.Background(), []uuid.UUID{userID}, now)
	if err != nil || users[userID].ConnectionCount != 2 {
		t.Fatalf("user presence = %+v, error = %v", users[userID], err)
	}
	if err := projection.RemoveConnection(context.Background(), first.ConnectionID); err != nil {
		t.Fatal(err)
	}
	users, err = projection.ReadUserPresence(context.Background(), []uuid.UUID{userID}, now)
	if err != nil || users[userID].ConnectionCount != 1 {
		t.Fatalf("presence after disconnect = %+v, error = %v", users[userID], err)
	}
	if _, err := source.Advance(11 * time.Second); err != nil {
		t.Fatal(err)
	}
	count, err = projection.CountOnlineUsers(context.Background(), source.Now())
	if err != nil || count != 0 {
		t.Fatalf("expired online users = %d, error = %v", count, err)
	}
}
