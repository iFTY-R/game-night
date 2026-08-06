package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iFTY-R/game-night/apps/internal/serviceheartbeat"
)

func TestLoadUsesDefaultsAndValidatesStaticDirectories(t *testing.T) {
	userDir := writeStaticRoot(t, "USER")
	adminDir := writeStaticRoot(t, "ADMIN")
	heartbeatTarget := "http://127.0.0.1:8081" + serviceheartbeat.Path
	heartbeatToken := strings.Repeat("h", 32)
	cfg, err := Load(func(name string) (string, bool) {
		switch name {
		case userStaticDirectoryEnvironment:
			return userDir, true
		case adminStaticDirectoryEnvironment:
			return adminDir, true
		case serviceheartbeat.TargetURLEnvironment:
			return heartbeatTarget, true
		case serviceheartbeat.TokenEnvironment:
			return heartbeatToken, true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != defaultListenAddress {
		t.Fatalf("listen address = %q", cfg.ListenAddress)
	}
	if got, want := cfg.APIUpstreamURL.String(), defaultAPIUpstreamURL; got != want {
		t.Fatalf("api upstream = %q", got)
	}
	if got, want := cfg.RealtimeUpstreamURL.String(), defaultRealtimeUpstreamURL; got != want {
		t.Fatalf("realtime upstream = %q", got)
	}
	if cfg.UserStaticDirectory != userDir || cfg.AdminStaticDirectory != adminDir {
		t.Fatalf("static directories = %q / %q", cfg.UserStaticDirectory, cfg.AdminStaticDirectory)
	}
	if len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("trusted proxy cidrs = %v", cfg.TrustedProxyCIDRs)
	}
	if cfg.InstanceID != defaultInstanceID {
		t.Fatalf("instance id = %q", cfg.InstanceID)
	}
	if got, want := cfg.Heartbeat.TargetURL, heartbeatTarget; got != want {
		t.Fatalf("heartbeat target = %q, want %q", got, want)
	}
	if got, want := cfg.Heartbeat.Token, heartbeatToken; got != want {
		t.Fatalf("heartbeat token = %q, want %q", got, want)
	}
}

func TestLoadRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "bad-url",
			env: map[string]string{
				apiUpstreamURLEnvironment: "://bad",
			},
		},
		{
			name: "missing-user-index",
			env:  map[string]string{},
		},
		{
			name: "missing-admin-index",
			env:  map[string]string{},
		},
		{
			name: "bad-instance-id",
			env: map[string]string{
				instanceIDEnvironment: " edge local ",
			},
		},
		{
			name: "missing-heartbeat-target",
			env:  map[string]string{},
		},
		{
			name: "short-heartbeat-token",
			env: map[string]string{
				serviceheartbeat.TokenEnvironment: "short-token",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userDir := t.TempDir()
			adminDir := t.TempDir()
			if tc.name != "missing-user-index" {
				writeIndexFile(t, userDir, "USER")
			}
			if tc.name != "missing-admin-index" {
				writeIndexFile(t, adminDir, "ADMIN")
			}
			tc.env[userStaticDirectoryEnvironment] = userDir
			tc.env[adminStaticDirectoryEnvironment] = adminDir
			if tc.name != "missing-heartbeat-target" {
				tc.env[serviceheartbeat.TargetURLEnvironment] = "http://127.0.0.1:8081" + serviceheartbeat.Path
			}
			if tc.name != "short-heartbeat-token" {
				tc.env[serviceheartbeat.TokenEnvironment] = strings.Repeat("h", 32)
			}
			_, err := Load(func(name string) (string, bool) {
				value, ok := tc.env[name]
				return value, ok
			})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func writeStaticRoot(t testing.TB, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeIndexFile(t, dir, body)
	return dir
}

func writeIndexFile(t testing.TB, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, staticIndexFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
