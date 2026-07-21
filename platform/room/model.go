package room

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Visibility controls whether a room is discoverable through the public lobby.
type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
)

// AdmissionMode describes how a room accepts a requested membership role.
type AdmissionMode string

const (
	AdmissionOpen     AdmissionMode = "open"
	AdmissionApproval AdmissionMode = "approval"
	AdmissionClosed   AdmissionMode = "closed"
)

// RoomStatus is the continuous-room lifecycle; a game session is a child of a playing room.
type RoomStatus string

const (
	RoomStatusLobby    RoomStatus = "lobby"
	RoomStatusPlaying  RoomStatus = "playing"
	RoomStatusPostGame RoomStatus = "post_game"
	RoomStatusClosed   RoomStatus = "closed"
)

// MemberRole is the access level held by a persistent room member.
type MemberRole string

const (
	MemberRoleParticipant MemberRole = "participant"
	MemberRoleSpectator   MemberRole = "spectator"
	MemberRoleWaiting     MemberRole = "waiting"
)

// JoinIntent identifies the role a new user wants to acquire when approval is required.
type JoinIntent MemberRole

const (
	JoinIntentParticipant JoinIntent = JoinIntent(MemberRoleParticipant)
	JoinIntentSpectator   JoinIntent = JoinIntent(MemberRoleSpectator)
)

// Version contains both optimistic-concurrency values required by membership mutations.
type Version struct {
	Room       uint64
	Membership uint64
}

// MemberSnapshot is safe to persist and project; waiting members retain their requested role.
type MemberSnapshot struct {
	UserID        uuid.UUID
	Role          MemberRole
	RequestedRole MemberRole
	SeatIndex     uint32
	JoinedAt      time.Time
	LastSeenAt    time.Time
}

// FrozenParticipant is the immutable seat assignment handed to a game session at start.
type FrozenParticipant struct {
	UserID    uuid.UUID
	SeatIndex uint32
}

