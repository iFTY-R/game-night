package internalgame

import (
	"testing"

	"connectrpc.com/connect"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestClassifyErrorMapsGameContractRejection(t *testing.T) {
	descriptor := classifyError(game.ErrInvalidContract)
	if descriptor.connectCode != connect.CodeInvalidArgument || descriptor.messageKey != "request.invalid" {
		t.Fatalf("descriptor = %+v", descriptor)
	}
}
