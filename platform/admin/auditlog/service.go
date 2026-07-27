package auditlog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
)

// Config exposes the verified audit reader and cursor dependency explicitly for deterministic tests.
type Config struct {
	Reader Reader
	Cursor *CursorCodec
	Clock  clock.Clock
}

// Service authorizes redacted audit inspection and binds every continuation token to its exact normalized filter.
type Service struct {
	reader Reader
	cursor *CursorCodec
	clock  clock.Clock
}

// NewService rejects partially wired audit readers so management traffic cannot silently bypass verification.
func NewService(config Config) (*Service, error) {
	if config.Reader == nil || config.Cursor == nil || config.Clock == nil {
		return nil, ErrInvalidInput
	}
	return &Service{reader: config.Reader, cursor: config.Cursor, clock: config.Clock}, nil
}

// ListEvents returns redacted metadata with a per-event verification outcome. A filtered page scans a bounded chain segment per request.
func (service *Service) ListEvents(ctx context.Context, actor admin.ActorContext, input ListInput) (Page, error) {
	if service == nil || service.reader == nil || service.cursor == nil || service.clock == nil || ctx == nil {
		return Page{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionAuditRead); err != nil {
		return Page{}, ErrPermissionDenied
	}
	filter, digest, pageSize, err := normalizeListInput(input)
	if err != nil {
		return Page{}, err
	}
	afterSequence := uint64(0)
	if input.PageToken != "" {
		afterSequence, err = service.cursor.Decode(input.PageToken, digest)
		if err != nil {
			return Page{}, err
		}
	}
	head, err := service.reader.ReadHead(ctx, audit.ChainAdmin)
	if err != nil {
		return Page{}, mapReaderError(err)
	}
	page := Page{SampledAt: service.clock.Now(), ChainHead: head, Events: make([]Event, 0, pageSize)}
	lastScanned := afterSequence
	remaining := MaximumScanEvents
	more := false
	for remaining > 0 && uint32(len(page.Events)) < pageSize {
		chunkSize := pageSize * 4
		if chunkSize < 100 {
			chunkSize = 100
		}
		if chunkSize > remaining {
			chunkSize = remaining
		}
		request, requestErr := audit.NewListRequest(audit.ChainAdmin, lastScanned, chunkSize)
		if requestErr != nil {
			return Page{}, ErrInvalidInput
		}
		events, listErr := service.reader.List(ctx, request)
		if listErr != nil {
			return Page{}, mapReaderError(listErr)
		}
		if len(events) == 0 {
			break
		}
		for index, signed := range events {
			event, conversionErr := redactedEvent(signed.Event)
			if conversionErr != nil {
				return Page{}, conversionErr
			}
			event.Verified = signed.Verified
			lastScanned = event.Sequence
			page.ScannedEvents++
			remaining--
			if filter.matches(event) {
				page.Events = append(page.Events, event)
			}
			if uint32(len(page.Events)) == pageSize {
				// Any unread row in this chunk, or a full chunk, means the HMAC cursor must continue from this sequence.
				more = index+1 < len(events) || uint32(len(events)) == chunkSize
				break
			}
		}
		if uint32(len(page.Events)) == pageSize || remaining == 0 {
			if remaining == 0 && lastScanned > afterSequence {
				more = true
			}
			break
		}
		if uint32(len(events)) < chunkSize {
			// The final short repository page proves there is no continuation after the last scanned sequence.
			more = false
			break
		}
		more = true
	}
	if more && lastScanned > afterSequence {
		page.NextPageToken, err = service.cursor.Encode(digest, lastScanned)
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

// ListInput is the transport-neutral audit query before filter normalization and cursor verification.
type ListInput struct {
	Filter    Filter
	PageSize  uint32
	PageToken string
}

type normalizedFilter struct {
	EventID      string    `json:"event_id,omitempty"`
	Actions      []int32   `json:"actions,omitempty"`
	ActorTypes   []int32   `json:"actor_types,omitempty"`
	ActorID      string    `json:"actor_id,omitempty"`
	TargetTypes  []int32   `json:"target_types,omitempty"`
	TargetID     string    `json:"target_id,omitempty"`
	RequestID    string    `json:"request_id,omitempty"`
	ReasonCode   string    `json:"reason_code,omitempty"`
	OccurredFrom time.Time `json:"occurred_from,omitempty"`
	OccurredTo   time.Time `json:"occurred_to,omitempty"`
}

func normalizeListInput(input ListInput) (Filter, [sha256.Size]byte, uint32, error) {
	pageSize := input.PageSize
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaximumPageSize {
		return Filter{}, [sha256.Size]byte{}, 0, ErrInvalidInput
	}
	filter, err := normalizeFilter(input.Filter)
	if err != nil {
		return Filter{}, [sha256.Size]byte{}, 0, err
	}
	body, err := json.Marshal(normalizedFilter{
		EventID: uuidText(filter.EventID), Actions: actionValues(filter.Actions), ActorTypes: actorTypeValues(filter.ActorTypes), ActorID: filter.ActorID,
		TargetTypes: targetTypeValues(filter.TargetTypes), TargetID: filter.TargetID, RequestID: filter.RequestID, ReasonCode: filter.ReasonCode,
		OccurredFrom: filter.OccurredFrom, OccurredTo: filter.OccurredTo,
	})
	if err != nil {
		return Filter{}, [sha256.Size]byte{}, 0, ErrInvalidInput
	}
	return filter, sha256.Sum256(body), pageSize, nil
}

func normalizeFilter(input Filter) (Filter, error) {
	filter := Filter{
		EventID: input.EventID, Actions: append([]audit.Action(nil), input.Actions...), ActorTypes: append([]audit.ActorType(nil), input.ActorTypes...),
		ActorID: strings.TrimSpace(input.ActorID), TargetTypes: append([]audit.TargetType(nil), input.TargetTypes...), TargetID: strings.TrimSpace(input.TargetID),
		RequestID: strings.TrimSpace(input.RequestID), ReasonCode: strings.TrimSpace(input.ReasonCode),
		OccurredFrom: canonicalTime(input.OccurredFrom), OccurredTo: canonicalTime(input.OccurredTo),
	}
	if !validActions(filter.Actions) || !validActorTypes(filter.ActorTypes) || !validTargetTypes(filter.TargetTypes) ||
		!validFilterText(filter.ActorID, 128) || !validFilterText(filter.TargetID, 128) || !validFilterText(filter.RequestID, audit.MaxRequestIDBytes) ||
		!validFilterText(filter.ReasonCode, audit.MaxReasonCodeBytes) || (!filter.OccurredFrom.IsZero() && !filter.OccurredTo.IsZero() && filter.OccurredFrom.After(filter.OccurredTo)) {
		return Filter{}, ErrInvalidInput
	}
	return filter, nil
}

func (filter Filter) matches(event Event) bool {
	return (filter.EventID == uuid.Nil || filter.EventID == event.EventID) &&
		containsAction(filter.Actions, event.Action) && containsActorType(filter.ActorTypes, event.ActorType) &&
		(filter.ActorID == "" || filter.ActorID == event.ActorID) && containsTargetType(filter.TargetTypes, event.TargetType) &&
		(filter.TargetID == "" || filter.TargetID == event.TargetID) && (filter.RequestID == "" || filter.RequestID == event.RequestID) &&
		(filter.ReasonCode == "" || filter.ReasonCode == event.ReasonCode) &&
		(filter.OccurredFrom.IsZero() || !event.OccurredAt.Before(filter.OccurredFrom)) &&
		(filter.OccurredTo.IsZero() || !event.OccurredAt.After(filter.OccurredTo))
}

func redactedEvent(signed audit.SignedEvent) (Event, error) {
	snapshot := signed.Snapshot()
	if snapshot.Event.EventID == uuid.Nil || snapshot.Event.Sequence == 0 || snapshot.Event.OccurredAt.IsZero() ||
		snapshot.Event.Actor.ID() == "" || snapshot.Event.Target.ID() == "" || !snapshot.Event.Action.Valid() || snapshot.Event.SigningKeyVersion == 0 {
		return Event{}, ErrRepositoryUnavailable
	}
	if len(snapshot.Event.DetailDigest) != sha256.Size {
		return Event{}, ErrRepositoryUnavailable
	}
	var detailDigest [sha256.Size]byte
	copy(detailDigest[:], snapshot.Event.DetailDigest)
	return Event{
		EventID: snapshot.Event.EventID, Sequence: snapshot.Event.Sequence, PreviousHash: snapshot.Event.PreviousHash, EventHash: snapshot.EventHash,
		RequestID: snapshot.Event.RequestID, OccurredAt: snapshot.Event.OccurredAt, ActorType: snapshot.Event.Actor.Type(), ActorID: snapshot.Event.Actor.ID(),
		TargetType: snapshot.Event.Target.Type(), TargetID: snapshot.Event.Target.ID(), Action: snapshot.Event.Action, ReasonCode: snapshot.Event.ReasonCode,
		DetailDigest: detailDigest, SigningKeyVersion: snapshot.Event.SigningKeyVersion,
	}, nil
}

func mapReaderError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, audit.ErrInvalidInput) {
		return ErrInvalidInput
	}
	return ErrRepositoryUnavailable
}

func canonicalTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.Round(0).UTC()
}