// RoomSnapshot is the persistence-neutral authoritative state of one continuous room.
type RoomSnapshot struct {
	ID                    uuid.UUID
	RoomCode              string
	Visibility            Visibility
	Status                RoomStatus
	HostUserID            uuid.UUID
	ParticipantCapacity   uint32
	ParticipantAdmission  AdmissionMode
	SpectatorAdmission    AdmissionMode
	Members               []MemberSnapshot
	ActiveSessionID       uuid.UUID
	ActiveGameID          string
	LastFinishedSessionID uuid.UUID
	LastFinishedGameID    string
	RoomVersion           uint64
	MembershipVersion     uint64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// Room is an immutable aggregate. Commands return a new value so repositories can CAS the exact snapshot version.
type Room struct {
	snapshot RoomSnapshot
}

// JoinResult describes whether a request created or changed a participant, spectator, or approval-waiting member.
type JoinResult struct {
	Member  MemberSnapshot
	Created bool
	Changed bool
}

// ApprovalResult contains the promoted member and the versions committed by the approval command.
type ApprovalResult struct {
	Member  MemberSnapshot
	Version Version
}

// SessionStart is the handoff from PartyRoom to a game runtime. The runtime owns the returned frozen snapshot.
type SessionStart struct {
	SessionID    uuid.UUID
	GameID       string
	Participants []FrozenParticipant
	Version      Version
	StartedAt    time.Time
}

// RemovalResult tells the realtime runtime whether it must process a frozen participant revocation.
type RemovalResult struct {
	Removed            MemberSnapshot
	ParticipantRevoked bool
	SessionID          uuid.UUID
	SourceEventID      uuid.UUID
	Version            Version
}

// New creates a lobby with open participant and spectator admission.
func New(id, hostUserID uuid.UUID, roomCode string, visibility Visibility, participantCapacity uint32, createdAt time.Time) (Room, error) {
	return NewWithAdmission(
		id, hostUserID, roomCode, visibility, participantCapacity, AdmissionOpen, AdmissionOpen, createdAt,
	)
}

// NewWithAdmission creates a lobby with explicit host-selected pregame admission policy.
func NewWithAdmission(
	id, hostUserID uuid.UUID,
	roomCode string,
	visibility Visibility,
	participantCapacity uint32,
	participantAdmission, spectatorAdmission AdmissionMode,
	createdAt time.Time,
) (Room, error) {
	createdAt = canonicalRoomTime(createdAt)
	if id == uuid.Nil || hostUserID == uuid.Nil || createdAt.IsZero() {
		return Room{}, ErrInvalidRoomInput
	}
	if err := validateRoomCode(roomCode); err != nil {
		return Room{}, err
	}
	if !visibility.Valid() || participantCapacity == 0 || !participantAdmission.Valid() || !spectatorAdmission.Valid() {
		return Room{}, ErrInvalidRoomInput
	}
	return Restore(RoomSnapshot{
		ID: id, RoomCode: roomCode, Visibility: visibility, Status: RoomStatusLobby,
		HostUserID: hostUserID, ParticipantCapacity: participantCapacity,
		ParticipantAdmission: participantAdmission, SpectatorAdmission: spectatorAdmission,
		Members: []MemberSnapshot{{
			UserID: hostUserID, Role: MemberRoleParticipant, SeatIndex: 0,
			JoinedAt: createdAt, LastSeenAt: createdAt,
		}},
		RoomVersion: 1, MembershipVersion: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
	})
}

// Restore validates persisted state and copies member slices so callers cannot mutate aggregate-owned state.
func Restore(snapshot RoomSnapshot) (Room, error) {
	snapshot.RoomCode = strings.TrimSpace(snapshot.RoomCode)
	snapshot.CreatedAt = canonicalRoomTime(snapshot.CreatedAt)
	snapshot.UpdatedAt = canonicalRoomTime(snapshot.UpdatedAt)
	if snapshot.ID == uuid.Nil || snapshot.HostUserID == uuid.Nil || validateRoomCode(snapshot.RoomCode) != nil ||
		!snapshot.Visibility.Valid() || !snapshot.Status.Valid() || snapshot.ParticipantCapacity == 0 ||
		!snapshot.ParticipantAdmission.Valid() || !snapshot.SpectatorAdmission.Valid() || snapshot.RoomVersion == 0 ||
		snapshot.MembershipVersion == 0 || snapshot.CreatedAt.IsZero() || snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return Room{}, ErrInvalidRoomInput
	}
	lastFinishedSet := snapshot.LastFinishedSessionID != uuid.Nil || snapshot.LastFinishedGameID != ""
	if (snapshot.LastFinishedSessionID == uuid.Nil) != (snapshot.LastFinishedGameID == "") {
		return Room{}, ErrInvalidRoomInput
	}
	if snapshot.Status == RoomStatusPlaying {
		if snapshot.ActiveSessionID == uuid.Nil || snapshot.ActiveGameID == "" || snapshot.ParticipantAdmission != AdmissionClosed {
			return Room{}, ErrInvalidRoomInput
		}
	} else if snapshot.ActiveSessionID != uuid.Nil || snapshot.ActiveGameID != "" {
		return Room{}, ErrInvalidRoomInput
	}
	if (snapshot.Status == RoomStatusLobby || snapshot.Status == RoomStatusPlaying) && lastFinishedSet {
		return Room{}, ErrInvalidRoomInput
	}
	if snapshot.Status == RoomStatusPostGame && !lastFinishedSet {
		return Room{}, ErrInvalidRoomInput
	}
	if snapshot.Status == RoomStatusClosed &&
		(snapshot.ParticipantAdmission != AdmissionClosed || snapshot.SpectatorAdmission != AdmissionClosed) {
		return Room{}, ErrInvalidRoomInput
	}
	if len(snapshot.Members) == 0 || len(snapshot.Members) > int(snapshot.ParticipantCapacity)+1_000_000 {
		return Room{}, ErrInvalidRoomInput
	}
	members := make([]MemberSnapshot, len(snapshot.Members))
	copy(members, snapshot.Members)
	seen := make(map[uuid.UUID]struct{}, len(members))
	seenSeats := make(map[uint32]struct{}, len(members))
	participantCount := 0
	hostFound := false
	for index := range members {
		members[index].JoinedAt = canonicalRoomTime(members[index].JoinedAt)
		members[index].LastSeenAt = canonicalRoomTime(members[index].LastSeenAt)
		member := members[index]
		if member.UserID == uuid.Nil || !member.Role.Valid() || member.JoinedAt.IsZero() || member.LastSeenAt.Before(member.JoinedAt) ||
			member.JoinedAt.Before(snapshot.CreatedAt) || member.LastSeenAt.After(snapshot.UpdatedAt) {
			return Room{}, ErrInvalidRoomInput
		}
		if _, exists := seen[member.UserID]; exists {
			return Room{}, ErrInvalidRoomInput
		}
		seen[member.UserID] = struct{}{}
		switch member.Role {
		case MemberRoleParticipant:
			if member.RequestedRole != "" {
				return Room{}, ErrInvalidRoomInput
			}
			if _, exists := seenSeats[member.SeatIndex]; exists {
				return Room{}, ErrInvalidRoomInput
			}
			seenSeats[member.SeatIndex] = struct{}{}
			participantCount++
		case MemberRoleSpectator:
			if member.RequestedRole != "" || member.SeatIndex != 0 {
				return Room{}, ErrInvalidRoomInput
			}
		case MemberRoleWaiting:
			if member.RequestedRole != MemberRoleParticipant && member.RequestedRole != MemberRoleSpectator {
				return Room{}, ErrInvalidRoomInput
			}
			if member.SeatIndex != 0 {
				return Room{}, ErrInvalidRoomInput
			}
		}
		if member.UserID == snapshot.HostUserID {
			hostFound = true
			if member.Role != MemberRoleParticipant {
				return Room{}, ErrInvalidRoomInput
			}
		}
	}
	if !hostFound || participantCount == 0 || participantCount > int(snapshot.ParticipantCapacity) {
		return Room{}, ErrInvalidRoomInput
	}
	snapshot.Members = members
	return Room{snapshot: snapshot}, nil
}

