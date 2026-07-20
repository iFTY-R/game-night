package engine

import (
	"errors"
	"fmt"

	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// ErrorCode is stable engine-level classification used by the module adapter.
type ErrorCode string

const (
	CodeInvalidConfig         ErrorCode = "invalid_config"
	CodeInvalidParticipants   ErrorCode = "invalid_participants"
	CodeInvalidState          ErrorCode = "invalid_state"
	CodeInvalidAction         ErrorCode = "invalid_action"
	CodeSessionFinished       ErrorCode = "session_finished"
	CodeNotCurrentActor       ErrorCode = "not_current_actor"
	CodeNotHost               ErrorCode = "not_host"
	CodeWrongPhase            ErrorCode = "wrong_phase"
	CodeTimerMismatch         ErrorCode = "timer_mismatch"
	CodeTimerNotDue           ErrorCode = "timer_not_due"
	CodeParticipantInactive   ErrorCode = "participant_inactive"
	CodeTargetInvalid         ErrorCode = "target_invalid"
	CodePoolAmountInvalid     ErrorCode = "pool_amount_invalid"
	CodePoolOverflow          ErrorCode = "pool_overflow"
	CodePenaltyOverflow       ErrorCode = "penalty_overflow"
	CodeRoundOverflow         ErrorCode = "round_overflow"
	CodeSeedInvalid           ErrorCode = "seed_invalid"
	CodeContinueModeInvalid   ErrorCode = "continue_mode_invalid"
	CodeDropReportClosed      ErrorCode = "drop_report_closed"
	CodeActionExpired         ErrorCode = "action_expired"
	CodeMalformedPayload      ErrorCode = "malformed_payload"
	CodeUnsupportedMigration  ErrorCode = "unsupported_migration"
	CodeProjectionUnavailable ErrorCode = "projection_unavailable"
)

// RuleError carries a stable code while preserving a short diagnostic for tests and logs.
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

// Unwrap makes every malformed engine value map to the shared SDK contract error.
func (err *RuleError) Unwrap() error { return game.ErrInvalidContract }

func ruleError(code ErrorCode, message string) error { return &RuleError{Code: code, Detail: message} }

// ErrorCodeOf extracts the stable classification without requiring callers to parse text.
func ErrorCodeOf(err error) ErrorCode {
	var typed *RuleError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code
	}
	return ""
}
