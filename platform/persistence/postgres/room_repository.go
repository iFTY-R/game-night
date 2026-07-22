package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RoomRepository persists the full PartyRoom aggregate under one transaction runner.
// Membership rows are replaced only after the room row wins its version CAS.
type RoomRepository struct {
	runner *TransactionRunner
}

// NewRoomRepository binds room persistence to the supplied runtime PostgreSQL pool.
func NewRoomRepository(pool *pgxpool.Pool) *RoomRepository {
	return &RoomRepository{runner: NewTransactionRunner(pool)}
}

// Create writes the room and its initial host/member snapshot atomically.
func (repository *RoomRepository) Create(ctx context.Context, room roomDomain.Room) (roomDomain.Room, error) {
	if repository == nil || repository.runner == nil || ctx == nil {
		return roomDomain.Room{}, roomDomain.ErrInvalidRoomInput
	}
	snapshot := room.Snapshot()
	if snapshot.RoomVersion != 1 || snapshot.MembershipVersion != 1 || snapshot.Status != roomDomain.RoomStatusLobby {
		return roomDomain.Room{}, roomDomain.ErrInvalidRoomInput
	}
	if err := validateRoomPersistenceWidths(snapshot); err != nil {
		return roomDomain.Room{}, err
	}
	var stored roomDomain.Room
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := queries.CreatePartyRoom(ctx, createPartyRoomParams(snapshot))
		if err != nil {
			return err
		}
		if err := queries.CreateRoomActivityLease(ctx, sqlcgen.CreateRoomActivityLeaseParams{
			RoomID: uuidToPG(snapshot.ID), LastSeenAt: timeToPG(snapshot.CreatedAt),
		}); err != nil {
			return err
		}
		for _, member := range snapshot.Members {
			if err := queries.CreateRoomMember(ctx, createRoomMemberParams(snapshot.ID, member)); err != nil {
				return err
			}
		}
		members, err := queries.ListRoomMembers(ctx, sqlcgen.ListRoomMembersParams{RoomID: uuidToPG(snapshot.ID)})
		if err != nil {
			return err
		}
		stored, err = roomFromRows(row, members)
		return err
	})
	if err != nil {
		return roomDomain.Room{}, mapRoomRepositoryError(ctx, err, roomDomain.ErrRoomCodeUnavailable)
	}
	return stored, nil
}

// GetByID loads one room and takes a share lock so members are read from the same transaction snapshot.
func (repository *RoomRepository) GetByID(ctx context.Context, roomID uuid.UUID) (roomDomain.Room, error) {
	if repository == nil || repository.runner == nil || ctx == nil || roomID == uuid.Nil {
		return roomDomain.Room{}, roomDomain.ErrInvalidRoomInput
	}
	return repository.get(ctx, func(queries QueryHandle) (sqlcgen.PartyRoom, error) {
		return queries.GetPartyRoomForShare(ctx, sqlcgen.GetPartyRoomForShareParams{RoomID: uuidToPG(roomID)})
	})
}

// GetByCode resolves an invitation code using the same consistent room/member snapshot as GetByID.
func (repository *RoomRepository) GetByCode(ctx context.Context, roomCode string) (roomDomain.Room, error) {
	if repository == nil || repository.runner == nil || ctx == nil {
		return roomDomain.Room{}, roomDomain.ErrInvalidRoomInput
	}
	if err := roomDomain.ValidateRoomCode(roomCode); err != nil {
		return roomDomain.Room{}, err
	}
	return repository.get(ctx, func(queries QueryHandle) (sqlcgen.PartyRoom, error) {
		return queries.GetPartyRoomByCodeForShare(ctx, sqlcgen.GetPartyRoomByCodeForShareParams{RoomCode: roomCode})
	})
}

