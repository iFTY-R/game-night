package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

func TestBuildServeAllSpecsMapsDedicatedEnvironment(t *testing.T) {
	apiSecrets := filepath.Join(t.TempDir(), "api-secrets")
	workerSecrets := filepath.Join(t.TempDir(), "worker-secrets")
	specs, err := buildServeAllSpecs([]string{
		environmentAPIDatabaseURL + "=postgres://api",
		environmentRealtimeDatabaseURL + "=postgres://realtime",
		environmentWorkerDatabaseURL + "=postgres://worker",
		environmentAPIKeyringDirectory + "=" + apiSecrets,
		environmentWorkerKeyringDirectory + "=" + workerSecrets,
		environmentAPIBootstrapSecretFile + "=" + filepath.Join(apiSecrets, "admin-bootstrap.txt"),
		environmentDatabaseURL + "=postgres://should-not-leak",
		environmentMigrationDatabaseURL + "=postgres://migration-should-not-leak",
		environmentPIIKeyringFile + "=should-not-leak",
	}, defaultBinDirectory)
	if err != nil {
		t.Fatal(err)
	}

	apiEnv := environmentFromList(t, specs[0].env)
	if apiEnv[environmentDatabaseURL] != "postgres://api" {
		t.Fatalf("api database mapping = %q", apiEnv[environmentDatabaseURL])
	}
	if apiEnv[environmentPIIKeyringFile] != filepath.Join(apiSecrets, "pii.json") ||
		apiEnv[environmentAdminSessionKeyringFile] != filepath.Join(apiSecrets, "admin-session.json") ||
		apiEnv[environmentAuditKeyringFile] != filepath.Join(apiSecrets, "audit.json") {
		t.Fatalf("api keyring mapping = %#v", apiEnv)
	}
	if apiEnv[environmentAdminBootstrapSecretFile] != filepath.Join(apiSecrets, "admin-bootstrap.txt") {
		t.Fatalf("api bootstrap mapping = %q", apiEnv[environmentAdminBootstrapSecretFile])
	}
	if _, leaked := apiEnv[environmentAPIDatabaseURL]; leaked {
		t.Fatal("api retained process-specific database environment")
	}
	if _, leaked := apiEnv[environmentMigrationDatabaseURL]; leaked {
		t.Fatal("api retained migration database environment")
	}

	realtimeEnv := environmentFromList(t, specs[1].env)
	if realtimeEnv[environmentDatabaseURL] != "postgres://realtime" {
		t.Fatalf("realtime database mapping = %q", realtimeEnv[environmentDatabaseURL])
	}
	if _, leaked := realtimeEnv[environmentPIIKeyringFile]; leaked {
		t.Fatal("realtime inherited secret keyring material")
	}
	if _, leaked := realtimeEnv[environmentMigrationDatabaseURL]; leaked {
		t.Fatal("realtime retained migration database environment")
	}

	workerEnv := environmentFromList(t, specs[2].env)
	if workerEnv[environmentDatabaseURL] != "postgres://worker" {
		t.Fatalf("worker database mapping = %q", workerEnv[environmentDatabaseURL])
	}
	if workerEnv[environmentPIIKeyringFile] != filepath.Join(workerSecrets, "pii.json") ||
		workerEnv[environmentAuditKeyringFile] != filepath.Join(workerSecrets, "audit.json") {
		t.Fatalf("worker keyring mapping = %#v", workerEnv)
	}
	if _, leaked := workerEnv[environmentResultEnvelopeKeyringFile]; leaked {
		t.Fatal("worker received API-only keyring material")
	}
	if _, leaked := workerEnv[environmentMigrationDatabaseURL]; leaked {
		t.Fatal("worker retained migration database environment")
	}

	edgeEnv := environmentFromList(t, specs[3].env)
	if _, mapped := edgeEnv[environmentDatabaseURL]; mapped {
		t.Fatal("edge unexpectedly received a database url")
	}
	if _, leaked := edgeEnv[environmentMigrationDatabaseURL]; leaked {
		t.Fatal("edge retained migration database environment")
	}
}

func TestRunHealthcheckReturnsFailureForUnreadyEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	err := runHealthcheck(mapLookup(map[string]string{environmentHealthcheckURL: server.URL}))
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("healthcheck error = %v", err)
	}
}

