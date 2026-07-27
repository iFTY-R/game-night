package runtimeinfo

import (
	"testing"
	"time"

	"github.com/iFTY-R/game-night/platform/admin/operations"
)

func TestInfoFreezesValidatedIdentityAndBuildFallback(t *testing.T) {
	startedAt := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	info, err := New(operations.ServiceAPI, "api-local-1", "", startedAt)
	if err != nil || info.BuildVersion == "" || !info.StartedAt.Equal(startedAt) {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if _, err = New(operations.ServiceKind("scheduler"), "api-local-1", "v1", startedAt); err == nil {
		t.Fatal("unknown service kind was accepted")
	}
}
