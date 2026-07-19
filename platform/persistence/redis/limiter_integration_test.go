package redis

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/security"
	goredis "github.com/redis/go-redis/v9"
)

// testRedisURLEnvironment opts into the real Redis suite without accepting ambient client configuration.
const testRedisURLEnvironment = "GAME_NIGHT_TEST_REDIS_URL"

func TestLimiterIntegration(t *testing.T) {
	client := openIntegrationRedis(t)
	keyring := integrationRateLimitKeyring(t)

	t.Run("atomic capacity under concurrency", func(t *testing.T) {
		const capacity = 8
		limiter, prefix := integrationLimiter(t, client, keyring, Rule{
			Capacity: capacity, RefillEvery: time.Hour, TTL: 9 * time.Hour,
		})
		request := integrationConsumptionRequest(t, ratelimit.OperationIdentityBootstrap, ratelimit.DimensionIP, "203.0.113.10")

		var allowed atomic.Int32
		var waitGroup sync.WaitGroup
		start := make(chan struct{})
		for range 32 {
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				<-start
				decision, err := limiter.Consume(context.Background(), request)
				if err != nil {
					t.Errorf("consume concurrent bucket: %v", err)
					return
				}
				if decision.Allowed {
					allowed.Add(1)
				}
			}()
		}
		close(start)
		waitGroup.Wait()
		if got := allowed.Load(); got != capacity {
			t.Fatalf("allowed concurrent requests = %d, want %d", got, capacity)
		}
		assertIntegrationKeyCount(t, client, prefix, 1)
	})

	t.Run("refills from Redis time", func(t *testing.T) {
		limiter, _ := integrationLimiter(t, client, keyring, Rule{
			Capacity: 1, RefillEvery: 150 * time.Millisecond, TTL: 500 * time.Millisecond,
		})
		request := integrationConsumptionRequest(t, ratelimit.OperationIdentityBootstrap, ratelimit.DimensionIP, "203.0.113.11")
		first, err := limiter.Consume(context.Background(), request)
		if err != nil || !first.Allowed {
			t.Fatalf("first decision = %+v, err = %v", first, err)
		}
		rejected, err := limiter.Consume(context.Background(), request)
		if err != nil || rejected.Allowed || rejected.RetryAfter <= 0 || rejected.RetryAfter > 150*time.Millisecond {
			t.Fatalf("exhausted decision = %+v, err = %v", rejected, err)
		}
		time.Sleep(200 * time.Millisecond)
		refilled, err := limiter.Consume(context.Background(), request)
		if err != nil || !refilled.Allowed {
			t.Fatalf("refilled decision = %+v, err = %v", refilled, err)
		}
	})

	t.Run("expires idle buckets", func(t *testing.T) {
		limiter, prefix := integrationLimiter(t, client, keyring, Rule{
			Capacity: 1, RefillEvery: 100 * time.Millisecond, TTL: 250 * time.Millisecond,
		})
		request := integrationConsumptionRequest(t, ratelimit.OperationIdentityBootstrap, ratelimit.DimensionIP, "203.0.113.12")
		if decision, err := limiter.Consume(context.Background(), request); err != nil || !decision.Allowed {
			t.Fatalf("consume expiring bucket = %+v, err = %v", decision, err)
		}
		keys := integrationKeys(t, client, prefix)
		if len(keys) != 1 {
			t.Fatalf("bucket keys = %v", keys)
		}
		ttl, err := client.PTTL(context.Background(), keys[0]).Result()
		if err != nil || ttl <= 0 || ttl > 250*time.Millisecond {
			t.Fatalf("bucket TTL = %s, err = %v", ttl, err)
		}
		time.Sleep(350 * time.Millisecond)
		assertIntegrationKeyCount(t, client, prefix, 0)
	})

	t.Run("pseudonymizes and separates bucket values", func(t *testing.T) {
		limiter, prefix := integrationLimiter(t, client, keyring, Rule{
			Capacity: 1, RefillEvery: time.Hour, TTL: 2 * time.Hour,
		})
		firstRaw := "sensitive-device-alpha"
		secondRaw := "sensitive-device-beta"
		first := integrationConsumptionRequest(t, ratelimit.OperationUsernameCheck, ratelimit.DimensionDevice, firstRaw)
		second := integrationConsumptionRequest(t, ratelimit.OperationUsernameCheck, ratelimit.DimensionDevice, secondRaw)
		for _, request := range []ratelimit.ConsumptionRequest{first, second} {
			decision, err := limiter.Consume(context.Background(), request)
			if err != nil || !decision.Allowed {
				t.Fatalf("independent decision = %+v, err = %v", decision, err)
			}
		}
		if decision, err := limiter.Consume(context.Background(), first); err != nil || decision.Allowed {
			t.Fatalf("reused bucket decision = %+v, err = %v", decision, err)
		}
		keys := integrationKeys(t, client, prefix)
		if len(keys) != 2 {
			t.Fatalf("independent bucket keys = %v", keys)
		}
		for _, key := range keys {
			if strings.Contains(key, firstRaw) || strings.Contains(key, secondRaw) || !strings.Contains(key, ":v7:") {
				t.Fatalf("unsafe or unversioned Redis key %q", key)
			}
		}
	})

	t.Run("fails closed after Redis disconnect", func(t *testing.T) {
		redisURL := integrationtest.RequireEnvironment(t, integrationtest.DependencyRedis, testRedisURLEnvironment)[0]
		options, err := goredis.ParseURL(redisURL)
		if err != nil {
			t.Fatal("parse Redis integration URL")
		}
		disconnectedClient := goredis.NewClient(options)
		if err := disconnectedClient.Ping(t.Context()).Err(); err != nil {
			t.Fatal("connect Redis outage fixture")
		}
		limiter, err := NewLimiter(disconnectedClient, keyring, Config{
			KeyPrefix: randomIntegrationPrefix(t), Timeout: 2 * time.Second, Rules: StandardRules(),
		})
		if err != nil {
			t.Fatal(err)
		}
		// Closing only this client deterministically models a severed connection without destabilizing the shared CI service.
		if err := disconnectedClient.Close(); err != nil {
			t.Fatal(err)
		}
		request := integrationConsumptionRequest(t, ratelimit.OperationIdentityBootstrap, ratelimit.DimensionIP, "203.0.113.13")
		decision, err := limiter.Consume(t.Context(), request)
		if !errors.Is(err, ErrUnavailable) || decision.Allowed || decision.RetryAfter != 0 {
			t.Fatalf("disconnected Redis decision = %+v, error = %v", decision, err)
		}
	})
}

