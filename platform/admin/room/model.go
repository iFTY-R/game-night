package room

import (
	"time"

	"github.com/google/uuid"
)

type SortDirection string

const (
	SortAscending  SortDirection = "ascending"
	SortDescending SortDirection = "descending"
)

type RoomSortField string

const (
	RoomSortUpdatedAt      RoomSortField = "updated_at"
	RoomSortCreatedAt      RoomSortField = "created_at"
	RoomSortLastActivityAt RoomSortField = "last_activity_at"
	RoomSortRoomCode       RoomSortField = "room_code"
)

type GameSortField string

const (
	GameSortUpdatedAt      GameSortField = "updated_at"
	GameSortStartedAt      GameSortField = "started_at"
	GameSortLastProgressAt GameSortField = "last_progress_at"
	GameSortSessionID      GameSortField = "session_id"
)

type RoomAnomalyFlag string

const (
	RoomAnomalyOwnerStale        RoomAnomalyFlag = "owner_stale"
	RoomAnomalyOwnerMissing      RoomAnomalyFlag = "owner_missing"
	RoomAnomalyAllPlayersOffline RoomAnomalyFlag = "all_players_offline"
	RoomAnomalyGameLinkMismatch  RoomAnomalyFlag = "room_game_link_mismatch"
)

type GameAnomalyFlag string

const (
	GameAnomalyOwnerStale       GameAnomalyFlag = "owner_stale"
	GameAnomalyOwnerMissing     GameAnomalyFlag = "owner_missing"
	GameAnomalyNoRecentProgress GameAnomalyFlag = "no_recent_progress"
	GameAnomalyRoomLinkMismatch GameAnomalyFlag = "room_link_mismatch"
)

type RepairType string

const (
	RepairClearStaleOwnerLease    RepairType = "clear_stale_owner_lease"
	RepairTerminateUnrecoverable  RepairType = "terminate_unrecoverable_game"
	RepairRepairRoomGameLink      RepairType = "repair_room_game_link"
	RepairTargetKindRoom          string     = "room"
	RepairTargetKindGameSession   string     = "game_session"
	RepairStatePreviewed          string     = "previewed"
	RepairStateExecuted           string     = "executed"
	RepairStateRejected           string     = "rejected"
	RepairStateExpired            string     = "expired"
	DefaultRoomDetailEventLimit   uint32     = 20
	DefaultGameDetailEventLimit   uint32     = 50
	DefaultRoomActiveGameLimit    uint32     = 8
	DefaultRoomMemberOnlineWindow            = 2 * time.Minute
)

type RoomCursor struct {
	RoomID   uuid.UUID
	SortTime time.Time
	SortText string
}

type RoomListQuery struct {
	RoomID        uuid.UUID
	RoomCode      string
	Statuses      []string
	GameIDs       []string
	HostUserID    uuid.UUID
	MemberUserID  uuid.UUID
	AnomaliesOnly bool
	CreatedFrom   time.Time
	CreatedTo     time.Time
	UpdatedFrom   time.Time
	UpdatedTo     time.Time
	SortField     RoomSortField
	Direction     SortDirection
	After         RoomCursor
	PageSize      uint32
}

type RoomSummary struct {
	RoomID               uuid.UUID
	RoomCode             string
	Status               string
	ActiveGameID         string
	ActiveSessionID      uuid.UUID
	HostUserID           uuid.UUID
	HostUsername         string
	ParticipantCount     uint32
	SpectatorCount       uint32
	ParticipantAdmission string
	SpectatorAdmission   string
	RoomVersion          uint64
	MembershipVersion    uint64
	OwnershipEpoch       uint64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	LastActivityAt       time.Time
	Anomalies            []RoomAnomalyFlag
}

type RoomMemberSummary struct {
	UserID            uuid.UUID
	Username          string
	Role              string
	RequestedRole     string
	MembershipVersion uint64
	JoinedAt          time.Time
	Online            bool
}

type RoomEventSummary struct {
	EventID     string
	EventType   string
	ActorUserID uuid.UUID
	Digest      string
	OccurredAt  time.Time
}

type RoomDetail struct {
	Summary      RoomSummary
	Members      []RoomMemberSummary
	ActiveGames  []GameSummary
	RecentEvents []RoomEventSummary
	SampledAt    time.Time
}

type GameCursor struct {
	SessionID uuid.UUID
	SortTime  time.Time
}

type GameListQuery struct {
	SessionID     uuid.UUID
	RoomID        uuid.UUID
	GameIDs       []string
	Statuses      []string
	AnomaliesOnly bool
	StartedFrom   time.Time
	StartedTo     time.Time
	UpdatedFrom   time.Time
	UpdatedTo     time.Time
	SortField     GameSortField
	Direction     SortDirection
	After         GameCursor
	PageSize      uint32
}

type GameSummary struct {
	SessionID      uuid.UUID
	RoomID         uuid.UUID
	RoomCode       string
	GameID         string
	GameVersion    string
	Status         string
	StateVersion   uint64
	OwnershipEpoch uint64
	StartedAt      time.Time
	UpdatedAt      time.Time
	LastProgressAt time.Time
	Anomalies      []GameAnomalyFlag
}

type GameParticipantSummary struct {
	UserID   uuid.UUID
	Username string
	RoomRole string
	Active   bool
}

type GameEventSummary struct {
	EventID      string
	EventType    string
	StateVersion uint64
	ActorUserID  uuid.UUID
	Digest       string
	OccurredAt   time.Time
}

type GameDetail struct {
	Summary      GameSummary
	Participants []GameParticipantSummary
	RecentEvents []GameEventSummary
	SampledAt    time.Time
}

type RepairOperation struct {
	RepairID                  uuid.UUID
	RepairType                RepairType
	State                     string
	TargetID                  uuid.UUID
	TargetKind                string
	TargetDigest              []byte
	PreviewDigest             []byte
	CommandVersion            uint64
	ExpectedRoomVersion       uint64
	ExpectedMembershipVersion uint64
	ExpectedStateVersion      uint64
	ExpectedOwnershipEpoch    uint64
	Summary                   string
	IrreversibleEffects       []string
	BeforeSnapshotDigest      []byte
	AfterSnapshotDigest       []byte
	RequestedByAdminID        uuid.UUID
	OperationID               string
	RequestDigest             []byte
	AuditEventID              uuid.UUID
	Reason                    string
	Version                   uint64
	CreatedAt                 time.Time
	ExpiresAt                 time.Time
	ExecutedAt                time.Time
}

type CreateRepairOperationCommand struct {
	RepairOperation
}

type CompleteRepairOperationCommand struct {
	RepairID            uuid.UUID
	OperationID         string
	RequestDigest       []byte
	AuditEventID        uuid.UUID
	AfterSnapshotDigest []byte
	Reason              string
	State               string
	ExpectedVersion     uint64
	ExecutedAt          time.Time
}
