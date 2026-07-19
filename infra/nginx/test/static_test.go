package nginxtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateUsesExactServiceAllowlistAndNoDefaultProxy(t *testing.T) {
	template := readRepositoryFile(t, "..", "templates", "game-night.conf.template")
	for _, path := range []string{
		"location ^~ /platform.identity.v1.IdentityService/",
		"location ^~ /platform.room.v1.RoomService/",
		"location ^~ /platform.admin.v1.AdminAuthService/",
		"location ^~ /platform.admin.v1.AdminIdentityService/",
	} {
		if strings.Count(template, path) != 1 {
			t.Fatalf("service allowlist entry %q must appear exactly once", path)
		}
	}
	if strings.Count(template, "location / {\n        return 404;\n    }") != 2 ||
		!strings.Contains(template, "listen 8443 ssl default_server;") ||
		!strings.Contains(template, "server_name _;") {
		t.Fatal("unknown hosts or paths do not terminate at an explicit rejection")
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
	if strings.Contains(userServer, "/platform.admin.v1.") || strings.Contains(userServer, "game_night_admin_api") ||
		strings.Contains(adminServer, "/platform.identity.v1.") || strings.Contains(adminServer, "/platform.room.v1.") ||
		strings.Contains(adminServer, "game_night_identity_api") {
		t.Fatal("a service path or upstream crossed the user/admin virtual-host boundary")
	}
}

func TestTemplateOverwritesForwardingHeadersAndDisablesCaching(t *testing.T) {
	template := readRepositoryFile(t, "..", "templates", "game-night.conf.template")
	for _, directive := range []string{
		"proxy_set_header Forwarded \"for=\\\"$remote_addr\\\";proto=https;host=\\\"$host\\\"\";",
		"proxy_set_header X-Forwarded-For $remote_addr;",
		"proxy_set_header X-Forwarded-Proto https;",
		"proxy_set_header X-Forwarded-Host $host;",
		"proxy_set_header X-Forwarded-Port 443;",
		"proxy_set_header X-Real-IP $remote_addr;",
		"proxy_set_header Connection \"\";",
		"proxy_hide_header Cache-Control;",
		"add_header Cache-Control \"no-store\" always;",
		"add_header Pragma \"no-cache\" always;",
		"proxy_cache off;",
	} {
		if strings.Count(template, directive) != 4 {
			t.Fatalf("security directive %q must cover all four allowed service locations", directive)
		}
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
