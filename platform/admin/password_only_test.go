package admin

import (
	"errors"
	"testing"
)

func TestPasswordLoginSessionStateKeepsMFASecureByDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		status            AccountStatus
		allowPasswordOnly bool
		wantKind          SessionKind
		wantStep          NextStep
		wantErr           error
	}{
		{name: "setup still changes password", status: AccountStatusSetupRequired, allowPasswordOnly: true, wantKind: SessionKindSetupPasswordPending, wantStep: NextStepChangePassword},
		{name: "active requires MFA by default", status: AccountStatusActive, wantKind: SessionKindMFAPending, wantStep: NextStepVerifyMFA},
		{name: "active allows password only explicitly", status: AccountStatusActive, allowPasswordOnly: true, wantKind: SessionKindFull, wantStep: NextStepAuthenticated},
		{name: "recovery can resume with password only", status: AccountStatusRecoveryPending, allowPasswordOnly: true, wantKind: SessionKindFull, wantStep: NextStepAuthenticated},
		{name: "recovery remains unavailable with MFA", status: AccountStatusRecoveryPending, wantErr: ErrUnavailable},
		{name: "bootstrap cannot log in", status: AccountStatusBootstrapPending, allowPasswordOnly: true, wantErr: ErrUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			kind, step, err := passwordLoginSessionState(test.status, test.allowPasswordOnly)
			if !errors.Is(err, test.wantErr) || kind != test.wantKind || step != test.wantStep {
				t.Fatalf("state = (%q, %q, %v), want (%q, %q, %v)", kind, step, err, test.wantKind, test.wantStep, test.wantErr)
			}
		})
	}
}

func TestPasswordChangeSessionStateFollowsMFAPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		initial           bool
		allowPasswordOnly bool
		wantKind          SessionKind
		wantStep          NextStep
	}{
		{name: "initial requires enrollment by default", initial: true, wantKind: SessionKindTOTPEnrollmentPending, wantStep: NextStepEnrollTOTP},
		{name: "rotation requires rebind by default", wantKind: SessionKindRecoveryPending, wantStep: NextStepRebindTOTP},
		{name: "initial password only", initial: true, allowPasswordOnly: true, wantKind: SessionKindFull, wantStep: NextStepAuthenticated},
		{name: "rotation password only", allowPasswordOnly: true, wantKind: SessionKindFull, wantStep: NextStepAuthenticated},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			kind, step := passwordChangeSessionState(test.initial, test.allowPasswordOnly)
			if kind != test.wantKind || step != test.wantStep {
				t.Fatalf("state = (%q, %q), want (%q, %q)", kind, step, test.wantKind, test.wantStep)
			}
		})
	}
}