// Snapshot returns a defensive copy for persistence, event publication, or API projection.
func (room Room) Snapshot() RoomSnapshot {
	snapshot := room.snapshot
	snapshot.Members = append([]MemberSnapshot(nil), room.snapshot.Members...)
	return snapshot
}

// Version returns the optimistic-concurrency values expected by the next command.
func (room Room) Version() Version {
	return Version{Room: room.snapshot.RoomVersion, Membership: room.snapshot.MembershipVersion}
}

// Member returns a copy of one current room member.
func (room Room) Member(userID uuid.UUID) (MemberSnapshot, bool) {
	for _, member := range room.snapshot.Members {
		if member.UserID == userID {
			return member, true
		}
	}
	return MemberSnapshot{}, false
}

// Join admits a new user or returns an existing member idempotently. New participants are never admitted mid-game.
func (room Room) Join(userID uuid.UUID, intent JoinIntent, expected Version, at time.Time) (Room, JoinResult, error) {
	at = canonicalRoomTime(at)
	if userID == uuid.Nil || !intent.Valid() || at.IsZero() {
		return Room{}, JoinResult{}, ErrInvalidRoomInput
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, JoinResult{}, err
	}
	if existing, ok := room.Member(userID); ok {
		// A current participant reconnects idempotently. A spectator may give up live viewing to queue for the next game.
		if room.snapshot.Status == RoomStatusPlaying && intent == JoinIntentParticipant && existing.Role == MemberRoleSpectator {
			next := room.snapshot
			for index := range next.Members {
				if next.Members[index].UserID == userID {
					next.Members[index].Role = MemberRoleWaiting
					next.Members[index].RequestedRole = MemberRoleParticipant
					next.Members[index].SeatIndex = 0
					existing = next.Members[index]
					break
				}
			}
			if err := bumpVersions(&next, true, at); err != nil {
				return Room{}, JoinResult{}, err
			}
			updated, err := Restore(next)
			if err != nil {
				return Room{}, JoinResult{}, err
			}
			return updated, JoinResult{Member: existing, Changed: true}, nil
		}
		return room, JoinResult{Member: existing}, nil
	}
	if room.snapshot.Status == RoomStatusClosed {
		return Room{}, JoinResult{}, ErrRoomClosed
	}
	mode := room.snapshot.ParticipantAdmission
	if intent == JoinIntentSpectator {
		mode = room.snapshot.SpectatorAdmission
	}
	member := MemberSnapshot{UserID: userID, JoinedAt: at, LastSeenAt: at}
	if room.snapshot.Status == RoomStatusPlaying && intent == JoinIntentParticipant {
		// Frozen seats cannot change mid-game, so every new participant request joins the next-game queue.
		member.Role, member.RequestedRole = MemberRoleWaiting, MemberRoleParticipant
	} else {
		switch mode {
		case AdmissionOpen:
			if intent == JoinIntentParticipant {
				seat, ok := room.nextSeat()
				if !ok {
					return Room{}, JoinResult{}, ErrRoomFull
				}
				member.Role, member.SeatIndex = MemberRoleParticipant, seat
			} else {
				member.Role = MemberRoleSpectator
			}
		case AdmissionApproval:
			member.Role, member.RequestedRole = MemberRoleWaiting, MemberRole(intent)
		case AdmissionClosed:
			return Room{}, JoinResult{}, ErrAdmissionClosed
		default:
			return Room{}, JoinResult{}, ErrInvalidRoomInput
		}
	}
	next := room.snapshot
	next.Members = append(append([]MemberSnapshot(nil), room.snapshot.Members...), member)
	if err := bumpVersions(&next, true, at); err != nil {
		return Room{}, JoinResult{}, err
	}

	updated, err := Restore(next)
	if err != nil {
		return Room{}, JoinResult{}, err
	}
	return updated, JoinResult{Member: member, Created: true, Changed: true}, nil
}

