// Package boundarycheck enforces the repository dependency directions defined by the platform spec.
package boundarycheck

import (
	"fmt"
	"path"
	"strings"
)

// Edge represents a direct dependency from a repository package to an internal path or external import.
type Edge struct {
	From string
	To   string
}

// Violation explains which dependency edge crossed a forbidden ownership boundary.
type Violation struct {
	Edge   Edge
	Reason string
}

// ValidateEdges returns every forbidden edge so CI can report all boundary failures in one run.
func ValidateEdges(edges []Edge) []Violation {
	violations := make([]Violation, 0)
	for _, edge := range edges {
		normalized := Edge{From: normalize(edge.From), To: normalize(edge.To)}
		if reason, forbidden := forbiddenReason(normalized); forbidden {
			violations = append(violations, Violation{Edge: normalized, Reason: reason})
		}
	}
	return violations
}

func forbiddenReason(edge Edge) (string, bool) {
	// Games and platform modules must remain below application composition boundaries.
	if under(edge.From, "games") && under(edge.To, "apps") {
		return "games cannot import application entrypoints", true
	}
	if under(edge.From, "platform") && under(edge.To, "apps") {
		return "platform modules cannot import application entrypoints", true
	}
	if under(edge.From, "platform") && under(edge.To, "games") {
		return "platform modules cannot import concrete games", true
	}
	if under(edge.From, "platform") && !under(edge.From, "platform/persistence") && under(edge.To, "platform/persistence") {
		return "platform domains cannot import persistence adapters", true
	}
	if reason, forbidden := forbiddenPlatformInfrastructureImport(edge); forbidden {
		return reason, true
	}

	// Stable SDK contracts cannot depend on concrete application or domain implementations.
	if under(edge.From, "sdk") && (under(edge.To, "apps") || under(edge.To, "platform") || under(edge.To, "games")) {
		return "SDK packages cannot import applications, platform implementations, or concrete games", true
	}
	if under(edge.From, "games") && under(edge.To, "platform/room") {
		return "games cannot import PartyRoom internals", true
	}

	// Authoritative engines stay pure and may only compose their own rules with the server SDK.
	if gameArea(edge.From, "engine") {
		if reason, forbidden := forbiddenEngineImport(edge.To); forbidden {
			return reason, true
		}
		if internalEngineDependencyForbidden(edge.From, edge.To) {
			return "game engines may only import their own engine packages and sdk/go/game", true
		}
	}

	// Clients stay inside their game module except for the two approved shared frontend contracts.
	if gameArea(edge.From, "client") && internalClientDependencyForbidden(edge.From, edge.To) {
		return "game clients may only import their own client packages, sdk/ts/game-client, and packages/game-ui-kit", true
	}

	// Themes may change presentation but cannot observe or modify authoritative rules and state.
	if gameArea(edge.From, "themes") && (gameArea(edge.To, "engine") || gameArea(edge.To, "projection") || under(edge.To, "platform/game-runtime")) {
		return "themes cannot import game rules or authoritative runtime state", true
	}
	return "", false
}

// forbiddenPlatformInfrastructureImport keeps each managed client inside its single owning adapter.
func forbiddenPlatformInfrastructureImport(edge Edge) (string, bool) {
	if !under(edge.From, "platform") {
		return "", false
	}
	adapterRoot, reason, restricted := platformInfrastructureBoundary(edge.To)
	if !restricted || under(edge.From, adapterRoot) {
		return "", false
	}
	return reason, true
}

// platformInfrastructureBoundary is shared by discovery and validation so CI records every external import governed here.
func platformInfrastructureBoundary(importPath string) (adapterRoot, reason string, restricted bool) {
	switch {
	case under(importPath, "github.com/jackc/pgx"), under(importPath, "database/sql"):
		return "platform/persistence/postgres", "PostgreSQL dependencies are only allowed in platform/persistence/postgres", true
	case under(importPath, "github.com/redis/go-redis"), under(importPath, "github.com/go-redis"):
		return "platform/persistence/redis", "Redis dependencies are only allowed in platform/persistence/redis", true
	case under(importPath, "github.com/aws/aws-sdk-go-v2"):
		return "platform/persistence/objectstorage", "object storage dependencies are only allowed in platform/persistence/objectstorage", true
	case under(importPath, "net/http"):
		return "platform/persistence/objectstorage", "HTTP dependencies are only allowed in platform/persistence/objectstorage", true
	default:
		return "", "", false
	}
}

func forbiddenEngineImport(importPath string) (string, bool) {
	for _, prefix := range []string{
		"os",
		"io/fs",
		"net",
		"database/sql",
		"crypto/rand",
		"math/rand",
		"time",
		"github.com/jackc/pgx",
		"github.com/redis/go-redis",
		"github.com/go-redis",
		"gorm.io",
	} {
		if under(importPath, prefix) {
			return "game engines cannot own IO, clocks, randomness, database, or Redis access", true
		}
	}
	return "", false
}

func internalEngineDependencyForbidden(from, to string) bool {
	if !isRepositoryRoot(to) || under(to, "sdk/go/game") {
		return false
	}
	fromParts := strings.Split(from, "/")
	toParts := strings.Split(to, "/")
	return len(fromParts) < 2 || len(toParts) < 3 || toParts[0] != "games" || toParts[1] != fromParts[1] || toParts[2] != "engine"
}

func internalClientDependencyForbidden(from, to string) bool {
	if !isRepositoryRoot(to) || under(to, "sdk/ts/game-client") || under(to, "packages/game-ui-kit") {
		return false
	}
	fromParts := strings.Split(from, "/")
	toParts := strings.Split(to, "/")
	return len(fromParts) < 2 || len(toParts) < 3 || toParts[0] != "games" || toParts[1] != fromParts[1] || toParts[2] != "client"
}

func gameArea(value, area string) bool {
	parts := strings.Split(normalize(value), "/")
	return len(parts) >= 3 && parts[0] == "games" && parts[2] == area
}

func isRepositoryRoot(value string) bool {
	for _, root := range []string{"apps", "platform", "sdk", "packages", "games", "contracts", "infra", "tooling"} {
		if under(value, root) {
			return true
		}
	}
	return false
}

func under(value, prefix string) bool {
	return value == prefix || strings.HasPrefix(value, prefix+"/")
}

func normalize(value string) string {
	forwardSlashes := strings.ReplaceAll(value, "\\", "/")
	return strings.TrimPrefix(path.Clean(forwardSlashes), "./")
}

// String formats one violation for stable local and CI diagnostics.
func (v Violation) String() string {
	return fmt.Sprintf("%s -> %s: %s", v.Edge.From, v.Edge.To, v.Reason)
}
