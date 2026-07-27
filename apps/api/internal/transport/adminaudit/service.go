// Package adminaudit adapts the authenticated audit-management RPC to the redacted audit query domain.
package adminaudit

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminauth"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	auditv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/audit/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	domain "github.com/iFTY-R/game-night/platform/admin/auditlog"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler maps the server-authenticated AdminAuditService contract to the redacted audit query service.
type Handler struct {
	adminv1connect.UnimplementedAdminAuditServiceHandler

	service *domain.Service
	clock   clock.Clock
}

// NewService requires explicit sampled time so every response can identify its read boundary.
func NewService(service *domain.Service, source clock.Clock) (*Handler, error) {
	if service == nil || source == nil {
		return nil, domain.ErrInvalidInput
	}
	return &Handler{service: service, clock: source}, nil
}

// ListAuditEvents returns redacted chain metadata and its verification outcome to an actor with audit.read permission.
func (handler *Handler) ListAuditEvents(ctx context.Context, request *connect.Request[adminv1.ListAuditEventsRequest]) (*connect.Response[adminv1.ListAuditEventsResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	input, err := listInputFromWire(request.Msg)
	if err != nil {
		return nil, err
	}
	page, err := handler.service.ListEvents(ctx, actor, input)
	if err != nil {
		return nil, err
	}
	events := make([]*adminv1.AdminAuditEvent, 0, len(page.Events))
	for _, event := range page.Events {
		events = append(events, auditEventToWire(event))
	}
	return connect.NewResponse(&adminv1.ListAuditEventsResponse{
		Events: events,
		Page: &adminv1.AdminPageInfo{
			NextPageToken: page.NextPageToken,
			SampledAt:     timestampOrNil(page.SampledAt),
		},
		ChainHead: &adminv1.AdminAuditChainHead{
			Sequence:  page.ChainHead.Sequence(),
			EventHash: page.ChainHead.Hash().Hex(),
			UpdatedAt: timestampOrNil(page.ChainHead.UpdatedAt()),
		},
		ScannedEvents: page.ScannedEvents,
	}), nil
}

func requestActor(ctx context.Context) (admin.ActorContext, error) {
	actor, ok := adminauth.ActorFromContext(ctx)
	if !ok {
		return admin.ActorContext{}, admin.ErrAuthentication
	}
	return actor, nil
}

func listInputFromWire(request *adminv1.ListAuditEventsRequest) (domain.ListInput, error) {
	if request == nil {
		return domain.ListInput{}, domain.ErrInvalidInput
	}
	filter, err := filterFromWire(request.GetFilter())
	if err != nil {
		return domain.ListInput{}, err
	}
	return domain.ListInput{Filter: filter, PageSize: request.GetPageSize(), PageToken: request.GetPageToken()}, nil
}

func filterFromWire(value *adminv1.AdminAuditFilter) (domain.Filter, error) {
	if value == nil {
		return domain.Filter{}, nil
	}
	eventID, err := optionalUUID(value.GetEventId())
	if err != nil {
		return domain.Filter{}, err
	}
	actions := make([]audit.Action, 0, len(value.GetActions()))
	for _, action := range value.GetActions() {
		candidate := audit.Action(action)
		if !candidate.Valid() {
			return domain.Filter{}, domain.ErrInvalidInput
		}
		actions = append(actions, candidate)
	}
	actorTypes, err := actorTypesFromWire(value.GetActorTypes())
	if err != nil {
		return domain.Filter{}, err
	}
	targetTypes, err := targetTypesFromWire(value.GetTargetTypes())
	if err != nil {
		return domain.Filter{}, err
	}
	occurredFrom, err := timeFromTimestamp(value.GetOccurredFrom())
	if err != nil {
		return domain.Filter{}, err
	}
	occurredTo, err := timeFromTimestamp(value.GetOccurredTo())
	if err != nil {
		return domain.Filter{}, err
	}
	return domain.Filter{
		EventID: eventID, Actions: actions, ActorTypes: actorTypes, ActorID: value.GetActorId(), TargetTypes: targetTypes,
		TargetID: value.GetTargetId(), RequestID: value.GetRequestId(), ReasonCode: value.GetReasonCode(),
		OccurredFrom: occurredFrom, OccurredTo: occurredTo,
	}, nil
}

func actorTypesFromWire(values []auditv1.AuditActorType) ([]audit.ActorType, error) {
	result := make([]audit.ActorType, 0, len(values))
	for _, value := range values {
		candidate := audit.ActorType(value)
		if candidate != audit.ActorUser && candidate != audit.ActorAdmin && candidate != audit.ActorSystem {
			return nil, domain.ErrInvalidInput
		}
		result = append(result, candidate)
	}
	return result, nil
}

func targetTypesFromWire(values []auditv1.AuditTargetType) ([]audit.TargetType, error) {
	result := make([]audit.TargetType, 0, len(values))
	for _, value := range values {
		candidate := audit.TargetType(value)
		if candidate != audit.TargetUser && candidate != audit.TargetDevice && candidate != audit.TargetProfileExport && candidate != audit.TargetAdmin && candidate != audit.TargetSystem {
			return nil, domain.ErrInvalidInput
		}
		result = append(result, candidate)
	}
	return result, nil
}

func auditEventToWire(event domain.Event) *adminv1.AdminAuditEvent {
	return &adminv1.AdminAuditEvent{
		EventId: event.EventID.String(), Sequence: event.Sequence, PreviousHash: event.PreviousHash.Hex(), EventHash: event.EventHash.Hex(),
		RequestId: event.RequestID, OccurredAt: timestampOrNil(event.OccurredAt),
		Actor:  &auditv1.AuditActor{Type: auditv1.AuditActorType(event.ActorType), ActorId: event.ActorID},
		Target: &auditv1.AuditTarget{Type: auditv1.AuditTargetType(event.TargetType), TargetId: event.TargetID},
		Action: auditv1.AuditAction(event.Action), ReasonCode: event.ReasonCode, DetailDigest: hex.EncodeToString(event.DetailDigest[:]),
		SigningKeyVersion: event.SigningKeyVersion, Verified: event.Verified,
	}
}

func optionalUUID(value string) (uuid.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return uuid.Nil, nil
	}
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil || parsed.String() != strings.TrimSpace(value) {
		return uuid.Nil, domain.ErrInvalidInput
	}
	return parsed, nil
}

func timeFromTimestamp(value *timestamppb.Timestamp) (time.Time, error) {
	if value == nil {
		return time.Time{}, nil
	}
	if err := value.CheckValid(); err != nil {
		return time.Time{}, domain.ErrInvalidInput
	}
	return value.AsTime().Round(0).UTC(), nil
}

func timestampOrNil(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}
