// Package checkpointstorage loads and constructs the process-owned append-only audit checkpoint sink.
package checkpointstorage

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
	"github.com/iFTY-R/game-night/platform/persistence/objectstorage/local"
	worms3 "github.com/iFTY-R/game-night/platform/persistence/objectstorage/s3"
)

const (
	checkpointSinkEnvironment           = "GAME_NIGHT_CHECKPOINT_SINK"
	checkpointLocalDirectoryEnvironment = "GAME_NIGHT_CHECKPOINT_LOCAL_DIRECTORY"
	checkpointS3RegionEnvironment       = "GAME_NIGHT_CHECKPOINT_S3_REGION"
	checkpointS3BucketEnvironment       = "GAME_NIGHT_CHECKPOINT_S3_BUCKET"
	checkpointS3EndpointEnvironment     = "GAME_NIGHT_CHECKPOINT_S3_ENDPOINT"
	checkpointS3RetentionEnvironment    = "GAME_NIGHT_CHECKPOINT_S3_RETENTION"
	// Retention is explicit because shortening it changes the immutable audit guarantee.
	maximumRetention = 100 * 365 * 24 * time.Hour
)

var (
	// ErrInvalidConfig never includes configured values because endpoints and paths can reveal deployment details.
	ErrInvalidConfig = errors.New("invalid checkpoint storage configuration")
	// ErrBuildSink hides AWS credential, endpoint, and filesystem details from process logs.
	ErrBuildSink  = errors.New("initialize checkpoint storage")
	regionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)
)

// SinkKind selects the only two supported durability policies.
type SinkKind string

const (
	// SinkLocal is an append-only process-local development sink without privileged deletion resistance.
	SinkLocal SinkKind = "local"
	// SinkS3 requires create-only writes, exact read-back, and Object Lock Compliance retention.
	SinkS3 SinkKind = "s3"
)

// S3Config contains non-secret target settings. Credentials stay in the AWS SDK provider chain.
type S3Config struct {
	Region    string
	Bucket    string
	Endpoint  string
	Retention time.Duration
}

// Config is shared by the API readiness probe and the worker writer so both processes evaluate the same sink policy.
type Config struct {
	Kind           SinkKind
	LocalDirectory string
	S3             S3Config
}

// Load validates one mutually exclusive sink configuration without opening files or network clients.
func Load(lookup sharedconfig.LookupEnv, environment sharedconfig.Environment) (Config, error) {
	if lookup == nil {
		return Config{}, ErrInvalidConfig
	}
	read := func(name string) string {
		value, _ := lookup(name)
		return strings.TrimSpace(value)
	}
	kind := SinkKind(read(checkpointSinkEnvironment))
	switch kind {
	case SinkLocal:
		if environment == sharedconfig.EnvironmentProduction {
			return Config{}, ErrInvalidConfig
		}
		directory := read(checkpointLocalDirectoryEnvironment)
		if directory == "" || read(checkpointS3RegionEnvironment) != "" || read(checkpointS3BucketEnvironment) != "" ||
			read(checkpointS3EndpointEnvironment) != "" || read(checkpointS3RetentionEnvironment) != "" {
			return Config{}, ErrInvalidConfig
		}
		absolute, err := filepath.Abs(directory)
		if err != nil || filepath.Clean(absolute) != absolute {
			return Config{}, ErrInvalidConfig
		}
		return Config{Kind: SinkLocal, LocalDirectory: absolute}, nil
	case SinkS3:
		if read(checkpointLocalDirectoryEnvironment) != "" {
			return Config{}, ErrInvalidConfig
		}
		region := read(checkpointS3RegionEnvironment)
		bucket := read(checkpointS3BucketEnvironment)
		retention, err := time.ParseDuration(read(checkpointS3RetentionEnvironment))
		if !regionPattern.MatchString(region) || !validBucket(bucket) || err != nil || retention <= 0 || retention > maximumRetention {
			return Config{}, ErrInvalidConfig
		}
		endpoint := read(checkpointS3EndpointEnvironment)
		if endpoint != "" && !validEndpoint(endpoint, environment == sharedconfig.EnvironmentProduction) {
			return Config{}, ErrInvalidConfig
		}
		return Config{Kind: SinkS3, S3: S3Config{
			Region: region, Bucket: bucket, Endpoint: endpoint, Retention: retention,
		}}, nil
	default:
		return Config{}, ErrInvalidConfig
	}
}

// Build creates the configured sink. AWS credentials are resolved only through the standard SDK provider chain.
func Build(ctx context.Context, config Config) (objectstorage.Sink, error) {
	if ctx == nil {
		return nil, ErrBuildSink
	}
	switch config.Kind {
	case SinkLocal:
		sink, err := local.New(config.LocalDirectory)
		if err != nil {
			return nil, ErrBuildSink
		}
		return sink, nil
	case SinkS3:
		awsConfiguration, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.S3.Region))
		if err != nil {
			return nil, ErrBuildSink
		}
		client := awss3.NewFromConfig(awsConfiguration, func(options *awss3.Options) {
			if config.S3.Endpoint != "" {
				options.BaseEndpoint = aws.String(config.S3.Endpoint)
				options.UsePathStyle = true
			}
		})
		sink, err := worms3.New(client, worms3.Config{Bucket: config.S3.Bucket, Retention: config.S3.Retention})
		if err != nil {
			return nil, ErrBuildSink
		}
		return sink, nil
	default:
		return nil, ErrBuildSink
	}
}

func validEndpoint(value string, requireHTTPS bool) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return !requireHTTPS || parsed.Scheme == "https"
}

func validBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.Trim(value, ".-") != value {
		return false
	}
	for index := range len(value) {
		current := value[index]
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '.' || current == '-' {
			continue
		}
		return false
	}
	return !strings.Contains(value, "..")
}
