package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andygeiss/tictactoe/internal/domain"
)

var ErrNotFound = errors.New("not found")

type Games struct {
	db *DB
}

func NewGames(db *DB) *Games {
	return &Games{db: db}
}

func (s *Games) Create(ctx context.Context, g *domain.Game) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO games (id, board, next, status, winner, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		g.ID, encodeBoard(g.Board), string(g.Next), string(g.Status), string(g.Winner), now, now)
	if err != nil {
		return fmt.Errorf("inserting game %s: %w", g.ID, err)
	}
	return nil
}

func (s *Games) Get(ctx context.Context, id string) (*domain.Game, error) {
	var g domain.Game
	var board, next, status, winner string
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT id, board, next, status, winner FROM games WHERE id = ?`, id).
		Scan(&g.ID, &board, &next, &status, &winner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("selecting game %s: %w", id, err)
	}
	g.Board, err = decodeBoard(board)
	if err != nil {
		return nil, fmt.Errorf("decoding game %s: %w", id, err)
	}
	g.Next = domain.Player(next)
	g.Status = domain.Status(status)
	g.Winner = domain.Player(winner)
	return &g, nil
}

func (s *Games) Update(ctx context.Context, g *domain.Game) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Write.ExecContext(ctx,
		`UPDATE games SET board = ?, next = ?, status = ?, winner = ?, updated_at = ? WHERE id = ?`,
		encodeBoard(g.Board), string(g.Next), string(g.Status), string(g.Winner), now, g.ID)
	if err != nil {
		return fmt.Errorf("updating game %s: %w", g.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("updating game %s: %w", g.ID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// encodeBoard packs the board as 9 characters, '.' for empty.
func encodeBoard(b [9]domain.Player) string {
	var sb strings.Builder
	for _, p := range b {
		if p == domain.PlayerNone {
			sb.WriteByte('.')
		} else {
			sb.WriteString(string(p))
		}
	}
	return sb.String()
}

func decodeBoard(s string) ([9]domain.Player, error) {
	var b [9]domain.Player
	if len(s) != 9 {
		return b, fmt.Errorf("board %q: want 9 cells", s)
	}
	for i, r := range s {
		switch r {
		case '.':
			b[i] = domain.PlayerNone
		case 'X':
			b[i] = domain.PlayerX
		case 'O':
			b[i] = domain.PlayerO
		default:
			return b, fmt.Errorf("board %q: bad cell %q", s, r)
		}
	}
	return b, nil
}
