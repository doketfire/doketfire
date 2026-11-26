package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/doketfire/doketfire/internal/app"
	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/storage"
)

func TestRunVersionAndHelp(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		if err := run([]string{arg}); err != nil {
			t.Fatalf("run(%q) error: %v", arg, err)
		}
	}

	for _, arg := range []string{"help", "--help", "-h"} {
		if err := run([]string{arg}); err != nil {
			t.Fatalf("run(%q) error: %v", arg, err)
		}
	}

	if err := run([]string{"nonexistent-command"}); err == nil {
		t.Fatal("expected error for nonexistent command")
	}
}

func TestServeCommandFlags(t *testing.T) {
	// Invalid flag should return error
	if err := run([]string{"serve", "--invalid-unknown-flag"}); err == nil {
		t.Fatal("expected error with unknown flag")
	}

	// Invalid config (negative max doc size) should fail validation
	if err := run([]string{"serve", "--max-doc-size=-1", "--dir=" + t.TempDir()}); err == nil {
		t.Fatal("expected error with invalid config")
	}
}

func TestCheckAndSuperuserCommands(t *testing.T) {
	dir := t.TempDir()

	// 1. Create superuser (initializes database)
	if err := run([]string{"superuser", "create", "admin@example.com", "super-secret-123", "--dir=" + dir}); err != nil {
		t.Fatalf("create superuser failed: %v", err)
	}

	// 2. Check passes on initialized database
	if err := run([]string{"check", "--dir=" + dir}); err != nil {
		t.Fatalf("check failed: %v", err)
	}

	// 3. Duplicate superuser creation fails
	if err := run([]string{"superuser", "create", "admin@example.com", "super-secret-123", "--dir=" + dir}); err == nil {
		t.Fatal("expected error when creating duplicate superuser")
	}

	// 4. Invalid superuser subcommand
	if err := run([]string{"superuser", "delete", "--dir=" + dir}); err == nil {
		t.Fatal("expected error for unknown superuser subcommand")
	}
	if err := run([]string{"superuser", "create"}); err == nil {
		t.Fatal("expected error for superuser create without credentials")
	}
}

