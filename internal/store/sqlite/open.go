// Package sqlite opens Doketfire databases backed by modernc.org/sqlite.
// It is the default adapter for the store port.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Default connection settings used when an Options field is left at zero.
const (
	DefaultBusyTimeout  = 5 * time.Second
	DefaultMaxOpenConns = 4
	DefaultSynchronous  = "NORMAL"
)

// Options configures the SQLite connection pool.
type Options struct {
	Path         string
	BusyTimeout  time.Duration
	MaxOpenConns int
	Synchronous  string
}

// Open opens the SQLite database at opts.Path, applies the pragmas required by
// Doketfire (WAL, foreign keys, busy timeout, synchronous mode and immediate
// transactions) and verifies the connection with a ping.
//
// The caller owns the returned *sql.DB and must close it.
func Open(ctx context.Context, opts Options) (*sql.DB, error) {
	opts, err := normalize(opts)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn(opts))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %q: %w", opts.Path, err)
	}
	db.SetMaxOpenConns(opts.MaxOpenConns)
	db.SetMaxIdleConns(opts.MaxOpenConns)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: ping %q: %w", opts.Path, err)
	}
	return db, nil
}

func normalize(opts Options) (Options, error) {
	opts.Path = strings.TrimSpace(opts.Path)
	if opts.Path == "" {
		return Options{}, errors.New("sqlite: path is required")
	}
	if opts.BusyTimeout < 0 {
		return Options{}, errors.New("sqlite: busy timeout cannot be negative")
	}
	if opts.BusyTimeout == 0 {
		opts.BusyTimeout = DefaultBusyTimeout
	}
	if opts.MaxOpenConns < 0 {
		return Options{}, errors.New("sqlite: max open conns cannot be negative")
	}
	if opts.MaxOpenConns == 0 {
		opts.MaxOpenConns = DefaultMaxOpenConns
	}
	opts.Synchronous = strings.ToUpper(strings.TrimSpace(opts.Synchronous))
	switch opts.Synchronous {
	case "":
		opts.Synchronous = DefaultSynchronous
	case "NORMAL", "FULL":
	default:
		return Options{}, fmt.Errorf("sqlite: synchronous must be NORMAL or FULL, got %q", opts.Synchronous)
	}
	return opts, nil
}

func dsn(opts Options) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", opts.BusyTimeout.Milliseconds()))
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", fmt.Sprintf("synchronous(%s)", opts.Synchronous))
	q.Set("_txlock", "immediate")
	return "file:" + filepath.ToSlash(opts.Path) + "?" + q.Encode()
}
