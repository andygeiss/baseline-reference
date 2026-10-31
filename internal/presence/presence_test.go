package presence

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUpRoundTripsAndClamps(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		inX, inY     int
		wantX, wantY uint16
	}{
		{0, 0, 0, 0},
		{500, 250, 500, 250},
		{Scale, Scale, Scale, Scale},
		{-40, Scale + 9, 0, Scale},
	} {
		x, y, ok := DecodeUp(EncodeUp(tt.inX, tt.inY))
		if !ok || x != tt.wantX || y != tt.wantY {
			t.Errorf("EncodeUp(%d, %d) decodes to %d, %d, %v; want %d, %d, true", tt.inX, tt.inY, x, y, ok, tt.wantX, tt.wantY)
		}
	}
}

func TestUpRefusesAnythingButOnePosition(t *testing.T) {
	t.Parallel()
	for _, b := range [][]byte{nil, {1, 2, 3}, {0, 1, 0, 1, 0}, {0xFF, 0xFF, 0, 0}} {
		if _, _, ok := DecodeUp(b); ok {
			t.Errorf("DecodeUp(%v) is ok; a server would act on it", b)
		}
	}
}

func TestDownRoundTrips(t *testing.T) {
	t.Parallel()
	seats := []Seat{{"Ada", 1, 2}, {"Grace", Scale, 0}, {"", 7, 7}}
	got, ok := DecodeDown(EncodeDown(seats))
	if !ok || !slices.Equal(got, seats) {
		t.Errorf("round trip = %v, %v; want %v", got, ok, seats)
	}
}

func TestDownCapsSeatsAndNames(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", MaxName) // two bytes a rune, so MaxName falls mid-rune half the time
	seats := make([]Seat, MaxSeats+5)
	for i := range seats {
		seats[i] = Seat{Name: long[:len(long)-i%2]}
	}
	got, ok := DecodeDown(EncodeDown(seats))
	if !ok || len(got) != MaxSeats {
		t.Fatalf("decoded %d seats, ok %v; want %d", len(got), ok, MaxSeats)
	}
	for _, s := range got {
		if len(s.Name) > MaxName || !utf8.ValidString(s.Name) {
			t.Errorf("name %q: %d bytes, valid %v", s.Name, len(s.Name), utf8.ValidString(s.Name))
		}
	}
}

func TestDownRefusesBrokenMessages(t *testing.T) {
	t.Parallel()
	good := EncodeDown([]Seat{{"Ada", 1, 2}})
	for name, b := range map[string][]byte{
		"empty":         nil,
		"cut short":     good[:len(good)-1],
		"trailing byte": append(slices.Clone(good), 0),
		"too many":      {MaxSeats + 1},
	} {
		if _, ok := DecodeDown(b); ok {
			t.Errorf("%s: DecodeDown is ok", name)
		}
	}
}
