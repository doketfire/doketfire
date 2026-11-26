package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doketfire/doketfire/internal/auth"
	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/files"
	"github.com/doketfire/doketfire/internal/query"
	"github.com/doketfire/doketfire/internal/realtime"
	"github.com/doketfire/doketfire/internal/rules"
	"github.com/doketfire/doketfire/internal/storage"
)

// Config controls API behavior.
type Config struct {
	MaxBodyBytes   int64
	MaxUploadBytes int64
	CORSOrigins    []string
	StrictIndexes  bool
	QueryLimits    query.Limits
	// TrustProxy enables X-Forwarded-For for client IP extraction.
	TrustProxy bool
}

func (c Config) withDefaults() Config {
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 2 << 20
	}
	if c.MaxUploadBytes <= 0 {
		c.MaxUploadBytes = 25 << 20
	}
	if c.QueryLimits.DefaultLimit == 0 {
		c.QueryLimits = query.DefaultLimits()
	}
	return c
}

// Hooks defines lifecycle callbacks for record operations.
type Hooks struct {
	OnRecordBeforeCreate func(col string, doc collections.Document, id *auth.Identity) error
	OnRecordAfterCreate  func(col string, doc collections.Document, id *auth.Identity)
	OnRecordBeforeUpdate func(col string, doc collections.Document, old collections.Document, id *auth.Identity) error
	OnRecordAfterUpdate  func(col string, doc collections.Document, old collections.Document, id *auth.Identity)
	OnRecordBeforeDelete func(col string, doc collections.Document, id *auth.Identity) error
	OnRecordAfterDelete  func(col string, doc collections.Document, id *auth.Identity)
}

// MaintenanceReport summarizes a database maintenance operation.
type MaintenanceReport struct {
	Before int64  `json:"before"`
	After  int64  `json:"after"`
	Backup string `json:"backup"`
}

// Maintenance is the optional production maintenance contract implemented by
// the application layer. Implementations must block concurrent HTTP requests
// for the duration of a store swap.
type Maintenance interface {
	Compact(context.Context) (*MaintenanceReport, error)
	Restore(context.Context, io.Reader) (*MaintenanceReport, error)
}

// Deps are the services used by the HTTP layer.
type Deps struct {
	Config      Config
	Store       storage.Storage
	Auth        *auth.Service
	Rules       *rules.Engine
	Hub         *realtime.Hub
	Files       *files.Store
	Engine      *query.Engine
	UI          fs.FS
	Logger      *slog.Logger
	Hooks       Hooks
	Version     string
	Maintenance Maintenance
}

// Server is the Doketfire HTTP API.
type Server struct {
	store       storage.Storage
	auth        *auth.Service
	rules       *rules.Engine
	hub         *realtime.Hub
	files       *files.Store
	engine      *query.Engine
	ui          fs.FS
	logger      *slog.Logger
	cfg         Config
	hooks       Hooks
	version     string
	started     time.Time
	handler     http.Handler
	limiter     *rateLimiter
	settings    *settingsStore
	logs        *requestLogRing
	maintenance Maintenance
	setupMu     sync.Mutex
}

