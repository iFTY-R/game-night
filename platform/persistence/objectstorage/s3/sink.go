// Package s3 provides a create-only WORM checkpoint sink backed by Amazon S3
// or a compatible service implementing conditional PutObject and Object Lock.
package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
)

const (
	// s3TimestampPrecision is the documented LastModified granularity used when comparing retention observations.
	s3TimestampPrecision = time.Second
	// maximumRetention leaves room for the precision compensation added to every explicit retain-until value.
	maximumRetention = time.Duration(1<<63-1) - s3TimestampPrecision
)

// Client is the narrow AWS SDK surface required by the create-only sink. It
// intentionally excludes every overwrite, copy, retention mutation, and delete API.
type Client interface {
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	HeadObject(context.Context, *awss3.HeadObjectInput, ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	GetObjectLockConfiguration(context.Context, *awss3.GetObjectLockConfigurationInput, ...func(*awss3.Options)) (*awss3.GetObjectLockConfigurationOutput, error)
}

// Sink conditionally creates deterministic objects and verifies the remote body
// and metadata after both successful creation and already-existing responses.
type Sink struct {
	client    Client
	bucket    string
	retention time.Duration
}

// Config binds every created object to a caller-selected minimum retention period.
type Config struct {
	Bucket    string
	Retention time.Duration
}

// New validates the immutable target bucket and mandatory retention policy.
func New(client Client, config Config) (*Sink, error) {
	if client == nil || !validBucket(config.Bucket) || config.Retention <= 0 || config.Retention > maximumRetention {
		return nil, objectstorage.ErrInvalidInput
	}
	return &Sink{client: client, bucket: config.Bucket, retention: config.Retention}, nil
}

// Write uses If-None-Match: * so no application request can replace an existing
// checkpoint. A precondition failure is resolved only by exact read-back verification.
func (sink *Sink) Write(ctx context.Context, object objectstorage.Object) error {
	if sink == nil || sink.client == nil || !validBucket(sink.bucket) || sink.retention <= 0 || !object.Valid() {
		return objectstorage.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	payload := object.Content()
	digest := object.SHA256()
	metadata := verificationMetadata(object)
	// The extra precision unit compensates for S3's second-granularity LastModified
	// while read-back still enforces at least the configured duration.
	retainUntil := time.Now().UTC().Add(sink.retention + s3TimestampPrecision)
	_, err := sink.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:                    aws.String(sink.bucket),
		Key:                       aws.String(object.Key().String()),
		Body:                      bytes.NewReader(payload),
		ContentLength:             aws.Int64(int64(len(payload))),
		ChecksumSHA256:            aws.String(base64.StdEncoding.EncodeToString(digest[:])),
		IfNoneMatch:               aws.String("*"),
		Metadata:                  cloneMetadata(metadata),
		ObjectLockMode:            types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: &retainUntil,
	})
	if err != nil && !isPreconditionFailure(err) {
		return mapClientError(ctx, err)
	}
	return sink.verify(ctx, object, payload, metadata)
}

