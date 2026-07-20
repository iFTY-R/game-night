package redis

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
	goredis "github.com/redis/go-redis/v9"
)

const (
	// CoordinationTimeout bounds Redis work used by tickets, fanout, and lease fencing.
	CoordinationTimeout = maximumOperationTimeout
	// MinimumTicketTTL prevents a ticket from expiring before a browser can complete the upgrade.
	MinimumTicketTTL = time.Second
	// MaximumTicketTTL limits the replay window for a connection grant.
	MaximumTicketTTL = 15 * time.Minute
	// MinimumSessionLeaseTTL leaves enough time for one renewal after a transient network delay.
	MinimumSessionLeaseTTL = time.Second
	// MaximumSessionLeaseTTL bounds stale ownership after a process disappears.
	MaximumSessionLeaseTTL = 5 * time.Minute
	// MaximumTicketGrantBytes bounds opaque connection authorization retained in Redis.
	MaximumTicketGrantBytes = 8 << 10
	// MaximumLeaseOwnerBytes bounds instance identifiers before they enter a Redis value or route log.
	MaximumLeaseOwnerBytes = 128
	// MaximumLeaseAddressBytes bounds the internal route address retained in a lease value.
	MaximumLeaseAddressBytes = 256
)

var (
	// ErrInvalidCoordinationConfig rejects an incomplete or unsafe Redis coordination boundary.
	ErrInvalidCoordinationConfig = errors.New("invalid Redis game coordination configuration")
	// ErrInvalidCoordinationInput rejects malformed ticket grants, session IDs, or lease metadata.
	ErrInvalidCoordinationInput = errors.New("invalid Redis game coordination input")
	// ErrCoordinationUnavailable hides Redis connection, script, and response details from callers.
	ErrCoordinationUnavailable = errors.New("Redis game coordination unavailable")

	coordinationTicketIssueScript = goredis.NewScript(`
local existing = redis.call('EXISTS', KEYS[1])
if existing == 1 then
    return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return 1
`)
	coordinationTicketConsumeScript = goredis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if not stored or stored ~= ARGV[1] then
    return 0
end
redis.call('DEL', KEYS[1])
return 1
`)
	coordinationLeaseAcquireScript = goredis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if stored then
    return {0, stored}
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2], 'NX')
stored = redis.call('GET', KEYS[1])
if not stored then
    return {0, ''}
end
if stored == ARGV[1] then
    return {1, stored}
end
return {0, stored}
`)
	coordinationLeaseLookupScript = goredis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if not stored then
    return ''
end
return stored
`)
	coordinationLeasePromoteScript = goredis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if not stored or stored ~= ARGV[1] then
    return 0
end
local updated = redis.call('SET', KEYS[1], ARGV[2], 'XX', 'KEEPTTL')
if not updated then
    return 0
end
return 1
`)
	coordinationLeaseRenewScript = goredis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if not stored or stored ~= ARGV[1] then
    return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)
	coordinationLeaseReleaseScript = goredis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if not stored or stored ~= ARGV[1] then
    return 0
