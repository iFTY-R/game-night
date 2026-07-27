package internalgame

import (
	"testing"

	"connectrpc.com/connect"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestClassifyErrorMapsGameContractRejection(t *testing.T) {
	descriptor := classifyError(game.ErrInvalidContract)
	if descriptor.connectCode != connect.CodeInvalidArgument || descriptor.messageKey != "request.invalid" {
		t.Fatalf("descriptor = %+v", descriptor)
	}
}

func TestClassifyErrorMapsMaintenanceAdmissionDecisions(t *testing.T) {
	for _, test := range []struct {
		name         string
		err          error
		connectCode  connect.Code
		businessCode commonv1.BusinessErrorCode
		messageKey   string
	}{
		{
			name: "maintenance active", err: gameruntime.ErrMutationBlocked,
			connectCode: connect.CodeFailedPrecondition, messageKey: "service.maintenance.active",
		},
		{
			name: "authority unavailable", err: gameruntime.ErrMutationStateUnavailable,
			connectCode:  connect.CodeUnavailable,
			businessCode: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE,
			messageKey:   "service.temporarily_unavailable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor := classifyError(test.err)
			if descriptor.connectCode != test.connectCode || descriptor.businessCode != test.businessCode || descriptor.messageKey != test.messageKey {
				t.Fatalf("descriptor = %+v", descriptor)
			}
		})
	}
}
