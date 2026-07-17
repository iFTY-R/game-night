package boundarycheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type goListPackage struct {
	ImportPath string
	Imports    []string
}

type workspaceManifest struct {
	Name                 string            `json:"name"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

type pnpmListPackage struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// DiscoverEdges collects internal package edges and pure-engine external imports from structured metadata.
func DiscoverEdges(ctx context.Context, root, modulePath string) ([]Edge, error) {
	goEdges, err := discoverGoEdges(ctx, root, modulePath)
	if err != nil {
		return nil, err
	}
	workspaceEdges, err := discoverWorkspaceEdges(ctx, root)
	if err != nil {
		return nil, err
	}
	return canonicalEdges(append(goEdges, workspaceEdges...)), nil
}

func discoverGoEdges(ctx context.Context, root, modulePath string) ([]Edge, error) {
	const command = "go list -json ./..."
	// Structured command output avoids coupling boundary enforcement to human-readable CLI formatting.
	cmd := exec.CommandContext(ctx, "go", "list", "-json", "./...")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return nil, commandError(ctx, command, root, err)
	}
	edges, err := decodeGoListEdges(strings.NewReader(string(output)), modulePath)
	if err != nil {
		return nil, fmt.Errorf("process output from %s in %s: %w", command, root, err)
	}
	return edges, nil
}

func decodeGoListEdges(reader io.Reader, modulePath string) ([]Edge, error) {
	decoder := json.NewDecoder(reader)
	edges := make([]Edge, 0)
	for {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		from, internal := moduleRelative(modulePath, pkg.ImportPath)
		if !internal {
			continue
		}
		for _, imported := range pkg.Imports {
			to, internal := moduleRelative(modulePath, imported)
			if internal {
				edges = append(edges, Edge{From: from, To: to})
				continue
			}
			// Engines expose every external import; platform packages expose the infrastructure imports governed by policy.
			_, _, platformImportRestricted := platformInfrastructureBoundary(imported)
			if gameArea(from, "engine") || under(from, "platform") && platformImportRestricted {
				edges = append(edges, Edge{From: from, To: imported})
			}
		}
	}
	return edges, nil
}

func discoverWorkspaceEdges(ctx context.Context, root string) ([]Edge, error) {
	const command = "pnpm list --recursive --depth -1 --json"
	// The package manager remains the source of truth for workspace membership; manifests provide dependency groups.
	cmd := exec.CommandContext(ctx, "pnpm", "list", "--recursive", "--depth", "-1", "--json")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return nil, commandError(ctx, command, root, err)
	}
	edges, err := decodePnpmWorkspaceEdges(strings.NewReader(string(output)), os.DirFS(root), root)
	if err != nil {
		return nil, fmt.Errorf("process output from %s in %s: %w", command, root, err)
	}
	return edges, nil
}

func decodePnpmWorkspaceEdges(reader io.Reader, root fs.FS, repositoryRoot string) ([]Edge, error) {
	var listed []pnpmListPackage
	if err := json.NewDecoder(reader).Decode(&listed); err != nil {
		return nil, fmt.Errorf("decode pnpm workspace list: %w", err)
	}
	manifests := make(map[string]string)
	parsed := make(map[string]workspaceManifest)
	for _, workspacePackage := range listed {
		relativeDirectory, err := filepath.Rel(repositoryRoot, workspacePackage.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace path %s: %w", workspacePackage.Path, err)
		}
		directory := normalize(relativeDirectory)
		if directory == "." {
			continue
		}
		if directory == ".." || strings.HasPrefix(directory, "../") {
			return nil, fmt.Errorf("workspace package %s is outside repository root", workspacePackage.Path)
		}
		manifestPath := path.Join(directory, "package.json")
		data, err := fs.ReadFile(root, manifestPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", manifestPath, err)
		}
		var manifest workspaceManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("decode %s: %w", manifestPath, err)
		}
		if manifest.Name == "" || manifest.Name != workspacePackage.Name {
			return nil, fmt.Errorf("workspace name mismatch in %s: pnpm=%s manifest=%s", manifestPath, workspacePackage.Name, manifest.Name)
		}
		if previous, exists := manifests[manifest.Name]; exists {
			return nil, fmt.Errorf("duplicate workspace package name %s in %s and %s", manifest.Name, previous, directory)
		}
		manifests[manifest.Name] = directory
		parsed[directory] = manifest
	}

	edges := make([]Edge, 0)
	for directory, manifest := range parsed {
		for _, dependencies := range []map[string]string{manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies, manifest.PeerDependencies} {
			for dependency := range dependencies {
				if target, internal := manifests[dependency]; internal {
					edges = append(edges, Edge{From: directory, To: target})
				}
			}
		}
	}
	return canonicalEdges(edges), nil
}

// canonicalEdges removes duplicate discovery records and sorts them for stable CLI counts and diagnostics.
func canonicalEdges(edges []Edge) []Edge {
	unique := make(map[Edge]struct{}, len(edges))
	for _, edge := range edges {
		unique[edge] = struct{}{}
	}
	result := make([]Edge, 0, len(unique))
	for edge := range unique {
		result = append(result, edge)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].From == result[j].From {
			return result[i].To < result[j].To
		}
		return result[i].From < result[j].From
	})
	return result
}

// commandError preserves the process or cancellation cause while adding the command and execution root.
func commandError(ctx context.Context, command, root string, err error) error {
	// Context cancellation is the actionable cause even when the OS reports a killed child process.
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s in %s failed: %w", command, root, contextErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr != "" {
			return fmt.Errorf("%s in %s failed with exit code %d: %s: %w", command, root, exitErr.ExitCode(), stderr, err)
		}
		return fmt.Errorf("%s in %s failed with exit code %d: %w", command, root, exitErr.ExitCode(), err)
	}
	return fmt.Errorf("%s in %s failed: %w", command, root, err)
}

func moduleRelative(modulePath, importPath string) (string, bool) {
	if importPath == modulePath {
		return ".", true
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	return normalize(strings.TrimPrefix(importPath, prefix)), true
}

// RepositoryRoot resolves the command root once so all discovery uses the same ownership boundary.
func RepositoryRoot(value string) (string, error) {
	root, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	return root, nil
}
