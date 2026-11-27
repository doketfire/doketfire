// Package config loads and validates Doketfire runtime configuration from
// defaults, strict YAML config files, and DOKET_* environment variables.
// Declarative collection schemas remain separate in doketfire.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config holds the runtime configuration for a Doketfire server instance.
type Config struct {
	SchemaPath    string              `yaml:"schema_path"`
	Server        ServerConfig        `yaml:"server"`
	Database      DatabaseConfig      `yaml:"database"`
	Auth          AuthConfig          `yaml:"auth"`
	Storage       StorageConfig       `yaml:"storage"`
	Query         QueryConfig         `yaml:"query"`
	Observability ObservabilityConfig `yaml:"observability"`
}

// ServerConfig configures the HTTP/TLS listener.
type ServerConfig struct {
	Addr string    `yaml:"addr"`
	CORS []string  `yaml:"cors"`
	TLS  TLSConfig `yaml:"tls"`
}

// TLSConfig configures transport security.
type TLSConfig struct {
	Mode        string `yaml:"mode"` // none | acme | manual
	Domain      string `yaml:"domain"`
	Cert        string `yaml:"cert"`
	Key         string `yaml:"key"`
	AutocertDir string `yaml:"autocert_dir"`
}

// DatabaseConfig configures the database engine and connection pool.
type DatabaseConfig struct {
	Engine       string        `yaml:"engine"` // sqlite | libsql | bbolt
	Path         string        `yaml:"path"`
	BusyTimeout  time.Duration `yaml:"busy_timeout"`
	MaxOpenConns int           `yaml:"max_open_conns"`
	Synchronous  string        `yaml:"synchronous"` // NORMAL | FULL
}

// AuthConfig configures authentication providers, roles, and token TTLs.
type AuthConfig struct {
	Providers  []string      `yaml:"providers"`
	Roles      []string      `yaml:"roles"`
	AccessTTL  time.Duration `yaml:"access_ttl"`
	RefreshTTL time.Duration `yaml:"refresh_ttl"`
}

// StorageConfig configures file storage.
type StorageConfig struct {
	Driver   string                  `yaml:"driver"` // local | s3
	LocalDir string                  `yaml:"local_dir"`
	Buckets  map[string]BucketConfig `yaml:"buckets"`
}

// BucketConfig configures a logical storage bucket.
type BucketConfig struct {
	Public       bool  `yaml:"public"`
	MaxSizeBytes int64 `yaml:"max_size_bytes"`
}

// QueryConfig bounds query pagination and execution time.
type QueryConfig struct {
	DefaultLimit int           `yaml:"default_limit"`
	MaxLimit     int           `yaml:"max_limit"`
	MaxSortDocs  int           `yaml:"max_sort_docs"`
	Timeout      time.Duration `yaml:"timeout"`
}

// ObservabilityConfig configures logging and metrics exposure.
type ObservabilityConfig struct {
	LogLevel string `yaml:"log_level"` // debug | info | warn | error
	Metrics  bool   `yaml:"metrics"`
}

// Default returns safe baseline configuration values.
func Default() Config {
	return Config{
		SchemaPath: "doketfire.yaml",
		Server: ServerConfig{
			Addr: "127.0.0.1:8090",
			TLS: TLSConfig{
				Mode: "none",
			},
		},
		Database: DatabaseConfig{
			Engine:       "sqlite",
			Path:         "./data/app.db",
			BusyTimeout:  5 * time.Second,
			MaxOpenConns: 4,
			Synchronous:  "NORMAL",
		},
		Auth: AuthConfig{
			Providers:  []string{"password"},
			Roles:      []string{"anon", "user", "admin"},
			AccessTTL:  15 * time.Minute,
			RefreshTTL: 720 * time.Hour,
		},
		Storage: StorageConfig{
			Driver:   "local",
			LocalDir: "./data/files",
		},
		Query: QueryConfig{
			DefaultLimit: 50,
			MaxLimit:     500,
			MaxSortDocs:  10000,
			Timeout:      5 * time.Second,
		},
		Observability: ObservabilityConfig{
			LogLevel: "info",
			Metrics:  true,
		},
	}
}

