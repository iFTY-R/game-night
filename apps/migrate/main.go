package main

import (
	"context"
	"fmt"
	"os"

	"github.com/iFTY-R/game-night/apps/migrate/internal/migrator"
)

func main() {
	if err := migrator.RunCLI(context.Background(), os.Args[1:], os.LookupEnv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
