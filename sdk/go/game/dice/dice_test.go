package dice

import (
	"errors"
	"testing"
)

func TestRollIsDeterministicAndPrefixStable(t *testing.T) {
	seed := [32]byte{1, 2, 3, 4}
	short, err := Roll(seed, 7, 8)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := Roll(seed, 7, 8)
	if err != nil {
		t.Fatal(err)
	}
	long, err := Roll(seed, 7, 16)
	if err != nil {
		t.Fatal(err)
	}
	for index := range short {
		if short[index] != repeat[index] || short[index] != long[index] || !short[index].Valid() {
			t.Fatalf("roll mismatch at %d: short=%v repeat=%v long=%v", index, short[index], repeat[index], long[index])
		}
	}
}

func TestRollRejectsZeroSeedAndInvalidCount(t *testing.T) {
	if _, err := Roll([32]byte{}, 1, 1); !errors.Is(err, ErrZeroSeed) {
		t.Fatalf("zero seed error = %v", err)
	}
	seed := [32]byte{1}
	for _, count := range []uint32{0, MaxDicePerRoll + 1} {
		if _, err := Roll(seed, 1, count); !errors.Is(err, ErrInvalidCount) {
			t.Fatalf("count %d error = %v", count, err)
		}
	}
}

func TestTicksUseIntegerArithmeticAndFillRemainders(t *testing.T) {
	if got := HalfFloor(5); got != 2 {
		t.Fatalf("floor half = %d", got)
	}
	if got := HalfCeil(5); got != 3 {
		t.Fatalf("ceil half = %d", got)
	}
	if got, err := NextAddAmount(8, 7, 2); err != nil || got != 1 {
		t.Fatalf("remainder add = %d, %v", got, err)
	}
	if _, err := Ticks(^uint32(0)).Add(1); !errors.Is(err, ErrTicksOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
	if _, err := Ticks(1).Sub(2); !errors.Is(err, ErrTicksUnderflow) {
		t.Fatalf("underflow error = %v", err)
	}
}

func TestSeatsCanonicalizeAndAdvanceInBothDirections(t *testing.T) {
	seats, err := CanonicalSeats([]uint32{4, 1, 3})
	if err != nil || len(seats) != 3 || seats[0] != 1 {
		t.Fatalf("canonical seats = %#v, %v", seats, err)
	}
	if next, err := Next(seats, 3, DirectionClockwise); err != nil || next != 4 {
		t.Fatalf("clockwise next = %d, %v", next, err)
	}
	if next, err := Next(seats, 1, DirectionCounterClockwise); err != nil || next != 4 {
		t.Fatalf("counterclockwise next = %d, %v", next, err)
	}
	if _, err := CanonicalSeats([]uint32{1, 1}); !errors.Is(err, ErrInvalidSeats) {
		t.Fatalf("duplicate seat error = %v", err)
	}
}
