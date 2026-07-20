package redis

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestGameCoordinatorIntegration(t *testing.T) {
	client := openIntegrationRedis(t)
	coordinator, err := NewGameCoordinator(client, CoordinationConfig{
		KeyPrefix: randomIntegrationPrefix(t), Timeout: 2 * time.Second,
		TicketTTL: 2 * time.Second, LeaseTTL: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("concurrent ticket consumption commits once", func(t *testing.T) {
		ticket, issueErr := coordinator.IssueConnectionTicket(t.Context(), []byte("concurrent-grant"))
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		var consumed atomic.Int32
		var waitGroup sync.WaitGroup
		start := make(chan struct{})
		for range 24 {
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				<-start
				accepted, consumeErr := coordinator.ConsumeConnectionTicket(context.Background(), ticket, []byte("concurrent-grant"))
				if consumeErr != nil {
					t.Errorf("consume ticket: %v", consumeErr)
					return
				}
				if accepted {
					consumed.Add(1)
				}
			}()
		}
		close(start)
		waitGroup.Wait()
		if got := consumed.Load(); got != 1 {
			t.Fatalf("successful consumes = %d, want 1", got)
		}
	})
	t.Run("connection ticket is grant-bound and one-time", func(t *testing.T) {
		grant := []byte("viewer-grant-v1")
		ticket, issueErr := coordinator.IssueConnectionTicket(t.Context(), grant)
		if issueErr != nil || !validTicket(ticket) {
			t.Fatalf("ticket = %q, error = %v", ticket, issueErr)
		}
		ttl, ttlErr := client.PTTL(t.Context(), coordinator.ticketKey(ticket)).Result()
		if ttlErr != nil || ttl <= 0 || ttl > 2*time.Second {
			t.Fatalf("ticket TTL = %s, error = %v", ttl, ttlErr)
		}
		consumed, consumeErr := coordinator.ConsumeConnectionTicket(t.Context(), ticket, []byte("other-grant"))
		if consumeErr != nil || consumed {
			t.Fatalf("mismatched consume = %t, error = %v", consumed, consumeErr)
		}
		consumed, consumeErr = coordinator.ConsumeConnectionTicket(t.Context(), ticket, grant)
		if consumeErr != nil || !consumed {
			t.Fatalf("matching consume = %t, error = %v", consumed, consumeErr)
		}
		consumed, consumeErr = coordinator.ConsumeConnectionTicket(t.Context(), ticket, grant)
		if consumeErr != nil || consumed {
			t.Fatalf("replayed consume = %t, error = %v", consumed, consumeErr)
		}
	})

	t.Run("concurrent lease acquisition elects one token holder", func(t *testing.T) {
		sessionID := uuid.New()
		var acquired atomic.Int32
		winner := make(chan SessionLease, 24)
		var waitGroup sync.WaitGroup
		start := make(chan struct{})
		for range 24 {
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				<-start
				lease, accepted, acquireErr := coordinator.AcquireSessionLease(
					context.Background(), sessionID, "realtime-contender", "10.0.0.3:9090",
				)
				if acquireErr != nil {
					t.Errorf("acquire lease: %v", acquireErr)
					return
				}
				if accepted {
					acquired.Add(1)
					winner <- lease
				}
			}()
		}
		close(start)
		waitGroup.Wait()
		if got := acquired.Load(); got != 1 {
			t.Fatalf("successful acquisitions = %d, want 1", got)
		}
		close(winner)
		lease := <-winner
		if released, releaseErr := coordinator.ReleaseSessionLease(t.Context(), lease); releaseErr != nil || !released {
			t.Fatalf("release winning lease = %t, error = %v", released, releaseErr)
		}
	})

	t.Run("lease renewal and release require the exact token", func(t *testing.T) {
		sessionID := uuid.New()
		first, acquired, acquireErr := coordinator.AcquireSessionLease(t.Context(), sessionID, "realtime-a", "10.0.0.1:9090")
		if acquireErr != nil || !acquired || !first.Active() {
			t.Fatalf("first lease = %+v, acquired = %t, error = %v", first, acquired, acquireErr)
		}
		first, promoted, promoteErr := coordinator.PromoteSessionLease(t.Context(), first, 7)
		if promoteErr != nil || !promoted || !first.Routable() {
			t.Fatalf("promoted lease = %+v, promoted = %t, error = %v", first, promoted, promoteErr)
		}
		route, acquired, acquireErr := coordinator.AcquireSessionLease(t.Context(), sessionID, "realtime-b", "10.0.0.2:9090")
		if acquireErr != nil || acquired || route.Owner != "realtime-a" || route.Address != "10.0.0.1:9090" ||
			route.Token != "" || !route.Routable() || route.OwnershipEpoch != 7 {
			t.Fatalf("contended route = %+v, acquired = %t, error = %v", route, acquired, acquireErr)
		}
		lookedUp, lookupErr := coordinator.LookupSessionLease(t.Context(), sessionID)
		if lookupErr != nil || lookedUp != route {
			t.Fatalf("lookup = %+v, error = %v, want %+v", lookedUp, lookupErr, route)
		}

		stale := first
		stale.Token = routeOnlyTokenFixture()
		if renewed, renewErr := coordinator.RenewSessionLease(t.Context(), stale); renewErr != nil || renewed {
			t.Fatalf("stale renew = %t, error = %v", renewed, renewErr)
		}
		if released, releaseErr := coordinator.ReleaseSessionLease(t.Context(), stale); releaseErr != nil || released {
			t.Fatalf("stale release = %t, error = %v", released, releaseErr)
		}
		if renewed, renewErr := coordinator.RenewSessionLease(t.Context(), first); renewErr != nil || !renewed {
			t.Fatalf("owner renew = %t, error = %v", renewed, renewErr)
		}
		if released, releaseErr := coordinator.ReleaseSessionLease(t.Context(), first); releaseErr != nil || !released {
			t.Fatalf("owner release = %t, error = %v", released, releaseErr)
		}
		second, acquired, acquireErr := coordinator.AcquireSessionLease(t.Context(), sessionID, "realtime-b", "10.0.0.2:9090")
		if acquireErr != nil || !acquired || !second.Active() {
			t.Fatalf("transferred lease = %+v, acquired = %t, error = %v", second, acquired, acquireErr)
		}
		if _, releaseErr := coordinator.ReleaseSessionLease(t.Context(), second); releaseErr != nil {
			t.Fatal(releaseErr)
		}
	})

	t.Run("fanout contains only committed session version", func(t *testing.T) {
		subscription, subscribeErr := coordinator.SubscribeSessionFanout(t.Context())
		if subscribeErr != nil {
			t.Fatal(subscribeErr)
		}
		t.Cleanup(func() {
			if err := subscription.Close(); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("close fanout subscription: %v", err)
			}
		})
		want := SessionFanoutEvent{SessionID: uuid.New(), StateVersion: 37}
		if publishErr := coordinator.PublishSessionFanout(t.Context(), want); publishErr != nil {
			t.Fatal(publishErr)
		}
		select {
		case message := <-subscription.Messages():
			if message == nil {
				t.Fatal("fanout subscription closed before delivery")
			}
			got, parseErr := ParseSessionFanoutEvent([]byte(message.Payload))
			if parseErr != nil || got != want {
				t.Fatalf("fanout = %+v, error = %v, want %+v", got, parseErr, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for fanout")
		}
	})
}

func routeOnlyTokenFixture() string {
	return "__________________________________________8"
}