// ListRoomMemberUsernames projects current identity names without copying mutable profile data into the room aggregate.
func (repository *RoomRepository) ListRoomMemberUsernames(ctx context.Context, roomID uuid.UUID) (map[uuid.UUID]string, error) {
	if repository == nil || repository.runner == nil || ctx == nil || roomID == uuid.Nil {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	var rows []sqlcgen.ListRoomMemberUsernamesRow
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		rows, err = queries.ListRoomMemberUsernames(ctx, sqlcgen.ListRoomMemberUsernamesParams{RoomID: uuidToPG(roomID)})
		return err
	})
	if err != nil {
		return nil, mapRoomRepositoryError(ctx, err, roomDomain.ErrRoomNotFound)
	}
	usernames := make(map[uuid.UUID]string, len(rows))
	for _, row := range rows {
		if row.UserID.Valid && row.Username.Valid {
			usernames[uuid.UUID(row.UserID.Bytes)] = row.Username.String
		}
	}
	return usernames, nil
}

// RecordRoomPresence renews one room-level lease only for a current member of a non-closed room.
func (repository *RoomRepository) RecordRoomPresence(ctx context.Context, roomID, userID uuid.UUID) (time.Time, error) {
	if repository == nil || repository.runner == nil || ctx == nil || roomID == uuid.Nil || userID == uuid.Nil {
		return time.Time{}, roomDomain.ErrInvalidRoomInput
	}
	var observedAt time.Time
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		if _, err := queries.LockRoomActivityLease(ctx, sqlcgen.LockRoomActivityLeaseParams{RoomID: uuidToPG(roomID)}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return roomDomain.ErrRoomNotFound
			}
			return err
		}
		room, err := queries.GetPartyRoomForShare(ctx, sqlcgen.GetPartyRoomForShareParams{RoomID: uuidToPG(roomID)})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return roomDomain.ErrRoomNotFound
			}
			return err
		}
		if room.Status == string(roomDomain.RoomStatusClosed) {
			return roomDomain.ErrRoomClosed
		}
		if _, err := queries.GetRoomMemberRole(ctx, sqlcgen.GetRoomMemberRoleParams{
			RoomID: uuidToPG(roomID), UserID: uuidToPG(userID),
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return roomDomain.ErrMemberNotFound
			}
			return err
		}
		value, err := queries.TouchRoomActivityLease(ctx, sqlcgen.TouchRoomActivityLeaseParams{RoomID: uuidToPG(roomID)})
		if err == nil && value.Valid {
			observedAt = value.Time
		}
		return err
	})
	if err != nil {
		return time.Time{}, mapUnitOfWorkError(err, roomDomain.ErrRoomRepositoryUnavailable,
			roomDomain.ErrInvalidRoomInput, roomDomain.ErrRoomNotFound, roomDomain.ErrRoomClosed, roomDomain.ErrMemberNotFound)
	}
	if observedAt.IsZero() {
		return time.Time{}, roomDomain.ErrRoomIntegrity
	}
	return observedAt, nil
}

// ListPublicRooms reads one actor-aware lobby page without loading invitation codes or complete member snapshots.
func (repository *RoomRepository) ListPublicRooms(
	ctx context.Context,
	request roomDomain.PublicRoomListRequest,
) ([]roomDomain.PublicRoomCard, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !request.Valid() {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	includeLobby, includePlaying, includePostGame := false, false, false
	for _, status := range request.Filter.Statuses {
		includeLobby = includeLobby || status == roomDomain.RoomStatusLobby
		includePlaying = includePlaying || status == roomDomain.RoomStatusPlaying
		includePostGame = includePostGame || status == roomDomain.RoomStatusPostGame
	}
	afterUpdatedAt, afterRoomID := timeToPG(roomLobbyCursorFloor()), uuidToPG(uuid.Nil)
	if !request.After.UpdatedAt.IsZero() {
		afterUpdatedAt, afterRoomID = timeToPG(request.After.UpdatedAt), uuidToPG(request.After.RoomID)
	}
	var rows []sqlcgen.ListPublicRoomCardsRow
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		rows, err = queries.ListPublicRoomCards(ctx, sqlcgen.ListPublicRoomCardsParams{
			ActorUserID: uuidToPG(request.ActorUserID), IncludeLobby: includeLobby, IncludePlaying: includePlaying,
			IncludePostGame: includePostGame,
			GameID:          request.Filter.GameID, ParticipantJoinableOnly: request.Filter.ParticipantJoinableOnly,
			HasAfter: !request.After.UpdatedAt.IsZero(), AfterUpdatedAt: afterUpdatedAt, AfterRoomID: afterRoomID,
			PageLimit: int32(request.Limit),
		})
		return err
	})
	if err != nil {
		return nil, mapRoomRepositoryError(ctx, err, roomDomain.ErrRoomRepositoryUnavailable)
	}
	cards := make([]roomDomain.PublicRoomCard, 0, len(rows))
	for _, row := range rows {
		card, mapErr := publicRoomCardFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		cards = append(cards, card)
	}
	return cards, nil
}

