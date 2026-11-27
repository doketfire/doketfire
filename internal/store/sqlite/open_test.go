package sqlite

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDSN(t *testing.T) {
	t.Parallel()

	got := dsn(Options{
		Path:        filepath.Join("data", "app.db"),
		BusyTimeout: 1500 * time.Millisecond,
		Synchronous: "FULL",
	})

	idx := strings.Index(got, "?")
	if idx < 0 {
		t.Fatalf("dsn %q has no query string", got)
	}
	if prefix := "file:data/app.db?"; !strings.HasPrefix(got, prefix) {
		t.Errorf("dsn = %q, want prefix %q", got, prefix)
	}

	values, err := url.ParseQuery(got[idx+1:])
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	wantPragmas := []string{
		"journal_mode(WAL)",
		"busy_timeout(1500)",
		"foreign_keys(1)",
		"synchronous(FULL)",
	}
	if pragmas := values["_pragma"]; !slices.Equal(pragmas, wantPragmas) {
		t.Errorf("_pragma = %q, want %q", pragmas, wantPragmas)
	}
	if got := values.Get("_txlock"); got != "immediate" {
		t.Errorf("_txlock = %q, want immediate", got)
	}
}

func TestOpenAppliesPragmas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, err := Open(ctx, Options{
		Path:         filepath.Join(t.TempDir(), "app.db"),
		BusyTimeout:  2500 * time.Millisecond,
		MaxOpenConns: 3,
		Synchronous:  "NORMAL",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyTimeout != 2500 {
		t.Errorf("busy_timeout = %d, want 2500", busyTimeout)
	}

	var synchronous int
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if synchronous != 1 {
		t.Errorf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}

	if got := db.Stats().MaxOpenConnections; got != 3 {
		t.Errorf("MaxOpenConnections = %d, want 3", got)
	}
}

func TestOpenDefaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "defaults.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if int64(busyTimeout) != DefaultBusyTimeout.Milliseconds() {
		t.Errorf("busy_timeout = %d, want %d", busyTimeout, DefaultBusyTimeout.Milliseconds())
	}

	var synchronous int
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if synchronous != 1 {
		t.Errorf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}

	if got := db.Stats().MaxOpenConnections; got != DefaultMaxOpenConns {
		t.Errorf("MaxOpenConnections = %d, want %d", got, DefaultMaxOpenConns)
	}
}

func TestOpenRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts Options
		want string
	}{
		{name: "missing path", opts: Options{}, want: "path is required"},
		{name: "negative busy timeout", opts: Options{Path: "app.db", BusyTimeout: -time.Second}, want: "busy timeout"},
		{name: "negative max conns", opts: Options{Path: "app.db", MaxOpenConns: -1}, want: "max open conns"},
		{name: "invalid synchronous", opts: Options{Path: "app.db", Synchronous: "OFF"}, want: "synchronous"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db, err := Open(context.Background(), tc.opts)
			if err == nil {
				if db != nil {
					_ = db.Close()
				}
				t.Fatal("Open succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestOpenPathWithSpaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dir := filepath.Join(t.TempDir(), "data dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	db, err := Open(ctx, Options{Path: filepath.Join(dir, "app db.sqlite")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `CREATE TABLE probe (id TEXT PRIMARY KEY) STRICT`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
}

func TestOpenEnforcesForeignKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "fk.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, stmt := range []string{
		`CREATE TABLE parent (id TEXT PRIMARY KEY) STRICT`,
		`CREATE TABLE child (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES parent(id)) STRICT`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO child (id, parent_id) VALUES ('c1', 'missing')`); err == nil {
		t.Fatal("insert with missing parent succeeded, want foreign key violation")
	}
}

func TestOpenConcurrentWrites(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := Open(ctx, Options{
		Path:         filepath.Join(t.TempDir(), "concurrent.db"),
		BusyTimeout:  5 * time.Second,
		MaxOpenConns: 4,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `CREATE TABLE writes (id INTEGER PRIMARY KEY, worker INTEGER NOT NULL) STRICT`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	const writers = 50
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			_, err := db.ExecContext(ctx, `INSERT INTO writes (id, worker) VALUES (?, ?)`, worker+1, worker)
			if err != nil {
				errs <- fmt.Errorf("worker %d: %w", worker, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM writes`).Scan(&count); err != nil {
		t.Fatalf("COUNT(*): %v", err)
	}
	if count != writers {
		t.Errorf("count = %d, want %d", count, writers)
	}
}

func TestOpenCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "canceled.db")})
	if err == nil {
		if db != nil {
			_ = db.Close()
		}
		t.Fatal("Open succeeded with canceled context, want error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
