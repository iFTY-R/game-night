// Package main exposes repository dependency boundary validation as a local and CI command.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/iFTY-R/game-night/tooling/boundarycheck"
)

// modulePath anchors Go imports to repository-relative ownership paths.
const modulePath = "github.com/iFTY-R/game-night"

func main() {
	rootFlag := flag.String("root", ".", "repository root to inspect")
	flag.Parse()

	root, err := boundarycheck.RepositoryRoot(*rootFlag)
	if err != nil {
		fail(err)
	}
	edges, err := boundarycheck.DiscoverEdges(context.Background(), root, modulePath)
	if err != nil {
		fail(err)
	}
	violations := boundarycheck.ValidateEdges(edges)
	for _, violation := range violations {
		fmt.Fprintln(os.Stderr, violation.String())
	}
	if len(violations) != 0 {
		os.Exit(1)
	}
	fmt.Printf("dependency boundaries passed (%d dependency edges)\n", len(edges))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
