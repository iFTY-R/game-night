// Package logging provides structured logging primitives that remove request secrets by construction.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// redactedValue preserves log shape while ensuring sensitive values cannot reach the handler output.
const redactedValue = "[REDACTED]"

// NewJSONHandler creates a structured handler with recursive sensitive-attribute replacement.
func NewJSONHandler(writer io.Writer, level slog.Level) slog.Handler {
	return slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level, ReplaceAttr: ReplaceAttr})
}

// ReplaceAttr redacts security and personal-data keys, including nested slog groups.
func ReplaceAttr(groups []string, attribute slog.Attr) slog.Attr {
	if sensitiveKey(attribute.Key) {
		return slog.String(attribute.Key, redactedValue)
	}
	if attribute.Value.Kind() != slog.KindGroup {
		return attribute
	}
	children := attribute.Value.Group()
	redacted := make([]slog.Attr, 0, len(children))
	for _, child := range children {
		redacted = append(redacted, ReplaceAttr(groups, child))
	}
	return slog.Attr{Key: attribute.Key, Value: slog.GroupValue(redacted...)}
}

func sensitiveKey(key string) bool {
	key = normalizedKey(key)
	if key == "" {
		return false
	}
	for _, marker := range []string{
		"authorization", "cookie", "body", "payload", "secret", "password", "token", "csrf", "totp",
		"recovery", "realname", "username", "ciphertext", "nonce", "wrappeddatakey", "credential",
	} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	switch key {
	case "ip", "clientip", "remoteip", "remoteaddr", "peeraddr", "ipaddress", "clientaddress":
		return true
	default:
		return false
	}
}

func normalizedKey(key string) string {
	return strings.Map(func(character rune) rune {
		switch character {
		case '-', '_', '.', ' ':
			return -1
		default:
			return character
		}
	}, strings.ToLower(key))
}
