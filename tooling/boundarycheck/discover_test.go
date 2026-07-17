package boundarycheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDecodeGoListEdges(t *testing.T) {
	t.Parallel()

	input := strings.NewReader(`{"ImportPath":"github.com/iFTY-R/game-night/platform/room","Imports":["github.com/iFTY-R/game-night/games/dice/engine","github.com/jackc/pgx/v5/pgxpool","context"]}
{"ImportPath":"github.com/iFTY-R/game-night/games/dice/engine","Imports":["github.com/iFTY-R/game-night/sdk/go/game","os"]}`)
	edges, err := decodeGoListEdges(input, "github.com/iFTY-R/game-night")
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 4 {
		t.Fatalf("expected two internal edges and two policy-relevant external edges, got %#v", edges)
	}
	if edges[1] != (Edge{From: "platform/room", To: "github.com/jackc/pgx/v5/pgxpool"}) {
		t.Fatalf("expected platform infrastructure import to be preserved, got %#v", edges)
	}
	if edges[3] != (Edge{From: "games/dice/engine", To: "os"}) {
		t.Fatalf("expected engine external import to be preserved, got %#v", edges)
	}
}

func TestDecodePnpmWorkspaceEdges(t *testing.T) {
	t.Parallel()

	root := filepath.Join(string(filepath.Separator), "repo")
	listed, err := json.Marshal([]pnpmListPackage{
		{Name: "@game-night/platform-ui", Path: filepath.Join(root, "packages", "platform-ui")},
		{Name: "@game-night/game-client", Path: filepath.Join(root, "sdk", "ts", "game-client")},
		{Name: "@game-night/admin", Path: filepath.Join(root, "apps", "admin")},
		{Name: "@game-night/theme-runtime", Path: filepath.Join(root, "packages", "theme-runtime")},
		{Name: "@game-night/game-ui-kit", Path: filepath.Join(root, "packages", "game-ui-kit")},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		"packages/platform-ui/package.json":   &fstest.MapFile{Data: []byte(`{"name":"@game-night/platform-ui","dependencies":{"@game-night/game-client":"workspace:*"},"devDependencies":{"@game-night/admin":"workspace:*","@game-night/game-client":"workspace:*"},"optionalDependencies":{"@game-night/theme-runtime":"workspace:*"},"peerDependencies":{"@game-night/game-ui-kit":"workspace:*"}}`)},
		"sdk/ts/game-client/package.json":     &fstest.MapFile{Data: []byte(`{"name":"@game-night/game-client"}`)},
		"apps/admin/package.json":             &fstest.MapFile{Data: []byte(`{"name":"@game-night/admin"}`)},
		"packages/theme-runtime/package.json": &fstest.MapFile{Data: []byte(`{"name":"@game-night/theme-runtime"}`)},
		"packages/game-ui-kit/package.json":   &fstest.MapFile{Data: []byte(`{"name":"@game-night/game-ui-kit"}`)},
	}
	edges, err := decodePnpmWorkspaceEdges(bytes.NewReader(listed), files, root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Edge{
		{From: "packages/platform-ui", To: "apps/admin"},
		{From: "packages/platform-ui", To: "packages/game-ui-kit"},
		{From: "packages/platform-ui", To: "packages/theme-runtime"},
		{From: "packages/platform-ui", To: "sdk/ts/game-client"},
	}
	if len(edges) != len(want) {
		t.Fatalf("expected all four dependency groups, got %#v", edges)
	}
	for index := range want {
		if edges[index] != want[index] {
			t.Fatalf("unexpected sorted workspace edges: %#v", edges)
		}
	}
}

func TestForbiddenFixtureProducesViolations(t *testing.T) {
	t.Parallel()

	root, err := RepositoryRoot("../testdata/boundaries/forbidden")
	if err != nil {
		t.Fatal(err)
	}
	edges, err := DiscoverEdges(context.Background(), root, "github.com/iFTY-R/game-night")
	if err != nil {
		t.Fatal(err)
	}
	violations := ValidateEdges(edges)
	if len(violations) < 4 {
		t.Fatalf("expected package, application, platform infrastructure, and engine IO violations, got %#v", violations)
	}
}

func TestCanonicalEdgesDeduplicatesMergedSources(t *testing.T) {
	t.Parallel()

	goEdges := []Edge{
		{From: "platform/room", To: "games/dice"},
		{From: "games/dice/engine", To: "os"},
	}
	workspaceEdges := []Edge{
		{From: "platform/room", To: "games/dice"},
		{From: "games/dice", To: "apps/realtime"},
	}
	got := canonicalEdges(append(goEdges, workspaceEdges...))
	want := []Edge{
		{From: "games/dice", To: "apps/realtime"},
		{From: "games/dice/engine", To: "os"},
		{From: "platform/room", To: "games/dice"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected merged edges to be unique, got %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("expected deterministic merged edges %#v, got %#v", want, got)
		}
	}
}

func TestDecodePnpmWorkspaceEdgesRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()

	root := filepath.Join(string(filepath.Separator), "repo")
	tests := []struct {
		name    string
		listed  []pnpmListPackage
		files   fstest.MapFS
		wantErr string
	}{
		{
			name:    "workspace outside repository",
			listed:  []pnpmListPackage{{Name: "@game-night/outside", Path: filepath.Join(root, "..", "outside")}},
			files:   fstest.MapFS{},
			wantErr: "outside repository root",
		},
		{
			name: "duplicate package name",
			listed: []pnpmListPackage{
				{Name: "@game-night/duplicate", Path: filepath.Join(root, "packages", "one")},
				{Name: "@game-night/duplicate", Path: filepath.Join(root, "packages", "two")},
			},
			files: fstest.MapFS{
				"packages/one/package.json": &fstest.MapFile{Data: []byte(`{"name":"@game-night/duplicate"}`)},
				"packages/two/package.json": &fstest.MapFile{Data: []byte(`{"name":"@game-night/duplicate"}`)},
			},
			wantErr: "duplicate workspace package name",
		},
		{
			name:   "manifest name mismatch",
			listed: []pnpmListPackage{{Name: "@game-night/listed", Path: filepath.Join(root, "packages", "mismatch")}},
			files: fstest.MapFS{
				"packages/mismatch/package.json": &fstest.MapFile{Data: []byte(`{"name":"@game-night/manifest"}`)},
			},
			wantErr: "workspace name mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listed, err := json.Marshal(tt.listed)
			if err != nil {
				t.Fatal(err)
			}
			_, err = decodePnpmWorkspaceEdges(bytes.NewReader(listed), tt.files, root)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestDiscoveryErrorsPreserveCanceledContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tests := []struct {
		name    string
		command string
		run     func(context.Context) error
	}{
		{
			name:    "go list",
			command: "go list -json ./...",
			run: func(ctx context.Context) error {
				_, err := discoverGoEdges(ctx, root, "github.com/iFTY-R/game-night")
				return err
			},
		},
		{
			name:    "pnpm list",
			command: "pnpm list --recursive --depth -1 --json",
			run: func(ctx context.Context) error {
				_, err := discoverWorkspaceEdges(ctx, root)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := tt.run(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context cancellation to be preserved, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.command) || !strings.Contains(err.Error(), root) {
				t.Fatalf("expected command and root context in error, got %v", err)
			}
		})
	}
}