func TestRunDispatchesDirectCommandsToExpectedBinary(t *testing.T) {
	for _, command := range []string{commandAPI, commandRealtime, commandWorker, commandEdge, commandMigrate} {
		t.Run(command, func(t *testing.T) {
			starter := &capturingStarter{waitErr: nil}
			err := run(
				context.Background(),
				[]string{command, "--flag", "value"},
				[]string{environmentBinDirectory + "=" + t.TempDir(), "UNCHANGED_ENV=retained"},
				ioStreams{stdin: bytes.NewBuffer(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}},
				mapLookup(map[string]string{environmentBinDirectory: t.TempDir()}),
				starter.start,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(starter.specs) != 1 {
				t.Fatalf("started %d commands", len(starter.specs))
			}
			spec := starter.specs[0]
			if spec.name != command {
				t.Fatalf("started %q", spec.name)
			}
			if !slices.Equal(spec.args, []string{"--flag", "value"}) {
				t.Fatalf("args = %#v", spec.args)
			}
		})
	}
}

func TestRunProxyCommandForwardsArgsAndExitCode(t *testing.T) {
	binDirectory := t.TempDir()
	outputFile := filepath.Join(t.TempDir(), "api.json")
	buildHelperBinary(t, commandAPI, binDirectory)

	helperEnvironment := []string{
		environmentBinDirectory + "=" + binDirectory,
		environmentShutdownTimeout + "=1s",
		"UNCHANGED_ENV=present",
		"HELPER_OUTPUT_FILE=" + outputFile,
		"HELPER_EXIT_CODE=23",
	}
	err := run(
		context.Background(),
		[]string{commandAPI, "--alpha", "beta"},
		helperEnvironment,
		ioStreams{stdin: bytes.NewBuffer(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}},
		mapLookup(map[string]string{
			environmentBinDirectory:    binDirectory,
			environmentShutdownTimeout: "1s",
			"HELPER_OUTPUT_FILE":       outputFile,
			"HELPER_EXIT_CODE":         "23",
			"UNCHANGED_ENV":            "present",
		}),
		startExecProcess,
		nil,
	)
	if err == nil {
		t.Fatal("run succeeded for a non-zero child exit")
	}
	if exitCode(err) != 23 {
		t.Fatalf("exit code = %d", exitCode(err))
	}

	var capture helperCapture
	readJSONFile(t, outputFile, &capture)
	if !slices.Equal(capture.Args, []string{"--alpha", "beta"}) {
		t.Fatalf("captured args = %#v", capture.Args)
	}
	if capture.Environment["UNCHANGED_ENV"] != "present" {
		t.Fatalf("captured environment = %#v", capture.Environment)
	}
}

func TestRunSupervisorStopsPeersWhenOneChildFails(t *testing.T) {
	api := newFakeProcess()
	realtime := newFakeProcess()
	worker := newFakeProcess()
	edge := newFakeProcess()
	realtime.signalHook = func(os.Signal) { realtime.finish(nil) }
	worker.signalHook = func(os.Signal) { worker.finish(nil) }
	edge.signalHook = func(os.Signal) { edge.finish(nil) }

	starter := newFakeStarter(map[string]*fakeProcess{
		commandAPI:      api,
		commandRealtime: realtime,
		commandWorker:   worker,
		commandEdge:     edge,
	})
	specs := fakeServeAllSpecs(t)

	done := make(chan error, 1)
	go func() {
		done <- runSupervisor(context.Background(), specs, 50*time.Millisecond, testStreams(), starter.start, nil)
	}()

	api.finish(errors.New("exit 7"))

	err := <-done
	if err == nil || !strings.Contains(err.Error(), commandAPI) {
		t.Fatalf("supervisor error = %v", err)
	}
	if len(realtime.signals) == 0 || len(worker.signals) == 0 || len(edge.signals) == 0 {
		t.Fatal("supervisor did not signal all surviving children")
	}
	if realtime.kills != 0 || worker.kills != 0 || edge.kills != 0 {
		t.Fatal("supervisor killed processes that exited during the grace period")
	}
}

func TestRunSupervisorKillsProcessesThatIgnoreShutdownSignal(t *testing.T) {
	api := newFakeProcess()
	realtime := newFakeProcess()
	worker := newFakeProcess()
	edge := newFakeProcess()
	realtime.killHook = func() { realtime.finish(nil) }
	worker.killHook = func() { worker.finish(nil) }
	edge.killHook = func() { edge.finish(nil) }

	starter := newFakeStarter(map[string]*fakeProcess{
		commandAPI:      api,
		commandRealtime: realtime,
		commandWorker:   worker,
		commandEdge:     edge,
	})
	specs := fakeServeAllSpecs(t)

	done := make(chan error, 1)
	go func() {
		done <- runSupervisor(context.Background(), specs, 20*time.Millisecond, testStreams(), starter.start, nil)
	}()

	api.finish(errors.New("exit 9"))

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("supervisor error = %v", err)
	}
	if realtime.kills == 0 || worker.kills == 0 || edge.kills == 0 {
		t.Fatal("supervisor did not kill hung children")
	}
}