// SetAdmission changes a host-controlled admission mode in the pre/post-game lobby.
func (room Room) SetAdmission(hostUserID uuid.UUID, participant, spectator AdmissionMode, expected Version, at time.Time) (Room, error) {
	at = canonicalRoomTime(at)
	if hostUserID == uuid.Nil || !participant.Valid() || !spectator.Valid() || at.IsZero() {
		return Room{}, ErrInvalidRoomInput
	}
	if err := room.checkHost(hostUserID); err != nil {
		return Room{}, err
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, err
	}
	if !room.snapshot.Status.admissionMutable() {
		return Room{}, ErrRoomStatus
	}
	next := room.snapshot
	next.ParticipantAdmission, next.SpectatorAdmission = participant, spectator
	if err := bumpVersions(&next, false, at); err != nil {
		return Room{}, err
	}
	return Restore(next)
}

// ApproveWaiting promotes one waiting request atomically after rechecking host, versions, and capacity.
func (room Room) ApproveWaiting(hostUserID, userID uuid.UUID, expected Version, at time.Time) (Room, ApprovalResult, error) {
	at = canonicalRoomTime(at)
	if hostUserID == uuid.Nil || userID == uuid.Nil || at.IsZero() {
		return Room{}, ApprovalResult{}, ErrInvalidRoomInput
	}
	if err := room.checkHost(hostUserID); err != nil {
		return Room{}, ApprovalResult{}, err
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, ApprovalResult{}, err
	}
	if !room.snapshot.Status.admissionMutable() {
		return Room{}, ApprovalResult{}, ErrRoomStatus
	}
	next := room.snapshot
	found := false
	var promoted MemberSnapshot
	for index := range next.Members {
		member := next.Members[index]
		if member.UserID != userID || member.Role != MemberRoleWaiting {
			continue
		}
		found = true
		promoted = member
		if member.RequestedRole == MemberRoleParticipant {
			seat, ok := room.nextSeat()
			if !ok {
				return Room{}, ApprovalResult{}, ErrRoomFull
			}
			promoted.Role, promoted.RequestedRole, promoted.SeatIndex = MemberRoleParticipant, "", seat
		} else {
			promoted.Role, promoted.RequestedRole = MemberRoleSpectator, ""
		}
		next.Members[index] = promoted
		break
	}
	if !found {
		return Room{}, ApprovalResult{}, ErrWaitingNotFound
	}
	if err := bumpVersions(&next, true, at); err != nil {
		return Room{}, ApprovalResult{}, err
	}
	updated, err := Restore(next)
	if err != nil {
		return Room{}, ApprovalResult{}, err
	}
	return updated, ApprovalResult{Member: promoted, Version: updated.Version()}, nil
}

