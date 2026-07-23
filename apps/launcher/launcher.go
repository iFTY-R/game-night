package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

const (
	commandAPI         = "api"
	commandRealtime    = "realtime"
	commandWorker      = "worker"
	commandEdge        = "edge"
	commandMigrate     = "migrate"
	commandServeAll    = "serve-all"
	commandHealthcheck = "healthcheck"

	environmentBinDirectory       = "GAME_NIGHT_BIN_DIRECTORY"
	environmentShutdownTimeout    = "GAME_NIGHT_SHUTDOWN_TIMEOUT"
	environmentHealthcheckURL     = "GAME_NIGHT_HEALTHCHECK_URL"
	environmentHealthcheckTimeout = "GAME_NIGHT_HEALTHCHECK_TIMEOUT"
	environmentDatabaseURL        = "GAME_NIGHT_DATABASE_URL"

	environmentAPIDatabaseURL      = "GAME_NIGHT_API_DATABASE_URL"
	environmentRealtimeDatabaseURL = "GAME_NIGHT_REALTIME_DATABASE_URL"
	environmentWorkerDatabaseURL   = "GAME_NIGHT_WORKER_DATABASE_URL"

	environmentAPIKeyringDirectory    = "GAME_NIGHT_API_KEYRING_DIRECTORY"
	environmentWorkerKeyringDirectory = "GAME_NIGHT_WORKER_KEYRING_DIRECTORY"

	environmentAPIBootstrapSecretFile   = "GAME_NIGHT_API_BOOTSTRAP_SECRET_FILE"
	environmentAdminBootstrapSecretFile = "GAME_NIGHT_ADMIN_BOOTSTRAP_SECRET_FILE"

	environmentPIIKeyringFile            = "GAME_NIGHT_PII_KEYRING_FILE"
	environmentTOTPKeyringFile           = "GAME_NIGHT_TOTP_KEYRING_FILE"
	environmentResultEnvelopeKeyringFile = "GAME_NIGHT_RESULT_ENVELOPE_KEYRING_FILE"
	environmentDeviceKeyringFile         = "GAME_NIGHT_DEVICE_KEYRING_FILE"
	environmentRateLimitKeyringFile      = "GAME_NIGHT_RATE_LIMIT_KEYRING_FILE"
	environmentUserChallengeKeyringFile  = "GAME_NIGHT_USER_CHALLENGE_KEYRING_FILE"
	environmentAdminChallengeKeyringFile = "GAME_NIGHT_ADMIN_CHALLENGE_KEYRING_FILE"
	environmentAdminSessionKeyringFile   = "GAME_NIGHT_ADMIN_SESSION_KEYRING_FILE"
	environmentAuditKeyringFile          = "GAME_NIGHT_AUDIT_KEYRING_FILE"
)

var (
	defaultBinDirectory    = "/app/bin"
	defaultShutdownTimeout = 30 * time.Second
)

// The launcher strips shared secret mounts first, then adds back only the minimal authority each child needs.
var secretEnvironmentNames = []string{
	environmentDatabaseURL,
	environmentAPIDatabaseURL,
	environmentRealtimeDatabaseURL,
	environmentWorkerDatabaseURL,
	environmentAPIKeyringDirectory,
	environmentWorkerKeyringDirectory,
	environmentAPIBootstrapSecretFile,
	environmentAdminBootstrapSecretFile,
	environmentPIIKeyringFile,
	environmentTOTPKeyringFile,
	environmentResultEnvelopeKeyringFile,
	environmentDeviceKeyringFile,
	environmentRateLimitKeyringFile,
	environmentUserChallengeKeyringFile,
	environmentAdminChallengeKeyringFile,
	environmentAdminSessionKeyringFile,
	environmentAuditKeyringFile,
}

type runtimeConfig struct {
	binDirectory    string
	shutdownTimeout time.Duration
}

type commandSpec struct {
	name string
	path string
	args []string
	env  []string
}

type managedProcess interface {
	Wait() error
	Signal(os.Signal) error
	Kill() error
}

type processStarter func(commandSpec, ioStreams) (managedProcess, error)

type execProcess struct {
	cmd *exec.Cmd
}

