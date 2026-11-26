package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/doketfire/doketfire/internal/api"
	"github.com/doketfire/doketfire/internal/auth"
	"github.com/doketfire/doketfire/internal/files"
	"github.com/doketfire/doketfire/internal/query"
	"github.com/doketfire/doketfire/internal/realtime"
	"github.com/doketfire/doketfire/internal/rules"
	"github.com/doketfire/doketfire/internal/storage"
	"github.com/doketfire/doketfire/ui"
)

// App owns every service and the HTTP server.
type App struct {
	Config Config
	Store  *storage.BoltStore
	Auth   *auth.Service
	Rules  *rules.Engine
	Hub    *realtime.Hub
	Files  *files.Store
	Engine *query.Engine
	API    *api.Server
	HTTP   *http.Server
	Logger *slog.Logger

	gate *switchingHandler

	tlsConfig *tls.Config
}

// appServices is one complete generation of services behind the stable HTTP
// handler. Maintenance swaps a whole generation at once.
type appServices struct {
	store   *storage.BoltStore
	authSvc *auth.Service
	rules   *rules.Engine
	hub     *realtime.Hub
	files   *files.Store
	engine  *query.Engine
	api     *api.Server
	handler http.Handler
}

// switchingHandler is the stable http.Server.Handler. It never mutates: an
// atomic pointer selects the current generation, and the gate lock blocks
// ordinary requests while a maintenance operation closes/swaps the store.
type switchingHandler struct {
	gateMu   sync.RWMutex
	maintain sync.Mutex
	current  atomic.Pointer[http.Handler]
}

func newSwitchingHandler() *switchingHandler {
	var empty http.Handler
	h := &switchingHandler{}
	h.current.Store(&empty)
	return h
}

func (h *switchingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler := h.current.Load()
	if handler == nil || *handler == nil {
		writeUnavailable(w)
		return
	}
	// The maintenance request itself must not wait for its own gate lock.
	if isMaintenanceRequest(r) {
		(*handler).ServeHTTP(w, r)
		return
	}
	h.gateMu.RLock()
	defer h.gateMu.RUnlock()
	handler = h.current.Load()
	if handler == nil || *handler == nil {
		writeUnavailable(w)
		return
	}
	(*handler).ServeHTTP(w, r)
}

func (h *switchingHandler) swap(handler http.Handler) { h.current.Store(&handler) }

func (h *switchingHandler) beginMaintenance() func() {
	h.maintain.Lock()
	h.gateMu.Lock()
	return func() {
		h.gateMu.Unlock()
		h.maintain.Unlock()
	}
}

func isMaintenanceRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	return r.URL.Path == "/api/admin/compact" || r.URL.Path == "/api/admin/restore"
}

func writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":{"code":"maintenance","message":"services are being restarted"}}`))
}

// New opens the data directory and wires the application.
func New(cfg Config) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logger := newLogger(cfg.LogLevel)
	a := &App{
		Config: cfg,
		Logger: logger,
		gate:   newSwitchingHandler(),
	}
	services, err := a.buildServices()
	if err != nil {
		return nil, err
	}
	a.installServices(services)

	addr := cfg.HTTPAddr
	if cfg.HTTPSDomain != "" {
		addr = cfg.HTTPSAddr
	}
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           a.gate,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// WriteTimeout must stay zero: SSE streams are long-lived.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
		ErrorLog:       slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	a.HTTP = httpServer
	if cfg.HTTPSDomain != "" {
		cacheDir := cfg.AutocertDir
		if cacheDir == "" {
			cacheDir = filepath.Join(cfg.Dir, "autocert")
		}
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.HTTPSDomain),
			Cache:      autocert.DirCache(cacheDir),
		}
		a.tlsConfig = manager.TLSConfig()
	}
	return a, nil
}

