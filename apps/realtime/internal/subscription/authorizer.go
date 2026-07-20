// Package subscription owns live connection grants and periodic room/session authorization.
package subscription

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

const (
	// maximumTicketBytes caps pre-Redis ticket parsing while covering the coordinator's Raw Base64URL range.
	maximumTicketBytes = 256
	// maximumGrantBytes matches the opaque Redis grant ceiling and bounds protobuf allocation before authentication.
	maximumGrantBytes = 8 << 10
)

var (
	ErrInvalidConfig        = errors.New("invalid realtime subscription authorizer configuration")
	ErrInvalidHandshake     = errors.New("invalid realtime subscription handshake")
	ErrTicketRejected       = errors.New("realtime subscription ticket rejected")
	ErrGrantExpired         = errors.New("realtime subscription grant expired")
	ErrUnauthorized         = errors.New("realtime subscription is no longer authorized")
	ErrAuthorizationChanged = errors.New("realtime subscription authorization changed")
)

// TicketConsumer atomically compares and deletes one opaque Redis grant.
type TicketConsumer interface {
	ConsumeConnectionTicket(context.Context, string, []byte) (bool, error)
}

// RoomReader reloads current role, host, and active-session authority from PostgreSQL.
type RoomReader interface {
	GetByID(context.Context, uuid.UUID) (roomdomain.Room, error)
}

// SessionReader reloads the current frozen participant seats and authoritative state cursor.
type SessionReader interface {
	Get(context.Context, uuid.UUID) (gameruntime.Session, error)
}

// Config limits how far into the future a server-produced grant may claim validity.
type Config struct {
	MaximumGrantLifetime time.Duration
}

// Authorization is connection-local, viewer-scoped authority; it contains no device secret or game state.
type Authorization struct {
	UserID            uuid.UUID
	RoomID            uuid.UUID
	SessionID         uuid.UUID
	Viewer            game.Viewer
	Cursor            uint64
	CurrentVersion    uint64
	RoomVersion       uint64
	MembershipVersion uint64
	Host              bool
}

// RefreshResult tells the transport to discard its delta path when authorization-affecting room data changed.
type RefreshResult struct {
	Authorization    Authorization
	SnapshotRequired bool
}

// Authorizer validates one-time handshakes and periodically reconciles long-lived connections with PostgreSQL.
type Authorizer struct {
	tickets  TicketConsumer
	rooms    RoomReader
	sessions SessionReader
	clock    clock.Clock
	config   Config
}

// NewAuthorizer requires all authoritative readers and a bounded connection-grant lifetime.
func NewAuthorizer(
	tickets TicketConsumer,
	rooms RoomReader,
	sessions SessionReader,
	source clock.Clock,
	config Config,
) (*Authorizer, error) {
	if tickets == nil || rooms == nil || sessions == nil || source == nil ||
		config.MaximumGrantLifetime < time.Second || config.MaximumGrantLifetime > 15*time.Minute {
		return nil, ErrInvalidConfig
	}
	return &Authorizer{tickets: tickets, rooms: rooms, sessions: sessions, clock: source, config: config}, nil
}

// Accept consumes the exact server grant only after bounded parsing, Origin binding, and expiry validation succeed.
func (authorizer *Authorizer) Accept(
	ctx context.Context,
	origin string,
	ticketBytes []byte,
	grantBytes []byte,
) (Authorization, error) {
	if authorizer == nil || ctx == nil || !validOriginValue(origin) || !validTicketBytes(ticketBytes) ||
		len(grantBytes) == 0 || len(grantBytes) > maximumGrantBytes {
		return Authorization{}, ErrInvalidHandshake
	}
	grant, err := parseCanonicalGrant(grantBytes)
	if err != nil || grant.GetOrigin() != origin || grant.GetLastEventOrdinal() != 0 {
		return Authorization{}, ErrInvalidHandshake
	}
	expiresAt, err := requiredGrantTime(grant)
	if err != nil {
		return Authorization{}, err
	}
	now := authorizer.clock.Now().Round(0).UTC()
	if !expiresAt.After(now) {
		return Authorization{}, ErrGrantExpired
	}
	if expiresAt.Sub(now) > authorizer.config.MaximumGrantLifetime {
		return Authorization{}, ErrInvalidHandshake
	}
	userID, roomID, sessionID, err := grantIDs(grant)
	if err != nil {
		return Authorization{}, err
	}
	viewerKind, err := viewerKind(grant.GetViewerKind())
	if err != nil {
		return Authorization{}, err
	}
	consumed, err := authorizer.tickets.ConsumeConnectionTicket(ctx, string(ticketBytes), grantBytes)
	if err != nil {
		return Authorization{}, err
	}
	if !consumed {
		return Authorization{}, ErrTicketRejected
	}
	return authorizer.authorizeCurrent(ctx, userID, roomID, sessionID, viewerKind, grant.GetSeatIndex(), grant.GetLastStateVersion())
}

