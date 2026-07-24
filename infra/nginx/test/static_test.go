package nginxtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateUsesDedicatedEdgeSPAUpstreamAndHostIsolation(t *testing.T) {
	template := readRepositoryFile(t, "..", "templates", "game-night.conf.template")
	for _, value := range []string{
		"upstream game_night_edge_spa {",
		"${GAME_NIGHT_EDGE_UPSTREAM}",
		"map $request_method $game_night_edge_allows_spa {",
		"GET 1;",
		"HEAD 1;",
		"listen 8443 ssl default_server;",
		"server_name _;",
	} {
		if !strings.Contains(template, value) {
			t.Fatalf("template is missing %q", value)
		}
	}
	if strings.Contains(template, "proxy_pass $request_uri") || strings.Contains(template, "proxy_intercept_errors off") {
		t.Fatal("template contains a fallback proxy path")
	}

	userStart := strings.Index(template, "server_name ${GAME_NIGHT_USER_HOST};")
	adminStart := strings.Index(template, "server_name ${GAME_NIGHT_ADMIN_HOST};")
	if userStart < 0 || adminStart <= userStart {
		t.Fatal("user and admin virtual servers are missing or out of order")
	}
	userServer := template[userStart:adminStart]
	adminServer := template[adminStart:]

	for _, value := range []string{
		"location / {",
		"proxy_pass http://game_night_edge_spa;",
		"if ($game_night_edge_allows_spa = 0) {",
		"location ^~ /platform.admin.v1. {",
		"location ^~ /readyz {",
		"location ^~ /platform {",
		"location ^~ /realtime {",
	} {
		if !strings.Contains(userServer, value) {
			t.Fatalf("user server is missing %q", value)
		}
	}
	for _, value := range []string{
		"location / {",
		"proxy_pass http://game_night_edge_spa;",
		"if ($game_night_edge_allows_spa = 0) {",
		"location ^~ /readyz {",
		"location ^~ /platform.identity.v1. {",
		"location ^~ /platform.room.v1. {",
		"location ^~ /platform.game.v1. {",
		"location ^~ /platform {",
		"location ^~ /realtime {",
	} {
		if !strings.Contains(adminServer, value) {
			t.Fatalf("admin server is missing %q", value)
		}
	}
	if strings.Contains(adminServer, "location = /readyz {") || strings.Contains(adminServer, "location = /readyz/sensitive {") {
		t.Fatal("admin readiness paths must not be proxied")
	}
}

func TestTemplateKeepsExactServiceAllowlistAndNoStoreBoundaries(t *testing.T) {
	template := readRepositoryFile(t, "..", "templates", "game-night.conf.template")
	for _, path := range []string{
		"location ^~ /platform.identity.v1.IdentityService/",
		"location ^~ /platform.room.v1.RoomService/",
		"location ^~ /platform.game.v1.GameService/",
		"location = /realtime/game {",
		"location ^~ /platform.admin.v1.AdminAuthService/",
		"location ^~ /platform.admin.v1.AdminIdentityService/",
	} {
		if strings.Count(template, path) != 1 {
			t.Fatalf("allowlist entry %q must appear exactly once", path)
		}
	}
	for _, directive := range []string{
		"proxy_set_header Forwarded \"for=\\\"$remote_addr\\\";proto=https;host=\\\"$host\\\"\";",
		"proxy_set_header X-Forwarded-For $remote_addr;",
		"proxy_set_header X-Forwarded-Proto https;",
		"proxy_set_header X-Forwarded-Host $host;",
		"proxy_set_header X-Forwarded-Port 443;",
		"proxy_set_header X-Real-IP $remote_addr;",
		"proxy_set_header Host $host;",
	} {
		if strings.Count(template, directive) != 8 {
			t.Fatalf("forwarding directive %q must cover all eight proxied locations", directive)
		}
	}
	for _, directive := range []string{
		"proxy_hide_header Cache-Control;",
		"proxy_hide_header Pragma;",
		"add_header Cache-Control \"no-store\" always;",
		"add_header Pragma \"no-cache\" always;",
	} {
		if strings.Count(template, directive) != 6 {
			t.Fatalf("no-store directive %q must cover the six RPC and WebSocket locations", directive)
		}
	}
	if strings.Count(template, "proxy_set_header Connection \"\";") != 7 ||
		strings.Count(template, "proxy_set_header Upgrade $http_upgrade;") != 1 ||
		strings.Count(template, "proxy_set_header Connection \"upgrade\";") != 1 {
		t.Fatal("RPC, SPA, and WebSocket connection headers are not isolated")
	}
	if strings.Contains(template, "$proxy_add_x_forwarded_for") {
		t.Fatal("client X-Forwarded-For would be appended instead of replaced")
	}
}

func TestTemplateRequiresTLSAndPinnedDeploymentInputs(t *testing.T) {
	template := readRepositoryFile(t, "..", "templates", "game-night.conf.template")
	for _, value := range []string{
		"${GAME_NIGHT_IDENTITY_UPSTREAM}",
		"${GAME_NIGHT_ADMIN_UPSTREAM}",
		"${GAME_NIGHT_REALTIME_UPSTREAM}",
		"${GAME_NIGHT_EDGE_UPSTREAM}",
		"${GAME_NIGHT_USER_HOST}",
		"${GAME_NIGHT_ADMIN_HOST}",
		"ssl_protocols TLSv1.2 TLSv1.3;",
		"ssl_session_tickets off;",
		"Strict-Transport-Security \"max-age=31536000\" always;",
	} {
		if !strings.Contains(template, value) {
			t.Fatalf("template is missing %q", value)
		}
	}
	config := readRepositoryFile(t, "..", "nginx.conf")
	if strings.Contains(config, "$request_body") || strings.Contains(config, "$http_authorization") ||
		strings.Contains(config, "$http_cookie") {
		t.Fatal("global access log includes sensitive request material")
	}
}

func readRepositoryFile(t testing.TB, elements ...string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(elements...))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
