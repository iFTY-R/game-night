package redis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

func TestNewGameCoordinatorValidatesEveryBound(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	valid := CoordinationConfig{
		KeyPrefix: "gn:test:", Timeout: time.Second, TicketTTL: time.Minute, LeaseTTL: 15 * time.Second,
	}
	coordinator, err := NewGameCoordinator(client, valid)
	if err != nil || coordinator == nil {
		t.Fatalf("valid coordinator = %v, error = %v", coordinator, err)
	}

	tests := []struct {
		name   string
		client CoordinationClient
		mutate func(*CoordinationConfig)
	}{
		{name: "nil client", client: nil},
		{name: "invalid prefix", client: client, mutate: func(config *CoordinationConfig) { config.KeyPrefix = "bad*" }},
		{name: "short timeout", client: client, mutate: func(config *CoordinationConfig) { config.Timeout = 0 }},
		{name: "long timeout", client: client, mutate: func(config *CoordinationConfig) { config.Timeout = CoordinationTimeout + time.Millisecond }},
		{name: "short ticket", client: client, mutate: func(config *CoordinationConfig) { config.TicketTTL = MinimumTicketTTL - time.Millisecond }},
		{name: "long ticket", client: client, mutate: func(config *CoordinationConfig) { config.TicketTTL = MaximumTicketTTL + time.Millisecond }},
		{name: "short lease", client: client, mutate: func(config *CoordinationConfig) { config.LeaseTTL = MinimumSessionLeaseTTL - time.Millisecond }},
		{name: "long lease", client: client, mutate: func(config *CoordinationConfig) { config.LeaseTTL = MaximumSessionLeaseTTL + time.Millisecond }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			if test.mutate != nil {
				test.mutate(&config)
			}
			if _, err := NewGameCoordinator(test.client, config); !errors.Is(err, ErrInvalidCoordinationConfig) {
				t.Fatalf("error = %v, want invalid config", err)
			}
		})
	}
}

func TestConnectionTicketKeyIsNamespacedAndDoesNotRetainTicket(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	coordinator := testGameCoordinator(t, client)
	ticket := strings.Repeat("A", 43)
	if !validTicket(ticket) {
		t.Fatal("canonical 32-byte ticket fixture is invalid")
	}
	key := coordinator.ticketKey(ticket)
	if !strings.HasPrefix(key, "gn:test:game:ticket:v1:") || strings.Contains(key, ticket) {
		t.Fatalf("unsafe ticket key %q", key)
	}
	if key != coordinator.ticketKey(ticket) || key == coordinator.ticketKey(strings.Repeat("B", 43)) {
		t.Fatal("ticket key derivation is not deterministic and separated")
	}
}

func TestSessionFanoutWireContainsOnlySessionAndVersion(t *testing.T) {
	event := SessionFanoutEvent{SessionID: uuid.MustParse("018f3f7c-296f-7a4e-8e16-0bba70f3f4aa"), StateVersion: 42}
	payload, err := MarshalSessionFanoutEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"session_id":"018f3f7c-296f-7a4e-8e16-0bba70f3f4aa","state_version":42}`
	if string(payload) != want {
		t.Fatalf("fanout payload = %s, want %s", payload, want)
	}
	parsed, err := ParseSessionFanoutEvent(payload)
	if err != nil || parsed != event {
		t.Fatalf("parsed fanout = %+v, error = %v", parsed, err)
	}

	for _, invalid := range [][]byte{
		nil,
		[]byte(`{"session_id":"018f3f7c-296f-7a4e-8e16-0bba70f3f4aa","state_version":0}`),
		[]byte(`{"session_id":"018F3F7C-296F-7A4E-8E16-0BBA70F3F4AA","state_version":1}`),
		[]byte(`{"session_id":"018f3f7c-296f-7a4e-8e16-0bba70f3f4aa","state_version":1,"payload":"secret"}`),
		[]byte(`{"session_id":"018f3f7c-296f-7a4e-8e16-0bba70f3f4aa","state_version":1} {}`),
	} {
		if _, err := ParseSessionFanoutEvent(invalid); !errors.Is(err, ErrInvalidCoordinationInput) {
			t.Fatalf("invalid fanout %q error = %v", invalid, err)
		}
	}
}

func TestSessionLeaseWireKeepsMutationTokenPrivateOnLookupShape(t *testing.T) {
	sessionID := uuid.New()
	token := strings.Repeat("A", 43)
	lease, err := decodeLeaseWire(sessionID, encodeLeaseWire("realtime-1", "10.0.0.2:9090", token, false, 0))
	if err != nil || !lease.Active() || lease.Routable() {
		t.Fatalf("active lease = %+v, error = %v", lease, err)
	}
	ready, err := decodeLeaseWire(sessionID, encodeLeaseWire("realtime-1", "10.0.0.2:9090", token, true, 7))
	if err != nil || !ready.Active() || !ready.Routable() || ready.OwnershipEpoch != 7 {
		t.Fatalf("ready lease = %+v, error = %v", ready, err)
	}
	lease.Token = ""
	if lease.Active() {
		t.Fatal("route-only lease unexpectedly authorizes mutation")
	}
	for _, invalid := range []string{
		"v1|ready|realtime-1|10.0.0.2:9090|0|" + token,
		"v1|claiming|realtime-1|10.0.0.2:9090|7|" + token,
		"realtime|bad|address|" + token,
		"realtime-1|10.0.0.2:9090|short",
		"realtime-1\n|10.0.0.2:9090|" + token,
		"realtime 1|10.0.0.2:9090|" + token,
		"realtime-1|10.0.0.2:90 90|" + token,
	} {
		if _, err := decodeLeaseWire(sessionID, invalid); !errors.Is(err, ErrInvalidCoordinationInput) {
			t.Fatalf("invalid lease %q error = %v", invalid, err)
		}
	}
}

func TestGameCoordinatorFailsClosedAfterRedisClientClose(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	coordinator := testGameCoordinator(t, client)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := coordinator.IssueConnectionTicket(ctx, []byte("grant")); !errors.Is(err, ErrCoordinationUnavailable) {
		t.Fatalf("ticket issue error = %v", err)
	}
	if err := coordinator.PublishSessionFanout(ctx, SessionFanoutEvent{SessionID: uuid.New(), StateVersion: 1}); !errors.Is(err, ErrCoordinationUnavailable) {
		t.Fatalf("fanout publish error = %v", err)
	}
	if _, _, err := coordinator.AcquireSessionLease(ctx, uuid.New(), "realtime-1", "127.0.0.1:9090"); !errors.Is(err, ErrCoordinationUnavailable) {
		t.Fatalf("lease acquire error = %v", err)
	}
	if _, err := coordinator.SubscribeSessionFanout(ctx); !errors.Is(err, ErrCoordinationUnavailable) {
		t.Fatalf("fanout subscribe error = %v", err)
	}
}

func testGameCoordinator(t testing.TB, client CoordinationClient) *GameCoordinator {
	t.Helper()
	coordinator, err := NewGameCoordinator(client, CoordinationConfig{
		KeyPrefix: "gn:test:", Timeout: time.Second, TicketTTL: time.Minute, LeaseTTL: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}
