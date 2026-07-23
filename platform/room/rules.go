package room

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrRuleRevisionConflict prevents a stale editor from overwriting a newer draft.
	ErrRuleRevisionConflict = errors.New("room game rule revision conflict")
	// ErrRulePermission reports a rule write attempted by a non-host or non-owner.
	ErrRulePermission = errors.New("room game rule permission denied")
	// ErrRuleOperationConflict reports reuse of an operation ID with different input.
	ErrRuleOperationConflict = errors.New("room game rule operation conflict")
	// ErrRuleNotFound is returned when a draft, preset, or pending start is absent.
	ErrRuleNotFound = errors.New("room game rule not found")
	// ErrPendingStartInvalid reports an expired, cancelled, or already consumed countdown.
	ErrPendingStartInvalid = errors.New("pending game start is invalid")
)

// ConfigEnvelope is the platform-neutral representation of a game-owned frozen payload.
// The platform validates framing and versions but deliberately does not inspect Payload.
type ConfigEnvelope struct {
	GameID          string
	EngineVersion   string
	ProtocolVersion string
	ClientVersion   string
	SchemaVersion   uint32
	MessageType     string
	Payload         []byte
}

// Clone returns an independent envelope for persistence and response projection.
func (envelope ConfigEnvelope) Clone() ConfigEnvelope {
	envelope.Payload = append([]byte(nil), envelope.Payload...)
	return envelope
}

// Valid checks the stable envelope framing before a game module performs its own payload validation.
func (envelope ConfigEnvelope) Valid() bool {
	return strings.TrimSpace(envelope.GameID) != "" &&
		strings.TrimSpace(envelope.EngineVersion) != "" &&
		strings.TrimSpace(envelope.ProtocolVersion) != "" &&
		strings.TrimSpace(envelope.ClientVersion) != "" &&
		envelope.SchemaVersion > 0 && strings.TrimSpace(envelope.MessageType) != "" && len(envelope.Payload) <= 1<<20
}

