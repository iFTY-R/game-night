package security

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	// Argon2 bounds reject corrupted hashes that would request unreasonable local CPU or memory.
	maxArgon2MemoryKiB    = 256 * 1024
	maxArgon2Iterations   = 10
	maxArgon2Parallelism  = 16
	maxPasswordHashLength = 1024
	maxArgon2SecretBytes  = 4096
	maxArgon2Workers      = 64
	maxArgon2Queue        = 4096
	// The aggregate ceiling prevents valid per-job settings from multiplying into process-wide memory exhaustion.
	maxArgon2AggregateMemoryKiB = 512 * 1024
)

var (
	// ErrInvalidArgon2Params reports unsafe service or encoded-hash cost parameters.
	ErrInvalidArgon2Params = errors.New("invalid Argon2 parameters")
	// ErrInvalidPasswordHash reports malformed PHC input without echoing it.
	ErrInvalidPasswordHash = errors.New("invalid password hash")
	// ErrArgon2Busy fails closed when all workers and bounded queue capacity are occupied.
	ErrArgon2Busy = errors.New("Argon2 service is busy")
	// ErrArgon2Closed prevents work from entering a service during shutdown.
	ErrArgon2Closed = errors.New("Argon2 service is closed")
	// ErrSecretRequired rejects empty or unreasonably large secret inputs.
	ErrSecretRequired = errors.New("secret is required")
)

// Argon2Params records all PHC cost and output settings needed for upgrade decisions.
type Argon2Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params returns the production baseline; deployments may raise it after capacity testing.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{
		MemoryKiB:   64 * 1024,
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// Validate bounds memory, CPU, parallelism, salt, and output sizes before any derivation starts.
func (params Argon2Params) Validate() error {
	if params.Parallelism == 0 || params.Parallelism > maxArgon2Parallelism ||
		params.MemoryKiB < 8*uint32(params.Parallelism) || params.MemoryKiB > maxArgon2MemoryKiB ||
		params.Iterations == 0 || params.Iterations > maxArgon2Iterations ||
		params.SaltLength < 16 || params.SaltLength > 64 ||
		params.KeyLength < 32 || params.KeyLength > 64 {
		return ErrInvalidArgon2Params
	}
	return nil
}

type deriveArgon2 func(secret, salt []byte, params Argon2Params) []byte

type argon2Job struct {
	ctx      context.Context
	secret   []byte
	salt     []byte
	params   Argon2Params
	expected []byte
	verify   bool
	response chan argon2Result
}

type argon2Result struct {
	derived []byte
	matched bool
}

// Argon2Service limits expensive derivations to fixed workers plus a bounded in-memory queue.
type Argon2Service struct {
	params    Argon2Params
	dummyHash string
	jobs      chan argon2Job
	derive    deriveArgon2

	stateMu   sync.RWMutex
	closed    bool
	workers   sync.WaitGroup
	closeOnce sync.Once
	closedCh  chan struct{}
}

// NewArgon2Service starts fixed workers; queueCapacity excludes currently running jobs.
func NewArgon2Service(params Argon2Params, workerCount, queueCapacity int) (*Argon2Service, error) {
	return newArgon2Service(params, workerCount, queueCapacity, deriveArgon2ID)
}

func newArgon2Service(params Argon2Params, workerCount, queueCapacity int, derive deriveArgon2) (*Argon2Service, error) {
	if err := params.Validate(); err != nil || workerCount <= 0 || workerCount > maxArgon2Workers ||
		queueCapacity < 0 || queueCapacity > maxArgon2Queue || derive == nil ||
		uint64(workerCount)*uint64(params.MemoryKiB) > maxArgon2AggregateMemoryKiB {
		return nil, ErrInvalidArgon2Params
	}
	dummySalt := make([]byte, params.SaltLength)
	dummyDerived := deriveArgon2ID([]byte("game-night-argon2-dummy-secret"), dummySalt, params)
	service := &Argon2Service{
		params:    params,
		dummyHash: encodePasswordHash(params, dummySalt, dummyDerived),
		jobs:      make(chan argon2Job, queueCapacity),
		derive:    derive,
		closedCh:  make(chan struct{}),
	}
	clear(dummyDerived)
	service.workers.Add(workerCount)
	for range workerCount {
		go service.runWorker()
	}
	return service, nil
}

// Hash derives and PHC-encodes a secret using current parameters and a fresh random salt.
func (service *Argon2Service) Hash(ctx context.Context, secret []byte) (string, error) {
	if !validArgon2Secret(secret) {
		return "", ErrSecretRequired
	}
	salt, err := RandomBytes(int(service.params.SaltLength))
	if err != nil {
		return "", err
	}
	result, err := service.submit(ctx, argon2Job{secret: secret, salt: salt, params: service.params})
	if err != nil {
		return "", err
	}
	defer clear(result.derived)
	return encodePasswordHash(service.params, salt, result.derived), nil
}

// Verify parses bounded PHC parameters, performs one derivation, and reports whether rehashing is required.
func (service *Argon2Service) Verify(ctx context.Context, encoded string, secret []byte) (matched, needsUpgrade bool, err error) {
	if !validArgon2Secret(secret) {
		return false, false, ErrSecretRequired
	}
	parsed, err := parsePasswordHash(encoded)
	if err != nil {
		return false, false, err
	}
	// Stored hashes are untrusted data and may not raise work above this service's provisioned ceiling.
	if parsed.params.MemoryKiB > service.params.MemoryKiB || parsed.params.Iterations > service.params.Iterations ||
		parsed.params.Parallelism > service.params.Parallelism {
		return false, false, ErrInvalidPasswordHash
	}
	result, err := service.submit(ctx, argon2Job{
		secret:   secret,
		salt:     parsed.salt,
		params:   parsed.params,
		expected: parsed.hash,
		verify:   true,
	})
	if err != nil {
		return false, false, err
	}
	return result.matched, parsed.params != service.params, nil
}

// VerifyOrDummy spends the same configured Argon2 cost when a selector has no stored hash.
func (service *Argon2Service) VerifyOrDummy(ctx context.Context, encoded string, secret []byte) (matched, needsUpgrade bool, err error) {
	if encoded == "" {
		_, _, err := service.Verify(ctx, service.dummyHash, secret)
		return false, false, err
	}
	return service.Verify(ctx, encoded, secret)
}

// ValidateArgon2Hash performs bounded PHC syntax, algorithm, version, and cost validation without deriving a secret.
func ValidateArgon2Hash(encoded string) error {
	_, err := parsePasswordHash(encoded)
	return err
}

// Close rejects new work, drains accepted jobs, and waits for all fixed workers to exit.
func (service *Argon2Service) Close() {
	service.closeOnce.Do(func() {
		service.stateMu.Lock()
		service.closed = true
		close(service.jobs)
		service.stateMu.Unlock()
		service.workers.Wait()
		close(service.closedCh)
	})
	<-service.closedCh
}

func (service *Argon2Service) submit(ctx context.Context, job argon2Job) (argon2Result, error) {
	if err := ctx.Err(); err != nil {
		return argon2Result{}, err
	}
	job.secret = bytes.Clone(job.secret)
	job.salt = bytes.Clone(job.salt)
	job.expected = bytes.Clone(job.expected)
	job.ctx = ctx
	job.response = make(chan argon2Result)

	service.stateMu.RLock()
	if service.closed {
		service.stateMu.RUnlock()
		clearArgon2Job(job)
		return argon2Result{}, ErrArgon2Closed
	}
	select {
	case service.jobs <- job:
		service.stateMu.RUnlock()
	case <-ctx.Done():
		service.stateMu.RUnlock()
		clearArgon2Job(job)
		return argon2Result{}, ctx.Err()
	default:
		service.stateMu.RUnlock()
		clearArgon2Job(job)
		return argon2Result{}, ErrArgon2Busy
	}

	select {
	case result := <-job.response:
		return result, nil
	case <-ctx.Done():
		return argon2Result{}, ctx.Err()
	}
}

func (service *Argon2Service) runWorker() {
	defer service.workers.Done()
	for job := range service.jobs {
		// Cancellation while queued must release cloned secrets without spending an Argon2 work slot.
		if job.ctx.Err() != nil {
			clearArgon2Job(job)
			continue
		}
		derived := service.derive(job.secret, job.salt, job.params)
		result := argon2Result{}
		if job.verify {
			result.matched = ConstantTimeEqual(derived, job.expected)
		} else {
			result.derived = bytes.Clone(derived)
		}
		clear(derived)
		clearArgon2Job(job)
		// An unbuffered response lets cancellation win without retaining derived password material in an orphaned channel.
		select {
		case job.response <- result:
		case <-job.ctx.Done():
			clear(result.derived)
		}
	}
}

func clearArgon2Job(job argon2Job) {
	clear(job.secret)
	clear(job.salt)
	clear(job.expected)
}

type parsedPasswordHash struct {
	params Argon2Params
	salt   []byte
	hash   []byte
}

func parsePasswordHash(encoded string) (parsedPasswordHash, error) {
	if len(encoded) == 0 || len(encoded) > maxPasswordHashLength {
		return parsedPasswordHash{}, ErrInvalidPasswordHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return parsedPasswordHash{}, ErrInvalidPasswordHash
	}
	params, err := parseArgon2Costs(parts[3])
	if err != nil {
		return parsedPasswordHash{}, ErrInvalidPasswordHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return parsedPasswordHash{}, ErrInvalidPasswordHash
	}
	hash, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return parsedPasswordHash{}, ErrInvalidPasswordHash
	}
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(hash))
	if err := params.Validate(); err != nil {
		return parsedPasswordHash{}, ErrInvalidPasswordHash
	}
	return parsedPasswordHash{params: params, salt: salt, hash: hash}, nil
}