// New builds the API server.
func New(deps Deps) (*Server, error) {
	if deps.Store == nil {
		return nil, errors.New("api: storage is required")
	}
	if deps.Auth == nil {
		return nil, errors.New("api: auth service is required")
	}
	if deps.Rules == nil {
		return nil, errors.New("api: rules engine is required")
	}
	if deps.Files == nil {
		return nil, errors.New("api: file store is required")
	}
	if deps.Hub == nil {
		deps.Hub = realtime.NewHub(256, 64)
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	engine := deps.Engine
	if engine == nil {
		engine = query.NewEngine(deps.Store, deps.Config.QueryLimits)
	}
	if deps.Version == "" {
		deps.Version = "dev"
	}
	cfg := deps.Config.withDefaults()
	settings := newSettingsStore(deps.Store, cfg)
	s := &Server{
		store:       deps.Store,
		auth:        deps.Auth,
		rules:       deps.Rules,
		hub:         deps.Hub,
		files:       deps.Files,
		engine:      engine,
		ui:          deps.UI,
		logger:      logger,
		cfg:         cfg,
		hooks:       deps.Hooks,
		version:     deps.Version,
		started:     time.Now(),
		limiter:     newRateLimiter(settings.Get().AuthRateLimit, time.Minute),
		settings:    settings,
		logs:        newRequestLogRing(settings.Get().LogRetention),
		maintenance: deps.Maintenance,
	}
	s.engine.StrictIndexes = s.cfg.StrictIndexes
	s.hub.SetAuthorizer(s.authorizeRealtimeEvent)
	s.handler = s.routes()
	return s, nil
}

// Handler returns the HTTP handler with middleware applied.
func (s *Server) Handler() http.Handler {
	h := http.Handler(s.handler)
	h = s.authMiddleware(h)
	h = s.corsMiddleware(h)
	h = s.gzipMiddleware(h)
	h = s.securityHeaders(h)
	h = s.rateLimitMiddleware(h)
	h = s.recoverMiddleware(h)
	h = s.logMiddleware(h)
	h = s.requestIDMiddleware(h)
	return h
}

// Close releases realtime resources.
func (s *Server) Close() { s.hub.Close() }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	mux.HandleFunc("GET /api/collections/{col}/records", s.handleListRecords)
	mux.HandleFunc("GET /api/collections/{col}/records/{id}", s.handleGetRecord)
	mux.HandleFunc("POST /api/collections/{col}/records", s.handleCreateRecord)
	mux.HandleFunc("PATCH /api/collections/{col}/records/{id}", s.handleUpdateRecord)
	mux.HandleFunc("PUT /api/collections/{col}/records/{id}", s.handleReplaceRecord)
	mux.HandleFunc("DELETE /api/collections/{col}/records/{id}", s.handleDeleteRecord)
	mux.HandleFunc("POST /api/batch", s.handleBatch)
	mux.HandleFunc("POST /api/collections/{col}/aggregate", s.handleAggregate)
	mux.HandleFunc("POST /api/transactions", s.handleTransactions)
	mux.HandleFunc("GET /api/collections/{col}/views", s.handleListViews)
	mux.HandleFunc("POST /api/collections/{col}/views", s.handleCreateView)
	mux.HandleFunc("DELETE /api/collections/{col}/views/{name}", s.handleDeleteView)

	mux.HandleFunc("POST /api/collections/{col}/auth-with-password", s.handleAuthWithPassword)
	mux.HandleFunc("POST /api/collections/{col}/auth-register", s.handleAuthRegister)
	mux.HandleFunc("PATCH /api/collections/{col}/records/{id}/password", s.handleChangePassword)
	mux.HandleFunc("POST /api/collections/{col}/auth-refresh", s.handleAuthRefresh)
	mux.HandleFunc("POST /api/collections/{col}/auth-logout", s.handleAuthLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.HandleFunc("POST /api/auth/refresh", s.handleGlobalRefresh)
	mux.HandleFunc("POST /api/auth/logout", s.handleGlobalLogout)

	mux.HandleFunc("GET /api/realtime", s.handleRealtimeStream)
	mux.HandleFunc("POST /api/realtime", s.handleRealtimeSubscribe)

	mux.HandleFunc("POST /api/collections/{col}/records/{id}/files", s.handleUploadFile)
	mux.HandleFunc("DELETE /api/collections/{col}/records/{id}/files/{name}", s.handleDeleteFile)
	mux.HandleFunc("GET /api/files/{col}/{id}/{name}", s.handleDownloadFile)

	mux.HandleFunc("GET /api/admin/setup", s.handleAdminSetupStatus)
	mux.HandleFunc("POST /api/admin/setup", s.handleAdminSetup)
	mux.HandleFunc("POST /api/admin/auth-with-password", s.handleAdminAuth)
	mux.HandleFunc("POST /api/admin/auth-refresh", s.handleAdminRefresh)
	mux.HandleFunc("GET /api/admin/me", s.handleAdminMe)
	mux.HandleFunc("GET /api/admin/collections", s.handleAdminListCollections)
	mux.HandleFunc("POST /api/admin/collections", s.handleAdminCreateCollection)
	mux.HandleFunc("GET /api/admin/collections/{col}", s.handleAdminGetCollection)
	mux.HandleFunc("PATCH /api/admin/collections/{col}", s.handleAdminUpdateCollection)
	mux.HandleFunc("DELETE /api/admin/collections/{col}", s.handleAdminDeleteCollection)
	mux.HandleFunc("POST /api/admin/collections/{col}/reindex", s.handleAdminReindex)
	mux.HandleFunc("GET /api/admin/collections/{col}/records", s.handleAdminListRecords)
	mux.HandleFunc("POST /api/admin/rules/test", s.handleRuleTester)
	mux.HandleFunc("POST /api/admin/check", s.handleAdminCheck)
	mux.HandleFunc("GET /api/admin/backup", s.handleAdminBackup)

	mux.HandleFunc("GET /api/admin/logs", s.handleAdminLogs)
	mux.HandleFunc("GET /api/admin/settings", s.handleAdminGetSettings)
	mux.HandleFunc("PATCH /api/admin/settings", s.handleAdminPatchSettings)
	mux.HandleFunc("GET /api/admin/admins", s.handleAdminListAdmins)
	mux.HandleFunc("POST /api/admin/admins", s.handleAdminCreateAdmin)
	mux.HandleFunc("DELETE /api/admin/admins/{id}", s.handleAdminDeleteAdmin)
	mux.HandleFunc("GET /api/admin/sessions", s.handleAdminListSessions)
	mux.HandleFunc("POST /api/admin/sessions/revoke", s.handleAdminRevokeSessions)
	mux.HandleFunc("POST /api/admin/compact", s.handleAdminCompact)
	mux.HandleFunc("POST /api/admin/restore", s.handleAdminRestore)

	mux.HandleFunc("GET /_/", s.handleUI)
	mux.HandleFunc("GET /{$}", s.handleRoot)

	return mux
}

// ---- context ----

type contextKey int

const identityKey contextKey = iota

func identityFrom(r *http.Request) *auth.Identity {
	v, _ := r.Context().Value(identityKey).(*auth.Identity)
	return v
}

func withIdentity(r *http.Request, id *auth.Identity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), identityKey, id))
}