// ListMyRooms reads active public and private room cards only through the actor's membership row.
func (repository *RoomRepository) ListMyRooms(
	ctx context.Context,
	request roomDomain.MyRoomListRequest,
) ([]roomDomain.MyRoomCard, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !request.Valid() {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	afterUpdatedAt, afterRoomID := timeToPG(roomLobbyCursorFloor()), uuidToPG(uuid.Nil)
	if !request.After.UpdatedAt.IsZero() {
		afterUpdatedAt, afterRoomID = timeToPG(request.After.UpdatedAt), uuidToPG(request.After.RoomID)
	}
	var rows []sqlcgen.ListMyRoomCardsRow
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		rows, err = queries.ListMyRoomCards(ctx, sqlcgen.ListMyRoomCardsParams{
			ActorUserID: uuidToPG(request.ActorUserID), HasAfter: !request.After.UpdatedAt.IsZero(),
			AfterIsHost: request.After.IsHost, AfterUpdatedAt: afterUpdatedAt, AfterRoomID: afterRoomID,
			PageLimit: int32(request.Limit),
		})
		return err
	})
	if err != nil {
		return nil, mapRoomRepositoryError(ctx, err, roomDomain.ErrRoomRepositoryUnavailable)
	}
	cards := make([]roomDomain.MyRoomCard, 0, len(rows))
	for _, row := range rows {
		card, mapErr := myRoomCardFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		cards = append(cards, card)
	}
	return cards, nil
}

// UpdateCAS commits a domain-produced snapshot only when both room and membership versions still match.
func (repository *RoomRepository) UpdateCAS(ctx context.Context, current, next roomDomain.Room) (roomDomain.Room, error) {
	if repository == nil || repository.runner == nil || ctx == nil {
		return roomDomain.Room{}, roomDomain.ErrInvalidRoomInput
	}
	before, after := current.Snapshot(), next.Snapshot()
	if err := validateRoomTransition(before, after); err != nil {
		return roomDomain.Room{}, err
	}
	var stored roomDomain.Room
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		stored, err = updateRoomAggregateCAS(ctx, queries, before, after)
		return err
	})
	if err != nil {
		return roomDomain.Room{}, mapRoomRepositoryError(ctx, err, roomDomain.ErrRoomVersionConflict)
	}
	return stored, nil
}

