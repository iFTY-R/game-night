package ratelimit_test

import (
	"fmt"
	"testing"

	"github.com/iFTY-R/game-night/platform/ratelimit"
)

func TestBucketValueRequiresNonEmptySensitiveValue(t *testing.T) {
	for _, value := range []string{"", " ", "\t\n"} {
		if _, err := ratelimit.NewBucketValue(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}

	value, err := ratelimit.NewBucketValue("203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if got := value.Reveal(); got != "203.0.113.7" {
		t.Fatalf("unexpected revealed value %q", got)
	}
	if got := fmt.Sprint(value); got != "[REDACTED]" {
		t.Fatalf("sensitive bucket value was formatted as %q", got)
	}
	if got := fmt.Sprintf("%#v", value); got != "[REDACTED]" {
		t.Fatalf("Go-syntax formatting exposed bucket value as %q", got)
	}
}

func TestBucketKeyRequiresKnownDimensionAndValue(t *testing.T) {
	value, err := ratelimit.NewBucketValue("device-1")
	if err != nil {
		t.Fatal(err)
	}

	key, err := ratelimit.NewBucketKey(ratelimit.DimensionDevice, value)
	if err != nil {
		t.Fatal(err)
	}
	if key.Dimension() != ratelimit.DimensionDevice || key.Value().Reveal() != "device-1" {
		t.Fatalf("unexpected bucket key: dimension=%s value=%s", key.Dimension(), key.Value())
	}
	if got := fmt.Sprint(key); got != "device:[REDACTED]" {
		t.Fatalf("bucket key was formatted as %q", got)
	}
	if got := fmt.Sprintf("%#v", key); got != "device:[REDACTED]" {
		t.Fatalf("Go-syntax formatting exposed bucket key as %q", got)
	}

	if _, err := ratelimit.NewBucketKey(ratelimit.Dimension("unknown"), value); err == nil {
		t.Fatal("expected unknown dimension to be rejected")
	}
	if _, err := ratelimit.NewBucketKey(ratelimit.DimensionIP, ratelimit.BucketValue{}); err == nil {
		t.Fatal("expected zero bucket value to be rejected")
	}
}

func TestDimensionsExposeOnlyStableNonSensitiveLabels(t *testing.T) {
	dimensions := []ratelimit.Dimension{
		ratelimit.DimensionIP,
		ratelimit.DimensionDevice,
		ratelimit.DimensionUsername,
		ratelimit.DimensionRecoverySelector,
		ratelimit.DimensionUser,
		ratelimit.DimensionAdminAccount,
		ratelimit.DimensionFlowPurpose,
		ratelimit.DimensionAdminSession,
		ratelimit.DimensionTargetUser,
	}
	for _, dimension := range dimensions {
		if !dimension.Valid() || dimension.String() == "" {
			t.Fatalf("expected valid dimension label for %q", dimension)
		}
	}
	if ratelimit.Dimension("other").Valid() {
		t.Fatal("unknown dimension must not be valid")
	}
}
