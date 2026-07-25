package game

import (
	"testing"

	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestSuspendedSessionWireCarriesPauseTimeAndMasksActions(t *testing.T) {
	t.Parallel()

	fixture := newGameTransportFixture(t, false)
	snapshot := fixture.session.Snapshot()
	snapshot.Status = gameruntime.StatusSuspended
	snapshot.SuspendedAt = snapshot.UpdatedAt
	suspended, err := gameruntime.RestoreSession(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	summary := sessionWire(suspended)
	if summary.GetSuspendedAt() == nil || !summary.GetSuspendedAt().AsTime().Equal(snapshot.SuspendedAt) {
		t.Fatalf("suspended_at=%v want=%v", summary.GetSuspendedAt(), snapshot.SuspendedAt)
	}
	projection := projectionWire(suspended, gameSDK.ViewerPlayer, fixture.runtime.projection, true)
	if len(projection.GetAllowedActions()) != 0 {
		t.Fatalf("suspended projection actions=%v", projection.GetAllowedActions())
	}
}