// Load reads runtime configuration from defaults, an optional strict YAML file,
// and DOKET_* environment variables (with DOCPOCKET_* legacy aliases where applicable),
// then validates the resulting configuration.
func Load(path string) (Config, error) {
	cfg := Default()
	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config %q: %w", path, err)
		}
		if err := decodeYAML(bytes.NewReader(data), &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %q: %w", path, err)
		}
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	normalize(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Parse decodes strict YAML config on top of defaults without reading env vars.
func Parse(r io.Reader) (Config, error) {
	cfg := Default()
	if err := decodeYAML(r, &cfg); err != nil {
		return Config{}, err
	}
	normalize(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func decodeYAML(r io.Reader, cfg *Config) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read yaml: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("decode yaml: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("decode yaml: multiple documents are not supported")
		}
		return fmt.Errorf("decode yaml: %w", err)
	}
	return nil
}

func normalize(cfg *Config) {
	cfg.SchemaPath = strings.TrimSpace(cfg.SchemaPath)
	cfg.Server.Addr = strings.TrimSpace(cfg.Server.Addr)
	cfg.Server.TLS.Mode = strings.ToLower(strings.TrimSpace(cfg.Server.TLS.Mode))
	cfg.Database.Engine = strings.ToLower(strings.TrimSpace(cfg.Database.Engine))
	cfg.Database.Path = strings.TrimSpace(cfg.Database.Path)
	cfg.Database.Synchronous = strings.ToUpper(strings.TrimSpace(cfg.Database.Synchronous))
	cfg.Storage.Driver = strings.ToLower(strings.TrimSpace(cfg.Storage.Driver))
	cfg.Storage.LocalDir = strings.TrimSpace(cfg.Storage.LocalDir)
	cfg.Observability.LogLevel = strings.ToLower(strings.TrimSpace(cfg.Observability.LogLevel))
}

func applyEnv(cfg *Config) error {
	if v, ok := lookupEnv("DOKET_SCHEMA"); ok {
		cfg.SchemaPath = v
	}
	if v, ok := lookupEnv("DOKET_HTTP", "DOCPOCKET_HTTP"); ok {
		cfg.Server.Addr = v
	}
	if v, ok := lookupEnv("DOKET_CORS", "DOCPOCKET_CORS"); ok {
		cfg.Server.CORS = splitCSV(v)
	}
	if v, ok := lookupEnv("DOKET_TLS_MODE"); ok {
		cfg.Server.TLS.Mode = v
	}
	if v, ok := lookupEnv("DOKET_TLS_DOMAIN"); ok {
		cfg.Server.TLS.Domain = v
	}
	if v, ok := lookupEnv("DOKET_TLS_CERT"); ok {
		cfg.Server.TLS.Cert = v
	}
	if v, ok := lookupEnv("DOKET_TLS_KEY"); ok {
		cfg.Server.TLS.Key = v
	}
	if v, ok := lookupEnv("DOKET_AUTOCERT_DIR"); ok {
		cfg.Server.TLS.AutocertDir = v
	}
	if v, ok := lookupEnv("DOKET_DB_ENGINE"); ok {
		cfg.Database.Engine = v
	}
	if v, ok := lookupEnv("DOKET_DB_PATH"); ok {
		cfg.Database.Path = v
	}
	if v, ok := lookupEnv("DOKET_DB_SYNCHRONOUS"); ok {
		cfg.Database.Synchronous = v
	}
	if err := applyDurationEnv(&cfg.Database.BusyTimeout, "DOKET_DB_BUSY_TIMEOUT"); err != nil {
		return err
	}
	if err := applyIntEnv(&cfg.Database.MaxOpenConns, "DOKET_DB_MAX_OPEN_CONNS"); err != nil {
		return err
	}
	if err := applyDurationEnv(&cfg.Auth.AccessTTL, "DOKET_AUTH_ACCESS_TTL"); err != nil {
		return err
	}
	if err := applyDurationEnv(&cfg.Auth.RefreshTTL, "DOKET_AUTH_REFRESH_TTL"); err != nil {
		return err
	}
	if v, ok := lookupEnv("DOKET_STORAGE_DRIVER"); ok {
		cfg.Storage.Driver = v
	}
	if v, ok := lookupEnv("DOKET_STORAGE_LOCAL_DIR"); ok {
		cfg.Storage.LocalDir = v
	}
	if err := applyIntEnv(&cfg.Query.DefaultLimit, "DOKET_QUERY_DEFAULT_LIMIT"); err != nil {
		return err
	}
	if err := applyIntEnv(&cfg.Query.MaxLimit, "DOKET_QUERY_MAX_LIMIT"); err != nil {
		return err
	}
	if err := applyIntEnv(&cfg.Query.MaxSortDocs, "DOKET_QUERY_MAX_SORT_DOCS"); err != nil {
		return err
	}
	if err := applyDurationEnv(&cfg.Query.Timeout, "DOKET_QUERY_TIMEOUT"); err != nil {
		return err
	}
	if v, ok := lookupEnv("DOKET_LOG_LEVEL", "DOCPOCKET_LOG_LEVEL"); ok {
		cfg.Observability.LogLevel = v
	}
	if v, ok := lookupEnv("DOKET_METRICS"); ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("invalid DOKET_METRICS %q: %w", v, err)
		}
		cfg.Observability.Metrics = b
	}
	return nil
}

