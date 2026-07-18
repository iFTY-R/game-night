package clock

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSystemNowReturnsUTC(t *testing.T) {
	before := time.Now().UTC()
	got := (System{}).Now()
	after := time.Now().UTC()

	if got.Location() != time.UTC {
		t.Fatalf("system clock location = %v, want UTC", got.Location())
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("system clock returned %s outside [%s, %s]", got, before, after)
	}
}

func TestFakeNormalizesAndAdvancesUTC(t *testing.T) {
	location := time.FixedZone("test-offset", 8*60*60)
	start := time.Date(2026, time.July, 18, 20, 30, 0, 123, location)
	fake := NewFake(start)

	if got, want := fake.Now(), start.UTC(); !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("fake clock now = %v, want UTC %v", got, want)
	}

	got, err := fake.Advance(90 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if want := start.UTC().Add(90 * time.Second); !got.Equal(want) || !fake.Now().Equal(want) {
		t.Fatalf("advanced time = %v, want %v", got, want)
	}
}

func TestFakeRejectsBackwardAdvanceWithoutMutation(t *testing.T) {
	start := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	fake := NewFake(start)

	_, err := fake.Advance(-time.Nanosecond)
	if !errors.Is(err, ErrNegativeAdvance) {
		t.Fatalf("advance error = %v, want ErrNegativeAdvance", err)
	}
	if got := fake.Now(); !got.Equal(start) {
		t.Fatalf("failed advance changed time to %v", got)
	}
}

func TestFakeAdvanceIsConcurrentSafe(t *testing.T) {
	const goroutines = 32
	const advancesPerGoroutine = 200

	start := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	fake := NewFake(start)
	var waitGroup sync.WaitGroup
	waitGroup.Add(goroutines)

	for range goroutines {
		go func() {
			defer waitGroup.Done()
			for range advancesPerGoroutine {
				if _, err := fake.Advance(time.Millisecond); err != nil {
					t.Errorf("advance: %v", err)
					return
				}
				_ = fake.Now()
			}
		}()
	}
	waitGroup.Wait()

	want := start.Add(goroutines * advancesPerGoroutine * time.Millisecond)
	if got := fake.Now(); !got.Equal(want) {
		t.Fatalf("concurrent advances produced %v, want %v", got, want)
	}
}
