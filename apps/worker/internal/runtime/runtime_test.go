package runtime

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/apps/worker/internal/checkpoint"
	"github.com/iFTY-R/game-night/platform/keyrotation"
)

func TestRuntimeRunsImmediatelyAndStopsOnCancellation(t *testing.T) {
	dispatcher := &countingDispatcher{called: make(chan struct{}, 1)}
	runtime, err := New(dispatcher, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()

	select {
	case <-dispatcher.called:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("dispatcher was not run immediately")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if dispatcher.calls == 0 {
		t.Fatal("runtime did not execute a pass")
	}
}

func TestRuntimeRunsRotationInTheSameSerialPass(t *testing.T) {
	dispatcher := &countingDispatcher{called: make(chan struct{}, 1)}
	rotation := &countingRotation{called: make(chan struct{}, 1)}
	runtime, err := NewWithOperations(dispatcher, rotation, nil, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-rotation.called:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("rotation was not run after dispatcher")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type countingDispatcher struct {
	mu     sync.Mutex
	calls  int
	called chan struct{}
}

type countingRotation struct{ called chan struct{} }

func (rotation *countingRotation) RunOnce(context.Context) (keyrotation.Result, error) {
	select {
	case rotation.called <- struct{}{}:
	default:
	}
	return keyrotation.Result{Idle: true}, nil
}

func (dispatcher *countingDispatcher) RunOnce(context.Context) (checkpoint.Result, error) {
	dispatcher.mu.Lock()
	dispatcher.calls++
	dispatcher.mu.Unlock()
	select {
	case dispatcher.called <- struct{}{}:
	default:
	}
	return checkpoint.Result{Idle: true}, nil
}