func TestBackupRestoreAndCompactCommands(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "my_backups")

	// Initialize store and insert some data
	store, err := storage.Open(dir, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(func(tx storage.WriteTx) error {
		def := &collections.CollectionDef{
			Name: "notes",
			Fields: []collections.Field{
				{Name: "text", Type: "string"},
			},
		}
		if err := tx.SaveCollection(def); err != nil {
			return err
		}
		return tx.PutDoc("notes", collections.Document{"text": "backup test note"})
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	// 1. Backup command
	if err := run([]string{"backup", "--dir=" + dir, "--out=" + outDir}); err != nil {
		t.Fatalf("backup failed: %v", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no backup files created: %v", err)
	}
	backupFile := filepath.Join(outDir, entries[0].Name())

	// 2. Restore into a new dir
	restoreDir := filepath.Join(dir, "restored_dir")
	if err := run([]string{"restore", backupFile, "--dir=" + restoreDir}); err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	// Verify restored database integrity
	if err := run([]string{"check", "--dir=" + restoreDir}); err != nil {
		t.Fatalf("check on restored database failed: %v", err)
	}

	// Restore errors
	if err := run([]string{"restore"}); err == nil {
		t.Fatal("expected error when restore is called without file argument")
	}
	if err := run([]string{"restore", "missing-file.db", "--dir=" + restoreDir}); err == nil {
		t.Fatal("expected error when restore is called on nonexistent file")
	}

	// 3. Compact command
	if err := run([]string{"compact", "--dir=" + dir}); err != nil {
		t.Fatalf("compact failed: %v", err)
	}
	if err := run([]string{"check", "--dir=" + dir}); err != nil {
		t.Fatalf("check after compact failed: %v", err)
	}
}

func TestReindexCommand(t *testing.T) {
	dir := t.TempDir()

	store, err := storage.Open(dir, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(func(tx storage.WriteTx) error {
		def := &collections.CollectionDef{
			Name: "articles",
			Fields: []collections.Field{
				{Name: "title", Type: "string"},
			},
			Indexes: []collections.Index{
				{Name: "title_idx", Field: "title"},
			},
		}
		if err := tx.SaveCollection(def); err != nil {
			return err
		}
		return tx.PutDoc("articles", collections.Document{"title": "Go 1.26"})
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Reindex all indexes of collection
	if err := run([]string{"reindex", "articles", "--dir=" + dir}); err != nil {
		t.Fatalf("reindex all failed: %v", err)
	}

	// Reindex specific index
	if err := run([]string{"reindex", "articles", "title_idx", "--dir=" + dir}); err != nil {
		t.Fatalf("reindex specific failed: %v", err)
	}

	// Reindex arg errors
	if err := run([]string{"reindex"}); err == nil {
		t.Fatal("expected error when reindex is called without collection")
	}
}

func TestEnvHelpers(t *testing.T) {
	os.Setenv("TEST_DP_VAR", "hello")
	defer os.Unsetenv("TEST_DP_VAR")

	if got := envOr("TEST_DP_VAR", "default"); got != "hello" {
		t.Fatalf("expected hello, got %s", got)
	}
	if got := envOr("TEST_DP_MISSING", "default"); got != "default" {
		t.Fatalf("expected default, got %s", got)
	}

	os.Setenv("TEST_DP_BOOL", "true")
	defer os.Unsetenv("TEST_DP_BOOL")
	if !envBool("TEST_DP_BOOL", false) {
		t.Fatal("expected true")
	}
	if envBool("TEST_DP_MISSING_BOOL", false) {
		t.Fatal("expected false")
	}

	os.Setenv("TEST_DP_INT", "42")
	defer os.Unsetenv("TEST_DP_INT")
	if got := envInt("TEST_DP_INT", 0); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
	if got := envInt64("TEST_DP_INT", 0); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}

	if parts := splitCSV(" a, b , c "); len(parts) != 3 || parts[1] != "b" {
		t.Fatalf("unexpected splitCSV result: %v", parts)
	}
	if parts := splitCSV(""); len(parts) != 0 {
		t.Fatalf("expected empty for empty string, got %v", parts)
	}
}

func TestEnvLegacyAlias(t *testing.T) {
	t.Setenv("DOKET_HTTP", "")
	t.Setenv("DOCPOCKET_HTTP", "127.0.0.1:9999")
	if got := envOr("DOKET_HTTP", "fallback"); got != "127.0.0.1:9999" {
		t.Fatalf("legacy alias: got %q", got)
	}

	t.Setenv("DOKET_HTTP", "0.0.0.0:8090")
	if got := envOr("DOKET_HTTP", "fallback"); got != "0.0.0.0:8090" {
		t.Fatalf("new name must win: got %q", got)
	}

	t.Setenv("DOKET_READ_ONLY", "")
	t.Setenv("DOCPOCKET_READ_ONLY", "true")
	if !envBool("DOKET_READ_ONLY", false) {
		t.Fatal("expected legacy boolean alias to be honored")
	}
}

func TestResolveDataDir(t *testing.T) {
	t.Setenv("DOKET_DIR", "")
	t.Setenv("DOCPOCKET_DIR", "")
	t.Chdir(t.TempDir())

	newCfg := func() (app.Config, *flag.FlagSet) {
		cfg := app.DefaultConfig()
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.StringVar(&cfg.Dir, "dir", cfg.Dir, "")
		return cfg, fs
	}

	cfg, fs := newCfg()
	resolveDataDir(&cfg, fs)
	if cfg.Dir != "./data" {
		t.Fatalf("expected ./data when no legacy directory exists, got %s", cfg.Dir)
	}

	if err := os.Mkdir("dp_data", 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, fs = newCfg()
	resolveDataDir(&cfg, fs)
	if cfg.Dir != "./dp_data" {
		t.Fatalf("expected legacy ./dp_data fallback, got %s", cfg.Dir)
	}

	if err := os.Mkdir("data", 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, fs = newCfg()
	resolveDataDir(&cfg, fs)
	if cfg.Dir != "./data" {
		t.Fatalf("expected ./data to win when both exist, got %s", cfg.Dir)
	}
}

func TestResolveDataDirExplicitFlagWins(t *testing.T) {
	t.Setenv("DOKET_DIR", "")
	t.Setenv("DOCPOCKET_DIR", "")
	t.Chdir(t.TempDir())

	if err := os.Mkdir("dp_data", 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := app.DefaultConfig()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.StringVar(&cfg.Dir, "dir", cfg.Dir, "")
	if err := fs.Set("dir", "./data"); err != nil {
		t.Fatal(err)
	}
	resolveDataDir(&cfg, fs)
	if cfg.Dir != "./data" {
		t.Fatalf("explicit --dir must win, got %s", cfg.Dir)
	}
}