func TestRunSupervisorPropagatesExternalSignal(t *testing.T) {
	api := newFakeProcess()
	realtime := newFakeProcess()
	worker := newFakeProcess()
	edge := newFakeProcess()
	for _, process := range []*fakeProcess{api, realtime, worker, edge} {
		current := process
		current.signalHook = func(os.Signal) { current.finish(nil) }
	}

	starter := newFakeStarter(map[string]*fakeProcess{
		commandAPI:      api,
		commandRealtime: realtime,
		commandWorker:   worker,
		commandEdge:     edge,
	})
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt

	err := runSupervisor(context.Background(), fakeServeAllSpecs(t), 50*time.Millisecond, testStreams(), starter.start, signals)
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range []*fakeProcess{api, realtime, worker, edge} {
		if len(process.signals) == 0 {
			t.Fatal("signal was not forwarded to every child")
		}
	}
}

type helperCapture struct {
	Args        []string          `json:"args"`
	Environment map[string]string `json:"environment"`
}

type capturingStarter struct {
	specs   []commandSpec
	waitErr error
}

func (starter *capturingStarter) start(spec commandSpec, _ ioStreams) (managedProcess, error) {
	starter.specs = append(starter.specs, spec)
	return fakeManagedProcess{waitErr: starter.waitErr}, nil
}

type fakeManagedProcess struct {
	waitErr error
}

func (process fakeManagedProcess) Wait() error            { return process.waitErr }
func (process fakeManagedProcess) Signal(os.Signal) error { return nil }
func (process fakeManagedProcess) Kill() error            { return nil }

type fakeStarter struct {
	processes map[string]*fakeProcess
}

func newFakeStarter(processes map[string]*fakeProcess) *fakeStarter {
	return &fakeStarter{processes: processes}
}

func (starter *fakeStarter) start(spec commandSpec, _ ioStreams) (managedProcess, error) {
	process, ok := starter.processes[spec.name]
	if !ok {
		return nil, errors.New("missing fake process")
	}
	return process, nil
}

type fakeProcess struct {
	waitCh     chan error
	signalHook func(os.Signal)
	killHook   func()

	mu      sync.Mutex
	signals []os.Signal
	kills   int
	closed  bool
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{waitCh: make(chan error, 1)}
}

func (process *fakeProcess) Wait() error {
	return <-process.waitCh
}

func (process *fakeProcess) Signal(signal os.Signal) error {
	process.mu.Lock()
	process.signals = append(process.signals, signal)
	hook := process.signalHook
	process.mu.Unlock()
	if hook != nil {
		hook(signal)
	}
	return nil
}

func (process *fakeProcess) Kill() error {
	process.mu.Lock()
	process.kills++
	hook := process.killHook
	process.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (process *fakeProcess) finish(err error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.closed {
		return
	}
	process.closed = true
	process.waitCh <- err
}

func fakeServeAllSpecs(t *testing.T) []commandSpec {
	t.Helper()
	return []commandSpec{
		{name: commandAPI, path: filepath.Join(t.TempDir(), "api")},
		{name: commandRealtime, path: filepath.Join(t.TempDir(), "realtime")},
		{name: commandWorker, path: filepath.Join(t.TempDir(), "worker")},
		{name: commandEdge, path: filepath.Join(t.TempDir(), "edge")},
	}
}

func testStreams() ioStreams {
	return ioStreams{stdin: bytes.NewBuffer(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
}

func buildHelperBinary(t *testing.T, name, binDirectory string) string {
	t.Helper()
	output := filepath.Join(binDirectory, executableName(name))
	command := exec.Command("go", "build", "-o", output, "./apps/launcher/testdata/helper")
	command.Dir = repoRoot(t)
	command.Env = os.Environ()
	outputBytes, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build helper %s: %v\n%s", name, err, outputBytes)
	}
	return output
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func environmentFromList(t *testing.T, values []string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(values))
	for _, entry := range values {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("invalid env entry %q", entry)
		}
		result[name] = value
	}
	return result
}

func mapLookup(values map[string]string) sharedconfig.LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, target); err != nil {
		t.Fatal(err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
