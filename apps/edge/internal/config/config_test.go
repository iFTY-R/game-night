package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUsesDefaultsAndValidatesStaticDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, staticIndexFileName), []byte("index"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(func(name string) (string, bool) {
		switch name {
		case staticDirectoryEnvironment:
			return dir, true
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
	if cfg.StaticDirectory != dir {
		t.Fatalf("static directory = %q", cfg.StaticDirectory)
	}
	if len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("trusted proxy cidrs = %v", cfg.TrustedProxyCIDRs)
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
			name: "bad-cidr",
			env: map[string]string{
				trustedProxyCIDRsEnvironment: "not-a-cidr",
			},
		},
		{
			name: "missing-index",
			env:  map[string]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.env[staticDirectoryEnvironment] = dir
			if tc.name != "missing-index" {
				if err := os.WriteFile(filepath.Join(dir, staticIndexFileName), []byte("index"), 0o600); err != nil {
					t.Fatal(err)
				}
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
