package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andygeiss/baseline-reference/internal/domain"
)

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

const selectGame = `SELECT id, board, next, status, winner FROM games WHERE id = ?`

func (s *Games) Get(ctx context.Context, id string) (*domain.Game, error) {
	return scanGame(s.db.Read.QueryRowContext(ctx, selectGame, id), id)
}

// Update loads the game, applies change to it, and stores the result — all in
// one write transaction. Read and write must be atomic: two clicks arriving
// together would otherwise both read the same board, and the second write
// would erase the first move. The write pool has a single connection, so these
// transactions serialize instead of colliding.
//
// The game is returned even when change reports a rule violation: that is the
// current board, which the caller renders back to the stale client.
func (s *Games) Update(ctx context.Context, id string, change func(*domain.Game) error) (*domain.Game, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning update of game %s: %w", id, err)
	}
	defer tx.Rollback()

	g, err := scanGame(tx.QueryRowContext(ctx, selectGame, id), id)
	if err != nil {
		return nil, err
	}
	if err := change(g); err != nil {
		return g, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE games SET board = ?, next = ?, status = ?, winner = ?, updated_at = ? WHERE id = ?`,
		encodeBoard(g.Board), string(g.Next), string(g.Status), string(g.Winner),
		time.Now().UTC().Format(time.RFC3339), id); err != nil {
		return nil, fmt.Errorf("updating game %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing update of game %s: %w", id, err)
	}
	return g, nil
}

// scanGame reads one game row, translating "no rows" to the domain sentinel.
func scanGame(row *sql.Row, id string) (*domain.Game, error) {
	var g domain.Game
	var board, next, status, winner string
	err := row.Scan(&g.ID, &board, &next, &status, &winner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
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
