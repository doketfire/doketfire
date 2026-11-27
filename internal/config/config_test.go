package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}
	if cfg.SchemaPath != "doketfire.yaml" {
		t.Fatalf("SchemaPath = %q", cfg.SchemaPath)
	}
	if cfg.Server.Addr != "127.0.0.1:8090" {
		t.Fatalf("Addr = %q", cfg.Server.Addr)
	}
	if cfg.Database.Engine != "sqlite" || cfg.Database.Synchronous != "NORMAL" || cfg.Database.MaxOpenConns != 4 {
		t.Fatalf("Database = %+v", cfg.Database)
	}
	if cfg.Query.DefaultLimit != 50 || cfg.Query.MaxLimit != 500 {
		t.Fatalf("Query = %+v", cfg.Query)
	}
}

func TestLoadYAMLAndEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doketfire.config.yaml")
	raw := `
schema_path: ./schema/app.yaml
server:
  addr: 0.0.0.0:9000
  cors: [https://example.com]
  tls:
    mode: none
database:
  engine: sqlite
  path: ./var/app.db
  busy_timeout: 8s
  max_open_conns: 8
  synchronous: full
query:
  default_limit: 25
  max_limit: 250
  timeout: 3s
observability:
  log_level: warn
  metrics: false
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DOKET_HTTP", "127.0.0.1:8123")
	t.Setenv("DOKET_CORS", "https://a.test, https://b.test")
	t.Setenv("DOKET_DB_MAX_OPEN_CONNS", "12")
	t.Setenv("DOKET_METRICS", "true")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.SchemaPath != "./schema/app.yaml" {
		t.Fatalf("SchemaPath = %q", cfg.SchemaPath)
	}
	if cfg.Server.Addr != "127.0.0.1:8123" {
		t.Fatalf("Addr = %q", cfg.Server.Addr)
	}
	if len(cfg.Server.CORS) != 2 || cfg.Server.CORS[1] != "https://b.test" {
		t.Fatalf("CORS = %v", cfg.Server.CORS)
	}
	if cfg.Database.MaxOpenConns != 12 || cfg.Database.Synchronous != "FULL" || cfg.Database.BusyTimeout != 8*time.Second {
		t.Fatalf("Database = %+v", cfg.Database)
	}
	if !cfg.Observability.Metrics || cfg.Observability.LogLevel != "warn" {
		t.Fatalf("Observability = %+v", cfg.Observability)
	}
}

func TestLoadLegacyEnvAlias(t *testing.T) {
	t.Setenv("DOKET_HTTP", "")
	t.Setenv("DOCPOCKET_HTTP", "127.0.0.1:7777")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Addr != "127.0.0.1:7777" {
		t.Fatalf("Addr = %q", cfg.Server.Addr)
	}

	t.Setenv("DOKET_HTTP", "127.0.0.1:8888")
	cfg, err = Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Addr != "127.0.0.1:8888" {
		t.Fatalf("DOKET_HTTP should override legacy alias, got %q", cfg.Server.Addr)
	}
}

func TestParseRejectsUnknownFieldsAndMultipleDocs(t *testing.T) {
	if _, err := Parse(strings.NewReader("unknown_key: true\n")); err == nil {
		t.Fatal("expected unknown field error")
	}
	if _, err := Parse(strings.NewReader("schema_path: a.yaml\n---\nschema_path: b.yaml\n")); err == nil {
		t.Fatal("expected multiple YAML documents error")
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"empty schema", func(c *Config) { c.SchemaPath = "" }, "schema_path"},
		{"empty addr", func(c *Config) { c.Server.Addr = "" }, "server.addr"},
		{"bad tls mode", func(c *Config) { c.Server.TLS.Mode = "auto" }, "tls.mode"},
		{"acme no domain", func(c *Config) { c.Server.TLS.Mode = "acme" }, "tls.domain"},
		{"manual no key", func(c *Config) { c.Server.TLS.Mode = "manual"; c.Server.TLS.Cert = "a" }, "tls.key"},
		{"bad engine", func(c *Config) { c.Database.Engine = "postgres" }, "database.engine"},
		{"empty db path", func(c *Config) { c.Database.Path = "" }, "database.path"},
		{"bad max open", func(c *Config) { c.Database.MaxOpenConns = 0 }, "max_open_conns"},
		{"bad sync", func(c *Config) { c.Database.Synchronous = "OFF" }, "synchronous"},
		{"limit inversion", func(c *Config) { c.Query.DefaultLimit = 600; c.Query.MaxLimit = 500 }, "default_limit"},
		{"bad refresh ttl", func(c *Config) { c.Auth.RefreshTTL = time.Minute }, "refresh_ttl"},
		{"bad log level", func(c *Config) { c.Observability.LogLevel = "trace" }, "log_level"},
		{"negative bucket size", func(c *Config) {
			c.Storage.Buckets = map[string]BucketConfig{"avatars": {MaxSizeBytes: -1}}
		}, "max_size_bytes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}
