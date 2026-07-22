// Command loadtest emits the release-gate realtime capacity and fault report as JSON.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/iFTY-R/game-night/apps/realtime/internal/loadtest"
)

func main() {
	config := loadtest.DefaultConfig()
	report, err := loadtest.Run(context.Background(), config)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "realtime load gate failed: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "realtime load report encoding failed: %v\n", err)
		os.Exit(1)
	}
	if !report.Success {
		os.Exit(1)
	}
}