// startExecProcess launches one child with inherited stdio so the single-image entrypoint remains transparent.
func startExecProcess(spec commandSpec, streams ioStreams) (managedProcess, error) {
	command := exec.Command(spec.path, spec.args...)
	command.Env = append([]string(nil), spec.env...)
	command.Stdin = streams.stdin
	command.Stdout = streams.stdout
	command.Stderr = streams.stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	return execProcess{cmd: command}, nil
}

func (process execProcess) Wait() error {
	return process.cmd.Wait()
}

func (process execProcess) Signal(signal os.Signal) error {
	if process.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return process.cmd.Process.Signal(signal)
}

func (process execProcess) Kill() error {
	if process.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return process.cmd.Process.Kill()
}

// runProxyCommand preserves the selected child process exit status while still forwarding shutdown signals.
func runProxyCommand(
	ctx context.Context,
	spec commandSpec,
	shutdownTimeout time.Duration,
	streams ioStreams,
	starter processStarter,
	signals <-chan os.Signal,
) error {
	process, err := starter(spec, streams)
	if err != nil {
		return commandError{message: fmt.Sprintf("launcher: start %s: %v", spec.name, err), code: 1}
	}
	waitResults := make(chan error, 1)
	go func() {
		waitResults <- process.Wait()
	}()

	deadline := shutdownTimer(shutdownTimeout)
	stopTimer(deadline)
	var timer <-chan time.Time
	shuttingDown := false

	for {
		select {
		case <-ctx.Done():
			if !shuttingDown {
				shuttingDown = true
				forwardProcessSignal(process, gracefulStopSignal())
				timer = resetShutdownTimer(deadline, shutdownTimeout)
			}
		case receivedSignal := <-signals:
			if !shuttingDown && receivedSignal != nil {
				shuttingDown = true
				forwardProcessSignal(process, receivedSignal)
				timer = resetShutdownTimer(deadline, shutdownTimeout)
			}
		case err := <-waitResults:
			stopTimer(deadline)
			if err == nil {
				return nil
			}
			return commandError{
				message: fmt.Sprintf("launcher: %s exited: %v", spec.name, err),
				code:    childExitCode(err),
			}
		case <-timer:
			timer = nil
			killProcess(process)
		}
	}
}

type childState struct {
	spec commandSpec
	proc managedProcess
	done bool
}

type childExit struct {
	index int
	err   error
}

// runSupervisor keeps all long-lived services alive together; the first unexpected exit begins an ordered shutdown.
func runSupervisor(
	ctx context.Context,
	specs []commandSpec,
	shutdownTimeout time.Duration,
	streams ioStreams,
	starter processStarter,
	signals <-chan os.Signal,
) error {
	children, err := startChildren(specs, streams, starter)
	if err != nil {
		return err
	}

	waitResults := make(chan childExit, len(children))
	for index := range children {
		go func(index int) {
			waitResults <- childExit{index: index, err: children[index].proc.Wait()}
		}(index)
	}

	deadline := shutdownTimer(shutdownTimeout)
	stopTimer(deadline)
	var timer <-chan time.Time
	var shutdownErr error
	shuttingDown := false
	remaining := len(children)

	beginShutdown := func(signal os.Signal, reason error) {
		if shuttingDown {
			return
		}
		shuttingDown = true
		shutdownErr = reason
		for index := range children {
			if children[index].done {
				continue
			}
			forwardProcessSignal(children[index].proc, signal)
		}
		timer = resetShutdownTimer(deadline, shutdownTimeout)
	}

	for remaining > 0 {
		select {
		case <-ctx.Done():
			beginShutdown(gracefulStopSignal(), nil)
		case receivedSignal := <-signals:
			if receivedSignal != nil {
				beginShutdown(receivedSignal, nil)
			}
		case result := <-waitResults:
			child := &children[result.index]
			if child.done {
				continue
			}
			child.done = true
			remaining--

			if !shuttingDown {
				if result.err != nil {
					beginShutdown(gracefulStopSignal(), fmt.Errorf("launcher: %s exited: %w", child.spec.name, result.err))
				} else {
					beginShutdown(gracefulStopSignal(), fmt.Errorf("launcher: %s exited unexpectedly", child.spec.name))
				}
				continue
			}

			// Signal-triggered shutdown returns success only when every child exits cleanly within the grace period.
			if result.err != nil && shutdownErr == nil {
				shutdownErr = fmt.Errorf("launcher: %s exited during shutdown: %w", child.spec.name, result.err)
			}
		case <-timer:
			timer = nil
			for index := range children {
				if children[index].done {
					continue
				}
				killProcess(children[index].proc)
			}
			if shutdownErr != nil {
				shutdownErr = fmt.Errorf("%w; launcher: shutdown timed out after %s", shutdownErr, shutdownTimeout)
			} else {
				shutdownErr = fmt.Errorf("launcher: shutdown timed out after %s", shutdownTimeout)
			}
		}
	}

	stopTimer(deadline)
	return shutdownErr
}