// StartSession freezes participant seats and closes participant admission until the session ends.
func (room Room) StartSession(
	hostUserID, sessionID uuid.UUID,
	gameID string,
	minimumParticipants, maximumParticipants uint32,
	expected Version,
	at time.Time,
) (Room, SessionStart, error) {
	at = canonicalRoomTime(at)
	if hostUserID == uuid.Nil || sessionID == uuid.Nil || strings.TrimSpace(gameID) == "" || minimumParticipants == 0 ||
		maximumParticipants < minimumParticipants || at.IsZero() {
		return Room{}, SessionStart{}, ErrInvalidRoomInput
	}
	if err := room.checkHost(hostUserID); err != nil {
		return Room{}, SessionStart{}, err
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, SessionStart{}, err
	}
	if room.snapshot.Status == RoomStatusClosed {
		return Room{}, SessionStart{}, ErrRoomClosed
	}
	if room.snapshot.Status != RoomStatusLobby && room.snapshot.Status != RoomStatusPostGame {
		return Room{}, SessionStart{}, ErrRoomStatus
	}
	if room.snapshot.ActiveSessionID != uuid.Nil {
		return Room{}, SessionStart{}, ErrSessionActive
	}
	participants := make([]FrozenParticipant, 0, len(room.snapshot.Members))
	for _, member := range room.snapshot.Members {
		if member.Role == MemberRoleParticipant {
			participants = append(participants, FrozenParticipant{UserID: member.UserID, SeatIndex: member.SeatIndex})
		}
	}
	if len(participants) < int(minimumParticipants) {
		return Room{}, SessionStart{}, ErrInsufficientParticipants
	}
	if len(participants) > int(maximumParticipants) {
		return Room{}, SessionStart{}, ErrParticipantLimitExceeded
	}
	next := room.snapshot
	next.Status, next.ActiveSessionID, next.ActiveGameID, next.ParticipantAdmission = RoomStatusPlaying, sessionID, strings.TrimSpace(gameID), AdmissionClosed
	next.LastFinishedSessionID, next.LastFinishedGameID = uuid.Nil, ""
	if err := bumpVersions(&next, false, at); err != nil {
		return Room{}, SessionStart{}, err
	}
	updated, err := Restore(next)
	if err != nil {
		return Room{}, SessionStart{}, err
	}
	return updated, SessionStart{SessionID: sessionID, GameID: next.ActiveGameID, Participants: participants, Version: updated.Version(), StartedAt: at}, nil
}

// FinishSession returns a room to its post-game lobby while keeping participant admission closed by default.
func (room Room) FinishSession(sessionID uuid.UUID, expected Version, at time.Time) (Room, error) {
	at = canonicalRoomTime(at)
	if sessionID == uuid.Nil || at.IsZero() {
		return Room{}, ErrInvalidRoomInput
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, err
	}
	if room.snapshot.Status != RoomStatusPlaying {
		return Room{}, ErrRoomStatus
	}
	if room.snapshot.ActiveSessionID != sessionID {
		return Room{}, ErrSessionNotFound
	}
	next := room.snapshot
	next.Status, next.ActiveSessionID, next.ActiveGameID, next.ParticipantAdmission = RoomStatusPostGame, uuid.Nil, "", AdmissionClosed
	next.LastFinishedSessionID, next.LastFinishedGameID = sessionID, room.snapshot.ActiveGameID
	if err := bumpVersions(&next, false, at); err != nil {
		return Room{}, err
	}
	return Restore(next)
}

// CancelSession returns a room to its initial lobby without advertising a cancelled session as replayable.
func (room Room) CancelSession(sessionID uuid.UUID, expected Version, at time.Time) (Room, error) {
	at = canonicalRoomTime(at)
	if sessionID == uuid.Nil || at.IsZero() {
		return Room{}, ErrInvalidRoomInput
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, err
	}
	if room.snapshot.Status != RoomStatusPlaying {
		return Room{}, ErrRoomStatus
	}
	if room.snapshot.ActiveSessionID != sessionID {
		return Room{}, ErrSessionNotFound
	}
	next := room.snapshot
	next.Status, next.ActiveSessionID, next.ActiveGameID, next.ParticipantAdmission = RoomStatusLobby, uuid.Nil, "", AdmissionClosed
	next.LastFinishedSessionID, next.LastFinishedGameID = uuid.Nil, ""
	if err := bumpVersions(&next, false, at); err != nil {
		return Room{}, err
	}
	return Restore(next)
}

