package redis

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/clock"
	goredis "github.com/redis/go-redis/v9"
)

const (
	// MaximumAdminPresenceUsersPerRead keeps one administrator page-presence sample bounded.
	MaximumAdminPresenceUsersPerRead = 200
	// AdminPresenceTTL bounds stale online state after a realtime process exits without disconnect cleanup.
	AdminPresenceTTL = 5 * time.Minute
)

// AdminPresenceConnection identifies one authenticated realtime connection projected into Redis.
type AdminPresenceConnection struct {
	ConnectionID uuid.UUID
	UserID       uuid.UUID
	RoomID       uuid.UUID
	SessionID    uuid.UUID
}

// Valid rejects anonymous or partially identified connections before Redis state changes.
func (connection AdminPresenceConnection) Valid() bool {
	return connection.ConnectionID != uuid.Nil && connection.UserID != uuid.Nil &&
		connection.RoomID != uuid.Nil && connection.SessionID != uuid.Nil
}

// AdminUserPresence is the bounded projection returned for one current-page user row.
type AdminUserPresence struct {
	UserID          uuid.UUID
	ConnectionCount uint32
}

// Online reports whether at least one live realtime connection is currently projected for this user.
func (presence AdminUserPresence) Online() bool { return presence.ConnectionCount > 0 }

// AdminPresenceSink is the best-effort realtime hook used by the subscription hub.
type AdminPresenceSink interface {
	UpsertConnection(context.Context, AdminPresenceConnection) error
	RemoveConnection(context.Context, uuid.UUID) error
}

// AdminPresenceReader exposes only the bounded administrator query paths.
type AdminPresenceReader interface {
	ReadPresenceSummary(context.Context) (operations.PresenceSummary, error)
	CountOnlineUsers(context.Context, time.Time) (uint64, error)
	ReadUserPresence(context.Context, []uuid.UUID, time.Time) (map[uuid.UUID]AdminUserPresence, error)
}

// AdminPresenceConfig bounds the Redis namespace, TTL, and operation timeout used by the projection.
type AdminPresenceConfig struct {
	KeyPrefix string
	Timeout   time.Duration
	TTL       time.Duration
	Clock     clock.Clock
}

// AdminPresenceProjection stores a rebuildable realtime presence projection in Redis.
// It never scans Redis and only touches caller-known keys.
type AdminPresenceProjection struct {
	client    adminPresenceClient
	keyPrefix string
	timeout   time.Duration
	ttl       time.Duration
	clock     clock.Clock
}

type adminPresenceClient interface {
	Incr(context.Context, string) *goredis.IntCmd
	Get(context.Context, string) *goredis.StringCmd
	Set(context.Context, string, interface{}, time.Duration) *goredis.StatusCmd
	Del(context.Context, ...string) *goredis.IntCmd
	ZAdd(context.Context, string, ...goredis.Z) *goredis.IntCmd
	ZScore(context.Context, string, string) *goredis.FloatCmd
	ZRem(context.Context, string, ...interface{}) *goredis.IntCmd
	ZRevRangeWithScores(context.Context, string, int64, int64) *goredis.ZSliceCmd
	NewPresencePipeline() adminPresencePipeline
}

type adminPresencePipeline interface {
	ZRemRangeByScore(context.Context, string, string, string) *goredis.IntCmd
	ZCount(context.Context, string, string, string) *goredis.IntCmd
	Exec(context.Context) ([]goredis.Cmder, error)
}

type redisAdminPresenceClient struct{ client *goredis.Client }

type redisAdminPresencePipeline struct{ pipeline goredis.Pipeliner }

// NewAdminPresenceProjection validates the Redis boundary and wraps a normal go-redis client.
func NewAdminPresenceProjection(client *goredis.Client, config AdminPresenceConfig) (*AdminPresenceProjection, error) {
	if client == nil {
		return nil, ErrInvalidCoordinationConfig
	}
	return newAdminPresenceProjection(redisAdminPresenceClient{client: client}, config)
}

func newAdminPresenceProjection(client adminPresenceClient, config AdminPresenceConfig) (*AdminPresenceProjection, error) {
	if client == nil || !redisKeyPrefixPattern.MatchString(config.KeyPrefix) ||
		config.Timeout < time.Millisecond || config.Timeout > CoordinationTimeout ||
		config.TTL < MinimumSessionLeaseTTL || config.TTL > MaximumSessionLeaseTTL || config.Clock == nil {
		return nil, ErrInvalidCoordinationConfig
	}
	return &AdminPresenceProjection{
		client: client, keyPrefix: config.KeyPrefix, timeout: config.Timeout, ttl: config.TTL, clock: config.Clock,
	}, nil
}

