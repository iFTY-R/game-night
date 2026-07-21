package engine

import (
	"errors"
	"fmt"

	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// ErrorCode is the stable game-owned rejection classification exposed through the module adapter.
type ErrorCode string

const (
	CodeInvalidConfig         ErrorCode = "invalid_config"
	CodeInvalidParticipants   ErrorCode = "invalid_participants"
	CodeInvalidState          ErrorCode = "invalid_state"
	CodeInvalidAction         ErrorCode = "invalid_action"
	CodeWrongPhase            ErrorCode = "wrong_phase"
	CodeNotCurrentTarget      ErrorCode = "not_current_target"
	CodeRerollLimitReached    ErrorCode = "reroll_limit_reached"
	CodeTimerMismatch         ErrorCode = "timer_mismatch"
	CodeTimerNotDue           ErrorCode = "timer_not_due"
	CodeActionExpired         ErrorCode = "action_expired"
	CodeParticipantInactive   ErrorCode = "participant_inactive"
	CodePenaltyOverflow       ErrorCode = "penalty_overflow"
	CodeRoundOverflow         ErrorCode = "round_overflow"
	CodeSeedInvalid           ErrorCode = "seed_invalid"
	CodeSessionFinished       ErrorCode = "session_finished"
	CodeMalformedPayload      ErrorCode = "malformed_payload"
	CodeUnsupportedMigration  ErrorCode = "unsupported_migration"
	CodeProjectionUnavailable ErrorCode = "projection_unavailable"
)

// RuleError keeps stable behavior separate from human-readable diagnostic detail.
type RuleError struct {
	Code   ErrorCode
	Detail string
}

func (err *RuleError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", err.Code, err.Detail)
}

// Unwrap lets platform transports safely map expected rule rejections to invalid game contracts.
func (err *RuleError) Unwrap() error { return game.ErrInvalidContract }

func ruleError(code ErrorCode, detail string) error { return &RuleError{Code: code, Detail: detail} }

// ErrorCodeOf extracts a stable classification without parsing error text.
func ErrorCodeOf(err error) ErrorCode {
	var typed *RuleError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code
	}
	return ""
}