// ---- response helpers ----

type apiErrorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, apiErrorBody{Error: apiError{Code: code, Message: message, Details: details}})
}

func (s *Server) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, msg, details := mapError(err)
	if status >= 500 {
		s.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	writeError(w, status, code, msg, details)
}

func mapError(err error) (int, string, string, map[string]any) {
	if err == nil {
		return 200, "ok", "", nil
	}
	var validation *collections.ValidationError
	var parseErr *query.ParseError
	var conflict *storage.ConflictError
	var ruleErr *rules.RuleError
	var maxBytes *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytes):
		return http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large", nil
	case errors.Is(err, errInvalidJSON):
		return http.StatusBadRequest, "invalid_json", err.Error(), nil
	case errors.As(err, &validation):
		details := map[string]any{"message": validation.Message}
		if validation.Field != "" {
			details["field"] = validation.Field
		}
		return http.StatusUnprocessableEntity, "validation_failed", validation.Error(), details
	case errors.As(err, &parseErr):
		return http.StatusBadRequest, "invalid_filter", parseErr.Error(), map[string]any{"pos": parseErr.Pos}
	case errors.As(err, &conflict):
		return http.StatusConflict, "conflict", err.Error(), map[string]any{"index": conflict.Index}
	case errors.As(err, &ruleErr):
		return http.StatusUnprocessableEntity, "invalid_rule", ruleErr.Error(), nil
	case errors.Is(err, storage.ErrCollectionNotFound):
		return http.StatusNotFound, "collection_not_found", "collection not found", nil
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, files.ErrNotFound):
		return http.StatusNotFound, "not_found", "record not found", nil
	case errors.Is(err, storage.ErrInvalidID):
		return http.StatusBadRequest, "invalid_id", "invalid document id", nil
	case errors.Is(err, storage.ErrConflict):
		return http.StatusConflict, "conflict", err.Error(), nil
	case errors.Is(err, storage.ErrIndexNotFound):
		return http.StatusNotFound, "index_not_found", "index not found", nil
	case errors.Is(err, storage.ErrReadOnly):
		return http.StatusServiceUnavailable, "read_only", "storage is read-only", nil
	case errors.Is(err, query.ErrInvalidCursor):
		return http.StatusBadRequest, "invalid_cursor", err.Error(), nil
	case errors.Is(err, query.ErrLimit):
		return http.StatusBadRequest, "invalid_limit", err.Error(), nil
	case errors.Is(err, query.ErrNoIndex):
		return http.StatusBadRequest, "index_required", err.Error(), nil
	case errors.Is(err, query.ErrSortLimit):
		return http.StatusUnprocessableEntity, "sort_limit", err.Error(), nil
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized", "invalid credentials or token", nil
	case errors.Is(err, auth.ErrRefreshReuse):
		return http.StatusUnauthorized, "refresh_reuse", err.Error(), nil
	case errors.Is(err, auth.ErrEmailTaken):
		return http.StatusConflict, "email_taken", err.Error(), nil
	case errors.Is(err, auth.ErrWeakPassword):
		return http.StatusUnprocessableEntity, "weak_password", err.Error(), nil
	case errors.Is(err, auth.ErrInvalidEmail):
		return http.StatusUnprocessableEntity, "invalid_email", err.Error(), nil
	case errors.Is(err, auth.ErrSetupComplete):
		return http.StatusConflict, "setup_complete", err.Error(), nil
	case errors.Is(err, auth.ErrAdminLast):
		return http.StatusConflict, "admin_last", err.Error(), nil
	case errors.Is(err, auth.ErrAdminSelf):
		return http.StatusConflict, "admin_self", err.Error(), nil
	case errors.Is(err, auth.ErrAdminNotFound):
		return http.StatusNotFound, "admin_not_found", err.Error(), nil
	case errors.Is(err, files.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "file_too_large", err.Error(), nil
	case errors.Is(err, files.ErrInvalidName), errors.Is(err, files.ErrInvalidPath):
		return http.StatusBadRequest, "invalid_file", err.Error(), nil
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "request timed out", nil
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "canceled", "request canceled", nil
	default:
		return http.StatusInternalServerError, "internal_error", "internal server error", nil
	}
}