end
redis.call('DEL', KEYS[1])
return 1
`)
)

// CoordinationClient is the smallest go-redis surface needed by game coordination.
// A normal *redis.Client satisfies it; the narrow interface keeps script and publish tests isolated.
type CoordinationClient interface {
	goredis.Scripter
	Publish(context.Context, string, interface{}) *goredis.IntCmd
	Subscribe(context.Context, ...string) *goredis.PubSub
}

// CoordinationConfig bounds the shared Redis namespace and all short-lived coordination state.
type CoordinationConfig struct {
	KeyPrefix string
	Timeout   time.Duration
	TicketTTL time.Duration
	LeaseTTL  time.Duration
}

// GameCoordinator owns only non-authoritative Redis state used around the game runtime.
// PostgreSQL remains the source of truth for session state, receipts, and ownership epochs.
type GameCoordinator struct {
	client    CoordinationClient
	keyPrefix string
	timeout   time.Duration
	ticketTTL time.Duration
	leaseTTL  time.Duration
}

// NewGameCoordinator validates policy before accepting connection grants or ownership work.
func NewGameCoordinator(client CoordinationClient, config CoordinationConfig) (*GameCoordinator, error) {
	if client == nil || !redisKeyPrefixPattern.MatchString(config.KeyPrefix) ||
		config.Timeout < time.Millisecond || config.Timeout > CoordinationTimeout ||
		config.TicketTTL < MinimumTicketTTL || config.TicketTTL > MaximumTicketTTL ||
		config.LeaseTTL < MinimumSessionLeaseTTL || config.LeaseTTL > MaximumSessionLeaseTTL {
		return nil, ErrInvalidCoordinationConfig
	}
	return &GameCoordinator{
		client: client, keyPrefix: config.KeyPrefix, timeout: config.Timeout,
		ticketTTL: config.TicketTTL, leaseTTL: config.LeaseTTL,
	}, nil
}

// IssueConnectionTicket stores an opaque grant under a one-time random ticket and returns only the ticket.
// The grant is never logged or returned by the consume path; callers bind it to room/session/viewer claims.
func (coordinator *GameCoordinator) IssueConnectionTicket(ctx context.Context, grant []byte) (string, error) {
	if coordinator == nil || ctx == nil || len(grant) == 0 || len(grant) > MaximumTicketGrantBytes {
		return "", ErrInvalidCoordinationInput
	}
	for range 3 {
		entropy, err := security.RandomBytes(32)
		if err != nil {
			return "", ErrCoordinationUnavailable
		}
		ticket := base64.RawURLEncoding.EncodeToString(entropy)
		clear(entropy)
		key := coordinator.ticketKey(ticket)
		result, runErr := coordinator.runScript(ctx, coordinationTicketIssueScript, []string{key}, grant, coordinator.ticketTTL.Milliseconds())
		if runErr != nil {
			return "", runErr
		}
		allowed, ok := result.(int64)
		if !ok {
			return "", ErrCoordinationUnavailable
		}
		if allowed == 1 {
			return ticket, nil
		}
		if allowed != 0 {
			return "", ErrCoordinationUnavailable
		}
	}
	return "", ErrCoordinationUnavailable
}

// ConsumeConnectionTicket atomically checks the exact grant and deletes the ticket on success.
// A missing ticket and a mismatched grant intentionally return the same false result.
func (coordinator *GameCoordinator) ConsumeConnectionTicket(ctx context.Context, ticket string, grant []byte) (bool, error) {
	if coordinator == nil || ctx == nil || !validTicket(ticket) || len(grant) == 0 || len(grant) > MaximumTicketGrantBytes {
		return false, ErrInvalidCoordinationInput
	}
	result, err := coordinator.runScript(ctx, coordinationTicketConsumeScript, []string{coordinator.ticketKey(ticket)}, grant)
	if err != nil {
		return false, err
	}
	consumed, ok := result.(int64)
	if !ok || (consumed != 0 && consumed != 1) {
		return false, ErrCoordinationUnavailable
	}
	return consumed == 1, nil
}

// SessionFanoutEvent is the only payload allowed on the game notification channel.
// Subscribers must query PostgreSQL and project for their own viewer after receiving it.
type SessionFanoutEvent struct {
	SessionID    uuid.UUID
	StateVersion uint64
}

// Valid rejects fanout payloads that could not identify one committed session version.
func (event SessionFanoutEvent) Valid() bool {
	return event.SessionID != uuid.Nil && event.StateVersion > 0
}

// MarshalSessionFanoutEvent emits a stable JSON payload with no raw event or game state fields.
func MarshalSessionFanoutEvent(event SessionFanoutEvent) ([]byte, error) {
	if !event.Valid() {
		return nil, ErrInvalidCoordinationInput
	}
	return json.Marshal(struct {
		SessionID    string `json:"session_id"`
		StateVersion uint64 `json:"state_version"`
	}{SessionID: event.SessionID.String(), StateVersion: event.StateVersion})
}

// ParseSessionFanoutEvent rejects unknown fields and non-canonical UUIDs before a subscriber acts.
func ParseSessionFanoutEvent(payload []byte) (SessionFanoutEvent, error) {
	if len(payload) == 0 || len(payload) > 256 {
		return SessionFanoutEvent{}, ErrInvalidCoordinationInput
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var wire struct {
		SessionID    string `json:"session_id"`
		StateVersion uint64 `json:"state_version"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return SessionFanoutEvent{}, ErrInvalidCoordinationInput
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return SessionFanoutEvent{}, ErrInvalidCoordinationInput
	}
	sessionID, err := uuid.Parse(wire.SessionID)
	if err != nil || sessionID == uuid.Nil || sessionID.String() != wire.SessionID || wire.StateVersion == 0 {
		return SessionFanoutEvent{}, ErrInvalidCoordinationInput
	}
	return SessionFanoutEvent{SessionID: sessionID, StateVersion: wire.StateVersion}, nil
}