// CheckProductionReady verifies that the bucket has Object Lock enabled and a
// valid default retention mode and period for all newly created objects.
func (sink *Sink) CheckProductionReady(ctx context.Context) error {
	if sink == nil || sink.client == nil || !validBucket(sink.bucket) || sink.retention <= 0 {
		return objectstorage.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	output, err := sink.client.GetObjectLockConfiguration(ctx, &awss3.GetObjectLockConfigurationInput{
		Bucket: aws.String(sink.bucket),
	})
	if err != nil {
		return mapClientError(ctx, err)
	}
	if !validObjectLockConfiguration(output) {
		return objectstorage.ErrProductionReadiness
	}
	return nil
}

func (sink *Sink) verify(ctx context.Context, object objectstorage.Object, expectedBody []byte, expectedMetadata map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := object.Key().String()
	head, err := sink.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(sink.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return mapClientError(ctx, err)
	}
	now := time.Now().UTC()
	if head == nil || head.ContentLength == nil || *head.ContentLength != int64(len(expectedBody)) ||
		!equalMetadata(head.Metadata, expectedMetadata) ||
		!validStoredRetention(head.ObjectLockMode, head.LastModified, head.ObjectLockRetainUntilDate, sink.retention, now) {
		return objectstorage.ErrIntegrity
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	result, err := sink.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(sink.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return mapClientError(ctx, err)
	}
	if result == nil || result.Body == nil {
		return objectstorage.ErrIntegrity
	}
	if result.ContentLength == nil || *result.ContentLength != int64(len(expectedBody)) {
		_ = result.Body.Close()
		return objectstorage.ErrIntegrity
	}
	if !equalMetadata(result.Metadata, expectedMetadata) ||
		!validStoredRetention(result.ObjectLockMode, result.LastModified, result.ObjectLockRetainUntilDate, sink.retention, now) ||
		!sameS3Time(head.LastModified, result.LastModified) ||
		!sameS3Time(head.ObjectLockRetainUntilDate, result.ObjectLockRetainUntilDate) {
		_ = result.Body.Close()
		return objectstorage.ErrIntegrity
	}

	actual, readErr := io.ReadAll(io.LimitReader(result.Body, int64(len(expectedBody))+1))
	closeErr := result.Body.Close()
	if readErr != nil {
		return mapClientError(ctx, readErr)
	}
	if closeErr != nil {
		return mapClientError(ctx, closeErr)
	}
	actualDigest := sha256.Sum256(actual)
	expectedDigest := object.SHA256()
	if len(actual) != len(expectedBody) || subtle.ConstantTimeCompare(actualDigest[:], expectedDigest[:]) != 1 ||
		!bytes.Equal(actual, expectedBody) {
		return objectstorage.ErrIntegrity
	}
	return nil
}

func verificationMetadata(object objectstorage.Object) map[string]string {
	metadata := object.Metadata()
	digest := object.SHA256()
	metadata[objectstorage.ContentSHA256MetadataKey] = hex.EncodeToString(digest[:])
	return metadata
}

func equalMetadata(actual, expected map[string]string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for key, expectedValue := range expected {
		actualValue, exists := actual[strings.ToLower(key)]
		if !exists || actualValue != expectedValue {
			return false
		}
	}
	return true
}

func cloneMetadata(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func validBucket(bucket string) bool {
	if bucket == "" || len(bucket) > 255 || strings.TrimSpace(bucket) != bucket {
		return false
	}
	for index := range len(bucket) {
		if bucket[index] <= 0x20 || bucket[index] == 0x7f {
			return false
		}
	}
	return true
}

func validObjectLockConfiguration(output *awss3.GetObjectLockConfigurationOutput) bool {
	if output == nil || output.ObjectLockConfiguration == nil {
		return false
	}
	configuration := output.ObjectLockConfiguration
	if configuration.ObjectLockEnabled != types.ObjectLockEnabledEnabled || configuration.Rule == nil ||
		configuration.Rule.DefaultRetention == nil {
		return false
	}
	retention := configuration.Rule.DefaultRetention
	if retention.Mode != types.ObjectLockRetentionModeCompliance {
		return false
	}
	if (retention.Days == nil) == (retention.Years == nil) {
		return false
	}
	if retention.Days != nil {
		return *retention.Days > 0
	}
	return *retention.Years > 0
}

func validStoredRetention(
	mode types.ObjectLockMode,
	lastModified *time.Time,
	retainUntil *time.Time,
	minimumRetention time.Duration,
	now time.Time,
) bool {
	if mode != types.ObjectLockModeCompliance || lastModified == nil || retainUntil == nil ||
		minimumRetention <= 0 || !retainUntil.After(now) || lastModified.After(now.Add(s3TimestampPrecision)) {
		return false
	}
	minimumRetainUntil := lastModified.Add(minimumRetention)
	return !retainUntil.Add(s3TimestampPrecision).Before(minimumRetainUntil)
}

func sameS3Time(left, right *time.Time) bool {
	if left == nil || right == nil {
		return false
	}
	difference := left.Sub(*right)
	if difference < 0 {
		difference = -difference
	}
	return difference <= s3TimestampPrecision
}

func isPreconditionFailure(err error) bool {
	var apiError smithy.APIError
	if errors.As(err, &apiError) && (apiError.ErrorCode() == "PreconditionFailed" || apiError.ErrorCode() == "412") {
		return true
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.Response != nil && responseError.Response.Response != nil &&
		responseError.HTTPStatusCode() == http.StatusPreconditionFailed
}

func mapClientError(ctx context.Context, err error) error {
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return objectstorage.ErrUnavailable
}
