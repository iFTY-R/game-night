package boundarycheck

import "testing"

func TestValidateEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		edge        Edge
		wantAllowed bool
	}{
		{name: "app may compose platform", edge: Edge{From: "apps/api", To: "platform/identity"}, wantAllowed: true},
		{name: "engine may use game sdk", edge: Edge{From: "games/dice/engine/rules", To: "sdk/go/game"}, wantAllowed: true},
		{name: "client may use ui kit", edge: Edge{From: "games/dice/client", To: "packages/game-ui-kit"}, wantAllowed: true},
		{name: "client may use client sdk", edge: Edge{From: "games/dice/client", To: "sdk/ts/game-client"}, wantAllowed: true},
		{name: "client may use own subpackage", edge: Edge{From: "games/dice/client/table", To: "games/dice/client/actions"}, wantAllowed: true},
		{name: "similar external prefix remains allowed", edge: Edge{From: "games/dice/engine", To: "network/http"}, wantAllowed: true},
		{name: "game cannot import app", edge: Edge{From: "games/dice/engine", To: "apps/realtime"}},
		{name: "platform cannot import app", edge: Edge{From: "platform/room", To: "apps/api"}},
		{name: "platform cannot import game", edge: Edge{From: "platform/game-runtime", To: "games/dice/engine"}},
		{name: "engine cannot import persistence", edge: Edge{From: "games/dice/engine", To: "platform/persistence"}},
		{name: "engine cannot import infra", edge: Edge{From: "games/dice/engine", To: "infra/migrations"}},
		{name: "engine cannot read environment", edge: Edge{From: "games/dice/engine", To: "os"}},
		{name: "engine cannot use network", edge: Edge{From: "games/dice/engine", To: "net/http"}},
		{name: "engine cannot use database sql", edge: Edge{From: "games/dice/engine", To: "database/sql"}},
		{name: "engine cannot use pgx", edge: Edge{From: "games/dice/engine", To: "github.com/jackc/pgx/v5"}},
		{name: "engine cannot use redis", edge: Edge{From: "games/dice/engine", To: "github.com/redis/go-redis/v9"}},
		{name: "engine cannot own randomness", edge: Edge{From: "games/dice/engine", To: "math/rand"}},
		{name: "engine cannot import another game engine", edge: Edge{From: "games/dice/engine", To: "games/cards/engine"}},
		{name: "projection cannot import party room", edge: Edge{From: "games/dice/projection", To: "platform/room"}},
		{name: "client cannot import projection", edge: Edge{From: "games/dice/client", To: "games/dice/projection"}},
		{name: "client cannot import platform", edge: Edge{From: "games/dice/client", To: "platform/identity"}},
		{name: "client cannot import another client", edge: Edge{From: "games/dice/client", To: "games/texas-holdem/client"}},
		{name: "client cannot import arbitrary shared package", edge: Edge{From: "games/dice/client", To: "packages/platform-ui"}},
		{name: "theme cannot import rules", edge: Edge{From: "games/dice/themes/neon", To: "games/dice/engine"}},
		{name: "sdk cannot import concrete game", edge: Edge{From: "sdk/go/game", To: "games/dice/engine"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			violations := ValidateEdges([]Edge{tt.edge})
			if tt.wantAllowed && len(violations) != 0 {
				t.Fatalf("expected edge to be allowed, got %#v", violations)
			}
			if !tt.wantAllowed && len(violations) != 1 {
				t.Fatalf("expected one violation, got %#v", violations)
			}
		})
	}
}

func TestValidateEdgesNormalizesWindowsPaths(t *testing.T) {
	t.Parallel()

	violations := ValidateEdges([]Edge{{From: `platform\room`, To: `apps\api`}})
	if len(violations) != 1 {
		t.Fatalf("expected one violation, got %#v", violations)
	}
	want := Edge{From: "platform/room", To: "apps/api"}
	if violations[0].Edge != want {
		t.Fatalf("expected normalized edge %#v, got %#v", want, violations[0].Edge)
	}
}

func TestValidateEdgesAllowsSimilarPrefixes(t *testing.T) {
	t.Parallel()

	edges := []Edge{
		{From: "platform-tools/room", To: "apps/api"},
		{From: "games/dice/engine", To: "network/http"},
		{From: "games/dice/engine", To: "operating-system"},
	}
	if violations := ValidateEdges(edges); len(violations) != 0 {
		t.Fatalf("expected similar prefixes to remain allowed, got %#v", violations)
	}
}

func TestViolationDiagnostic(t *testing.T) {
	t.Parallel()

	violations := ValidateEdges([]Edge{{From: "platform/room", To: "apps/api"}})
	if len(violations) != 1 {
		t.Fatalf("expected one violation, got %#v", violations)
	}

	const wantReason = "platform modules cannot import application entrypoints"
	if violations[0].Reason != wantReason {
		t.Fatalf("expected reason %q, got %q", wantReason, violations[0].Reason)
	}
	const wantDiagnostic = "platform/room -> apps/api: platform modules cannot import application entrypoints"
	if diagnostic := violations[0].String(); diagnostic != wantDiagnostic {
		t.Fatalf("expected diagnostic %q, got %q", wantDiagnostic, diagnostic)
	}
}
