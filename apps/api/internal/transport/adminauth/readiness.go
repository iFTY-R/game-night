package adminauth

import (
	"github.com/iFTY-R/game-night/apps/api/internal/server"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
)

func runtimeReadinessState(snapshot server.RuntimeReadinessSnapshot) *adminv1.RuntimeReadinessState {
	return &adminv1.RuntimeReadinessState{
		Mode:       snapshot.Mode,
		Ready:      snapshot.Ready,
		Components: snapshot.Components,
	}
}
