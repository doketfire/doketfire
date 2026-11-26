package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/doketfire/doketfire/internal/auth"
	"github.com/doketfire/doketfire/internal/collections"
)

// ---- superusers ----

func (s *Server) handleAdminListAdmins(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	admins, err := s.auth.ListAdmins()
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": admins})
}

func (s *Server) handleAdminCreateAdmin(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	identity, err := s.auth.CreateAdmin(strings.TrimSpace(req.Email), req.Password)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, auth.AdminInfo{
		ID:      identity.ID,
		Email:   identity.Email,
		Created: identity.Record.GetString(collections.FieldCreated),
		Updated: identity.Record.GetString(collections.FieldUpdated),
	})
}

func (s *Server) handleAdminDeleteAdmin(w http.ResponseWriter, r *http.Request) {
	current := s.requireAdmin(w, r)
	if current == nil {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid superuser id", nil)
		return
	}
	if err := s.auth.DeleteAdmin(id, current.ID); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- sessions ----

const defaultSessionPageSize = 100

func (s *Server) handleAdminListSessions(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "admin" && kind != "user" {
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind must be admin or user", nil)
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if limit == 0 {
		limit = defaultSessionPageSize
	} else if limit > maxLogPageSize {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 500", nil)
		return
	}
	items, err := s.auth.ListSessions(kind, limit)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type sessionRevocation struct {
	family   string
	all      bool
	identity *auth.Identity
}

func (s *Server) decodeRevocation(w http.ResponseWriter, r *http.Request) (sessionRevocation, bool) {
	fields, err := s.decodeJSONObject(w, r)
	if err != nil {
		s.writeServiceError(w, r, err)
		return sessionRevocation{}, false
	}
	if len(fields) != 1 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed",
			"provide exactly one of family, all or identity", nil)
		return sessionRevocation{}, false
	}
	for name := range fields {
		switch name {
		case "family":
			var family string
			if err := json.Unmarshal(fields[name], &family); err != nil || strings.TrimSpace(family) == "" {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed", "family must be a non-empty string", map[string]any{"field": "family"})
				return sessionRevocation{}, false
			}
			return sessionRevocation{family: strings.TrimSpace(family)}, true
		case "all":
			var all bool
			if err := json.Unmarshal(fields[name], &all); err != nil || !all {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed", "all must be true", map[string]any{"field": "all"})
				return sessionRevocation{}, false
			}
			return sessionRevocation{all: true}, true
		case "identity":
			var value struct {
				Kind       string `json:"kind"`
				Collection string `json:"collection"`
				ID         string `json:"id"`
			}
			if err := decodeStrictRaw(fields[name], &value); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed", "invalid identity", map[string]any{"field": "identity"})
				return sessionRevocation{}, false
			}
			value.Kind = strings.TrimSpace(value.Kind)
			value.ID = strings.TrimSpace(value.ID)
			value.Collection = strings.TrimSpace(value.Collection)
			if value.Kind != "admin" && value.Kind != "user" {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed", "identity.kind must be admin or user", map[string]any{"field": "identity.kind"})
				return sessionRevocation{}, false
			}
			if value.ID == "" || (value.Kind == "user" && value.Collection == "") {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed", "identity.id and identity.collection are required", map[string]any{"field": "identity"})
				return sessionRevocation{}, false
			}
			return sessionRevocation{identity: &auth.Identity{
				Kind:       value.Kind,
				Collection: value.Collection,
				ID:         value.ID,
			}}, true
		default:
			writeError(w, http.StatusUnprocessableEntity, "validation_failed",
				"provide exactly one of family, all or identity", nil)
			return sessionRevocation{}, false
		}
	}
	writeError(w, http.StatusUnprocessableEntity, "validation_failed",
		"provide exactly one of family, all or identity", nil)
	return sessionRevocation{}, false
}

func decodeStrictRaw(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func (s *Server) handleAdminRevokeSessions(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	req, ok := s.decodeRevocation(w, r)
	if !ok {
		return
	}
	var (
		revoked int
		err     error
	)
	switch {
	case req.family != "":
		revoked, err = s.auth.RevokeFamily(req.family)
	case req.all:
		revoked, err = s.auth.RevokeAllRefreshTokens()
	default:
		revoked, err = s.auth.RevokeAllSessionsWithCount(req.identity)
	}
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": revoked})
}

// ---- maintenance ----

func (s *Server) handleAdminCompact(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	if s.maintenance == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "maintenance is not available", nil)
		return
	}
	report, err := s.maintenance.Compact(r.Context())
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleAdminRestore(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	if s.maintenance == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "maintenance is not available", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded database is too large", nil)
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_multipart", "expected a multipart/form-data body", nil)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	src, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_file", `the form must include a "file" part`, nil)
		return
	}
	defer src.Close()
	if header.Size > s.cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded database is too large", nil)
		return
	}
	tmp, err := os.CreateTemp("", "doketfire-restore-*.db")
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	written, err := io.CopyN(tmp, src, s.cfg.MaxUploadBytes+1)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = tmp.Close()
		s.writeServiceError(w, r, err)
		return
	}
	if written > s.cfg.MaxUploadBytes {
		_ = tmp.Close()
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded database is too large", nil)
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		s.writeServiceError(w, r, err)
		return
	}
	if err := tmp.Close(); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	// Reject arbitrary files before the application layer touches data.db.
	if err := validateBboltDatabase(tmpName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_database", "uploaded file is not a valid bbolt database: "+err.Error(), nil)
		return
	}
	file, err := os.Open(tmpName)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	defer file.Close()
	report, err := s.maintenance.Restore(r.Context(), file)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func validateBboltDatabase(path string) error {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	return db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("_meta")) == nil || tx.Bucket([]byte("_collections")) == nil {
			return errors.New("missing Doketfire system buckets")
		}
		return nil
	})
}
