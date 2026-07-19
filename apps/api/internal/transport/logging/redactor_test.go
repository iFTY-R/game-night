package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestJSONHandlerRecursivelyRedactsSensitiveAttributes(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewJSONHandler(&output, slog.LevelInfo))
	logger.LogAttrs(context.Background(), slog.LevelInfo, "request completed",
		slog.String("operation", "identity.bootstrap"),
		slog.String("cookie", "device-cookie-secret"),
		slog.Group("request",
			slog.String("username", "Alice"),
			slog.String("result", "ok"),
			slog.Group("credentials", slog.String("csrf_token", "csrf-secret")),
		),
	)
	logged := output.String()
	for _, secret := range []string{"device-cookie-secret", "Alice", "csrf-secret"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log leaked %q: %s", secret, logged)
		}
	}
	for _, safe := range []string{"identity.bootstrap", "request completed", "ok", redactedValue} {
		if !strings.Contains(logged, safe) {
			t.Fatalf("log omitted %q: %s", safe, logged)
		}
	}
}

func TestReplaceAttrRedactsKeyVariants(t *testing.T) {
	for _, key := range []string{
		"Authorization", "Set-Cookie", "recoveryCode", "real_name_ciphertext", "realName", "request_body",
		"wrappedDataKey", "deviceCredential", "clientIp", "remote_addr",
	} {
		attribute := ReplaceAttr(nil, slog.String(key, "sensitive"))
		if attribute.Value.String() != redactedValue {
			t.Fatalf("attribute %q was not redacted: %+v", key, attribute)
		}
	}
}

func TestReplaceAttrKeepsReviewedOperationalKeys(t *testing.T) {
	for _, key := range []string{"operation", "result", "duration_ms", "request_id", "actor_id", "target_id"} {
		attribute := ReplaceAttr(nil, slog.String(key, "safe"))
		if attribute.Value.String() != "safe" {
			t.Fatalf("attribute %q was unexpectedly redacted: %+v", key, attribute)
		}
	}
}