func validFilterText(value string, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validActions(values []audit.Action) bool {
	seen := make(map[audit.Action]struct{}, len(values))
	for _, value := range values {
		if !value.Valid() {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validActorTypes(values []audit.ActorType) bool {
	seen := make(map[audit.ActorType]struct{}, len(values))
	for _, value := range values {
		if value != audit.ActorUser && value != audit.ActorAdmin && value != audit.ActorSystem {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validTargetTypes(values []audit.TargetType) bool {
	seen := make(map[audit.TargetType]struct{}, len(values))
	for _, value := range values {
		if value != audit.TargetUser && value != audit.TargetDevice && value != audit.TargetProfileExport && value != audit.TargetAdmin && value != audit.TargetSystem {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func containsAction(values []audit.Action, candidate audit.Action) bool {
	return len(values) == 0 || contains(values, candidate)
}

func containsActorType(values []audit.ActorType, candidate audit.ActorType) bool {
	return len(values) == 0 || contains(values, candidate)
}

func containsTargetType(values []audit.TargetType, candidate audit.TargetType) bool {
	return len(values) == 0 || contains(values, candidate)
}

func contains[T comparable](values []T, candidate T) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func uuidText(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func actionValues(values []audit.Action) []int32 {
	result := make([]int32, 0, len(values))
	for _, value := range values {
		result = append(result, int32(value))
	}
	return result
}

func actorTypeValues(values []audit.ActorType) []int32 {
	result := make([]int32, 0, len(values))
	for _, value := range values {
		result = append(result, int32(value))
	}
	return result
}

func targetTypeValues(values []audit.TargetType) []int32 {
	result := make([]int32, 0, len(values))
	for _, value := range values {
		result = append(result, int32(value))
	}
	return result
}
