// Command doketfire runs the Doketfire server and its maintenance commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/doketfire/doketfire/internal/app"
	"github.com/doketfire/doketfire/internal/auth"
	"github.com/doketfire/doketfire/internal/storage"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "doketfire:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return serveCommand(nil)
	}
	switch args[0] {
	case "serve":
		return serveCommand(args[1:])
	case "backup":
		return backupCommand(args[1:])
	case "restore":
		return restoreCommand(args[1:])
	case "compact":
		return compactCommand(args[1:])
	case "check":
		return checkCommand(args[1:])
	case "reindex":
		return reindexCommand(args[1:])
	case "superuser":
		return superuserCommand(args[1:])
	case "version", "--version", "-v":
		fmt.Println("doketfire " + version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Doketfire - document database in a single binary

Usage:
  doketfire serve [flags]
  doketfire backup [--dir=DIR] [--out=DIR]
  doketfire restore [--dir=DIR] <file>
  doketfire compact [--dir=DIR]
  doketfire check [--dir=DIR]
  doketfire reindex [--dir=DIR] <collection> [index]
  doketfire superuser create [--dir=DIR] <email> <password>
  doketfire version

Flags may appear before or after positional arguments.

Environment:
  DOKET_DIR, DOKET_HTTP, DOKET_CORS, DOKET_LOG_LEVEL
  Legacy aliases: DOCPOCKET_DIR, DOCPOCKET_HTTP, DOCPOCKET_CORS, DOCPOCKET_LOG_LEVEL
`)
}

func serveCommand(args []string) error {
	cfg := app.DefaultConfig()
	cfg.Version = version
	var cors string
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.Dir, "dir", envOr("DOKET_DIR", cfg.Dir), "data directory")
	fs.StringVar(&cfg.HTTPAddr, "http", envOr("DOKET_HTTP", cfg.HTTPAddr), "HTTP listen address")
	fs.StringVar(&cfg.HTTPSDomain, "https", envOr("DOKET_HTTPS_DOMAIN", ""), "domain for automatic TLS (ACME)")
	fs.StringVar(&cfg.HTTPSAddr, "https-addr", envOr("DOKET_HTTPS_ADDR", cfg.HTTPSAddr), "HTTPS listen address")
	fs.StringVar(&cfg.TLSCert, "tls-cert", envOr("DOKET_TLS_CERT", ""), "TLS certificate file")
	fs.StringVar(&cfg.TLSKey, "tls-key", envOr("DOKET_TLS_KEY", ""), "TLS private key file")
	fs.StringVar(&cfg.AutocertDir, "autocert-dir", envOr("DOKET_AUTOCERT_DIR", ""), "ACME certificate cache")
	fs.BoolVar(&cfg.ReadOnly, "read-only", envBool("DOKET_READ_ONLY", false), "open the database read-only")
	fs.BoolVar(&cfg.NoSync, "no-sync", envBool("DOKET_NO_SYNC", false), "disable fsync (unsafe)")
	fs.BoolVar(&cfg.StrictIndexes, "strict-indexes", envBool("DOKET_STRICT_INDEXES", false), "reject queries without an index")
	fs.BoolVar(&cfg.TrustProxy, "trust-proxy", envBool("DOKET_TRUST_PROXY", false), "trust X-Forwarded-For")
	fs.IntVar(&cfg.MaxDocSize, "max-doc-size", envInt("DOKET_MAX_DOC_SIZE", 0), "maximum document size in bytes (0 = 1 MiB)")
	fs.Int64Var(&cfg.MaxBodyBytes, "max-body", envInt64("DOKET_MAX_BODY", cfg.MaxBodyBytes), "maximum JSON body size")
	fs.Int64Var(&cfg.MaxUploadBytes, "max-upload", envInt64("DOKET_MAX_UPLOAD", cfg.MaxUploadBytes), "maximum upload size")
	fs.StringVar(&cors, "cors", envOr("DOKET_CORS", ""), "comma-separated allowed origins")
	fs.DurationVar(&cfg.AccessTTL, "access-ttl", cfg.AccessTTL, "access token lifetime")
	fs.DurationVar(&cfg.RefreshTTL, "refresh-ttl", cfg.RefreshTTL, "refresh token lifetime")
	fs.IntVar(&cfg.QueryLimits.DefaultLimit, "default-limit", cfg.QueryLimits.DefaultLimit, "default page size")
	fs.IntVar(&cfg.QueryLimits.MaxLimit, "max-limit", cfg.QueryLimits.MaxLimit, "maximum page size")
	fs.IntVar(&cfg.QueryLimits.MaxSortDocs, "max-sort-docs", cfg.QueryLimits.MaxSortDocs, "maximum documents sorted in memory")
	fs.DurationVar(&cfg.QueryLimits.Timeout, "query-timeout", cfg.QueryLimits.Timeout, "maximum query duration")
	fs.StringVar(&cfg.LogLevel, "log-level", envOr("DOKET_LOG_LEVEL", cfg.LogLevel), "debug, info, warn or error")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	resolveDataDir(&cfg, fs)
	cfg.CORSOrigins = splitCSV(cors)
	a, err := app.New(cfg)
	if err != nil {
		return err
	}
	defer a.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	addr := cfg.HTTPAddr
	if cfg.HTTPSDomain != "" {
		addr = cfg.HTTPSAddr
	}
	a.Logger.Info("doketfire starting", "addr", addr, "dir", cfg.Dir, "version", cfg.Version)
	if err := a.Run(ctx); err != nil {
		return err
	}
	a.Logger.Info("doketfire stopped")
	return nil
}

func backupCommand(args []string) error {
	cfg := app.DefaultConfig()
	out := ""
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.Dir, "dir", envOr("DOKET_DIR", cfg.Dir), "data directory")
	fs.StringVar(&out, "out", "", "output directory (default: DIR/backups)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolveDataDir(&cfg, fs)
	if out == "" {
		out = filepath.Join(cfg.Dir, "backups")
	}
	store, err := storage.Open(cfg.Dir, storage.Options{ReadOnly: true})
	if err != nil {
		return err
	}
	defer store.Close()
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	name := "doketfire-" + time.Now().UTC().Format("20060102-150405") + ".db"
	path := filepath.Join(out, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := store.Backup(f); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	st, _ := os.Stat(path)
	fmt.Printf("backup written to %s (%d bytes)\n", path, st.Size())
	return nil
}

func restoreCommand(args []string) error {
	cfg := app.DefaultConfig()
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.Dir, "dir", envOr("DOKET_DIR", cfg.Dir), "data directory")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	resolveDataDir(&cfg, fs)
	if len(positional) != 1 {
		return errors.New("usage: doketfire restore [--dir=DIR] <file>")
	}
	src := positional[0]
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(cfg.Dir, "data.db")
	if err := copyFileAtomic(src, target); err != nil {
		return err
	}
	fmt.Printf("restored %s into %s\n", src, target)
	return nil
}

func compactCommand(args []string) error {
	cfg := app.DefaultConfig()
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.Dir, "dir", envOr("DOKET_DIR", cfg.Dir), "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolveDataDir(&cfg, fs)
	store, err := storage.Open(cfg.Dir, storage.Options{})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(cfg.Dir, "compact-*.db")
	if err != nil {
		store.Close()
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := store.Backup(tmp); err != nil {
		tmp.Close()
		store.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		store.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		store.Close()
		return err
	}
	before := store.Size()
	if err := store.Close(); err != nil {
		return err
	}
	backup := filepath.Join(cfg.Dir, "data.db.pre-compact-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.Rename(filepath.Join(cfg.Dir, "data.db"), backup); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(cfg.Dir, "data.db")); err != nil {
		_ = os.Rename(backup, filepath.Join(cfg.Dir, "data.db"))
		return err
	}
	st, _ := os.Stat(filepath.Join(cfg.Dir, "data.db"))
	fmt.Printf("compacted %d -> %d bytes (previous file kept at %s)\n", before, st.Size(), backup)
	return nil
}

func checkCommand(args []string) error {
	cfg := app.DefaultConfig()
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.Dir, "dir", envOr("DOKET_DIR", cfg.Dir), "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolveDataDir(&cfg, fs)
	store, err := storage.Open(cfg.Dir, storage.Options{ReadOnly: true})
	if err != nil {
		return err
	}
	defer store.Close()
	report, err := store.Check(context.Background())
	if err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(raw))
	if !report.OK() {
		return fmt.Errorf("database check found %d issue(s)", len(report.Issues))
	}
	return nil
}

func reindexCommand(args []string) error {
	cfg := app.DefaultConfig()
	fs := flag.NewFlagSet("reindex", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.Dir, "dir", envOr("DOKET_DIR", cfg.Dir), "data directory")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	resolveDataDir(&cfg, fs)
	if len(positional) < 1 || len(positional) > 2 {
		return errors.New("usage: doketfire reindex [--dir=DIR] <collection> [index]")
	}
	collection := positional[0]
	index := ""
	if len(positional) == 2 {
		index = positional[1]
	}
	store, err := storage.Open(cfg.Dir, storage.Options{})
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()
	if index != "" {
		if err := store.BuildIndex(ctx, collection, index); err != nil {
			return err
		}
		fmt.Printf("index %s/%s is ready\n", collection, index)
		return nil
	}
	var names []string
	if err := store.View(func(tx storage.ReadTx) error {
		def, err := tx.Collection(collection)
		if err != nil {
			return err
		}
		for _, idx := range def.Indexes {
			names = append(names, idx.Name)
		}
		return nil
	}); err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Printf("collection %s has no indexes\n", collection)
		return nil
	}
	for _, name := range names {
		if err := store.BuildIndex(ctx, collection, name); err != nil {
			return err
		}
		fmt.Printf("index %s/%s is ready\n", collection, name)
	}
	return nil
}

func superuserCommand(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return errors.New("usage: doketfire superuser create [--dir=DIR] <email> <password>")
	}
	cfg := app.DefaultConfig()
	fs := flag.NewFlagSet("superuser create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.Dir, "dir", envOr("DOKET_DIR", cfg.Dir), "data directory")
	positional, err := parseFlags(fs, args[1:])
	if err != nil {
		return err
	}
	resolveDataDir(&cfg, fs)
	if len(positional) != 2 {
		return errors.New("usage: doketfire superuser create [--dir=DIR] <email> <password>")
	}
	store, err := storage.Open(cfg.Dir, storage.Options{})
	if err != nil {
		return err
	}
	defer store.Close()
	svc, err := auth.New(store, auth.Config{})
	if err != nil {
		return err
	}
	id, err := svc.CreateAdmin(positional[0], positional[1])
	if err != nil {
		return err
	}
	fmt.Printf("superuser %s created with id %s\n", id.Email, id.ID)
	return nil
}

// parseFlags parses fs while allowing flags to appear before, between or after
// positional arguments. The flag package stops at the first positional
// argument, so flags are parsed one at a time here. A literal "--" ends flag
// parsing and every remaining argument is treated as positional.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			return positional, nil
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		name := strings.TrimPrefix(arg, "-")
		if strings.HasPrefix(arg, "--") {
			name = strings.TrimPrefix(arg, "--")
		}
		if name == "" || name[0] == '-' || name[0] == '=' {
			return nil, fmt.Errorf("bad flag syntax: %s", arg)
		}
		hasValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
			hasValue = true
		}
		f := fs.Lookup(name)
		if f == nil {
			return nil, fmt.Errorf("flag provided but not defined: -%s", name)
		}
		chunk := args[i : i+1]
		if !hasValue && !isBoolFlag(f) {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag needs an argument: -%s", name)
			}
			chunk = args[i : i+2]
			i++
		}
		if err := fs.Parse(chunk); err != nil {
			return nil, err
		}
	}
	return positional, nil
}

// isBoolFlag reports whether f may be used without a value, matching the flag
// package's handling of boolean flags.
func isBoolFlag(f *flag.Flag) bool {
	if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
		return b.IsBoolFlag()
	}
	return false
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".restore-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	previous := ""
	if _, err := os.Stat(dst); err == nil {
		previous = dst + ".pre-restore-" + time.Now().UTC().Format("20060102-150405")
		if err := os.Rename(dst, previous); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, dst); err != nil {
		if previous != "" {
			_ = os.Rename(previous, dst)
		}
		return err
	}
	if previous != "" {
		fmt.Printf("previous database kept at %s\n", previous)
	}
	return nil
}

// resolveDataDir keeps existing installations working after the default data
// directory moved from ./dp_data to ./data. An explicit --dir flag or a
// DOKET_DIR/DOCPOCKET_DIR variable always wins.
func resolveDataDir(cfg *app.Config, fs *flag.FlagSet) {
	if cfg.Dir != "./data" || flagWasSet(fs, "dir") || envSet("DOKET_DIR") || envSet("DOCPOCKET_DIR") {
		return
	}
	if _, err := os.Stat("./data"); err == nil {
		return
	}
	if st, err := os.Stat("./dp_data"); err == nil && st.IsDir() {
		cfg.Dir = "./dp_data"
	}
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func envSet(key string) bool {
	v, ok := os.LookupEnv(key)
	return ok && v != ""
}

// lookupEnv reads a DOKET_* variable and falls back to the legacy DOCPOCKET_*
// name for one release.
func lookupEnv(key string) (string, bool) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v, true
	}
	if legacy, ok := legacyEnvKey(key); ok {
		if v, ok := os.LookupEnv(legacy); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

func legacyEnvKey(key string) (string, bool) {
	if strings.HasPrefix(key, "DOKET_") {
		return "DOCPOCKET_" + strings.TrimPrefix(key, "DOKET_"), true
	}
	return "", false
}

func envOr(key, fallback string) string {
	if v, ok := lookupEnv(key); ok {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, ok := lookupEnv(key)
	if !ok {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt(key string, fallback int) int {
	v, ok := lookupEnv(key)
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envInt64(key string, fallback int64) int64 {
	v, ok := lookupEnv(key)
	if !ok {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
