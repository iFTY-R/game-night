package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadinessSeparatesOrdinaryReadsFromSensitiveWrites(t *testing.T) {
	postgres := newToggleCheck(true)
	redis := newToggleCheck(false)
	keyring := newToggleCheck(true)
	bootstrap := newToggleCheck(true)
	checkpoint := newToggleCheck(false)
	readiness, err := NewReadiness(ReadinessChecks{
		PostgreSQL: postgres, Redis: redis, Keyring: keyring, Bootstrap: bootstrap, Checkpoint: checkpoint,
	})
	if err != nil {
		t.Fatal(err)
	}

	ordinary := readinessRequest(t, readiness.handler(ordinaryReadiness), ReadinessPath)
	if ordinary.code != http.StatusOK || !ordinary.body.Ready || ordinary.body.Components[componentRedis] != "unavailable" || ordinary.body.Components[componentCheckpoint] != "unavailable" {
		t.Fatalf("ordinary readiness = %+v", ordinary)
	}
	sensitive := readinessRequest(t, readiness.handler(sensitiveReadiness), SensitiveReadinessPath)
	if sensitive.code != http.StatusServiceUnavailable || sensitive.body.Ready {
		t.Fatalf("sensitive readiness = %+v", sensitive)
	}

	redis.ready.Store(true)
	checkpoint.ready.Store(true)
	sensitive = readinessRequest(t, readiness.handler(sensitiveReadiness), SensitiveReadinessPath)
	if sensitive.code != http.StatusOK || !sensitive.body.Ready {
		t.Fatalf("restored sensitive readiness = %+v", sensitive)
	}

	bootstrap.ready.Store(false)
	ordinary = readinessRequest(t, readiness.handler(ordinaryReadiness), ReadinessPath)
	sensitive = readinessRequest(t, readiness.handler(sensitiveReadiness), SensitiveReadinessPath)
	if ordinary.code != http.StatusServiceUnavailable || sensitive.code != http.StatusServiceUnavailable {
		t.Fatalf("bootstrap failure did not close both gates: ordinary=%+v sensitive=%+v", ordinary, sensitive)
	}
}

func TestReadinessNeverSerializesComponentErrors(t *testing.T) {
	secret := "postgres://runtime:secret@example.test/database"
	readiness, err := NewReadiness(ReadinessChecks{
		PostgreSQL: CheckFunc(func(context.Context) error { return errors.New(secret) }),
		Redis:      newToggleCheck(true),
		Keyring:    newToggleCheck(true),
		Bootstrap:  newToggleCheck(true),
		Checkpoint: newToggleCheck(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, ReadinessPath, nil)
	readiness.handler(ordinaryReadiness).ServeHTTP(recorder, request)
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatal("readiness leaked component error")
	}
}

type toggleCheck struct{ ready atomic.Bool }

func newToggleCheck(ready bool) *toggleCheck {
	check := &toggleCheck{}
	check.ready.Store(ready)
	return check
}

func (check *toggleCheck) Check(context.Context) error {
	if check.ready.Load() {
		return nil
	}
	return errors.New("unavailable")
}

type readinessResult struct {
	code int
	body readinessResponse
}

func readinessRequest(t testing.TB, handler http.Handler, path string) readinessResult {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	var body readinessResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("readiness response is cacheable")
	}
	return readinessResult{code: recorder.Code, body: body}
}

func readyReadiness(t testing.TB) *Readiness {
	t.Helper()
	readiness, err := NewReadiness(ReadinessChecks{
		PostgreSQL: newToggleCheck(true), Redis: newToggleCheck(true), Keyring: newToggleCheck(true),
		Bootstrap: newToggleCheck(true), Checkpoint: newToggleCheck(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	return readiness
}
