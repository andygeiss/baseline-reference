// Package presence is the wire format of a room's map: what a browser sends up
// (where its pointer is) and what the server sends down (where everybody's is).
//
// Both sides import it, and the browser module is built from it, so it imports
// nothing at all: every import here is bytes in the module, and deps_test.go at
// the module root holds it to that (patterns/wasm-client.md rule 4).
package presence

// Scale is the far edge of the map on both axes. A position is 0 to Scale, so
// the size of anybody's canvas never reaches the wire.
const Scale = 1000

// MaxSeats is how many people one room's map shows. The server refuses the
// next one, so a down message never grows past MaxSeats seats.
const MaxSeats = 32

// MaxName is how many bytes of a name the wire carries. A longer one is cut on
// a rune boundary, so what arrives is still text.
const MaxName = 32

// UpSize is the one size an up message has: x and y, two bytes each.
const UpSize = 4

// Seat is one person on the map: their name and where their pointer is.
type Seat struct {
	Name string
	X, Y uint16
}

// EncodeUp is what a browser sends: its pointer, clamped to the map.
func EncodeUp(x, y int) []byte {
	cx, cy := clamp(x), clamp(y)
	return []byte{byte(cx >> 8), byte(cx), byte(cy >> 8), byte(cy)}
}

// DecodeUp reads an up message. ok is false for any message that is not
// exactly one position on the map — the server closes on that.
func DecodeUp(b []byte) (x, y uint16, ok bool) {
	if len(b) != UpSize {
		return 0, 0, false
	}
	x = uint16(b[0])<<8 | uint16(b[1])
	y = uint16(b[2])<<8 | uint16(b[3])
	return x, y, x <= Scale && y <= Scale
}

// EncodeDown is what the server sends: a count, then each seat's position and
// name. Seats past MaxSeats are dropped and names cut to MaxName.
func EncodeDown(seats []Seat) []byte {
	seats = seats[:min(len(seats), MaxSeats)]
	b := make([]byte, 1, 1+len(seats)*(5+MaxName))
	b[0] = byte(len(seats))
	for _, s := range seats {
		name := cut(s.Name)
		b = append(b, byte(s.X>>8), byte(s.X), byte(s.Y>>8), byte(s.Y), byte(len(name)))
		b = append(b, name...)
	}
	return b
}

// DecodeDown reads a down message. ok is false for one that is cut short or
// carries anything after its last seat.
func DecodeDown(b []byte) (seats []Seat, ok bool) {
	if len(b) < 1 || int(b[0]) > MaxSeats {
		return nil, false
	}
	n := int(b[0])
	b = b[1:]
	seats = make([]Seat, 0, n)
	for range n {
		if len(b) < 5 || int(b[4]) > MaxName || len(b) < 5+int(b[4]) {
			return nil, false
		}
		seats = append(seats, Seat{
			X:    uint16(b[0])<<8 | uint16(b[1]),
			Y:    uint16(b[2])<<8 | uint16(b[3]),
			Name: string(b[5 : 5+int(b[4])]),
		})
		b = b[5+int(b[4]):]
	}
	return seats, len(b) == 0
}

func clamp(v int) uint16 {
	return uint16(max(0, min(v, Scale)))
}

// cut shortens a name to MaxName bytes without splitting a rune: it backs off
// over continuation bytes, which are 10xxxxxx in UTF-8.
func cut(name string) string {
	if len(name) <= MaxName {
		return name
	}
	n := MaxName
	for n > 0 && name[n]&0xC0 == 0x80 {
		n--
	}
	return name[:n]
}
