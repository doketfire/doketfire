package main

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const recordColumns = `id, title, slug, body, published, author_id, created_at, updated_at`

var schemaStatements = []string{
	`CREATE TABLE posts (
		id         TEXT PRIMARY KEY,
		title      TEXT NOT NULL,
		slug       TEXT NOT NULL,
		body       TEXT NOT NULL,
		published  INTEGER NOT NULL,
		author_id  TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	) STRICT`,
	`CREATE UNIQUE INDEX idx_posts_slug ON posts(slug)`,
	`CREATE INDEX idx_posts_published_created ON posts(published, created_at DESC)`,
	`CREATE INDEX idx_posts_author ON posts(author_id)`,
}

// sqliteEngine uses modernc.org/sqlite (pure Go) with the same pragmas
// the v1.0 design specifies: WAL, busy timeout, foreign keys, NORMAL
// synchronous and immediate write transactions.
type sqliteEngine struct {
	db  *sql.DB
	dir string
}

func newSQLiteEngine() Engine { return &sqliteEngine{} }

func (e *sqliteEngine) Name() string { return "sqlite" }

func (e *sqliteEngine) Open(ctx context.Context, dir string) error {
	e.dir = dir
	path := filepath.Join(dir, "app.db")
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		filepath.ToSlash(path),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return err
	}
	for _, stmt := range schemaStatements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			_ = db.Close()
			return fmt.Errorf("schema: %w", err)
		}
	}
	e.db = db
	return nil
}

func (e *sqliteEngine) Close() error {
	if e.db == nil {
		return nil
	}
	return e.db.Close()
}

func (e *sqliteEngine) Seed(ctx context.Context, records []Record, batch int) error {
	if batch <= 0 {
		batch = len(records)
	}
	const insert = `INSERT INTO posts (id, title, slug, body, published, author_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	for start := 0; start < len(records); start += batch {
		end := min(start+batch, len(records))
		tx, err := e.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, insert)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, r := range records[start:end] {
			if _, err := stmt.ExecContext(ctx,
				r.ID, r.Title, r.Slug, r.Body, boolInt(r.Published),
				r.AuthorID, r.CreatedAt, r.UpdatedAt,
			); err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
		if err := stmt.Close(); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (e *sqliteEngine) PointGet(ctx context.Context, id string) (Record, error) {
	row := e.db.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM posts WHERE id = ?`, id)
	return scanRecord(row)
}

func (e *sqliteEngine) ListPublished(ctx context.Context, limit int, beforeCreatedAt int64) ([]Record, error) {
	rows, err := e.db.QueryContext(ctx,
		`SELECT `+recordColumns+` FROM posts
		 WHERE published = 1 AND created_at < ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		beforeCreatedAt, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Record, 0, limit)
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (e *sqliteEngine) CountPublished(ctx context.Context) (int, error) {
	var n int
	err := e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM posts WHERE published = 1`).Scan(&n)
	return n, err
}

func (e *sqliteEngine) UpdateTitle(ctx context.Context, id, title string) error {
	res, err := e.db.ExecContext(ctx,
		`UPDATE posts SET title = ?, updated_at = updated_at + 1 WHERE id = ?`,
		title, id,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("record %q not found", id)
	}
	return nil
}

func (e *sqliteEngine) DiskBytes() (int64, error) {
	var total int64
	err := filepath.WalkDir(e.dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var r Record
	var published int64
	if err := row.Scan(
		&r.ID, &r.Title, &r.Slug, &r.Body, &published,
		&r.AuthorID, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return Record{}, err
	}
	r.Published = published != 0
	return r, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
