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
		{name: "dice engine may use shared dice sdk", edge: Edge{From: "games/liars-dice/engine/rules", To: "sdk/go/game/dice"}, wantAllowed: true},
		{name: "789 engine may use own engine package", edge: Edge{From: "games/dice-789/engine/rules", To: "games/dice-789/engine/turns"}, wantAllowed: true},
		{name: "meet client may use own generated protocol", edge: Edge{From: "games/meet-by-chance/client", To: "games/meet-by-chance/client/generated"}, wantAllowed: true},
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

func TestValidateEdgesEnforcesPlatformAdapterBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		edge        Edge
		wantAllowed bool
		wantReason  string
	}{
		{
			name:       "identity cannot import persistence implementation",
			edge:       Edge{From: "platform/identity", To: "platform/persistence/postgres"},
			wantReason: "platform domains cannot import persistence adapters",
		},
		{
			name:       "identity cannot import pgx",
			edge:       Edge{From: "platform/identity", To: "github.com/jackc/pgx/v5/pgxpool"},
			wantReason: "PostgreSQL dependencies are only allowed in platform/persistence/postgres",
		},
		{
			name:       "admin cannot import redis",
			edge:       Edge{From: "platform/admin", To: "github.com/redis/go-redis/v9"},
			wantReason: "Redis dependencies are only allowed in platform/persistence/redis",
		},
		{
			name:       "profile cannot import object storage sdk",
			edge:       Edge{From: "platform/profile", To: "github.com/aws/aws-sdk-go-v2/service/s3"},
			wantReason: "object storage dependencies are only allowed in platform/persistence/objectstorage",
		},
		{
			name:       "audit cannot import http transport",
			edge:       Edge{From: "platform/audit", To: "net/http"},
			wantReason: "HTTP dependencies are only allowed in platform/persistence/objectstorage",
		},
		{
			name:       "postgres adapter cannot import redis",
			edge:       Edge{From: "platform/persistence/postgres", To: "github.com/redis/go-redis/v9"},
			wantReason: "Redis dependencies are only allowed in platform/persistence/redis",
		},
		{
			name:        "postgres adapter may import pgx",
			edge:        Edge{From: "platform/persistence/postgres", To: "github.com/jackc/pgx/v5/pgxpool"},
			wantAllowed: true,
		},
		{
			name:        "redis adapter may import go redis",
			edge:        Edge{From: "platform/persistence/redis", To: "github.com/redis/go-redis/v9"},
			wantAllowed: true,
		},
		{
			name:        "object storage adapter may import aws sdk",
			edge:        Edge{From: "platform/persistence/objectstorage/s3", To: "github.com/aws/aws-sdk-go-v2/service/s3"},
			wantAllowed: true,
		},
		{
			name:        "object storage adapter may configure http client",
			edge:        Edge{From: "platform/persistence/objectstorage/s3", To: "net/http"},
			wantAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			violations := ValidateEdges([]Edge{tt.edge})
			if tt.wantAllowed {
				if len(violations) != 0 {
					t.Fatalf("expected edge to be allowed, got %#v", violations)
				}
				return
			}
			if len(violations) != 1 {
				t.Fatalf("expected one violation, got %#v", violations)
			}
			if violations[0].Reason != tt.wantReason {
				t.Fatalf("expected reason %q, got %q", tt.wantReason, violations[0].Reason)
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
