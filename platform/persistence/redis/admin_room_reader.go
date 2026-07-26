package redis

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	adminroom "github.com/iFTY-R/game-night/platform/admin/room"
	goredis "github.com/redis/go-redis/v9"
)

type adminRoomLeaseClient interface {
	Get(context.Context, string) *goredis.StringCmd
	PTTL(context.Context, string) *goredis.DurationCmd
}

type AdminRoomOwnerReader struct {
	client    adminRoomLeaseClient
	keyPrefix string
	timeout   time.Duration
}

// NewAdminRoomOwnerReader creates a token-free lease reader for administrator query screens.
func NewAdminRoomOwnerReader(client adminRoomLeaseClient, config CoordinationConfig) (*AdminRoomOwnerReader, error) {
	if client == nil || !redisKeyPrefixPattern.MatchString(config.KeyPrefix) ||
		config.Timeout < time.Millisecond || config.Timeout > CoordinationTimeout {
		return nil, ErrInvalidCoordinationConfig
	}
	return &AdminRoomOwnerReader{client: client, keyPrefix: config.KeyPrefix, timeout: config.Timeout}, nil
}

// ReadOwners samples bounded Redis lease keys and omits missing leases so the domain can apply one fallback path.
func (reader *AdminRoomOwnerReader) ReadOwners(ctx context.Context, sessionIDs []uuid.UUID, observedAt time.Time) (map[uuid.UUID]adminroom.OwnerLeaseSummary, error) {
	if reader == nil || reader.client == nil || ctx == nil || observedAt.IsZero() || len(sessionIDs) > 200 {
		return nil, ErrInvalidCoordinationInput
	}
	limited, cancel := context.WithTimeout(ctx, reader.timeout)
	defer cancel()
	result := make(map[uuid.UUID]adminroom.OwnerLeaseSummary, len(sessionIDs))
	seen := make(map[uuid.UUID]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID == uuid.Nil {
			return nil, ErrInvalidCoordinationInput
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		owner, err := reader.readOwner(limited, sessionID, observedAt)
		if err != nil {
			return nil, err
		}
		if owner.Freshness != adminroom.OwnerFreshnessMissing {
			result[sessionID] = owner
		}
	}
	return result, nil
}

func (reader *AdminRoomOwnerReader) readOwner(ctx context.Context, sessionID uuid.UUID, observedAt time.Time) (adminroom.OwnerLeaseSummary, error) {
	key := reader.leaseKey(sessionID)
	raw, err := reader.client.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return adminroom.OwnerLeaseSummary{SessionID: sessionID, Freshness: adminroom.OwnerFreshnessMissing, ObservedAt: observedAt}, nil
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return adminroom.OwnerLeaseSummary{}, ctxErr
		}
		return adminroom.OwnerLeaseSummary{}, ErrCoordinationUnavailable
	}
	lease, err := decodeLeaseWire(sessionID, raw)
	if err != nil {
		return adminroom.OwnerLeaseSummary{}, ErrCoordinationUnavailable
	}
	ttl, err := reader.client.PTTL(ctx, key).Result()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return adminroom.OwnerLeaseSummary{}, ctxErr
		}
		return adminroom.OwnerLeaseSummary{}, ErrCoordinationUnavailable
	}
	freshness := adminroom.OwnerFreshnessFresh
	expiresAt := observedAt.Add(ttl)
	if ttl <= 0 {
		freshness = adminroom.OwnerFreshnessExpired
		expiresAt = time.Time{}
	} else if !lease.Routable() {
		freshness = adminroom.OwnerFreshnessStale
	}
	return adminroom.OwnerLeaseSummary{
		SessionID: sessionID, OwnerInstance: lease.Owner, OwnerAddress: lease.Address, OwnershipEpoch: lease.OwnershipEpoch,
		Freshness: freshness, ObservedAt: observedAt, ExpiresAt: expiresAt,
	}, nil
}

func (reader *AdminRoomOwnerReader) leaseKey(sessionID uuid.UUID) string {
	return reader.keyPrefix + "game:lease:v1:" + sessionID.String()
}

var _ adminroom.OwnerReader = (*AdminRoomOwnerReader)(nil)