// PublishSessionFanout publishes only after the caller's PostgreSQL transaction has committed.
func (coordinator *GameCoordinator) PublishSessionFanout(ctx context.Context, event SessionFanoutEvent) error {
	if coordinator == nil || ctx == nil {
		return ErrInvalidCoordinationInput
	}
	payload, err := MarshalSessionFanoutEvent(event)
	if err != nil {
		return err
	}
	limited, cancel := coordinator.operationContext(ctx)
	defer cancel()
	if err := coordinator.client.Publish(limited, coordinator.fanoutChannel(), payload).Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return ErrCoordinationUnavailable
	}
	return nil
}

// SessionFanoutSubscription owns the dedicated Redis Pub/Sub connection for one realtime process.
type SessionFanoutSubscription struct {
	pubsub *goredis.PubSub
	stream <-chan *goredis.Message
}

// Messages returns the channel of raw fanout payloads; callers must parse each payload before use.
func (subscription *SessionFanoutSubscription) Messages() <-chan *goredis.Message {
	if subscription == nil {
		return nil
	}
	return subscription.stream
}

// Close releases the Redis Pub/Sub connection owned by the subscription.
func (subscription *SessionFanoutSubscription) Close() error {
	if subscription == nil || subscription.pubsub == nil {
		return nil
	}
	return subscription.pubsub.Close()
}

// SubscribeSessionFanout creates a subscription only after Redis confirms the channel.
func (coordinator *GameCoordinator) SubscribeSessionFanout(ctx context.Context) (*SessionFanoutSubscription, error) {
	if coordinator == nil || ctx == nil {
		return nil, ErrInvalidCoordinationInput
	}
	limited, cancel := coordinator.operationContext(ctx)
	defer cancel()
	pubsub := coordinator.client.Subscribe(limited, coordinator.fanoutChannel())
	if _, err := pubsub.Receive(limited); err != nil {
		_ = pubsub.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, ErrCoordinationUnavailable
	}
	return &SessionFanoutSubscription{pubsub: pubsub, stream: pubsub.Channel()}, nil
}

// SessionLease identifies the Redis liveness token held by one realtime instance.
// Token is populated only for an acquired lease and is required for compare-token renewal/release.
type SessionLease struct {
	SessionID      uuid.UUID
	Owner          string
	Address        string
	Token          string
	Ready          bool
	OwnershipEpoch uint64
}

// Active reports whether this value carries the secret needed to mutate the lease.
func (lease SessionLease) Active() bool {
	return lease.SessionID != uuid.Nil && validLeaseOwner(lease.Owner) && validLeaseAddress(lease.Address) && validTicket(lease.Token)
}

// Routable reports whether PostgreSQL fencing completed and this value may be used for internal command routing.
func (lease SessionLease) Routable() bool {
	return lease.SessionID != uuid.Nil && validLeaseOwner(lease.Owner) && validLeaseAddress(lease.Address) &&
		lease.Ready && lease.OwnershipEpoch > 0
}