// CommitRemoval atomically applies the membership fence, durable room event, and neutral runtime inbox entry.
func (repository *RoomRepository) CommitRemoval(
	ctx context.Context,
	current roomDomain.Room,
	next roomDomain.Room,
	event outbox.Event,
) (roomDomain.Room, error) {
	if repository == nil || repository.runner == nil || ctx == nil {
		return roomDomain.Room{}, roomDomain.ErrInvalidRoomInput
	}
	before, after := current.Snapshot(), next.Snapshot()
	if err := validateRoomTransition(before, after); err != nil {
		return roomDomain.Room{}, err
	}
	fact, err := roomDomain.ParseParticipantRevokedEvent(event)
	if err != nil || fact.RoomID != before.ID || fact.SessionID != before.ActiveSessionID ||
		fact.ActorKind != roomDomain.RemovalActorHost || fact.ActorID != before.HostUserID ||
		fact.MembershipVersion != after.MembershipVersion || !fact.OccurredAt.Equal(after.UpdatedAt) ||
		!removedParticipantTransition(before, after, fact.UserID) {
		return roomDomain.Room{}, roomDomain.ErrInvalidRoomInput
	}
	digest := sha256.Sum256(event.Snapshot().Payload)
	var stored roomDomain.Room
	err = repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		updated, updateErr := updateRoomAggregateCAS(ctx, queries, before, after)
		if updateErr != nil {
			return updateErr
		}
		if _, insertErr := newOutboxEventRepository(queries).Insert(ctx, event); insertErr != nil {
			return insertErr
		}
		if _, inboxErr := queries.CreateGameSystemInboxPending(ctx, sqlcgen.CreateGameSystemInboxPendingParams{
			SessionID: uuidToPG(fact.SessionID), SourceEventID: uuidToPG(fact.EventID),
			EventType: string(roomDomain.ParticipantRevokedEventType), PayloadDigest: digest[:], CreatedAt: timeToPG(fact.OccurredAt),
		}); inboxErr != nil {
			return inboxErr
		}
		stored = updated
		return nil
	})
	if err != nil {
		return roomDomain.Room{}, mapRoomRepositoryError(ctx, err, roomDomain.ErrRoomVersionConflict)
	}
	return stored, nil
}

// removedParticipantTransition prevents callers from using the atomic path for unrelated room mutations or non-player roles.
func removedParticipantTransition(before, after roomDomain.RoomSnapshot, userID uuid.UUID) bool {
	if before.Status != roomDomain.RoomStatusPlaying || before.ActiveSessionID == uuid.Nil ||
		after.RoomVersion != before.RoomVersion+1 || after.MembershipVersion != before.MembershipVersion+1 {
		return false
	}
	beforeParticipant := false
	for _, member := range before.Members {
		beforeParticipant = beforeParticipant || member.UserID == userID && member.Role == roomDomain.MemberRoleParticipant
	}
	if !beforeParticipant {
		return false
	}
	for _, member := range after.Members {
		if member.UserID == userID {
			return false
		}
	}
	return true
}

func (repository *RoomRepository) get(ctx context.Context, read func(QueryHandle) (sqlcgen.PartyRoom, error)) (roomDomain.Room, error) {
	var loaded roomDomain.Room
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := read(queries)
		if err != nil {
			return err
		}
		members, err := queries.ListRoomMembers(ctx, sqlcgen.ListRoomMembersParams{RoomID: row.RoomID})
		if err != nil {
			return err
		}
		loaded, err = roomFromRows(row, members)
		return err
	})
	if err != nil {
		return roomDomain.Room{}, mapRoomRepositoryError(ctx, err, roomDomain.ErrRoomNotFound)
	}
	return loaded, nil
}

func createPartyRoomParams(snapshot roomDomain.RoomSnapshot) sqlcgen.CreatePartyRoomParams {
	return sqlcgen.CreatePartyRoomParams{
		RoomID: uuidToPG(snapshot.ID), RoomCode: snapshot.RoomCode, Visibility: string(snapshot.Visibility),
		Status: string(snapshot.Status), HostUserID: uuidToPG(snapshot.HostUserID),
		ParticipantCapacity: int32(snapshot.ParticipantCapacity), ParticipantAdmission: string(snapshot.ParticipantAdmission),
		SpectatorAdmission: string(snapshot.SpectatorAdmission), ActiveSessionID: optionalUUIDToPG(snapshot.ActiveSessionID),
		ActiveGameID: textToPG(snapshot.ActiveGameID), LastFinishedSessionID: optionalUUIDToPG(snapshot.LastFinishedSessionID),
		LastFinishedGameID: textToPG(snapshot.LastFinishedGameID), RoomVersion: int64(snapshot.RoomVersion),
		MembershipVersion: int64(snapshot.MembershipVersion), CreatedAt: timeToPG(snapshot.CreatedAt), UpdatedAt: timeToPG(snapshot.UpdatedAt),
	}
}