// Digest computes the stable payload binding used by idempotent rule writes.
func (envelope ConfigEnvelope) Digest() [32]byte {
	hash := sha256.New()
	for _, value := range []string{envelope.GameID, envelope.EngineVersion, envelope.ProtocolVersion, envelope.ClientVersion, envelope.MessageType} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	var version [4]byte
	version[0] = byte(envelope.SchemaVersion)
	version[1] = byte(envelope.SchemaVersion >> 8)
	version[2] = byte(envelope.SchemaVersion >> 16)
	version[3] = byte(envelope.SchemaVersion >> 24)
	_, _ = hash.Write(version[:])
	_, _ = hash.Write(envelope.Payload)
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

// RuleDraft is one normalized room/game configuration revision.
type RuleDraft struct {
	RoomID    uuid.UUID
	GameID    string
	Config    ConfigEnvelope
	Revision  uint64
	UpdatedBy uuid.UUID
	UpdatedAt time.Time
}

// RulePreset is a host-owned reusable configuration independent of a room lifecycle.
type RulePreset struct {
	ID          uuid.UUID
	OwnerUserID uuid.UUID
	GameID      string
	Name        string
	Config      ConfigEnvelope
	Revision    uint64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastUsedAt  time.Time
	Compatible  bool
}

// PendingStart is the durable countdown binding all versions that must remain unchanged.
type PendingStart struct {
	ID             uuid.UUID
	RoomID         uuid.UUID
	CancelToken    string
	Deadline       time.Time
	GameID         string
	ConfigRevision uint64
	Expected       Version
	OwnershipEpoch uint64
	OperationID    string
	RequestDigest  [32]byte
	Cancelled      bool
	Consumed       bool
}

// RuleDraftUpdate carries all fencing values for a host rule edit.
type RuleDraftUpdate struct {
	RoomID           uuid.UUID
	ActorUserID      uuid.UUID
	GameID           string
	Config           ConfigEnvelope
	ExpectedRevision uint64
	Expected         Version
	OwnershipEpoch   uint64
	OperationID      string
	RequestDigest    [32]byte
	At               time.Time
}

// RulePresetWrite carries optimistic version data for create, overwrite, or copy operations.
type RulePresetWrite struct {
	PresetID         uuid.UUID
	OwnerUserID      uuid.UUID
	GameID           string
	Name             string
	Config           ConfigEnvelope
	ExpectedRevision uint64
	OperationID      string
	RequestDigest    [32]byte
	At               time.Time
	Copy             bool
}

// PendingStartCreate carries the server-side countdown binding.
type PendingStartCreate struct {
	RoomID         uuid.UUID
	ActorUserID    uuid.UUID
	GameID         string
	ConfigRevision uint64
	Expected       Version
	OwnershipEpoch uint64
	OperationID    string
	RequestDigest  [32]byte
	Deadline       time.Time
	At             time.Time
}

// RuleRepository is the persistence port shared by transport and future PostgreSQL adapters.
type RuleRepository interface {
	ListDrafts(context.Context, uuid.UUID) ([]RuleDraft, error)
	GetDraft(context.Context, uuid.UUID, string) (RuleDraft, error)
	UpdateDraft(context.Context, RuleDraftUpdate) (RuleDraft, error)
	ListPresets(context.Context, uuid.UUID, string) ([]RulePreset, error)
	SavePreset(context.Context, RulePresetWrite) (RulePreset, error)
	DeletePreset(context.Context, uuid.UUID, uuid.UUID, uint64, string, [32]byte) error
	BeginPendingStart(context.Context, PendingStartCreate) (PendingStart, error)
	GetPendingStart(context.Context, uuid.UUID) (PendingStart, error)
	CancelPendingStart(context.Context, uuid.UUID, uuid.UUID, string, uint64, [32]byte, time.Time) error
	ConsumePendingStart(context.Context, uuid.UUID, uuid.UUID, string, string, [32]byte, time.Time) (PendingStart, error)
}

// MemoryRuleRepository is a deterministic fallback used by transport tests and local development.
// Production wiring replaces it with the PostgreSQL adapter without changing the API surface.
type MemoryRuleRepository struct {
	mu       sync.RWMutex
	drafts   map[uuid.UUID]map[string]RuleDraft
	draftOps map[string]struct {
		digest [32]byte
		draft  RuleDraft
	}
	presets   map[uuid.UUID]RulePreset
	presetOps map[string]struct {
		digest [32]byte
		preset RulePreset
	}
	startByRoom map[uuid.UUID]PendingStart
	startOps    map[string]struct {
		digest [32]byte
		start  PendingStart
	}
	deleteOps map[string]struct {
		digest [32]byte
	}
}

// NewMemoryRuleRepository creates an empty fallback repository.
func NewMemoryRuleRepository() *MemoryRuleRepository {
	return &MemoryRuleRepository{
		drafts: make(map[uuid.UUID]map[string]RuleDraft), draftOps: make(map[string]struct {
			digest [32]byte
			draft  RuleDraft
		}), presets: make(map[uuid.UUID]RulePreset),
		presetOps: make(map[string]struct {
			digest [32]byte
			preset RulePreset
		}),
		startByRoom: make(map[uuid.UUID]PendingStart), startOps: make(map[string]struct {
			digest [32]byte
			start  PendingStart
		}), deleteOps: make(map[string]struct {
			digest [32]byte
		}),
	}
}

// ListDrafts returns defensive copies sorted by game ID for deterministic projections.
func (repository *MemoryRuleRepository) ListDrafts(ctx context.Context, roomID uuid.UUID) ([]RuleDraft, error) {
	if err := validateRuleContext(ctx, roomID); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	values := make([]RuleDraft, 0, len(repository.drafts[roomID]))
	for _, draft := range repository.drafts[roomID] {
		values = append(values, cloneDraft(draft))
	}
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if values[right].GameID < values[left].GameID {
				values[left], values[right] = values[right], values[left]
			}
		}
	}
	return values, nil
}

