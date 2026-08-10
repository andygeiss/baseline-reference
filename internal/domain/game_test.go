package domain

import (
	"errors"
	"fmt"
	"testing"
)

// play makes the given moves, failing the test on any rule error.
func play(t *testing.T, g *Game, cells ...int) {
	t.Helper()
	for _, c := range cells {
		if err := g.Move(c); err != nil {
			t.Fatalf("move %d: %v", c, err)
		}
	}
}

func TestGame_AllWinningLines(t *testing.T) {
	t.Parallel()
	// For each of the eight lines, X takes the line while O plays elsewhere.
	filler := map[int][]int{ // per-line safe cells for O that never complete an O line
		0: {3, 4}, 1: {0, 1}, 2: {0, 1}, 3: {1, 2},
		4: {0, 2}, 5: {0, 1}, 6: {1, 2}, 7: {1, 3},
	}
	for i, line := range lines {
		t.Run(fmt.Sprintf("line %v", line), func(t *testing.T) {
			t.Parallel()
			g := NewGame("t")
			play(t, g, line[0], filler[i][0], line[1], filler[i][1], line[2])
			if g.Status != StatusWon || g.Winner != PlayerX {
				t.Errorf("line %v: status=%q winner=%q, want won by X", line, g.Status, g.Winner)
			}
		})
	}
}

func TestGame_OWins(t *testing.T) {
	t.Parallel()
	g := NewGame("t")
	play(t, g, 0, 3, 1, 4, 8, 5) // O completes 3,4,5
	if g.Status != StatusWon || g.Winner != PlayerO {
		t.Errorf("status=%q winner=%q, want won by O", g.Status, g.Winner)
	}
}

func TestGame_Draw(t *testing.T) {
	t.Parallel()
	g := NewGame("t")
	// X O X / X O O / O X X — full board, no line.
	play(t, g, 0, 1, 2, 4, 3, 5, 7, 6, 8)
	if g.Status != StatusDraw {
		t.Errorf("status=%q winner=%q, want draw", g.Status, g.Winner)
	}
	if g.Winner != PlayerNone {
		t.Errorf("draw has winner %q", g.Winner)
	}
}

func TestGame_Alternation(t *testing.T) {
	t.Parallel()
	g := NewGame("t")
	if g.Next != PlayerX {
		t.Fatalf("first player = %q, want X", g.Next)
	}
	play(t, g, 4)
	if g.Board[4] != PlayerX || g.Next != PlayerO {
		t.Errorf("after first move: cell=%q next=%q, want X then O", g.Board[4], g.Next)
	}
}

func TestGame_MoveErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, g *Game)
		cell  int
		want  error
	}{
		{"negative cell", func(*testing.T, *Game) {}, -1, ErrInvalidCell},
		{"cell too large", func(*testing.T, *Game) {}, 9, ErrInvalidCell},
		{"taken cell", func(t *testing.T, g *Game) { play(t, g, 4) }, 4, ErrCellTaken},
		{"after win", func(t *testing.T, g *Game) { play(t, g, 0, 3, 1, 4, 2) }, 5, ErrGameOver},
		{"after draw", func(t *testing.T, g *Game) { play(t, g, 0, 1, 2, 4, 3, 5, 7, 6, 8) }, 0, ErrGameOver},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGame("t")
			tt.setup(t, g)
			before := g.Board
			if err := g.Move(tt.cell); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
			if g.Board != before {
				t.Error("failed move mutated the board")
			}
		})
	}
}
