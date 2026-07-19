package room

import (
	"context"

	"github.com/google/uuid"
)

// Repository persists a complete room aggregate and owns atomic membership replacement under CAS.
type Repository interface {
	Create(context.Context, Room) (Room, error)
	GetByID(context.Context, uuid.UUID) (Room, error)
	GetByCode(context.Context, string) (Room, error)
	UpdateCAS(context.Context, Room, Room) (Room, error)
}

// ValidateRoomCode exposes the canonical code grammar to transport and persistence adapters.
func ValidateRoomCode(value string) error {
	return validateRoomCode(value)
}