// GetDraft returns one draft or ErrRuleNotFound when the game has not been edited yet.
func (repository *MemoryRuleRepository) GetDraft(ctx context.Context, roomID uuid.UUID, gameID string) (RuleDraft, error) {
	if err := validateRuleContext(ctx, roomID); err != nil {
		return RuleDraft{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	draft, ok := repository.drafts[roomID][strings.TrimSpace(gameID)]
	if !ok {
		return RuleDraft{}, ErrRuleNotFound
	}
	return cloneDraft(draft), nil
}

// UpdateDraft applies a host edit with revision and operation-id fencing.
func (repository *MemoryRuleRepository) UpdateDraft(ctx context.Context, update RuleDraftUpdate) (RuleDraft, error) {
	if err := validateRuleContext(ctx, update.RoomID); err != nil {
		return RuleDraft{}, err
	}
	update.GameID = strings.TrimSpace(update.GameID)
	if update.ActorUserID == uuid.Nil || update.OwnershipEpoch == 0 || update.OperationID == "" || !update.Config.Valid() ||
		update.GameID == "" || update.Config.GameID != update.GameID || update.Expected.Room == 0 || update.Expected.Membership == 0 || update.At.IsZero() {
		return RuleDraft{}, ErrInvalidRoomInput
	}
	if update.RequestDigest == ([32]byte{}) {
		update.RequestDigest = update.Config.Digest()
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if operation, ok := repository.draftOps[update.OperationID]; ok {
		if operation.digest != update.RequestDigest && update.RequestDigest != ([32]byte{}) {
			return RuleDraft{}, ErrRuleOperationConflict
		}
		return cloneDraft(operation.draft), nil
	}
	if repository.drafts[update.RoomID] == nil {
		repository.drafts[update.RoomID] = make(map[string]RuleDraft)
	}
	current, exists := repository.drafts[update.RoomID][update.GameID]
	if exists && current.Revision != update.ExpectedRevision {
		return RuleDraft{}, ErrRuleRevisionConflict
	}
	if !exists && update.ExpectedRevision != 0 {
		return RuleDraft{}, ErrRuleRevisionConflict
	}
	draft := RuleDraft{RoomID: update.RoomID, GameID: update.GameID, Config: update.Config.Clone(), Revision: update.ExpectedRevision + 1, UpdatedBy: update.ActorUserID, UpdatedAt: update.At.UTC()}
	repository.drafts[update.RoomID][update.GameID] = draft
	repository.draftOps[update.OperationID] = struct {
		digest [32]byte
		draft  RuleDraft
	}{digest: update.RequestDigest, draft: draft}
	return cloneDraft(draft), nil
}

// ListPresets returns only presets owned by the requesting user.
func (repository *MemoryRuleRepository) ListPresets(ctx context.Context, ownerID uuid.UUID, gameID string) ([]RulePreset, error) {
	if err := validateRuleContext(ctx, ownerID); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	values := make([]RulePreset, 0)
	for _, preset := range repository.presets {
		if preset.OwnerUserID == ownerID && (gameID == "" || preset.GameID == gameID) {
			values = append(values, clonePreset(preset))
		}
	}
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if values[right].UpdatedAt.After(values[left].UpdatedAt) {
				values[left], values[right] = values[right], values[left]
			}
		}
	}
	return values, nil
}

// SavePreset creates or updates one owner-scoped preset under optimistic revision control.
func (repository *MemoryRuleRepository) SavePreset(ctx context.Context, write RulePresetWrite) (RulePreset, error) {
	if err := validateRuleContext(ctx, write.OwnerUserID); err != nil {
		return RulePreset{}, err
	}
	write.GameID = strings.TrimSpace(write.GameID)
	if write.OwnerUserID == uuid.Nil || write.GameID == "" || strings.TrimSpace(write.Name) == "" || !write.Config.Valid() ||
		write.Config.GameID != write.GameID || write.OperationID == "" || write.At.IsZero() {
		return RulePreset{}, ErrInvalidRoomInput
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if operation, ok := repository.presetOps[write.OperationID]; ok {
		if operation.digest != write.RequestDigest {
			return RulePreset{}, ErrRuleOperationConflict
		}
		return clonePreset(operation.preset), nil
	}
	if write.Copy {
		write.PresetID = uuid.New()
		write.ExpectedRevision = 0
	} else if write.PresetID == uuid.Nil {
		write.PresetID = uuid.New()
	}
	current, exists := repository.presets[write.PresetID]
	if exists && (current.OwnerUserID != write.OwnerUserID || current.GameID != write.GameID) {
		return RulePreset{}, ErrRulePermission
	}
	if exists && current.Revision != write.ExpectedRevision {
		return RulePreset{}, ErrRuleRevisionConflict
	}
	if !exists && write.ExpectedRevision != 0 {
		return RulePreset{}, ErrRuleRevisionConflict
	}
	if exists {
		current.Revision++
	} else if !exists {
		current.Revision = 1
	}
	if current.CreatedAt.IsZero() {
		current.CreatedAt = write.At.UTC()
	}
	current.ID, current.OwnerUserID, current.GameID, current.Name = write.PresetID, write.OwnerUserID, write.GameID, strings.TrimSpace(write.Name)
	current.Config, current.UpdatedAt, current.LastUsedAt, current.Compatible = write.Config.Clone(), write.At.UTC(), write.At.UTC(), true
	repository.presets[current.ID] = current
	repository.presetOps[write.OperationID] = struct {
		digest [32]byte
		preset RulePreset
	}{digest: write.RequestDigest, preset: current}
	return clonePreset(current), nil
}

// DeletePreset removes one owner preset after checking its revision and idempotency binding.
func (repository *MemoryRuleRepository) DeletePreset(ctx context.Context, ownerID, presetID uuid.UUID, expectedRevision uint64, operationID string, digest [32]byte) error {
	if err := validateRuleContext(ctx, ownerID); err != nil {
		return err
	}
	if presetID == uuid.Nil || operationID == "" {
		return ErrInvalidRoomInput
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if operation, ok := repository.deleteOps[operationID]; ok {
		if operation.digest != digest {
			return ErrRuleOperationConflict
		}
		return nil
	}
	preset, ok := repository.presets[presetID]
	if !ok {
		return ErrRuleNotFound
	}
	if preset.OwnerUserID != ownerID {
		return ErrRulePermission
	}
	if preset.Revision != expectedRevision {
		return ErrRuleRevisionConflict
	}
	delete(repository.presets, presetID)
	repository.deleteOps[operationID] = struct{ digest [32]byte }{digest: digest}
	return nil
}

// BeginPendingStart creates one room-scoped countdown and replays identical retries.
func (repository *MemoryRuleRepository) BeginPendingStart(ctx context.Context, create PendingStartCreate) (PendingStart, error) {
	if err := validateRuleContext(ctx, create.RoomID); err != nil {
		return PendingStart{}, err
	}
	if create.ActorUserID == uuid.Nil || create.OwnershipEpoch == 0 || create.OperationID == "" || create.Deadline.Before(create.At) {
		return PendingStart{}, ErrInvalidRoomInput
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if operation, ok := repository.startOps[create.OperationID]; ok {
		if operation.digest != create.RequestDigest {
			return PendingStart{}, ErrRuleOperationConflict
		}
		return operation.start, nil
	}
	if current, ok := repository.startByRoom[create.RoomID]; ok && !current.Cancelled && !current.Consumed && current.Deadline.After(create.At) {
		return PendingStart{}, ErrPendingStartInvalid
	}
	start := PendingStart{ID: uuid.New(), RoomID: create.RoomID, CancelToken: uuid.NewString(), Deadline: create.Deadline.UTC(), GameID: create.GameID, ConfigRevision: create.ConfigRevision, Expected: create.Expected, OwnershipEpoch: create.OwnershipEpoch, OperationID: create.OperationID, RequestDigest: create.RequestDigest}
	repository.startByRoom[create.RoomID] = start
	repository.startOps[create.OperationID] = struct {
		digest [32]byte
		start  PendingStart
	}{digest: create.RequestDigest, start: start}
	return start, nil
}

// GetPendingStart returns the current room countdown.
func (repository *MemoryRuleRepository) GetPendingStart(ctx context.Context, roomID uuid.UUID) (PendingStart, error) {
	if err := validateRuleContext(ctx, roomID); err != nil {
		return PendingStart{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	start, ok := repository.startByRoom[roomID]
	if !ok {
		return PendingStart{}, ErrRuleNotFound
	}
	return start, nil
}

// CancelPendingStart invalidates a countdown only when its token and fencing values match.
func (repository *MemoryRuleRepository) CancelPendingStart(ctx context.Context, roomID, pendingID uuid.UUID, cancelToken string, ownershipEpoch uint64, _ [32]byte, at time.Time) error {
	if err := validateRuleContext(ctx, roomID); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	start, ok := repository.startByRoom[roomID]
	if !ok || start.ID != pendingID || start.CancelToken != cancelToken || start.OwnershipEpoch != ownershipEpoch || start.Consumed || start.Deadline.Before(at) {
		return ErrPendingStartInvalid
	}
	start.Cancelled = true
	repository.startByRoom[roomID] = start
	return nil
}

// ConsumePendingStart atomically marks a valid countdown as consumed exactly once.
func (repository *MemoryRuleRepository) ConsumePendingStart(ctx context.Context, roomID, pendingID uuid.UUID, cancelToken, operationID string, digest [32]byte, at time.Time) (PendingStart, error) {
	if err := validateRuleContext(ctx, roomID); err != nil {
		return PendingStart{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	start, ok := repository.startByRoom[roomID]
	if !ok || start.ID != pendingID || start.CancelToken != cancelToken || start.OperationID != operationID || start.RequestDigest != digest || start.Cancelled || start.Deadline.After(at) {
		return PendingStart{}, ErrPendingStartInvalid
	}
	if start.Consumed {
		return start, nil
	}
	start.Consumed = true
	repository.startByRoom[roomID] = start
	return start, nil
}

func validateRuleContext(ctx context.Context, id uuid.UUID) error {
	if ctx == nil || id == uuid.Nil {
		return ErrInvalidRoomInput
	}
	return ctx.Err()
}

func cloneDraft(value RuleDraft) RuleDraft    { value.Config = value.Config.Clone(); return value }
func clonePreset(value RulePreset) RulePreset { value.Config = value.Config.Clone(); return value }
