package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
)

const testRetention = 24 * time.Hour

func TestNewRequiresBucketAndBoundedPositiveRetention(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	tests := []Config{
		{Retention: testRetention},
		{Bucket: "game-night-audit"},
		{Bucket: "game-night-audit", Retention: -time.Second},
		{Bucket: "game-night-audit", Retention: maximumRetention + 1},
	}
	for _, config := range tests {
		if _, err := New(client, config); !errors.Is(err, objectstorage.ErrInvalidInput) {
			t.Fatalf("New(%+v) error = %v, want ErrInvalidInput", config, err)
		}
	}
}

func TestSinkCreatesConditionallyAndVerifiesReadBack(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	sink := newTestSink(t, client)
	object := testObject(t, "audit/checkpoints/00000001.pb", "checkpoint")
	client.expectedKey = object.Key().String()
	if err := sink.Write(context.Background(), object); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.putCalls != 1 || client.headCalls != 1 || client.getCalls != 1 {
		t.Fatalf("calls = put:%d head:%d get:%d, want one each", client.putCalls, client.headCalls, client.getCalls)
	}
	if client.lastIfNoneMatch != "*" {
		t.Fatalf("IfNoneMatch = %q, want *", client.lastIfNoneMatch)
	}
	if client.lastChecksum == "" {
		t.Fatal("PutObject must send an SDK-verified SHA-256 checksum")
	}
	if client.contractErr != nil {
		t.Fatalf("PutObject contract error = %v", client.contractErr)
	}
}

func TestSinkConcurrentExistingObjectIsIdempotentOnlyForSameContent(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	sink := newTestSink(t, client)
	original := testObject(t, "audit/checkpoints/00000002.pb", "original")
	if err := sink.Write(context.Background(), original); err != nil {
		t.Fatal(err)
	}

	const writers = 12
	errorsByWriter := make(chan error, writers)
	var group sync.WaitGroup
	for range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsByWriter <- sink.Write(context.Background(), original)
		}()
	}
	group.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("same-content Write() error = %v", err)
		}
	}

	different := testObject(t, original.Key().String(), "different")
	if err := sink.Write(context.Background(), different); !errors.Is(err, objectstorage.ErrIntegrity) {
		t.Fatalf("different-content Write() error = %v, want ErrIntegrity", err)
	}
}

func TestSinkRejectsMismatchedReadBackAfterSuccessfulPut(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*fakeClient)
	}{
		{name: "partial body", mutate: func(client *fakeClient) { client.truncateAfterPut = true }},
		{name: "metadata", mutate: func(client *fakeClient) { client.changeMetadataAfterPut = true }},
		{name: "missing retention", mutate: func(client *fakeClient) { client.missingRetentionAfterPut = true }},
		{name: "expired retention", mutate: func(client *fakeClient) { client.expireRetentionAfterPut = true }},
		{name: "short retention", mutate: func(client *fakeClient) { client.shortenRetentionAfterPut = true }},
		{name: "wrong retention mode", mutate: func(client *fakeClient) { client.changeRetentionModeAfterPut = true }},
		{name: "missing last modified", mutate: func(client *fakeClient) { client.missingLastModifiedAfterPut = true }},
		{name: "get missing retention", mutate: func(client *fakeClient) { client.missingGetRetention = true }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := newFakeClient()
			test.mutate(client)
			sink := newTestSink(t, client)
			err := sink.Write(context.Background(), testObject(t, "audit/checkpoints/mismatch.pb", "checkpoint"))
			if !errors.Is(err, objectstorage.ErrIntegrity) {
				t.Fatalf("Write() error = %v, want ErrIntegrity", err)
			}
		})
	}
}

func TestSinkMapsClientErrorsWithoutLeakingDetails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*fakeClient)
	}{
		{name: "put", configure: func(client *fakeClient) { client.putErr = errors.New("internal endpoint hostname") }},
		{name: "head after precondition", configure: func(client *fakeClient) {
			client.forcePrecondition = true
			client.headErr = errors.New("private bucket detail")
		}},
		{name: "get", configure: func(client *fakeClient) { client.getErr = errors.New("provider request id") }},
		{name: "malformed HTTP response", configure: func(client *fakeClient) {
			client.putErr = &smithyhttp.ResponseError{Err: errors.New("missing response")}
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := newFakeClient()
			test.configure(client)
			sink := newTestSink(t, client)
			err := sink.Write(context.Background(), testObject(t, "audit/checkpoints/error.pb", "checkpoint"))
			if !errors.Is(err, objectstorage.ErrUnavailable) || err.Error() != objectstorage.ErrUnavailable.Error() {
				t.Fatalf("Write() error = %v, want sanitized ErrUnavailable", err)
			}
		})
	}
}

