package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

func main() {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, launcherSignals()...)
	defer signal.Stop(signals)

	streams := ioStreams{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
	if err := run(context.Background(), os.Args[1:], os.Environ(), streams, os.LookupEnv, startExecProcess, signals); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

type ioStreams struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// run loads launcher-only runtime settings, then proxies to one binary or supervises the full single-image stack.
func run(
	ctx context.Context,
	args []string,
	environ []string,
	streams ioStreams,
	lookup sharedconfig.LookupEnv,
	starter processStarter,
	signals <-chan os.Signal,
) error {
	if ctx == nil || lookup == nil || starter == nil || streams.stdin == nil || streams.stdout == nil || streams.stderr == nil {
		return commandError{message: "launcher: invalid runtime dependencies", code: 1}
	}

	config, err := loadLauncherConfig(lookup)
	if err != nil {
		return commandError{message: err.Error(), code: 1}
	}
	if len(args) == 0 {
		return commandError{message: usageText, code: 1}
	}

	command := args[0]
	forwardedArgs := args[1:]

	switch command {
	case commandHealthcheck:
		return runHealthcheck(lookup)
	case commandAPI, commandRealtime, commandWorker, commandEdge, commandMigrate:
		spec, buildErr := newProxyCommandSpec(command, forwardedArgs, environ, config.binDirectory)
		if buildErr != nil {
			return commandError{message: buildErr.Error(), code: 1}
		}
		return runProxyCommand(ctx, spec, config.shutdownTimeout, streams, starter, signals)
	case commandServeAll:
		specs, buildErr := buildServeAllSpecs(environ, config.binDirectory)
		if buildErr != nil {
			return commandError{message: buildErr.Error(), code: 1}
		}
		if err := runSupervisor(ctx, specs, config.shutdownTimeout, streams, starter, signals); err != nil {
			return commandError{message: err.Error(), code: 1}
		}
		return nil
	default:
		return commandError{message: fmt.Sprintf("launcher: unsupported command %q\n%s", command, usageText), code: 1}
	}
}

const usageText = "usage: game-night <api|realtime|worker|edge|migrate|serve-all|healthcheck> [args...]"

func loadLauncherConfig(lookup sharedconfig.LookupEnv) (runtimeConfig, error) {
	if lookup == nil {
		return runtimeConfig{}, errors.New("launcher: missing environment lookup")
	}
	binDirectory := readLauncherValue(lookup, environmentBinDirectory, defaultBinDirectory)
	rawTimeout := readLauncherValue(lookup, environmentShutdownTimeout, defaultShutdownTimeout.String())
	shutdownTimeout, err := time.ParseDuration(rawTimeout)
	if err != nil || shutdownTimeout <= 0 {
		return runtimeConfig{}, fmt.Errorf("%s: invalid duration", environmentShutdownTimeout)
	}
	return runtimeConfig{binDirectory: binDirectory, shutdownTimeout: shutdownTimeout}, nil
}

func readLauncherValue(lookup sharedconfig.LookupEnv, name, fallback string) string {
	value, _ := lookup(name)
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
