package dice

import (
	"errors"
	"sort"
)

var (
	// ErrInvalidSeats rejects an empty, duplicated, or unsorted seat set.
	ErrInvalidSeats = errors.New("seat set is invalid")
	// ErrCurrentSeatMissing rejects turn advancement from a seat outside the active set.
	ErrCurrentSeatMissing = errors.New("current seat is not active")
)

// Direction controls deterministic traversal over stable seat indexes.
type Direction uint8

const (
	DirectionClockwise Direction = iota + 1
	DirectionCounterClockwise
)

// Valid reports whether direction has a defined traversal meaning.
func (direction Direction) Valid() bool {
	return direction == DirectionClockwise || direction == DirectionCounterClockwise
}

// CanonicalSeats validates and returns a sorted defensive copy of active seats.
func CanonicalSeats(seats []uint32) ([]uint32, error) {
	if len(seats) == 0 {
		return nil, ErrInvalidSeats
	}
	canonical := append([]uint32(nil), seats...)
	sort.Slice(canonical, func(left, right int) bool { return canonical[left] < canonical[right] })
	for index := 1; index < len(canonical); index++ {
		if canonical[index] == canonical[index-1] {
			return nil, ErrInvalidSeats
		}
	}
	return canonical, nil
}

// Next returns the adjacent active seat in the requested stable direction.
func Next(seats []uint32, current uint32, direction Direction) (uint32, error) {
	if !direction.Valid() {
		return 0, ErrInvalidSeats
	}
	canonical, err := CanonicalSeats(seats)
	if err != nil {
		return 0, err
	}
	index := sort.Search(len(canonical), func(index int) bool { return canonical[index] >= current })
	if index == len(canonical) || canonical[index] != current {
		return 0, ErrCurrentSeatMissing
	}
	if direction == DirectionClockwise {
		return canonical[(index+1)%len(canonical)], nil
	}
	return canonical[(index+len(canonical)-1)%len(canonical)], nil
}