// Refresh revokes stale role/seat grants and requires a full projection after host or membership changes.
func (authorizer *Authorizer) Refresh(ctx context.Context, previous Authorization) (RefreshResult, error) {
	if authorizer == nil || ctx == nil || previous.UserID == uuid.Nil || previous.RoomID == uuid.Nil ||
		previous.SessionID == uuid.Nil || !previous.Viewer.Valid() || previous.Cursor == 0 {
		return RefreshResult{}, ErrInvalidHandshake
	}
	current, err := authorizer.authorizeCurrent(
		ctx, previous.UserID, previous.RoomID, previous.SessionID,
		previous.Viewer.Kind, previous.Viewer.SeatIndex, previous.Cursor,
	)
	if err != nil {
		return RefreshResult{}, err
	}
	if current.Viewer != previous.Viewer {
		return RefreshResult{}, ErrAuthorizationChanged
	}
	return RefreshResult{
		Authorization: current,
		SnapshotRequired: current.Host != previous.Host || current.RoomVersion != previous.RoomVersion ||
			current.MembershipVersion != previous.MembershipVersion,
	}, nil
}

func (authorizer *Authorizer) authorizeCurrent(
	ctx context.Context,
	userID, roomID, sessionID uuid.UUID,
	requested game.ViewerKind,
	expectedSeat uint32,
	cursor uint64,
) (Authorization, error) {
	session, err := authorizer.sessions.Get(ctx, sessionID)
	if err != nil {
		return Authorization{}, errors.Join(ErrUnauthorized, err)
	}
	if session.Snapshot().RoomID != roomID || cursor > session.Snapshot().State.StateVersion {
		return Authorization{}, ErrUnauthorized
	}
	room, err := authorizer.rooms.GetByID(ctx, roomID)
	if err != nil {
		return Authorization{}, errors.Join(ErrUnauthorized, err)
	}
	current, err := gameruntime.AuthorizeLiveViewer(room, session, userID, requested)
	if err != nil {
		return Authorization{}, errors.Join(ErrUnauthorized, err)
	}
	if current.Viewer.SeatIndex != expectedSeat {
		return Authorization{}, ErrAuthorizationChanged
	}
	roomSnapshot, sessionSnapshot := room.Snapshot(), session.Snapshot()
	return Authorization{
		UserID: userID, RoomID: roomID, SessionID: sessionID, Viewer: current.Viewer,
		Cursor: cursor, CurrentVersion: sessionSnapshot.State.StateVersion,
		RoomVersion: roomSnapshot.RoomVersion, MembershipVersion: roomSnapshot.MembershipVersion, Host: current.Host,
	}, nil
}

func parseCanonicalGrant(raw []byte) (*gamev1.SubscriptionGrant, error) {
	grant := &gamev1.SubscriptionGrant{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, grant); err != nil || len(grant.ProtoReflect().GetUnknown()) != 0 {
		return nil, ErrInvalidHandshake
	}
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(grant)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, ErrInvalidHandshake
	}
	return grant, nil
}

func requiredGrantTime(grant *gamev1.SubscriptionGrant) (time.Time, error) {
	if grant.GetExpiresAt() == nil || !grant.GetExpiresAt().IsValid() {
		return time.Time{}, ErrInvalidHandshake
	}
	value := grant.GetExpiresAt().AsTime().Round(0).UTC()
	if value.IsZero() {
		return time.Time{}, ErrInvalidHandshake
	}
	return value, nil
}

func grantIDs(grant *gamev1.SubscriptionGrant) (uuid.UUID, uuid.UUID, uuid.UUID, error) {
	userID, err := canonicalUUID(grant.GetUserId())
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, err
	}
	roomID, err := canonicalUUID(grant.GetRoomId())
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, err
	}
	sessionID, err := canonicalUUID(grant.GetSessionId())
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, err
	}
	return userID, roomID, sessionID, nil
}

func canonicalUUID(raw string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed == uuid.Nil || parsed.String() != raw {
		return uuid.Nil, ErrInvalidHandshake
	}
	return parsed, nil
}

func viewerKind(value gamev1.ViewerKind) (game.ViewerKind, error) {
	switch value {
	case gamev1.ViewerKind_VIEWER_KIND_PLAYER:
		return game.ViewerPlayer, nil
	case gamev1.ViewerKind_VIEWER_KIND_SPECTATOR:
		return game.ViewerSpectator, nil
	default:
		return "", ErrInvalidHandshake
	}
}

func validOriginValue(value string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && parsed.User == nil && parsed.Path == "" && parsed.RawPath == "" &&
		parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Scheme == "https" || parsed.Scheme == "http") &&
		parsed.String() == value
}

func validTicketBytes(value []byte) bool {
	if len(value) < 22 || len(value) > maximumTicketBytes {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}
