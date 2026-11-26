package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/doketfire/doketfire/internal/collections"
)

type passwordLoginRequest struct {
	Identity string `json:"identity"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// handleAuthWithPassword implements POST /api/collections/{col}/auth-with-password.
func (s *Server) handleAuthWithPassword(w http.ResponseWriter, r *http.Request) {
	col := r.PathValue("col")
	def, err := s.collectionByName(col)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if def.Type != collections.TypeAuth {
		writeError(w, http.StatusBadRequest, "not_an_auth_collection",
			"this endpoint is only available for auth collections", nil)
		return
	}
	var req passwordLoginRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	identity := firstNonEmpty(strings.TrimSpace(req.Identity), strings.TrimSpace(req.Email))
	session, err := s.auth.Authenticate(col, identity, req.Password)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// handleAuthRegister implements POST /api/collections/{col}/auth-register.
// The body is a regular document plus the password; arbitrary extra fields are
// copied to the new user record.
func (s *Server) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	col := r.PathValue("col")
	def, err := s.collectionByName(col)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if def.Type != collections.TypeAuth {
		writeError(w, http.StatusBadRequest, "not_an_auth_collection",
			"this endpoint is only available for auth collections", nil)
		return
	}
	var body collections.Document
	if err := s.decodeJSON(w, r, &body); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	email, _ := body[collections.FieldEmail].(string)
	password, _ := body["password"].(string)
	delete(body, "password")
	delete(body, collections.FieldPasswordHash)
	delete(body, collections.FieldTokenKey)
	delete(body, collections.FieldID)
	delete(body, collections.FieldCreated)
	delete(body, collections.FieldUpdated)
	delete(body, collections.FieldVerified)

	allowed, err := s.authorize(def, "create", identityFrom(r), nil, body, r.URL.Query())
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "you cannot register users in this collection", nil)
		return
	}
	session, err := s.auth.Register(col, email, password, body)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

// handleAuthRefresh implements POST /api/collections/{col}/auth-refresh.
func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	col := r.PathValue("col")
	var req refreshRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	session, err := s.auth.Refresh(req.RefreshToken, col)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if session.Identity == nil || session.Identity.Collection != col {
		writeError(w, http.StatusUnauthorized, "unauthorized", "refresh token does not belong to this collection", nil)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// handleAuthLogout implements POST /api/collections/{col}/auth-logout.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if err := s.auth.Logout(req.RefreshToken); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGlobalRefresh implements POST /api/auth/refresh. It works for both
// users and superusers and is the endpoint used by the TypeScript SDK.
func (s *Server) handleGlobalRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	session, err := s.auth.Refresh(req.RefreshToken, "")
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// handleGlobalLogout implements POST /api/auth/logout.
func (s *Server) handleGlobalLogout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if err := s.auth.Logout(req.RefreshToken); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMe returns the current user or superuser.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id := s.requireUser(w, r)
	if id == nil {
		return
	}
	writeJSON(w, http.StatusOK, id)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	Password        string `json:"password"`
}

// handleChangePassword implements
// PATCH /api/collections/{col}/records/{id}/password. Users can change their
// own password (with the current one); superusers can change any password.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	col, docID := r.PathValue("col"), r.PathValue("id")
	def, err := s.collectionByName(col)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if def.Type != collections.TypeAuth {
		writeError(w, http.StatusBadRequest, "not_an_auth_collection",
			"this endpoint is only available for auth collections", nil)
		return
	}
	id := identityFrom(r)
	if id == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required", nil)
		return
	}
	if !id.IsAdmin() && id.ID != docID {
		writeError(w, http.StatusForbidden, "forbidden", "you cannot change this password", nil)
		return
	}
	var req changePasswordRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if !id.IsAdmin() && strings.TrimSpace(req.CurrentPassword) == "" {
		writeError(w, http.StatusBadRequest, "current_password_required", "current password is required", nil)
		return
	}
	if err := s.auth.ChangePassword(col, docID, req.CurrentPassword, req.Password); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": true})
}

// ensureEmailIndex guarantees that auth collections have a unique email index.
// Existing non-unique indexes are upgraded in place; storage marks them as
// building again when the shape changes.
func ensureEmailIndex(def *collections.CollectionDef) {
	if def.Type != collections.TypeAuth {
		return
	}
	if idx, ok := def.IndexForField(collections.FieldEmail, false); ok {
		if !idx.Unique {
			idx.Unique = true
			idx.State = "building"
		}
		return
	}
	name := "email_unique"
	for n := 2; ; n++ {
		if _, taken := def.Index(name); !taken {
			break
		}
		name = "email_unique_" + strconv.Itoa(n)
	}
	def.Indexes = append(def.Indexes, collections.Index{
		Name:   name,
		Field:  collections.FieldEmail,
		Unique: true,
		State:  "building",
	})
}
