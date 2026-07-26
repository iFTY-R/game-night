package room

import (
	"context"

	"github.com/google/uuid"
)

type QueryRepository interface {
	ListRooms(context.Context, RoomListQuery) ([]RoomSummary, error)
	GetRoom(context.Context, uuid.UUID) (RoomDetail, error)
	ListGames(context.Context, GameListQuery) ([]GameSummary, error)
	GetGame(context.Context, uuid.UUID) (GameDetail, error)
}

type RepairRepository interface {
	CreateRepairOperation(context.Context, CreateRepairOperationCommand) (RepairOperation, error)
	GetRepairOperation(context.Context, uuid.UUID) (RepairOperation, error)
	ExpireRepairOperation(context.Context, uuid.UUID, uint64) (RepairOperation, error)
	CompleteRepairOperation(context.Context, CompleteRepairOperationCommand) (RepairOperation, error)
}