// errInvalidJSON marks a malformed request body independently from storage
// errors, so mapError can answer 400 instead of 500.
var errInvalidJSON = errors.New("invalid JSON body")

// decodeJSON reads a bounded JSON object and rejects trailing data.
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return fmt.Errorf("%w: request body is required", errInvalidJSON)
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %v", errInvalidJSON, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: unexpected trailing JSON", errInvalidJSON)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: unexpected trailing JSON", errInvalidJSON)
	}
	return nil
}

// collectionByName loads a collection definition in its own read transaction.
func (s *Server) collectionByName(name string) (*collections.CollectionDef, error) {
	var def *collections.CollectionDef
	err := s.store.View(func(tx storage.ReadTx) error {
		var err error
		def, err = tx.Collection(name)
		return err
	})
	return def, err
}

func queryValues(u url.Values) map[string]any {
	out := make(map[string]any, len(u))
	for k, vs := range u {
		if len(vs) == 1 {
			out[k] = vs[0]
		} else if len(vs) > 1 {
			list := make([]any, len(vs))
			for i, v := range vs {
				list[i] = v
			}
			out[k] = list
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%w: must be a positive integer", query.ErrLimit)
	}
	return n, nil
}

func parseFields(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !collections.ValidPath(p) {
			return nil, fmt.Errorf("invalid field %q", p)
		}
		out = append(out, p)
	}
	return out, nil
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			if ip := strings.TrimSpace(xff); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

func (s *Server) collectionDef(tx storage.ReadTx, name string) (*collections.CollectionDef, error) {
	return tx.Collection(name)
}

func sanitizeRecord(def *collections.CollectionDef, d collections.Document) collections.Document {
	if d == nil {
		return nil
	}
	// Responses only need a read-only view: hidden fields are removed and the
	// encoder never mutates nested values.
	return d.SanitizeView()
}