// AcquireSessionLease attempts an atomic NX lease. A false result returns the current owner/address without its token.
func (coordinator *GameCoordinator) AcquireSessionLease(ctx context.Context, sessionID uuid.UUID, owner, address string) (SessionLease, bool, error) {
	if coordinator == nil || ctx == nil || sessionID == uuid.Nil || !validLeaseOwner(owner) || !validLeaseAddress(address) {
		return SessionLease{}, false, ErrInvalidCoordinationInput
	}
	entropy, err := security.RandomBytes(32)
	if err != nil {
		return SessionLease{}, false, ErrCoordinationUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(entropy)
	clear(entropy)
	wire := encodeLeaseWire(owner, address, token, false, 0)
	result, runErr := coordinator.runScript(ctx, coordinationLeaseAcquireScript, []string{coordinator.leaseKey(sessionID)}, wire, coordinator.leaseTTL.Milliseconds())
	if runErr != nil {
		return SessionLease{}, false, runErr
	}
	values, ok := result.([]interface{})
	if !ok || len(values) != 2 {
		return SessionLease{}, false, ErrCoordinationUnavailable
	}
	acquired, ok := redisInt(values[0])
	if !ok || (acquired != 0 && acquired != 1) {
		return SessionLease{}, false, ErrCoordinationUnavailable
	}
	raw, ok := redisString(values[1])
	if !ok || raw == "" {
		return SessionLease{}, false, ErrCoordinationUnavailable
	}
	lease, parseErr := decodeLeaseWire(sessionID, raw)
	if parseErr != nil {
		return SessionLease{}, false, ErrCoordinationUnavailable
	}
	if acquired == 1 {
		if lease.Token != token {
			return SessionLease{}, false, ErrCoordinationUnavailable
		}
		return lease, true, nil
	}
	lease.Token = ""
	return lease, false, nil
}

// PromoteSessionLease makes a claiming lease routable only after PostgreSQL returned the new fencing epoch.
func (coordinator *GameCoordinator) PromoteSessionLease(ctx context.Context, lease SessionLease, ownershipEpoch uint64) (SessionLease, bool, error) {
	if coordinator == nil || ctx == nil || !lease.Active() || lease.Ready || lease.OwnershipEpoch != 0 || ownershipEpoch == 0 {
		return SessionLease{}, false, ErrInvalidCoordinationInput
	}
	ready := lease
	ready.Ready = true
	ready.OwnershipEpoch = ownershipEpoch
	result, err := coordinator.runScript(
		ctx,
		coordinationLeasePromoteScript,
		[]string{coordinator.leaseKey(lease.SessionID)},
		encodeLeaseWire(lease.Owner, lease.Address, lease.Token, false, 0),
		encodeLeaseWire(ready.Owner, ready.Address, ready.Token, true, ready.OwnershipEpoch),
	)
	if err != nil {
		return SessionLease{}, false, err
	}
	value, ok := result.(int64)
	if !ok || (value != 0 && value != 1) {
		return SessionLease{}, false, ErrCoordinationUnavailable
	}
	if value == 0 {
		return SessionLease{}, false, nil
	}
	return ready, true, nil
}

// LookupSessionLease reads only the non-authoritative owner/address route; it never returns the lease token.
func (coordinator *GameCoordinator) LookupSessionLease(ctx context.Context, sessionID uuid.UUID) (SessionLease, error) {
	if coordinator == nil || ctx == nil || sessionID == uuid.Nil {
		return SessionLease{}, ErrInvalidCoordinationInput
	}
	result, err := coordinator.runScript(ctx, coordinationLeaseLookupScript, []string{coordinator.leaseKey(sessionID)})
	if err != nil {
		return SessionLease{}, err
	}
	raw, ok := result.(string)
	if !ok {
		return SessionLease{}, ErrCoordinationUnavailable
	}
	if raw == "" {
		return SessionLease{}, nil
	}
	lease, err := decodeLeaseWire(sessionID, raw)
	if err != nil {
		return SessionLease{}, ErrCoordinationUnavailable
	}
	lease.Token = ""
	return lease, nil
}

// RenewSessionLease extends only the exact owner token and treats a lost token as a normal false result.
func (coordinator *GameCoordinator) RenewSessionLease(ctx context.Context, lease SessionLease) (bool, error) {
	if coordinator == nil || ctx == nil || !lease.Active() {
		return false, ErrInvalidCoordinationInput
	}
	result, err := coordinator.runScript(ctx, coordinationLeaseRenewScript, []string{coordinator.leaseKey(lease.SessionID)}, encodeLeaseWire(lease.Owner, lease.Address, lease.Token, lease.Ready, lease.OwnershipEpoch), coordinator.leaseTTL.Milliseconds())
	if err != nil {
		return false, err
	}
	value, ok := result.(int64)
	if !ok || (value != 0 && value != 1) {
		return false, ErrCoordinationUnavailable
	}
	return value == 1, nil
}

// ReleaseSessionLease deletes only the exact owner token and never clears a newer owner's lease.
func (coordinator *GameCoordinator) ReleaseSessionLease(ctx context.Context, lease SessionLease) (bool, error) {
	if coordinator == nil || ctx == nil || !lease.Active() {
		return false, ErrInvalidCoordinationInput
	}
	result, err := coordinator.runScript(ctx, coordinationLeaseReleaseScript, []string{coordinator.leaseKey(lease.SessionID)}, encodeLeaseWire(lease.Owner, lease.Address, lease.Token, lease.Ready, lease.OwnershipEpoch))
	if err != nil {
		return false, err
	}
	value, ok := result.(int64)
	if !ok || (value != 0 && value != 1) {
		return false, ErrCoordinationUnavailable
	}
	return value == 1, nil
}

func (coordinator *GameCoordinator) runScript(ctx context.Context, script *goredis.Script, keys []string, args ...interface{}) (interface{}, error) {
	limited, cancel := coordinator.operationContext(ctx)
	defer cancel()
	value, err := script.Run(limited, coordinator.client, keys, args...).Result()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, ErrCoordinationUnavailable
	}
	return value, nil
}