// RemoveMember removes a non-host member and reports whether the active game must revoke a frozen participant.
func (room Room) RemoveMember(hostUserID, userID uuid.UUID, expected Version, at time.Time) (Room, RemovalResult, error) {
	at = canonicalRoomTime(at)
	if hostUserID == uuid.Nil || userID == uuid.Nil || at.IsZero() {
		return Room{}, RemovalResult{}, ErrInvalidRoomInput
	}
	if err := room.checkHost(hostUserID); err != nil {
		return Room{}, RemovalResult{}, err
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, RemovalResult{}, err
	}
	if userID == room.snapshot.HostUserID {
		return Room{}, RemovalResult{}, ErrCannotRemoveHost
	}
	next := room.snapshot
	removedIndex := -1
	var removed MemberSnapshot
	for index, member := range next.Members {
		if member.UserID == userID {
			removedIndex, removed = index, member
			break
		}
	}
	if removedIndex < 0 {
		return Room{}, RemovalResult{}, ErrMemberNotFound
	}
	next.Members = append(next.Members[:removedIndex], next.Members[removedIndex+1:]...)
	if err := bumpVersions(&next, true, at); err != nil {
		return Room{}, RemovalResult{}, err
	}
	updated, err := Restore(next)
	if err != nil {
		return Room{}, RemovalResult{}, err
	}
	return updated, RemovalResult{
		Removed: removed, ParticipantRevoked: room.snapshot.Status == RoomStatusPlaying && removed.Role == MemberRoleParticipant,
		SessionID: room.snapshot.ActiveSessionID, Version: updated.Version(),
	}, nil
}

// Close marks a room unavailable for new commands; an active game must be cancelled by its runtime first.
func (room Room) Close(hostUserID uuid.UUID, expected Version, at time.Time) (Room, error) {
	at = canonicalRoomTime(at)
	if hostUserID == uuid.Nil || at.IsZero() {
		return Room{}, ErrInvalidRoomInput
	}
	if err := room.checkHost(hostUserID); err != nil {
		return Room{}, err
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, err
	}
	if room.snapshot.Status == RoomStatusPlaying {
		return Room{}, ErrSessionActive
	}
	if room.snapshot.Status == RoomStatusClosed {
		return room, nil
	}
	next := room.snapshot
	next.Status, next.ParticipantAdmission, next.SpectatorAdmission = RoomStatusClosed, AdmissionClosed, AdmissionClosed
	if err := bumpVersions(&next, false, at); err != nil {
		return Room{}, err
	}
	return Restore(next)
}

func (visibility Visibility) Valid() bool {
	return visibility == VisibilityPrivate || visibility == VisibilityPublic
}

func (mode AdmissionMode) Valid() bool {
	return mode == AdmissionOpen || mode == AdmissionApproval || mode == AdmissionClosed
}

func (status RoomStatus) Valid() bool {
	return status == RoomStatusLobby || status == RoomStatusPlaying || status == RoomStatusPostGame || status == RoomStatusClosed
}

func (status RoomStatus) admissionMutable() bool {
	return status == RoomStatusLobby || status == RoomStatusPostGame
}

func (role MemberRole) Valid() bool {
	return role == MemberRoleParticipant || role == MemberRoleSpectator || role == MemberRoleWaiting
}

func (intent JoinIntent) Valid() bool {
	return intent == JoinIntentParticipant || intent == JoinIntentSpectator
}

func (room Room) checkHost(userID uuid.UUID) error {
	if userID != room.snapshot.HostUserID {
		return ErrHostRequired
	}
	return nil
}

func (room Room) checkVersion(expected Version) error {
	if expected != room.Version() {
		return ErrRoomVersionConflict
	}
	return nil
}

func (room Room) nextSeat() (uint32, bool) {
	used := make(map[uint32]struct{})
	for _, member := range room.snapshot.Members {
		if member.Role == MemberRoleParticipant {
			used[member.SeatIndex] = struct{}{}
		}
	}
	for seat := uint32(0); seat < room.snapshot.ParticipantCapacity; seat++ {
		if _, exists := used[seat]; !exists {
			return seat, true
		}
	}
	return 0, false
}

func bumpVersions(snapshot *RoomSnapshot, membershipChanged bool, at time.Time) error {
	if snapshot.RoomVersion == ^uint64(0) || (membershipChanged && snapshot.MembershipVersion == ^uint64(0)) {
		return ErrRoomVersionOverflow
	}
	snapshot.RoomVersion++
	if membershipChanged {
		snapshot.MembershipVersion++
	}
	snapshot.UpdatedAt = at
	return nil
}

func validateRoomCode(value string) error {
	if len(value) < 4 || len(value) > 16 || strings.TrimSpace(value) != value {
		return ErrInvalidRoomInput
	}
	for _, character := range value {
		if !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') {
			return ErrInvalidRoomInput
		}
	}
	return nil
}

func canonicalRoomTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}