func openIntegrationRedis(t testing.TB) *goredis.Client {
	t.Helper()
	redisURL := integrationtest.RequireEnvironment(t, integrationtest.DependencyRedis, testRedisURLEnvironment)[0]
	options, err := goredis.ParseURL(redisURL)
	if err != nil {
		t.Fatal("parse Redis integration URL")
	}
	client := goredis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatal("connect to Redis integration service")
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close Redis integration client: %v", err)
		}
	})
	return client
}

func integrationLimiter(
	t testing.TB,
	client *goredis.Client,
	keyring *security.HMACKeyring[security.RateLimitHMACKeyPurpose],
	rule Rule,
) (*Limiter, string) {
	t.Helper()
	prefix := randomIntegrationPrefix(t)
	rules := StandardRules()
	for operation, dimensions := range rules {
		for dimension := range dimensions {
			rules[operation][dimension] = rule
		}
	}
	limiter, err := NewLimiter(client, keyring, Config{KeyPrefix: prefix, Timeout: 2 * time.Second, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteIntegrationKeys(t, client, prefix) })
	return limiter, prefix
}

func integrationConsumptionRequest(
	t testing.TB,
	operation ratelimit.Operation,
	dimension ratelimit.Dimension,
	raw string,
) ratelimit.ConsumptionRequest {
	t.Helper()
	value, err := ratelimit.NewBucketValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ratelimit.NewBucketKey(dimension, value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := ratelimit.NewConsumptionRequest(operation, key)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func integrationRateLimitKeyring(t testing.TB) *security.HMACKeyring[security.RateLimitHMACKeyPurpose] {
	t.Helper()
	now := time.Now().UTC()
	document := struct {
		ActiveVersion uint32 `json:"active_version"`
		Keys          []struct {
			Version   uint32    `json:"version"`
			Key       string    `json:"key"`
			NotBefore time.Time `json:"not_before"`
		} `json:"keys"`
	}{ActiveVersion: 7}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	document.Keys = append(document.Keys, struct {
		Version   uint32    `json:"version"`
		Key       string    `json:"key"`
		NotBefore time.Time `json:"not_before"`
	}{Version: 7, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour)})
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + string(os.PathSeparator) + "rate-limit-keyring.json"
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o400)
	if runtime.GOOS == "windows" {
		mode = 0o444
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	keyring, err := security.LoadHMACKeyring[security.RateLimitHMACKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func randomIntegrationPrefix(t testing.TB) string {
	t.Helper()
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return "gn:test:" + hex.EncodeToString(value) + ":"
}

func integrationKeys(t testing.TB, client *goredis.Client, prefix string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cursor uint64
	var result []string
	for {
		keys, next, err := client.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			t.Fatal("scan Redis integration keys")
		}
		result = append(result, keys...)
		cursor = next
		if cursor == 0 {
			return result
		}
	}
}

func assertIntegrationKeyCount(t testing.TB, client *goredis.Client, prefix string, want int) {
	t.Helper()
	if keys := integrationKeys(t, client, prefix); len(keys) != want {
		t.Fatalf("Redis key count = %d, want %d: %v", len(keys), want, keys)
	}
}

func deleteIntegrationKeys(t testing.TB, client *goredis.Client, prefix string) {
	t.Helper()
	keys := integrationKeys(t, client, prefix)
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Unlink(ctx, keys...).Err(); err != nil {
		t.Errorf("delete Redis integration keys")
	}
}