func updatePartyRoomParams(before, after roomDomain.RoomSnapshot) sqlcgen.UpdatePartyRoomCASParams {
	return sqlcgen.UpdatePartyRoomCASParams{
		Visibility: string(after.Visibility), Status: string(after.Status), HostUserID: uuidToPG(after.HostUserID),
		ParticipantCapacity: int32(after.ParticipantCapacity), ParticipantAdmission: string(after.ParticipantAdmission),
		SpectatorAdmission: string(after.SpectatorAdmission), ActiveSessionID: optionalUUIDToPG(after.ActiveSessionID),
		ActiveGameID: textToPG(after.ActiveGameID), LastFinishedSessionID: optionalUUIDToPG(after.LastFinishedSessionID),
		LastFinishedGameID: textToPG(after.LastFinishedGameID), RoomVersion: int64(after.RoomVersion),
		MembershipVersion: int64(after.MembershipVersion), UpdatedAt: timeToPG(after.UpdatedAt),
		RoomID: uuidToPG(before.ID), RoomCode: before.RoomCode, ExpectedRoomVersion: int64(before.RoomVersion),
		ExpectedMembershipVersion: int64(before.MembershipVersion),
	}
}

// updateRoomAggregateCAS writes the room row and complete member snapshot through one query handle.
// The caller owns the surrounding transaction, so a later aggregate failure rolls back the whole room update.
func updateRoomAggregateCAS(ctx context.Context, queries QueryHandle, beforeSnapshot, afterSnapshot roomDomain.RoomSnapshot) (roomDomain.Room, error) {
	row, err := queries.UpdatePartyRoomCAS(ctx, updatePartyRoomParams(beforeSnapshot, afterSnapshot))
	if err != nil {
		return roomDomain.Room{}, err
	}
	if err := queries.DeleteRoomMembers(ctx, sqlcgen.DeleteRoomMembersParams{RoomID: uuidToPG(afterSnapshot.ID)}); err != nil {
		return roomDomain.Room{}, err
	}
	for _, member := range afterSnapshot.Members {
		if err := queries.CreateRoomMember(ctx, createRoomMemberParams(afterSnapshot.ID, member)); err != nil {
			return roomDomain.Room{}, err
		}
	}
	members, err := queries.ListRoomMembers(ctx, sqlcgen.ListRoomMembersParams{RoomID: uuidToPG(afterSnapshot.ID)})
	if err != nil {
		return roomDomain.Room{}, err
	}
	return roomFromRows(row, members)
}

func createRoomMemberParams(roomID uuid.UUID, member roomDomain.MemberSnapshot) sqlcgen.CreateRoomMemberParams {
	return sqlcgen.CreateRoomMemberParams{
		RoomID: uuidToPG(roomID), UserID: uuidToPG(member.UserID), Role: string(member.Role),
		RequestedRole: textToPG(string(member.RequestedRole)), SeatIndex: optionalSeatToPG(member),
		JoinedAt: timeToPG(member.JoinedAt), LastSeenAt: timeToPG(member.LastSeenAt),
	}
}