func TestSinkPreservesContextErrors(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	sink := newTestSink(t, client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sink.Write(ctx, testObject(t, "audit/checkpoints/canceled.pb", "checkpoint")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Write() error = %v, want context.Canceled", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.putCalls != 0 {
		t.Fatalf("PutObject calls = %d, want zero", client.putCalls)
	}
}

func TestSinkPreservesClientContextError(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	client.putErr = errors.Join(context.DeadlineExceeded, errors.New("private endpoint detail"))
	sink := newTestSink(t, client)
	err := sink.Write(context.Background(), testObject(t, "audit/checkpoints/deadline.pb", "checkpoint"))
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != context.DeadlineExceeded.Error() {
		t.Fatalf("Write() error = %v, want sanitized context.DeadlineExceeded", err)
	}
}

func TestSinkProductionReadinessRequiresDefaultObjectLockRetention(t *testing.T) {
	t.Parallel()

	positiveDays := int32(7)
	positiveYears := int32(1)
	zero := int32(0)
	tests := []struct {
		name          string
		configuration *types.ObjectLockConfiguration
		clientError   error
		wantError     error
	}{
		{
			name:          "governance days",
			configuration: lockConfiguration(types.ObjectLockRetentionModeGovernance, &positiveDays, nil),
			wantError:     objectstorage.ErrProductionReadiness,
		},
		{
			name:          "compliance years",
			configuration: lockConfiguration(types.ObjectLockRetentionModeCompliance, nil, &positiveYears),
		},
		{name: "disabled", configuration: &types.ObjectLockConfiguration{}, wantError: objectstorage.ErrProductionReadiness},
		{name: "missing rule", configuration: &types.ObjectLockConfiguration{ObjectLockEnabled: types.ObjectLockEnabledEnabled}, wantError: objectstorage.ErrProductionReadiness},
		{name: "zero period", configuration: lockConfiguration(types.ObjectLockRetentionModeGovernance, &zero, nil), wantError: objectstorage.ErrProductionReadiness},
		{name: "ambiguous period", configuration: lockConfiguration(types.ObjectLockRetentionModeGovernance, &positiveDays, &positiveYears), wantError: objectstorage.ErrProductionReadiness},
		{name: "client unavailable", clientError: errors.New("bucket host"), wantError: objectstorage.ErrUnavailable},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := newFakeClient()
			client.lockConfiguration = test.configuration
			client.lockErr = test.clientError
			sink := newTestSink(t, client)
			err := sink.CheckProductionReady(context.Background())
			if !errors.Is(err, test.wantError) {
				t.Fatalf("CheckProductionReady() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

type storedObject struct {
	body         []byte
	metadata     map[string]string
	mode         types.ObjectLockMode
	retainUntil  *time.Time
	lastModified *time.Time
}

type fakeClient struct {
	mu                          sync.Mutex
	objects                     map[string]storedObject
	putCalls                    int
	headCalls                   int
	getCalls                    int
	lastIfNoneMatch             string
	lastChecksum                string
	expectedKey                 string
	expectedMetadata            map[string]string
	contractErr                 error
	putErr                      error
	headErr                     error
	getErr                      error
	lockErr                     error
	forcePrecondition           bool
	truncateAfterPut            bool
	changeMetadataAfterPut      bool
	missingRetentionAfterPut    bool
	expireRetentionAfterPut     bool
	shortenRetentionAfterPut    bool
	changeRetentionModeAfterPut bool
	missingLastModifiedAfterPut bool
	missingGetRetention         bool
	lockConfiguration           *types.ObjectLockConfiguration
}

func newFakeClient() *fakeClient {
	days := int32(7)
	return &fakeClient{
		objects:           make(map[string]storedObject),
		expectedMetadata:  map[string]string{"chain-sequence": "1"},
		lockConfiguration: lockConfiguration(types.ObjectLockRetentionModeCompliance, &days, nil),
	}
}

func (client *fakeClient) PutObject(_ context.Context, input *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.putCalls++
	client.lastIfNoneMatch = aws.ToString(input.IfNoneMatch)
	client.lastChecksum = aws.ToString(input.ChecksumSHA256)
	body, readErr := io.ReadAll(input.Body)
	if readErr != nil {
		return nil, readErr
	}
	if contractErr := client.validatePut(input, body); contractErr != nil {
		client.contractErr = contractErr
		return nil, contractErr
	}
	if client.putErr != nil {
		return nil, client.putErr
	}
	key := aws.ToString(input.Key)
	if client.forcePrecondition {
		return nil, preconditionError()
	}
	if _, exists := client.objects[key]; exists {
		return nil, preconditionError()
	}
	metadata := cloneMetadata(input.Metadata)
	lastModified := time.Now().UTC().Truncate(s3TimestampPrecision)
	retainUntil := input.ObjectLockRetainUntilDate.UTC().Truncate(s3TimestampPrecision)
	mode := input.ObjectLockMode
	if client.truncateAfterPut {
		body = body[:len(body)-1]
	}
	if client.changeMetadataAfterPut {
		metadata["chain-sequence"] = "tampered"
	}
	if client.missingRetentionAfterPut {
		retainUntil = time.Time{}
	}
	if client.expireRetentionAfterPut {
		retainUntil = lastModified.Add(-time.Hour)
	}
	if client.shortenRetentionAfterPut {
		retainUntil = lastModified.Add(testRetention - 2*s3TimestampPrecision)
	}
	if client.changeRetentionModeAfterPut {
		mode = types.ObjectLockModeGovernance
	}
	object := storedObject{
		body:         body,
		metadata:     metadata,
		mode:         mode,
		retainUntil:  &retainUntil,
		lastModified: &lastModified,
	}
	if client.missingRetentionAfterPut {
		object.retainUntil = nil
	}
	if client.missingLastModifiedAfterPut {
		object.lastModified = nil
	}
	client.objects[key] = object
	return &awss3.PutObjectOutput{}, nil
}

func (client *fakeClient) HeadObject(_ context.Context, input *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.headCalls++
	if client.headErr != nil {
		return nil, client.headErr
	}
	object, exists := client.objects[aws.ToString(input.Key)]
	if !exists {
		return nil, errors.New("not found")
	}
	length := int64(len(object.body))
	return &awss3.HeadObjectOutput{
		ContentLength:             &length,
		LastModified:              cloneTime(object.lastModified),
		Metadata:                  cloneMetadata(object.metadata),
		ObjectLockMode:            object.mode,
		ObjectLockRetainUntilDate: cloneTime(object.retainUntil),
	}, nil
}

func (client *fakeClient) GetObject(_ context.Context, input *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.getCalls++
	if client.getErr != nil {
		return nil, client.getErr
	}
	object, exists := client.objects[aws.ToString(input.Key)]
	if !exists {
		return nil, errors.New("not found")
	}
	length := int64(len(object.body))
	retainUntil := cloneTime(object.retainUntil)
	if client.missingGetRetention {
		retainUntil = nil
	}
	return &awss3.GetObjectOutput{
		Body:                      io.NopCloser(bytes.NewReader(append([]byte(nil), object.body...))),
		ContentLength:             &length,
		LastModified:              cloneTime(object.lastModified),
		Metadata:                  cloneMetadata(object.metadata),
		ObjectLockMode:            object.mode,
		ObjectLockRetainUntilDate: retainUntil,
	}, nil
}

func (client *fakeClient) GetObjectLockConfiguration(_ context.Context, _ *awss3.GetObjectLockConfigurationInput, _ ...func(*awss3.Options)) (*awss3.GetObjectLockConfigurationOutput, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.lockErr != nil {
		return nil, client.lockErr
	}
	return &awss3.GetObjectLockConfigurationOutput{ObjectLockConfiguration: client.lockConfiguration}, nil
}

func newTestSink(t *testing.T, client Client) *Sink {
	t.Helper()
	sink, err := New(client, Config{Bucket: "game-night-audit", Retention: testRetention})
	if err != nil {
		t.Fatal(err)
	}
	return sink
}

func (client *fakeClient) validatePut(input *awss3.PutObjectInput, body []byte) error {
	if aws.ToString(input.Bucket) != "game-night-audit" {
		return errors.New("unexpected bucket")
	}
	key := aws.ToString(input.Key)
	if _, err := objectstorage.NewKey(key); err != nil || client.expectedKey != "" && key != client.expectedKey {
		return errors.New("unexpected object key")
	}
	if aws.ToString(input.IfNoneMatch) != "*" {
		return errors.New("missing create-only condition")
	}
	if input.ContentLength == nil || *input.ContentLength != int64(len(body)) {
		return errors.New("content length does not match body")
	}
	digest := sha256.Sum256(body)
	if aws.ToString(input.ChecksumSHA256) != base64.StdEncoding.EncodeToString(digest[:]) {
		return errors.New("checksum does not match body")
	}
	expectedMetadata := cloneMetadata(client.expectedMetadata)
	expectedMetadata[objectstorage.ContentSHA256MetadataKey] = hex.EncodeToString(digest[:])
	if !equalMetadata(input.Metadata, expectedMetadata) {
		return errors.New("metadata does not match object contract")
	}
	if input.ObjectLockMode != types.ObjectLockModeCompliance || input.ObjectLockRetainUntilDate == nil {
		return errors.New("missing compliance retention headers")
	}
	minimumRetainUntil := time.Now().UTC().Add(testRetention)
	if input.ObjectLockRetainUntilDate.Add(s3TimestampPrecision).Before(minimumRetainUntil) {
		return errors.New("retention header is too short")
	}
	return nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func testObject(t *testing.T, keyValue, body string) objectstorage.Object {
	t.Helper()
	key, err := objectstorage.NewKey(keyValue)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := objectstorage.NewMetadata(map[string]string{"chain-sequence": "1"})
	if err != nil {
		t.Fatal(err)
	}
	object, err := objectstorage.NewObject(key, []byte(body), metadata)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func preconditionError() error {
	return &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "object exists", Fault: smithy.FaultClient}
}

func lockConfiguration(mode types.ObjectLockRetentionMode, days, years *int32) *types.ObjectLockConfiguration {
	return &types.ObjectLockConfiguration{
		ObjectLockEnabled: types.ObjectLockEnabledEnabled,
		Rule: &types.ObjectLockRule{DefaultRetention: &types.DefaultRetention{
			Mode:  mode,
			Days:  days,
			Years: years,
		}},
	}
}