func (a *App) apiConfig() api.Config {
	return api.Config{
		MaxBodyBytes:   a.Config.MaxBodyBytes,
		MaxUploadBytes: a.Config.MaxUploadBytes,
		CORSOrigins:    a.Config.CORSOrigins,
		StrictIndexes:  a.Config.StrictIndexes,
		QueryLimits:    a.Config.QueryLimits,
		TrustProxy:     a.Config.TrustProxy,
	}
}

// buildServices creates a fresh generation. Nothing is installed until the
// caller explicitly calls installServices.
func (a *App) buildServices() (*appServices, error) {
	store, err := storage.Open(a.Config.Dir, storage.Options{
		ReadOnly:   a.Config.ReadOnly,
		NoSync:     a.Config.NoSync,
		MaxDocSize: a.Config.MaxDocSize,
	})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*appServices, error) {
		_ = store.Close()
		return nil, err
	}
	authSvc, err := auth.New(store, auth.Config{
		AccessTTL:  a.Config.AccessTTL,
		RefreshTTL: a.Config.RefreshTTL,
	})
	if err != nil {
		return fail(err)
	}
	ruleEngine, err := rules.New()
	if err != nil {
		return fail(err)
	}
	fileStore, err := files.New(filepath.Join(a.Config.Dir, "files"), a.Config.MaxUploadBytes)
	if err != nil {
		return fail(err)
	}
	hub := realtime.NewHub(1024, 128)
	engine := query.NewEngine(store, a.Config.QueryLimits)
	apiServer, err := api.New(api.Deps{
		Config:      a.apiConfig(),
		Store:       store,
		Auth:        authSvc,
		Rules:       ruleEngine,
		Hub:         hub,
		Files:       fileStore,
		Engine:      engine,
		UI:          ui.FS(),
		Logger:      a.Logger,
		Hooks:       a.Config.Hooks,
		Version:     a.Config.Version,
		Maintenance: a,
	})
	if err != nil {
		hub.Close()
		return fail(err)
	}
	return &appServices{
		store:   store,
		authSvc: authSvc,
		rules:   ruleEngine,
		hub:     hub,
		files:   fileStore,
		engine:  engine,
		api:     apiServer,
		handler: apiServer.Handler(),
	}, nil
}

func (a *App) installServices(s *appServices) {
	a.Store = s.store
	a.Auth = s.authSvc
	a.Rules = s.rules
	a.Hub = s.hub
	a.Files = s.files
	a.Engine = s.engine
	a.API = s.api
	a.gate.swap(s.handler)
}

func (a *App) currentServices() *appServices {
	return &appServices{
		store:   a.Store,
		authSvc: a.Auth,
		rules:   a.Rules,
		hub:     a.Hub,
		files:   a.Files,
		engine:  a.Engine,
		api:     a.API,
	}
}

func closeServices(s *appServices) {
	if s == nil {
		return
	}
	// Server.Close closes the hub owned by this generation.
	if s.api != nil {
		s.api.Close()
	} else if s.hub != nil {
		s.hub.Close()
	}
	if s.store != nil {
		_ = s.store.Close()
	}
}

// Handler exposes the stable HTTP handler for tests and integrations.
func (a *App) Handler() http.Handler { return a.HTTP.Handler }

// Run listens until the context is canceled or the server fails.
func (a *App) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	errCh := make(chan error, 1)
	go func() {
		var err error
		switch {
		case a.tlsConfig != nil:
			a.HTTP.TLSConfig = a.tlsConfig
			err = a.HTTP.ListenAndServeTLS("", "")
		case a.Config.TLSCert != "":
			err = a.HTTP.ListenAndServeTLS(a.Config.TLSCert, a.Config.TLSKey)
		default:
			err = a.HTTP.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.HTTP.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		return nil
	}
}

// Close stops realtime delivery and closes the database.
func (a *App) Close() error {
	if a == nil {
		return nil
	}
	if a.gate != nil {
		a.gate.gateMu.Lock()
		defer a.gate.gateMu.Unlock()
	}
	current := a.currentServices()
	closeServices(current)
	if current.store != nil {
		return current.store.Close()
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