// UpsertConnection records one connection heartbeat and keeps a unique per-user online score.
func (projection *AdminPresenceProjection) UpsertConnection(ctx context.Context, connection AdminPresenceConnection) error {
	if projection == nil || projection.client == nil || ctx == nil || !connection.Valid() {
		return ErrInvalidCoordinationInput
	}
	limited, cancel := context.WithTimeout(ctx, projection.timeout)
	defer cancel()

	now := projection.clock.Now()
	expiry := now.Add(projection.ttl)
	expiryScore := float64(expiry.UnixMilli())
	connectionID := connection.ConnectionID.String()
	userID := connection.UserID.String()

	previousUserID := ""
	loadedUserID, err := projection.client.Get(limited, projection.connectionUserKey(connection.ConnectionID)).Result()
	switch {
	case errors.Is(err, goredis.Nil):
	case err != nil:
		return projection.mapUnavailable(ctx, err)
	default:
		previousUserID = loadedUserID
	}

	generation, err := projection.client.Incr(limited, projection.generationKey()).Result()
	if err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	if err := projection.client.Set(limited, projection.connectionVersionKey(connection.ConnectionID), strconv.FormatInt(generation, 10), projection.ttl).Err(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	if err := projection.client.Set(limited, projection.connectionTTLKey(connection.ConnectionID), "1", projection.ttl).Err(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	if err := projection.client.Set(limited, projection.connectionUserKey(connection.ConnectionID), userID, projection.ttl).Err(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	if err := projection.client.ZAdd(limited, projection.userConnectionsKey(connection.UserID), goredis.Z{
		Score: expiryScore, Member: connectionID,
	}).Err(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	if err := projection.client.ZAdd(limited, projection.allConnectionsKey(), goredis.Z{Score: expiryScore, Member: connectionID}).Err(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}

	currentScore, err := projection.client.ZScore(limited, projection.onlineUsersKey(), userID).Result()
	switch {
	case errors.Is(err, goredis.Nil):
		currentScore = 0
	case err != nil:
		return projection.mapUnavailable(ctx, err)
	}
	if currentScore < expiryScore {
		if err := projection.client.ZAdd(limited, projection.onlineUsersKey(), goredis.Z{
			Score: expiryScore, Member: userID,
		}).Err(); err != nil {
			return projection.mapUnavailable(ctx, err)
		}
	}

	// A connection ID should remain unique to one subscriber lifetime. If a reused ID ever
	// crosses users, repair the old user index immediately so counts remain exact.
	if previousUserID != "" && previousUserID != userID {
		previousUUID, parseErr := uuid.Parse(previousUserID)
		if parseErr != nil || previousUUID == uuid.Nil || previousUUID.String() != previousUserID {
			return ErrCoordinationUnavailable
		}
		if err := projection.client.ZRem(limited, projection.userConnectionsKey(previousUUID), connectionID).Err(); err != nil {
			return projection.mapUnavailable(ctx, err)
		}
		if err := projection.repairOnlineUserScore(limited, previousUUID, now); err != nil {
			return err
		}
	}
	return nil
}

// RemoveConnection removes one connection immediately and repairs the unique-user index.
func (projection *AdminPresenceProjection) RemoveConnection(ctx context.Context, connectionID uuid.UUID) error {
	if projection == nil || projection.client == nil || ctx == nil || connectionID == uuid.Nil {
		return ErrInvalidCoordinationInput
	}
	limited, cancel := context.WithTimeout(ctx, projection.timeout)
	defer cancel()

	generationErr := projection.client.Incr(limited, projection.generationKey()).Err()
	if generationErr != nil {
		return projection.mapUnavailable(ctx, generationErr)
	}
	loadedUserID, err := projection.client.Get(limited, projection.connectionUserKey(connectionID)).Result()
	switch {
	case errors.Is(err, goredis.Nil):
		_, deleteErr := projection.client.Del(limited,
			projection.connectionVersionKey(connectionID),
			projection.connectionTTLKey(connectionID),
			projection.connectionUserKey(connectionID),
		).Result()
		return projection.mapUnavailable(ctx, deleteErr)
	case err != nil:
		return projection.mapUnavailable(ctx, err)
	}

	userID, parseErr := uuid.Parse(loadedUserID)
	if parseErr != nil || userID == uuid.Nil || userID.String() != loadedUserID {
		return ErrCoordinationUnavailable
	}
	if _, err := projection.client.Del(limited,
		projection.connectionVersionKey(connectionID),
		projection.connectionTTLKey(connectionID),
		projection.connectionUserKey(connectionID),
	).Result(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	if err := projection.client.ZRem(limited, projection.userConnectionsKey(userID), connectionID.String()).Err(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	if err := projection.client.ZRem(limited, projection.allConnectionsKey(), connectionID.String()).Err(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	return projection.repairOnlineUserScore(limited, userID, projection.clock.Now())
}

// ReadPresenceSummary returns aggregate connection and unique-user counts with the projection TTL as freshness evidence.
func (projection *AdminPresenceProjection) ReadPresenceSummary(ctx context.Context) (operations.PresenceSummary, error) {
	if projection == nil || projection.client == nil || ctx == nil {
		return operations.PresenceSummary{}, ErrInvalidCoordinationInput
	}
	limited, cancel := context.WithTimeout(ctx, projection.timeout)
	defer cancel()
	sampledAt := projection.clock.Now()
	pipeline := projection.client.NewPresencePipeline()
	pipeline.ZRemRangeByScore(limited, projection.onlineUsersKey(), "-inf", expiredMaxScore(sampledAt))
	pipeline.ZRemRangeByScore(limited, projection.allConnectionsKey(), "-inf", expiredMaxScore(sampledAt))
	onlineUsers := pipeline.ZCount(limited, projection.onlineUsersKey(), onlineMinScore(sampledAt), "+inf")
	connections := pipeline.ZCount(limited, projection.allConnectionsKey(), onlineMinScore(sampledAt), "+inf")
	if _, err := pipeline.Exec(limited); err != nil {
		return operations.PresenceSummary{}, projection.mapUnavailable(ctx, err)
	}
	onlineCount, err := onlineUsers.Uint64()
	if err != nil {
		return operations.PresenceSummary{}, projection.mapUnavailable(ctx, err)
	}
	connectionCount, err := connections.Uint64()
	if err != nil {
		return operations.PresenceSummary{}, projection.mapUnavailable(ctx, err)
	}
	return operations.PresenceSummary{
		Status:            operations.HealthHealthy,
		ActiveConnections: connectionCount,
		OnlineUsers:       onlineCount,
		SampledAt:         sampledAt,
		FreshUntil:        sampledAt.Add(projection.ttl),
	}, nil
}

// CountOnlineUsers returns the current unique online-user total from the global user index.
func (projection *AdminPresenceProjection) CountOnlineUsers(ctx context.Context, observedAt time.Time) (uint64, error) {
	if projection == nil || projection.client == nil || ctx == nil || observedAt.IsZero() {
		return 0, ErrInvalidCoordinationInput
	}
	limited, cancel := context.WithTimeout(ctx, projection.timeout)
	defer cancel()

	pipeline := projection.client.NewPresencePipeline()
	pipeline.ZRemRangeByScore(limited, projection.onlineUsersKey(), "-inf", expiredMaxScore(observedAt))
	countCmd := pipeline.ZCount(limited, projection.onlineUsersKey(), onlineMinScore(observedAt), "+inf")
	if _, err := pipeline.Exec(limited); err != nil {
		return 0, projection.mapUnavailable(ctx, err)
	}
	count, err := countCmd.Uint64()
	if err != nil {
		return 0, projection.mapUnavailable(ctx, err)
	}
	return count, nil
}

// ReadUserPresence returns only the current page's presence projection using a bounded Redis pipeline.
func (projection *AdminPresenceProjection) ReadUserPresence(ctx context.Context, userIDs []uuid.UUID, observedAt time.Time) (map[uuid.UUID]AdminUserPresence, error) {
	if projection == nil || projection.client == nil || ctx == nil || observedAt.IsZero() || len(userIDs) > MaximumAdminPresenceUsersPerRead {
		return nil, ErrInvalidCoordinationInput
	}
	limited, cancel := context.WithTimeout(ctx, projection.timeout)
	defer cancel()

	result := make(map[uuid.UUID]AdminUserPresence, len(userIDs))
	seen := make(map[uuid.UUID]struct{}, len(userIDs))
	pipeline := projection.client.NewPresencePipeline()
	counts := make(map[uuid.UUID]*goredis.IntCmd, len(userIDs))
	for _, userID := range userIDs {
		if userID == uuid.Nil {
			return nil, ErrInvalidCoordinationInput
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		key := projection.userConnectionsKey(userID)
		pipeline.ZRemRangeByScore(limited, key, "-inf", expiredMaxScore(observedAt))
		counts[userID] = pipeline.ZCount(limited, key, onlineMinScore(observedAt), "+inf")
	}
	if _, err := pipeline.Exec(limited); err != nil {
		return nil, projection.mapUnavailable(ctx, err)
	}
	for userID, cmd := range counts {
		count, err := cmd.Uint64()
		if err != nil {
			return nil, projection.mapUnavailable(ctx, err)
		}
		if count > uint64(^uint32(0)) {
			return nil, ErrCoordinationUnavailable
		}
		result[userID] = AdminUserPresence{UserID: userID, ConnectionCount: uint32(count)}
	}
	return result, nil
}

func (projection *AdminPresenceProjection) repairOnlineUserScore(ctx context.Context, userID uuid.UUID, observedAt time.Time) error {
	if err := projection.client.ZRem(ctx, projection.onlineUsersKey(), userID.String()).Err(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	pipeline := projection.client.NewPresencePipeline()
	pipeline.ZRemRangeByScore(ctx, projection.userConnectionsKey(userID), "-inf", expiredMaxScore(observedAt))
	if _, err := pipeline.Exec(ctx); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	top, err := projection.client.ZRevRangeWithScores(ctx, projection.userConnectionsKey(userID), 0, 0).Result()
	if err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	if len(top) == 0 {
		return nil
	}
	if err := projection.client.ZAdd(ctx, projection.onlineUsersKey(), goredis.Z{
		Score: top[0].Score, Member: userID.String(),
	}).Err(); err != nil {
		return projection.mapUnavailable(ctx, err)
	}
	return nil
}

func (projection *AdminPresenceProjection) mapUnavailable(parent context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := parent.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrCoordinationUnavailable
}

func (projection *AdminPresenceProjection) generationKey() string {
	return projection.keyPrefix + "admin:presence:v1:generation"
}

func (projection *AdminPresenceProjection) connectionVersionKey(connectionID uuid.UUID) string {
	return projection.keyPrefix + "admin:presence:v1:connection_version:" + connectionID.String()
}

func (projection *AdminPresenceProjection) connectionTTLKey(connectionID uuid.UUID) string {
	return projection.keyPrefix + "admin:presence:v1:connection_ttl:" + connectionID.String()
}

func (projection *AdminPresenceProjection) connectionUserKey(connectionID uuid.UUID) string {
	return projection.keyPrefix + "admin:presence:v1:connection_user:" + connectionID.String()
}

func (projection *AdminPresenceProjection) userConnectionsKey(userID uuid.UUID) string {
	return projection.keyPrefix + "admin:presence:v1:user_connections:" + userID.String()
}

func (projection *AdminPresenceProjection) onlineUsersKey() string {
	return projection.keyPrefix + "admin:presence:v1:online_users"
}

func (projection *AdminPresenceProjection) allConnectionsKey() string {
	return projection.keyPrefix + "admin:presence:v1:all_connections"
}

func onlineMinScore(observedAt time.Time) string {
	return "(" + strconv.FormatInt(observedAt.Round(0).UTC().UnixMilli(), 10)
}

func expiredMaxScore(observedAt time.Time) string {
	return strconv.FormatInt(observedAt.Round(0).UTC().UnixMilli(), 10)
}

func (client redisAdminPresenceClient) Incr(ctx context.Context, key string) *goredis.IntCmd {
	return client.client.Incr(ctx, key)
}

func (client redisAdminPresenceClient) Get(ctx context.Context, key string) *goredis.StringCmd {
	return client.client.Get(ctx, key)
}

func (client redisAdminPresenceClient) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *goredis.StatusCmd {
	return client.client.Set(ctx, key, value, expiration)
}

func (client redisAdminPresenceClient) Del(ctx context.Context, keys ...string) *goredis.IntCmd {
	return client.client.Del(ctx, keys...)
}

func (client redisAdminPresenceClient) ZAdd(ctx context.Context, key string, members ...goredis.Z) *goredis.IntCmd {
	return client.client.ZAdd(ctx, key, members...)
}

func (client redisAdminPresenceClient) ZScore(ctx context.Context, key, member string) *goredis.FloatCmd {
	return client.client.ZScore(ctx, key, member)
}

func (client redisAdminPresenceClient) ZRem(ctx context.Context, key string, members ...interface{}) *goredis.IntCmd {
	return client.client.ZRem(ctx, key, members...)
}

func (client redisAdminPresenceClient) ZRevRangeWithScores(ctx context.Context, key string, start, stop int64) *goredis.ZSliceCmd {
	return client.client.ZRevRangeWithScores(ctx, key, start, stop)
}

func (client redisAdminPresenceClient) NewPresencePipeline() adminPresencePipeline {
	return redisAdminPresencePipeline{pipeline: client.client.Pipeline()}
}

func (pipeline redisAdminPresencePipeline) ZRemRangeByScore(ctx context.Context, key, min, max string) *goredis.IntCmd {
	return pipeline.pipeline.ZRemRangeByScore(ctx, key, min, max)
}

func (pipeline redisAdminPresencePipeline) ZCount(ctx context.Context, key, min, max string) *goredis.IntCmd {
	return pipeline.pipeline.ZCount(ctx, key, min, max)
}

func (pipeline redisAdminPresencePipeline) Exec(ctx context.Context) ([]goredis.Cmder, error) {
	return pipeline.pipeline.Exec(ctx)
}

var _ AdminPresenceSink = (*AdminPresenceProjection)(nil)
var _ AdminPresenceReader = (*AdminPresenceProjection)(nil)