func roomFromRows(row sqlcgen.PartyRoom, members []sqlcgen.RoomMember) (roomDomain.Room, error) {
	if !row.RoomID.Valid || !row.HostUserID.Valid || row.ParticipantCapacity <= 0 || row.RoomVersion <= 0 || row.MembershipVersion <= 0 ||
		!row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return roomDomain.Room{}, roomDomain.ErrRoomIntegrity
	}
	snapshot := roomDomain.RoomSnapshot{
		ID: uuid.UUID(row.RoomID.Bytes), RoomCode: row.RoomCode, Visibility: roomDomain.Visibility(row.Visibility),
		Status: roomDomain.RoomStatus(row.Status), HostUserID: uuid.UUID(row.HostUserID.Bytes),
		ParticipantCapacity: uint32(row.ParticipantCapacity), ParticipantAdmission: roomDomain.AdmissionMode(row.ParticipantAdmission),
		SpectatorAdmission: roomDomain.AdmissionMode(row.SpectatorAdmission), RoomVersion: uint64(row.RoomVersion),
		MembershipVersion: uint64(row.MembershipVersion), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		ActiveSessionID: optionalUUIDFromPG(row.ActiveSessionID), ActiveGameID: optionalTextFromPG(row.ActiveGameID),
		LastFinishedSessionID: optionalUUIDFromPG(row.LastFinishedSessionID), LastFinishedGameID: optionalTextFromPG(row.LastFinishedGameID),
		Members: make([]roomDomain.MemberSnapshot, 0, len(members)),
	}
	for _, member := range members {
		if !member.RoomID.Valid || uuid.UUID(member.RoomID.Bytes) != snapshot.ID || !member.UserID.Valid || !member.JoinedAt.Valid || !member.LastSeenAt.Valid {
			return roomDomain.Room{}, roomDomain.ErrRoomIntegrity
		}
		mapped := roomDomain.MemberSnapshot{
			UserID: uuid.UUID(member.UserID.Bytes), Role: roomDomain.MemberRole(member.Role),
			RequestedRole: optionalMemberRoleFromPG(member.RequestedRole), SeatIndex: optionalSeatFromPG(member.SeatIndex),
			JoinedAt: member.JoinedAt.Time, LastSeenAt: member.LastSeenAt.Time,
		}
		snapshot.Members = append(snapshot.Members, mapped)
	}
	room, err := roomDomain.Restore(snapshot)
	if err != nil {
		return roomDomain.Room{}, roomDomain.ErrRoomIntegrity
	}
	return room, nil
}

func publicRoomCardFromRow(row sqlcgen.ListPublicRoomCardsRow) (roomDomain.PublicRoomCard, error) {
	if !row.RoomID.Valid || !row.HostUsername.Valid || row.ParticipantCapacity <= 0 ||
		row.ParticipantCount < 0 || row.ParticipantCount > math.MaxUint32 ||
		row.SpectatorCount < 0 || row.SpectatorCount > math.MaxUint32 ||
		row.WaitingCount < 0 || row.WaitingCount > math.MaxUint32 || !row.UpdatedAt.Valid {
		return roomDomain.PublicRoomCard{}, roomDomain.ErrRoomIntegrity
	}
	return roomDomain.RestorePublicRoomCard(roomDomain.PublicRoomCardSnapshot{
		RoomID: uuid.UUID(row.RoomID.Bytes), HostUsername: row.HostUsername.String,
		Status: roomDomain.RoomStatus(row.Status), ParticipantCapacity: uint32(row.ParticipantCapacity),
		ParticipantCount: uint32(row.ParticipantCount), SpectatorCount: uint32(row.SpectatorCount),
		WaitingCount: uint32(row.WaitingCount), ParticipantAdmission: roomDomain.AdmissionMode(row.ParticipantAdmission),
		SpectatorAdmission: roomDomain.AdmissionMode(row.SpectatorAdmission), ActiveGameID: optionalTextFromPG(row.ActiveGameID),
		ViewerRole: optionalMemberRoleFromPG(row.ViewerRole), ViewerRequestedRole: optionalMemberRoleFromPG(row.ViewerRequestedRole),
		UpdatedAt: row.UpdatedAt.Time,
	})
}