func startChildren(specs []commandSpec, streams ioStreams, starter processStarter) ([]childState, error) {
	children := make([]childState, 0, len(specs))
	for _, spec := range specs {
		process, err := starter(spec, streams)
		if err != nil {
			stopStartedChildren(children)
			return nil, fmt.Errorf("launcher: start %s: %w", spec.name, err)
		}
		children = append(children, childState{spec: spec, proc: process})
	}
	return children, nil
}

func stopStartedChildren(children []childState) {
	for index := range children {
		forwardProcessSignal(children[index].proc, gracefulStopSignal())
	}
	for index := range children {
		killProcess(children[index].proc)
	}
}

func buildServeAllSpecs(environ []string, binDirectory string) ([]commandSpec, error) {
	base := newEnvironment(environ)
	specs := make([]commandSpec, 0, 4)
	for _, name := range []string{commandAPI, commandRealtime, commandWorker, commandEdge} {
		path, err := resolveExecutable(binDirectory, name)
		if err != nil {
			return nil, err
		}
		childEnv := newChildEnvironment(base)
		switch name {
		case commandAPI:
			applyDatabaseMapping(childEnv, base, environmentAPIDatabaseURL)
			applyAPISecrets(childEnv, base)
		case commandRealtime:
			applyDatabaseMapping(childEnv, base, environmentRealtimeDatabaseURL)
		case commandWorker:
			applyDatabaseMapping(childEnv, base, environmentWorkerDatabaseURL)
			applyWorkerSecrets(childEnv, base)
		case commandEdge:
			// Edge intentionally stays database-free in single-image mode.
		}
		specs = append(specs, commandSpec{name: name, path: path, env: childEnv.list()})
	}
	return specs, nil
}

func newProxyCommandSpec(name string, args []string, environ []string, binDirectory string) (commandSpec, error) {
	path, err := resolveExecutable(binDirectory, name)
	if err != nil {
		return commandSpec{}, err
	}
	return commandSpec{name: name, path: path, args: append([]string(nil), args...), env: newEnvironment(environ).list()}, nil
}

func runHealthcheck(lookup sharedconfig.LookupEnv) error {
	rawURL := readLauncherValue(lookup, environmentHealthcheckURL, "http://127.0.0.1:8080/health/live")
	target, err := url.Parse(rawURL)
	if err != nil || target.Scheme != "http" || target.Host == "" || target.User != nil {
		return commandError{message: "launcher: invalid healthcheck URL", code: 1}
	}
	timeoutValue := readLauncherValue(lookup, environmentHealthcheckTimeout, "2s")
	timeout, err := time.ParseDuration(timeoutValue)
	if err != nil || timeout <= 0 {
		return commandError{message: "launcher: invalid healthcheck timeout", code: 1}
	}
	request, err := http.NewRequest(http.MethodGet, target.String(), nil)
	if err != nil {
		return commandError{message: "launcher: build healthcheck request", code: 1}
	}
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return commandError{message: "launcher: healthcheck failed", code: 1}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return commandError{message: "launcher: healthcheck returned non-success status", code: 1}
	}
	return nil
}