func (coordinator *GameCoordinator) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, coordinator.timeout)
}

func (coordinator *GameCoordinator) ticketKey(ticket string) string {
	digest := sha256.Sum256([]byte(ticket))
	return coordinator.keyPrefix + "game:ticket:v1:" + hex.EncodeToString(digest[:])
}

func (coordinator *GameCoordinator) fanoutChannel() string {
	return coordinator.keyPrefix + "game:fanout:v1"
}

func (coordinator *GameCoordinator) leaseKey(sessionID uuid.UUID) string {
	return coordinator.keyPrefix + "game:lease:v1:" + sessionID.String()
}

func validTicket(value string) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		clear(decoded)
		return false
	}
	clear(decoded)
	return true
}

func validLeaseOwner(value string) bool {
	return validLeaseText(value, MaximumLeaseOwnerBytes)
}

func validLeaseAddress(value string) bool {
	return validLeaseText(value, MaximumLeaseAddressBytes)
}

func validLeaseText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range []byte(value) {
		if character <= 0x20 || character >= 0x7f || character == '|' {
			return false
		}
	}
	return true
}

func encodeLeaseWire(owner, address, token string, ready bool, ownershipEpoch uint64) string {
	status := "claiming"
	if ready {
		status = "ready"
	}
	return "v1|" + status + "|" + owner + "|" + address + "|" + strconv.FormatUint(ownershipEpoch, 10) + "|" + token
}

func decodeLeaseWire(sessionID uuid.UUID, value string) (SessionLease, error) {
	parts := strings.Split(value, "|")
	if len(parts) != 6 || parts[0] != "v1" || !validLeaseOwner(parts[2]) || !validLeaseAddress(parts[3]) || !validTicket(parts[5]) {
		return SessionLease{}, ErrInvalidCoordinationInput
	}
	epoch, err := strconv.ParseUint(parts[4], 10, 64)
	if err != nil {
		return SessionLease{}, ErrInvalidCoordinationInput
	}
	ready := parts[1] == "ready"
	if parts[1] != "claiming" && !ready || ready && epoch == 0 || !ready && epoch != 0 {
		return SessionLease{}, ErrInvalidCoordinationInput
	}
	return SessionLease{
		SessionID: sessionID, Owner: parts[2], Address: parts[3], Token: parts[5], Ready: ready, OwnershipEpoch: epoch,
	}, nil
}

func redisInt(value interface{}) (int64, bool) {
	parsed, ok := value.(int64)
	return parsed, ok
}

func redisString(value interface{}) (string, bool) {
	switch parsed := value.(type) {
	case string:
		return parsed, true
	case []byte:
		return string(parsed), true
	default:
		return "", false
	}
}