func myRoomCardFromRow(row sqlcgen.ListMyRoomCardsRow) (roomDomain.MyRoomCard, error) {
	if !row.RoomID.Valid || !row.HostUsername.Valid || row.ParticipantCapacity <= 0 ||
		row.ParticipantCount < 0 || row.ParticipantCount > math.MaxUint32 ||
		row.SpectatorCount < 0 || row.SpectatorCount > math.MaxUint32 ||
		row.WaitingCount < 0 || row.WaitingCount > math.MaxUint32 || !row.UpdatedAt.Valid {
		return roomDomain.MyRoomCard{}, roomDomain.ErrRoomIntegrity
	}
	return roomDomain.RestoreMyRoomCard(roomDomain.MyRoomCardSnapshot{
		RoomID: uuid.UUID(row.RoomID.Bytes), RoomCode: row.RoomCode, Visibility: roomDomain.Visibility(row.Visibility),
		HostUsername: row.HostUsername.String, Status: roomDomain.RoomStatus(row.Status), IsHost: row.IsHost,
		ParticipantCapacity: uint32(row.ParticipantCapacity), ParticipantCount: uint32(row.ParticipantCount),
		SpectatorCount: uint32(row.SpectatorCount), WaitingCount: uint32(row.WaitingCount),
		ParticipantAdmission: roomDomain.AdmissionMode(row.ParticipantAdmission),
		SpectatorAdmission:   roomDomain.AdmissionMode(row.SpectatorAdmission), ActiveGameID: optionalTextFromPG(row.ActiveGameID),
		LastFinishedGameID: optionalTextFromPG(row.LastFinishedGameID), ViewerRole: roomDomain.MemberRole(row.ViewerRole),
		ViewerRequestedRole: optionalMemberRoleFromPG(row.ViewerRequestedRole), UpdatedAt: row.UpdatedAt.Time,
	})
}

func roomLobbyCursorFloor() time.Time {
	return time.Unix(0, 0).UTC()
}

func validateRoomTransition(before, after roomDomain.RoomSnapshot) error {
	if err := validateRoomPersistenceWidths(before); err != nil {
		return err
	}
	if err := validateRoomPersistenceWidths(after); err != nil {
		return err
	}
	if before.ID != after.ID || before.RoomCode != after.RoomCode || !before.CreatedAt.Equal(after.CreatedAt) ||
		after.RoomVersion != before.RoomVersion+1 ||
		(after.MembershipVersion != before.MembershipVersion && after.MembershipVersion != before.MembershipVersion+1) {
		return roomDomain.ErrInvalidRoomInput
	}
	return nil
}

func validateRoomPersistenceWidths(snapshot roomDomain.RoomSnapshot) error {
	if snapshot.ParticipantCapacity > math.MaxInt32 || snapshot.RoomVersion > math.MaxInt64 || snapshot.MembershipVersion > math.MaxInt64 {
		return roomDomain.ErrInvalidRoomInput
	}
	for _, member := range snapshot.Members {
		if member.SeatIndex > math.MaxInt32 {
			return roomDomain.ErrInvalidRoomInput
		}
	}
	return nil
}

func optionalUUIDToPG(value uuid.UUID) pgtype.UUID {
	if value == uuid.Nil {
		return pgtype.UUID{}
	}
	return uuidToPG(value)
}

func optionalUUIDFromPG(value pgtype.UUID) uuid.UUID {
	if !value.Valid {
		return uuid.Nil
	}
	return uuid.UUID(value.Bytes)
}

func optionalTextFromPG(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func optionalSeatToPG(member roomDomain.MemberSnapshot) pgtype.Int4 {
	if member.Role != roomDomain.MemberRoleParticipant {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(member.SeatIndex), Valid: true}
}

func optionalSeatFromPG(value pgtype.Int4) uint32 {
	if !value.Valid {
		return 0
	}
	return uint32(value.Int32)
}

func optionalMemberRoleFromPG(value pgtype.Text) roomDomain.MemberRole {
	if !value.Valid {
		return ""
	}
	return roomDomain.MemberRole(value.String)
}

func mapRoomRepositoryError(ctx context.Context, err, noRowsError error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505":
			if pgError.ConstraintName == "party_rooms_room_code_unique" {
				return roomDomain.ErrRoomCodeUnavailable
			}
			return roomDomain.ErrRoomVersionConflict
		case "23503", "23514":
			return roomDomain.ErrRoomIntegrity
		case "40001", "40P01":
			return roomDomain.ErrRoomVersionConflict
		}
	}
	if errors.Is(err, roomDomain.ErrRoomIntegrity) || errors.Is(err, roomDomain.ErrInvalidRoomInput) {
		return err
	}
	return roomDomain.ErrRoomRepositoryUnavailable
}

var _ roomDomain.Repository = (*RoomRepository)(nil)
var _ roomDomain.Store = (*RoomRepository)(nil)
