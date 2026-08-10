// Package domain holds the tic-tac-toe rules. It imports nothing from the
// application and knows nothing about HTTP or storage.
package domain

import "errors"

var (
	ErrNotFound    = errors.New("not found")
	ErrInvalidCell = errors.New("cell out of range")
	ErrCellTaken   = errors.New("cell already taken")
	ErrGameOver    = errors.New("game is over")
)

type Player string

const (
	PlayerNone Player = ""
	PlayerX    Player = "X"
	PlayerO    Player = "O"
)

type Status string

const (
	StatusInProgress Status = "in_progress"
	StatusWon        Status = "won"
	StatusDraw       Status = "draw"
)

type Game struct {
	ID     string
	Board  [9]Player
	Next   Player
	Status Status
	Winner Player
}

func NewGame(id string) *Game {
	return &Game{ID: id, Next: PlayerX, Status: StatusInProgress}
}

// lines are the eight winning cell triples: rows, columns, diagonals.
var lines = [8][3]int{
	{0, 1, 2}, {3, 4, 5}, {6, 7, 8},
	{0, 3, 6}, {1, 4, 7}, {2, 5, 8},
	{0, 4, 8}, {2, 4, 6},
}

// Move places the next player's mark on cell (0–8) and advances the game state.
func (g *Game) Move(cell int) error {
	if g.Status != StatusInProgress {
		return ErrGameOver
	}
	if cell < 0 || cell > 8 {
		return ErrInvalidCell
	}
	if g.Board[cell] != PlayerNone {
		return ErrCellTaken
	}
	g.Board[cell] = g.Next

	if w := g.winner(); w != PlayerNone {
		g.Status = StatusWon
		g.Winner = w
		return nil
	}
	if g.full() {
		g.Status = StatusDraw
		return nil
	}
	if g.Next == PlayerX {
		g.Next = PlayerO
	} else {
		g.Next = PlayerX
	}
	return nil
}

func (g *Game) winner() Player {
	for _, l := range lines {
		if p := g.Board[l[0]]; p != PlayerNone && p == g.Board[l[1]] && p == g.Board[l[2]] {
			return p
		}
	}
	return PlayerNone
}

func (g *Game) full() bool {
	for _, c := range g.Board {
		if c == PlayerNone {
			return false
		}
	}
	return true
}