func parseArgon2Costs(encoded string) (Argon2Params, error) {
	values := strings.Split(encoded, ",")
	if len(values) != 3 {
		return Argon2Params{}, ErrInvalidArgon2Params
	}
	parsed := make(map[string]uint64, 3)
	for _, value := range values {
		pair := strings.SplitN(value, "=", 2)
		if len(pair) != 2 {
			return Argon2Params{}, ErrInvalidArgon2Params
		}
		number, err := strconv.ParseUint(pair[1], 10, 32)
		if err != nil {
			return Argon2Params{}, ErrInvalidArgon2Params
		}
		if _, duplicate := parsed[pair[0]]; duplicate {
			return Argon2Params{}, ErrInvalidArgon2Params
		}
		parsed[pair[0]] = number
	}
	if len(parsed) != 3 || parsed["p"] > 255 {
		return Argon2Params{}, ErrInvalidArgon2Params
	}
	return Argon2Params{
		MemoryKiB:   uint32(parsed["m"]),
		Iterations:  uint32(parsed["t"]),
		Parallelism: uint8(parsed["p"]),
	}, nil
}

func encodePasswordHash(params Argon2Params, salt, hash []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		params.MemoryKiB,
		params.Iterations,
		params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
}

func deriveArgon2ID(secret, salt []byte, params Argon2Params) []byte {
	return argon2.IDKey(secret, salt, params.Iterations, params.MemoryKiB, params.Parallelism, params.KeyLength)
}

func validArgon2Secret(secret []byte) bool {
	return len(secret) > 0 && len(secret) <= maxArgon2SecretBytes
}
