package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type capture struct {
	Args        []string          `json:"args"`
	Environment map[string]string `json:"environment"`
}

func main() {
	if outputPath := strings.TrimSpace(os.Getenv("HELPER_OUTPUT_FILE")); outputPath != "" {
		payload := capture{
			Args: os.Args[1:],
			Environment: map[string]string{
				"UNCHANGED_ENV": os.Getenv("UNCHANGED_ENV"),
				"BINARY_NAME":   filepath.Base(os.Args[0]),
			},
		}
		contents, _ := json.Marshal(payload)
		_ = os.WriteFile(outputPath, contents, 0o600)
	}

	exitCode := 0
	if rawCode := strings.TrimSpace(os.Getenv("HELPER_EXIT_CODE")); rawCode != "" {
		if parsed, err := strconv.Atoi(rawCode); err == nil {
			exitCode = parsed
		}
	}
	os.Exit(exitCode)
}
