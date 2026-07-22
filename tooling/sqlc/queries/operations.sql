-- name: RunExpiryCleanup :one
SELECT run_expiry_cleanup();

-- name: CloseExpiredPartyRooms :one
SELECT close_expired_party_rooms(sqlc.arg(room_idle_seconds));
