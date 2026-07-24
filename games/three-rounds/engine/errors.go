package engine

import (
	"errors"
	"fmt"

	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// ErrorCode is the stable module-owned rejection surface.
type ErrorCode string

const (
	CodeInvalidConfig         ErrorCode = "invalid_config"
	CodeInvalidParticipants   ErrorCode = "invalid_participants"
	CodeInvalidState          ErrorCode = "invalid_state"
	CodeInvalidAction         ErrorCode = "invalid_action"
	CodeWrongPhase            ErrorCode = "wrong_phase"
	CodeActionExpired         ErrorCode = "action_expired"
	CodeTimerMismatch         ErrorCode = "timer_mismatch"
	CodeTimerNotDue           ErrorCode = "timer_not_due"
	CodeParticipantInactive   ErrorCode = "participant_inactive"
	CodeCardInvalid           ErrorCode = "card_invalid"
	CodeCardCountInvalid      ErrorCode = "card_count_invalid"
	CodeSelectionExists       ErrorCode = "selection_exists"
	CodeRoundMismatch         ErrorCode = "round_mismatch"
	CodeSessionFinished       ErrorCode = "session_finished"
	CodeSeedInvalid           ErrorCode = "seed_invalid"
	CodeUnsupportedMigration  ErrorCode = "unsupported_migration"
	CodeProjectionUnavailable ErrorCode = "projection_unavailable"
	CodeMalformedPayload      ErrorCode = "malformed_payload"
)

// RuleError keeps stable error codes separate from operator-facing detail.
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

// Unwrap keeps platform transports on the shared invalid-contract path.
func (err *RuleError) Unwrap() error { return game.ErrInvalidContract }

func ruleError(code ErrorCode, detail string) error { return &RuleError{Code: code, Detail: detail} }

// ErrorCodeOf extracts the stable code without parsing message text.
func ErrorCodeOf(err error) ErrorCode {
	var typed *RuleError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code
	}
	return ""
}