func resolveExecutable(binDirectory, name string) (string, error) {
	if strings.TrimSpace(binDirectory) == "" {
		return "", errors.New("launcher: binary directory is empty")
	}
	candidates := []string{filepath.Join(binDirectory, name)}
	if runtime.GOOS == "windows" {
		candidates = append([]string{filepath.Join(binDirectory, name+".exe")}, candidates...)
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return candidates[0], nil
}

func applyDatabaseMapping(child *environment, source environment, sourceName string) {
	if value, ok := source.get(sourceName); ok && value != "" {
		child.set(environmentDatabaseURL, value)
	}
}

func applyAPISecrets(child *environment, source environment) {
	directory, ok := source.get(environmentAPIKeyringDirectory)
	if ok && directory != "" {
		child.set(environmentPIIKeyringFile, filepath.Join(directory, "pii.json"))
		child.set(environmentTOTPKeyringFile, filepath.Join(directory, "totp.json"))
		child.set(environmentResultEnvelopeKeyringFile, filepath.Join(directory, "result-envelope.json"))
		child.set(environmentDeviceKeyringFile, filepath.Join(directory, "device.json"))
		child.set(environmentRateLimitKeyringFile, filepath.Join(directory, "rate-limit.json"))
		child.set(environmentUserChallengeKeyringFile, filepath.Join(directory, "user-challenge.json"))
		child.set(environmentAdminChallengeKeyringFile, filepath.Join(directory, "admin-challenge.json"))
		child.set(environmentAdminSessionKeyringFile, filepath.Join(directory, "admin-session.json"))
		child.set(environmentAuditKeyringFile, filepath.Join(directory, "audit.json"))
	}
	if bootstrapSecretFile, ok := source.get(environmentAPIBootstrapSecretFile); ok && bootstrapSecretFile != "" {
		child.set(environmentAdminBootstrapSecretFile, bootstrapSecretFile)
	}
}

func applyWorkerSecrets(child *environment, source environment) {
	directory, ok := source.get(environmentWorkerKeyringDirectory)
	if ok && directory != "" {
		child.set(environmentPIIKeyringFile, filepath.Join(directory, "pii.json"))
		child.set(environmentTOTPKeyringFile, filepath.Join(directory, "totp.json"))
		child.set(environmentAuditKeyringFile, filepath.Join(directory, "audit.json"))
	}
}

func newChildEnvironment(base environment) *environment {
	child := base.clone()
	child.unset(secretEnvironmentNames...)
	return &child
}

type environment struct {
	values map[string]string
}

func newEnvironment(environ []string) environment {
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" {
			continue
		}
		values[name] = value
	}
	return environment{values: values}
}

func (env environment) clone() environment {
	cloned := make(map[string]string, len(env.values))
	for name, value := range env.values {
		cloned[name] = value
	}
	return environment{values: cloned}
}

func (env *environment) set(name, value string) {
	if value == "" {
		delete(env.values, name)
		return
	}
	env.values[name] = value
}

func (env *environment) unset(names ...string) {
	for _, name := range names {
		delete(env.values, name)
	}
}

func (env environment) get(name string) (string, bool) {
	value, ok := env.values[name]
	return value, ok
}

func (env environment) list() []string {
	names := make([]string, 0, len(env.values))
	for name := range env.values {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]string, 0, len(names))
	for _, name := range names {
		list = append(list, name+"="+env.values[name])
	}
	return list
}

func shutdownTimer(timeout time.Duration) *time.Timer {
	timer := time.NewTimer(timeout)
	return timer
}

func resetShutdownTimer(timer *time.Timer, timeout time.Duration) <-chan time.Time {
	stopTimer(timer)
	timer.Reset(timeout)
	return timer.C
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func forwardProcessSignal(process managedProcess, signal os.Signal) {
	if process == nil || signal == nil {
		return
	}
	_ = process.Signal(signal)
}

func killProcess(process managedProcess) {
	if process == nil {
		return
	}
	_ = process.Kill()
}

type commandError struct {
	message string
	code    int
}

func (error commandError) Error() string {
	return error.message
}

func (error commandError) ExitCode() int {
	if error.code <= 0 {
		return 1
	}
	return error.code
}

func exitCode(err error) int {
	type exitCoder interface {
		ExitCode() int
	}
	var coder exitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return 1
}
