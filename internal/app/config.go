// Package app wires the storage, auth, rules, query, realtime, file and HTTP
// layers into a runnable Doketfire instance.
package app

import (
	"errors"
	"strings"
	"time"

	"github.com/doketfire/doketfire/internal/api"
	"github.com/doketfire/doketfire/internal/query"
)

// Config is the complete runtime configuration.
type Config struct {
	Dir      string
	HTTPAddr string
	// HTTPSDomain enables automatic TLS certificates through ACME
	// (TLS-ALPN-01) for this domain.
	HTTPSDomain string
	HTTPSAddr   string
	TLSCert     string
	TLSKey      string
	AutocertDir string

	ReadOnly bool
	NoSync   bool

	MaxDocSize     int
	MaxBodyBytes   int64
	MaxUploadBytes int64
	CORSOrigins    []string
	StrictIndexes  bool
	TrustProxy     bool

	AccessTTL  time.Duration
	RefreshTTL time.Duration

	QueryLimits query.Limits
	LogLevel    string
	Version     string
	Hooks       api.Hooks
}

// DefaultConfig returns production-friendly defaults.
func DefaultConfig() Config {
	return Config{
		Dir:            "./data",
		HTTPAddr:       "0.0.0.0:8090",
		HTTPSAddr:      ":443",
		MaxBodyBytes:   2 << 20,
		MaxUploadBytes: 25 << 20,
		AccessTTL:      15 * time.Minute,
		RefreshTTL:     30 * 24 * time.Hour,
		QueryLimits:    query.DefaultLimits(),
		LogLevel:       "info",
		Version:        "dev",
	}
}

// Validate checks runtime invariants before opening any file.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Dir) == "" {
		return errors.New("app: data directory is required")
	}
	if c.HTTPAddr == "" && c.HTTPSDomain == "" && (c.TLSCert == "" || c.TLSKey == "") {
		return errors.New("app: an HTTP or HTTPS listen address is required")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("app: both --tls-cert and --tls-key are required")
	}
	if c.HTTPSDomain != "" && c.TLSCert != "" {
		return errors.New("app: choose either ACME automatic TLS or a certificate pair")
	}
	if c.MaxDocSize < 0 || c.MaxBodyBytes < 0 || c.MaxUploadBytes < 0 {
		return errors.New("app: size limits cannot be negative")
	}
	return nil
}
