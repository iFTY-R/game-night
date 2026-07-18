package security

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestArgon2ServiceHashVerifyAndUpgrade(t *testing.T) {
	params := fastArgon2TestParams()
	service, err := NewArgon2Service(params, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	encoded, err := service.Hash(context.Background(), []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	matched, needsUpgrade, err := service.Verify(context.Background(), encoded, []byte("correct horse battery staple"))
	if err != nil || !matched || needsUpgrade {
		t.Fatalf("unexpected verification result: matched=%t upgrade=%t err=%v", matched, needsUpgrade, err)
	}
	matched, _, err = service.Verify(context.Background(), encoded, []byte("wrong password"))
	if err != nil || matched {
		t.Fatalf("wrong password result: matched=%t err=%v", matched, err)
	}

	strongerParams := params
	strongerParams.Iterations++
	stronger, err := NewArgon2Service(strongerParams, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer stronger.Close()
	matched, needsUpgrade, err = stronger.Verify(context.Background(), encoded, []byte("correct horse battery staple"))
	if err != nil || !matched || !needsUpgrade {
		t.Fatalf("expected parameter upgrade: matched=%t upgrade=%t err=%v", matched, needsUpgrade, err)
	}
}

func TestArgon2ServiceDummyVerification(t *testing.T) {
	service, err := NewArgon2Service(fastArgon2TestParams(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	matched, needsUpgrade, err := service.VerifyOrDummy(context.Background(), "", []byte("submitted secret"))
	if err != nil || matched || needsUpgrade {
		t.Fatalf("unexpected dummy result: matched=%t upgrade=%t err=%v", matched, needsUpgrade, err)
	}
}

func TestArgon2ServiceRejectsMalformedHashWithoutEcho(t *testing.T) {
	service, err := NewArgon2Service(fastArgon2TestParams(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	raw := "$argon2id$attacker-secret"
	_, _, err = service.Verify(context.Background(), raw, []byte("secret"))
	if !errors.Is(err, ErrInvalidPasswordHash) {
		t.Fatalf("expected invalid hash, got %v", err)
	}
	if strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "attacker-secret") {
		t.Fatalf("hash error echoed encoded input: %v", err)
	}
}

func TestArgon2ServiceBoundsWorkersAndQueue(t *testing.T) {
	params := fastArgon2TestParams()
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	var active int32
	var maximum int32
	derive := func(secret, salt []byte, params Argon2Params) []byte {
		current := atomic.AddInt32(&active, 1)
		for {
			observed := atomic.LoadInt32(&maximum)
			if current <= observed || atomic.CompareAndSwapInt32(&maximum, observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		atomic.AddInt32(&active, -1)
		return make([]byte, params.KeyLength)
	}
	service, err := newArgon2Service(params, 1, 1, derive)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	results := make(chan error, 2)
	go func() {
		_, err := service.Hash(context.Background(), []byte("first password"))
		results <- err
	}()
	<-started
	go func() {
		_, err := service.Hash(context.Background(), []byte("second password"))
		results <- err
	}()
	deadline := time.Now().Add(time.Second)
	for len(service.jobs) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(service.jobs) != 1 {
		t.Fatal("second Argon2 request did not enter the bounded queue")
	}
	if _, err := service.Hash(context.Background(), []byte("third password")); !errors.Is(err, ErrArgon2Busy) {
		t.Fatalf("expected full queue rejection, got %v", err)
	}
	release <- struct{}{}
	<-started
	release <- struct{}{}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if maximum != 1 {
		t.Fatalf("expected one concurrent derivation, got %d", maximum)
	}
}

func TestArgon2ServiceRejectsWorkAfterClose(t *testing.T) {
	service, err := NewArgon2Service(fastArgon2TestParams(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	service.Close()
	if _, err := service.Hash(context.Background(), []byte("password")); !errors.Is(err, ErrArgon2Closed) {
		t.Fatalf("expected closed service error, got %v", err)
	}
}

func TestArgon2ServiceSkipsCancelledQueuedWorkDuringClose(t *testing.T) {
	params := fastArgon2TestParams()
	started := make(chan struct{})
	release := make(chan struct{})
	var derivations int32
	derive := func(secret, salt []byte, params Argon2Params) []byte {
		if atomic.AddInt32(&derivations, 1) == 1 {
			close(started)
			<-release
		}
		return make([]byte, params.KeyLength)
	}
	service, err := newArgon2Service(params, 1, 1, derive)
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, hashErr := service.Hash(context.Background(), []byte("first password"))
		firstDone <- hashErr
	}()
	<-started

	queuedContext, cancelQueued := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, hashErr := service.Hash(queuedContext, []byte("cancelled password"))
		secondDone <- hashErr
	}()
	deadline := time.Now().Add(time.Second)
	for len(service.jobs) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(service.jobs) != 1 {
		t.Fatal("cancelled request did not enter the queue")
	}
	cancelQueued()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation returned %v", err)
	}

	closed := make(chan struct{})
	go func() {
		service.Close()
		close(closed)
	}()
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not discard cancelled queued work")
	}
	if got := atomic.LoadInt32(&derivations); got != 1 {
		t.Fatalf("cancelled queued work derived %d hashes, want 1", got)
	}
}

func TestArgon2ServiceRejectsAggregateWorkerMemoryAboveBudget(t *testing.T) {
	params := fastArgon2TestParams()
	params.MemoryKiB = 128 * 1024
	if _, err := NewArgon2Service(params, 5, 0); !errors.Is(err, ErrInvalidArgon2Params) {
		t.Fatalf("expected aggregate memory rejection, got %v", err)
	}
}

func TestArgon2ServiceRejectsStoredCostsAboveServiceCeiling(t *testing.T) {
	params := fastArgon2TestParams()
	var derivations int32
	service, err := newArgon2Service(params, 1, 1, func(secret, salt []byte, params Argon2Params) []byte {
		atomic.AddInt32(&derivations, 1)
		return make([]byte, params.KeyLength)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	tests := []struct {
		name   string
		params Argon2Params
	}{
		{name: "memory", params: Argon2Params{MemoryKiB: params.MemoryKiB + 1, Iterations: params.Iterations, Parallelism: params.Parallelism, SaltLength: params.SaltLength, KeyLength: params.KeyLength}},
		{name: "iterations", params: Argon2Params{MemoryKiB: params.MemoryKiB, Iterations: params.Iterations + 1, Parallelism: params.Parallelism, SaltLength: params.SaltLength, KeyLength: params.KeyLength}},
		{name: "parallelism", params: Argon2Params{MemoryKiB: params.MemoryKiB, Iterations: params.Iterations, Parallelism: params.Parallelism + 1, SaltLength: params.SaltLength, KeyLength: params.KeyLength}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded := encodePasswordHash(test.params, make([]byte, test.params.SaltLength), make([]byte, test.params.KeyLength))
			if _, _, err := service.Verify(context.Background(), encoded, []byte("password")); !errors.Is(err, ErrInvalidPasswordHash) {
				t.Fatalf("expected stored cost rejection, got %v", err)
			}
		})
	}
	if got := atomic.LoadInt32(&derivations); got != 0 {
		t.Fatalf("untrusted stored costs started %d derivations", got)
	}
}

func fastArgon2TestParams() Argon2Params {
	return Argon2Params{
		MemoryKiB:   64,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}
