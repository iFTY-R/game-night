package s3

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
)

const (
	// S3 integration settings stay explicit so requiring object-storage cannot fall back to ambient AWS credentials.
	testS3EndpointEnvironment  = "GAME_NIGHT_TEST_S3_ENDPOINT"
	testS3RegionEnvironment    = "GAME_NIGHT_TEST_S3_REGION"
	testS3BucketEnvironment    = "GAME_NIGHT_TEST_S3_BUCKET"
	testS3AccessKeyEnvironment = "GAME_NIGHT_TEST_S3_ACCESS_KEY"
	testS3SecretKeyEnvironment = "GAME_NIGHT_TEST_S3_SECRET_KEY"
)

func TestSinkAgainstRealObjectLockService(t *testing.T) {
	values := integrationtest.RequireEnvironment(t, integrationtest.DependencyObjectStorage,
		testS3EndpointEnvironment, testS3RegionEnvironment, testS3BucketEnvironment,
		testS3AccessKeyEnvironment, testS3SecretKeyEnvironment)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := newIntegrationS3Client(t, ctx, values[0], values[1], values[3], values[4])
	sink, err := New(client, Config{Bucket: values[2], Retention: testRetention})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.CheckProductionReady(ctx); err != nil {
		t.Fatalf("Object Lock Compliance readiness failed: %v", err)
	}

	keyValue := "audit/integration/" + uuid.NewString() + ".checkpoint"
	original := integrationObject(t, keyValue, "immutable-checkpoint")
	if err := sink.Write(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(ctx, original); err != nil {
		t.Fatalf("same-content retry failed: %v", err)
	}
	if err := sink.Write(ctx, integrationObject(t, keyValue, "different-checkpoint")); !errors.Is(err, objectstorage.ErrIntegrity) {
		t.Fatalf("different-content overwrite error = %v, want ErrIntegrity", err)
	}

	head, err := client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(values[2]), Key: aws.String(keyValue)})
	if err != nil {
		t.Fatal(err)
	}
	if head.VersionId == nil || *head.VersionId == "" {
		t.Fatal("Object Lock object is missing a version ID")
	}
	retention, err := client.GetObjectRetention(ctx, &awss3.GetObjectRetentionInput{
		Bucket: aws.String(values[2]), Key: aws.String(keyValue), VersionId: head.VersionId,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retention.Retention == nil || retention.Retention.Mode != types.ObjectLockRetentionModeCompliance ||
		retention.Retention.RetainUntilDate == nil || !retention.Retention.RetainUntilDate.After(time.Now().UTC()) {
		t.Fatalf("stored Object Lock retention is not active Compliance mode: %+v", retention.Retention)
	}
	// Delete the exact retained version: deleting an unversioned key could only create a delete marker and would not test WORM.
	if _, err := client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(values[2]), Key: aws.String(keyValue), VersionId: head.VersionId,
	}); err == nil {
		t.Fatal("Object Lock allowed deletion of the retained checkpoint version")
	}
}

// newIntegrationS3Client disables ambient credentials and uses path addressing for local S3-compatible services.
func newIntegrationS3Client(t testing.TB, ctx context.Context, endpoint, region, accessKey, secretKey string) *awss3.Client {
	t.Helper()
	configuration, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return awss3.NewFromConfig(configuration, func(options *awss3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
}

// integrationObject creates one valid checkpoint whose digest and metadata must survive remote read-back.
func integrationObject(t testing.TB, keyValue, content string) objectstorage.Object {
	t.Helper()
	key, err := objectstorage.NewKey(keyValue)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := objectstorage.NewMetadata(map[string]string{"integration": "object-lock"})
	if err != nil {
		t.Fatal(err)
	}
	object, err := objectstorage.NewObject(key, []byte(content), metadata)
	if err != nil {
		t.Fatal(err)
	}
	return object
}