func lookupEnv(keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
			return v, true
		}
	}
	return "", false
}

func applyIntEnv(dst *int, key string) error {
	v, ok := lookupEnv(key)
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	*dst = n
	return nil
}

func applyDurationEnv(dst *time.Duration, key string) error {
	v, ok := lookupEnv(key)
	if !ok {
		return nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	*dst = d
	return nil
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate checks runtime configuration invariants before starting services.
func (c Config) Validate() error {
	if c.SchemaPath == "" {
		return errors.New("config: schema_path is required")
	}
	if c.Server.Addr == "" {
		return errors.New("config: server.addr is required")
	}
	switch c.Server.TLS.Mode {
	case "none":
	case "acme":
		if strings.TrimSpace(c.Server.TLS.Domain) == "" {
			return errors.New("config: server.tls.domain is required when tls.mode is acme")
		}
	case "manual":
		if strings.TrimSpace(c.Server.TLS.Cert) == "" || strings.TrimSpace(c.Server.TLS.Key) == "" {
			return errors.New("config: server.tls.cert and server.tls.key are required when tls.mode is manual")
		}
	default:
		return fmt.Errorf("config: invalid server.tls.mode %q", c.Server.TLS.Mode)
	}

	switch c.Database.Engine {
	case "sqlite", "libsql", "bbolt":
	default:
		return fmt.Errorf("config: invalid database.engine %q", c.Database.Engine)
	}
	if c.Database.Path == "" {
		return errors.New("config: database.path is required")
	}
	if c.Database.BusyTimeout <= 0 {
		return errors.New("config: database.busy_timeout must be positive")
	}
	if c.Database.MaxOpenConns < 1 {
		return errors.New("config: database.max_open_conns must be >= 1")
	}
	if c.Database.Synchronous != "NORMAL" && c.Database.Synchronous != "FULL" {
		return fmt.Errorf("config: database.synchronous must be NORMAL or FULL, got %q", c.Database.Synchronous)
	}

	if len(c.Auth.Providers) == 0 {
		return errors.New("config: auth.providers cannot be empty")
	}
	if len(c.Auth.Roles) == 0 {
		return errors.New("config: auth.roles cannot be empty")
	}
	if c.Auth.AccessTTL <= 0 {
		return errors.New("config: auth.access_ttl must be positive")
	}
	if c.Auth.RefreshTTL < c.Auth.AccessTTL {
		return errors.New("config: auth.refresh_ttl must be >= auth.access_ttl")
	}

	switch c.Storage.Driver {
	case "local":
		if c.Storage.LocalDir == "" {
			return errors.New("config: storage.local_dir is required for local driver")
		}
	case "s3":
	default:
		return fmt.Errorf("config: invalid storage.driver %q", c.Storage.Driver)
	}
	for name, b := range c.Storage.Buckets {
		if b.MaxSizeBytes < 0 {
			return fmt.Errorf("config: bucket %q max_size_bytes cannot be negative", name)
		}
	}

	if c.Query.DefaultLimit < 1 {
		return errors.New("config: query.default_limit must be >= 1")
	}
	if c.Query.DefaultLimit > c.Query.MaxLimit {
		return errors.New("config: query.default_limit cannot exceed query.max_limit")
	}
	if c.Query.MaxSortDocs < 1 {
		return errors.New("config: query.max_sort_docs must be >= 1")
	}
	if c.Query.Timeout <= 0 {
		return errors.New("config: query.timeout must be positive")
	}

	switch c.Observability.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: invalid observability.log_level %q", c.Observability.LogLevel)
	}
	return nil
}
