package application

import (
	"errors"
	"testing"
	"time"
)

func TestDatabaseClockUsesRoundTripMidpointAndRejectsUnsafeSkew(t *testing.T) {
	startedAt := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(20 * time.Millisecond)
	databaseNow := startedAt.Add(2*time.Second + 10*time.Millisecond)
	source, err := databaseClockFromSamples(startedAt, databaseNow, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	calibrated, ok := source.(databaseClock)
	if !ok || calibrated.offset != 2*time.Second {
		t.Fatalf("database clock offset = %v, type=%T", calibrated.offset, source)
	}

	for name, observed := range map[string]time.Time{
		"ahead":  startedAt.Add(maximumDatabaseClockSkew + time.Second),
		"behind": startedAt.Add(-maximumDatabaseClockSkew - time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := databaseClockFromSamples(startedAt, observed, startedAt); !errors.Is(err, errInitializeClock) {
				t.Fatalf("unsafe database clock error = %v", err)
			}
		})
	}
	if _, err := databaseClockFromSamples(finishedAt, databaseNow, startedAt); !errors.Is(err, errInitializeClock) {
		t.Fatalf("reversed observation window error = %v", err)
	}
}
