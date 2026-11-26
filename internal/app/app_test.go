package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/doketfire/doketfire/internal/auth"
	"github.com/doketfire/doketfire/internal/collections"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Dir != "./data" {
		t.Fatalf("unexpected dir: %s", cfg.Dir)
	}
	if cfg.HTTPAddr != "0.0.0.0:8090" {
		t.Fatalf("unexpected addr: %s", cfg.HTTPAddr)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name: "empty dir",
			mutate: func(c *Config) {
				c.Dir = "   "
			},
			wantErr: true,
		},
		{
			name: "no listen address",
			mutate: func(c *Config) {
				c.HTTPAddr = ""
				c.HTTPSDomain = ""
				c.TLSCert = ""
				c.TLSKey = ""
			},
			wantErr: true,
		},
		{
			name: "cert without key",
			mutate: func(c *Config) {
				c.TLSCert = "cert.pem"
				c.TLSKey = ""
			},
			wantErr: true,
		},
		{
			name: "key without cert",
			mutate: func(c *Config) {
				c.TLSCert = ""
				c.TLSKey = "key.pem"
			},
			wantErr: true,
		},
		{
			name: "acme and cert pair both set",
			mutate: func(c *Config) {
				c.HTTPSDomain = "example.com"
				c.TLSCert = "cert.pem"
				c.TLSKey = "key.pem"
			},
			wantErr: true,
		},
		{
			name: "negative max doc size",
			mutate: func(c *Config) {
				c.MaxDocSize = -1
			},
			wantErr: true,
		},
		{
			name: "negative max body bytes",
			mutate: func(c *Config) {
				c.MaxBodyBytes = -1
			},
			wantErr: true,
		},
		{
			name: "negative max upload bytes",
			mutate: func(c *Config) {
				c.MaxUploadBytes = -1
			},
			wantErr: true,
		},
		{
			name: "valid cert pair without plain http",
			mutate: func(c *Config) {
				c.HTTPAddr = ""
				c.TLSCert = "cert.pem"
				c.TLSKey = "key.pem"
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for case %q", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for case %q: %v", tc.name, err)
			}
		})
	}
}

func TestAppLifecycle(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Dir = filepath.Join(dir, "data")
	cfg.HTTPAddr = "127.0.0.1:0"
	cfg.LogLevel = "debug"

	var beforeCreateCalled bool
	cfg.Hooks.OnRecordBeforeCreate = func(col string, doc collections.Document, id *auth.Identity) error {
		beforeCreateCalled = true
		return nil
	}

	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New app failed: %v", err)
	}
	defer app.Close()

	if app.Handler() == nil {
		t.Fatal("expected non-nil HTTP handler")
	}

	// Validate logger levels
	for _, lvl := range []string{"debug", "warn", "warning", "error", "info", "unknown"} {
		l := newLogger(lvl)
		if l == nil {
			t.Fatalf("newLogger(%q) returned nil", lvl)
		}
	}

	// Test Run with a canceled context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	if err := app.Run(ctx); err != nil {
		t.Fatalf("Run with canceled context returned error: %v", err)
	}

	// Verify idempotency of Close
	if err := app.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}

	// Safe to call on nil
	var nilApp *App
	if err := nilApp.Close(); err != nil {
		t.Fatalf("nilApp.Close returned error: %v", err)
	}

	_ = beforeCreateCalled
}

func TestAppNewErrors(t *testing.T) {
	// Invalid config
	cfg := DefaultConfig()
	cfg.Dir = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error with empty dir")
	}

	// Read-only with non-existent data.db should fail
	cfg = DefaultConfig()
	cfg.Dir = filepath.Join(t.TempDir(), "nonexistent")
	cfg.ReadOnly = true
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error opening read-only store on empty dir")
	}
}
