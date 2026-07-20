package engine

import (
	"errors"
	"fmt"

	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// ErrorCode is a stable machine-readable rejection reason surfaced through the game API.
type ErrorCode string

const (
	CodeInvalidConfig         ErrorCode = "invalid_config"
	CodeInvalidParticipants   ErrorCode = "invalid_participants"
	CodeInvalidState          ErrorCode = "invalid_state"
	CodeMalformedPayload      ErrorCode = "malformed_payload"
	CodeNotCurrentActor       ErrorCode = "not_current_actor"
	CodeActionExpired         ErrorCode = "action_expired"
	CodeBidRequired           ErrorCode = "bid_required"
	CodeBidTooLow             ErrorCode = "bid_too_low"
	CodeBidNotHigher          ErrorCode = "bid_not_higher"
	CodeBidQuantityOverflow   ErrorCode = "bid_quantity_overflow"
	CodeInvalidFace           ErrorCode = "invalid_face"
	CodeInvalidBidMode        ErrorCode = "invalid_bid_mode"
	CodeFaceOneMustBeStrict   ErrorCode = "face_one_must_be_strict"
	CodeStrictDisabled        ErrorCode = "strict_disabled"
	CodeFlyingDisabled        ErrorCode = "flying_disabled"
	CodeParticipantInactive   ErrorCode = "participant_inactive"
	CodeTimerMismatch         ErrorCode = "timer_mismatch"
	CodeTimerNotDue           ErrorCode = "timer_not_due"
	CodeSessionFinished       ErrorCode = "session_finished"
	CodePenaltyOverflow       ErrorCode = "penalty_overflow"
	CodeRoundOverflow         ErrorCode = "round_overflow"
	CodeUnsupportedMigration  ErrorCode = "unsupported_migration"
	CodeProjectionUnavailable ErrorCode = "projection_unavailable"
)

// RuleError retains a stable code while keeping details suitable for server logs.
type RuleError struct {
	Code   ErrorCode
	Detail string
}

func (err *RuleError) Error() string {
	if err == nil {
		return ""
	}
	if err.Detail == "" {
		return string(err.Code)
	}
	return fmt.Sprintf("%s: %s", err.Code, err.Detail)
}

// Unwrap classifies deterministic rule and codec rejections as invalid game
// contract input while preserving the concrete ErrorCode for trusted callers.
func (err *RuleError) Unwrap() error {
	if err == nil {
		return nil
	}
	return game.ErrInvalidContract
}

func ruleError(code ErrorCode, detail string) error {
	return &RuleError{Code: code, Detail: detail}
}

// ErrorCodeOf extracts an engine rejection without requiring callers to parse error text.
func ErrorCodeOf(err error) ErrorCode {
	var typed *RuleError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code
	}
	return ""
}
