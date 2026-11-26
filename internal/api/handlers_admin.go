package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/doketfire/doketfire/internal/auth"
	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/rules"
	"github.com/doketfire/doketfire/internal/storage"
)

// ---- setup ----

func (s *Server) handleAdminSetupStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.auth.AdminCount()
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": count > 0})
}

func (s *Server) handleAdminSetup(w http.ResponseWriter, r *http.Request) {
	var req passwordLoginRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	count, err := s.auth.AdminCount()
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if count > 0 {
		s.writeServiceError(w, r, auth.ErrSetupComplete)
		return
	}
	email := firstNonEmpty(strings.TrimSpace(req.Email), strings.TrimSpace(req.Identity))
	id, err := s.auth.CreateFirstAdmin(email, req.Password)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, id)
}

// ---- sessions ----

func (s *Server) handleAdminAuth(w http.ResponseWriter, r *http.Request) {
	var req passwordLoginRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	email := firstNonEmpty(strings.TrimSpace(req.Email), strings.TrimSpace(req.Identity))
	session, err := s.auth.AuthenticateAdmin(email, req.Password)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) handleAdminRefresh(w http.ResponseWriter, r *http.Request) {
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
	if session.Identity == nil || !session.Identity.IsAdmin() {
		writeError(w, http.StatusUnauthorized, "unauthorized", "refresh token is not a superuser session", nil)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) handleAdminMe(w http.ResponseWriter, r *http.Request) {
	id := s.requireAdmin(w, r)
	if id == nil {
		return
	}
	writeJSON(w, http.StatusOK, id)
}

// ---- collections ----

type adminCollection struct {
	*collections.CollectionDef
	DocumentCount int `json:"documentCount"`
}

func (s *Server) handleAdminListCollections(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	items := []adminCollection{}
	err := s.store.View(func(tx storage.ReadTx) error {
		defs, err := tx.Collections()
		if err != nil {
			return err
		}
		items = make([]adminCollection, 0, len(defs))
		for _, def := range defs {
			count, err := tx.CountDocs(def.Name)
			if err != nil {
				return err
			}
			items = append(items, adminCollection{CollectionDef: def, DocumentCount: count})
		}
		return nil
	})
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAdminGetCollection(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	col := r.PathValue("col")
	var out adminCollection
	err := s.store.View(func(tx storage.ReadTx) error {
		def, err := tx.Collection(col)
		if err != nil {
			return err
		}
		count, err := tx.CountDocs(col)
		if err != nil {
			return err
		}
		out = adminCollection{CollectionDef: def, DocumentCount: count}
		return nil
	})
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminCreateCollection(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var def collections.CollectionDef
	if err := s.decodeJSON(w, r, &def); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	def.Name = strings.TrimSpace(def.Name)
	def.Normalize()
	for i := range def.Indexes {
		if def.Indexes[i].State == "" {
			def.Indexes[i].State = "building"
		}
	}
	ensureEmailIndex(&def)
	if err := s.validateDefinition(&def); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if _, err := s.collectionByName(def.Name); err == nil {
		writeError(w, http.StatusConflict, "collection_exists", "a collection with this name already exists", nil)
		return
	} else if !errors.Is(err, storage.ErrCollectionNotFound) {
		s.writeServiceError(w, r, err)
		return
	}
	if err := s.store.Update(func(tx storage.WriteTx) error { return tx.SaveCollection(&def) }); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	out, err := s.buildPendingIndexes(r.Context(), def.Name)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleAdminUpdateCollection(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	col := r.PathValue("col")
	existing, err := s.collectionByName(col)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	var patch collections.Document
	if err := s.decodeJSON(w, r, &patch); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if name, ok := patch["name"].(string); ok && name != "" && name != col {
		writeError(w, http.StatusUnprocessableEntity, "invalid_collection", "collections cannot be renamed", nil)
		return
	}
	delete(patch, "name")

	raw, err := json.Marshal(existing)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	current, err := collections.Decode(raw)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	collections.MergePatch(current, patch)
	merged, err := json.Marshal(current)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	var def collections.CollectionDef
	if err := json.Unmarshal(merged, &def); err != nil {
		s.writeServiceError(w, r, &collections.ValidationError{Message: "invalid collection definition: " + err.Error()})
		return
	}
	def.Name = col
	if def.Type != existing.Type {
		writeError(w, http.StatusUnprocessableEntity, "invalid_collection",
			"the collection type cannot be changed after creation", nil)
		return
	}
	def.Normalize()
	ensureEmailIndex(&def)
	if err := s.validateDefinition(&def); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if err := s.store.Update(func(tx storage.WriteTx) error { return tx.SaveCollection(&def) }); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	out, err := s.buildPendingIndexes(r.Context(), col)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminDeleteCollection(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	col := r.PathValue("col")
	if r.URL.Query().Get("confirm") != col {
		writeError(w, http.StatusBadRequest, "confirmation_required",
			"add ?confirm="+col+" to delete the collection and all of its documents", nil)
		return
	}
	if err := s.store.Update(func(tx storage.WriteTx) error { return tx.DeleteCollection(col) }); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if err := s.files.DeleteCollection(col); err != nil {
		s.logger.Warn("could not delete collection files", "collection", col, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminReindex(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	col := r.PathValue("col")
	def, err := s.collectionByName(col)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	index := r.URL.Query().Get("index")
	if r.ContentLength != 0 && r.Body != nil {
		var req struct {
			Index string `json:"index"`
		}
		if err := s.decodeJSON(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
			s.writeServiceError(w, r, err)
			return
		}
		if index == "" {
			index = req.Index
		}
	}
	if index != "" {
		if _, ok := def.Index(index); !ok {
			s.writeServiceError(w, r, storage.ErrIndexNotFound)
			return
		}
		if err := s.store.BuildIndex(r.Context(), col, index); err != nil {
			s.writeServiceError(w, r, err)
			return
		}
	} else {
		for _, idx := range def.Indexes {
			if err := s.store.BuildIndex(r.Context(), col, idx.Name); err != nil {
				s.writeServiceError(w, r, err)
				return
			}
		}
	}
	out, err := s.collectionByName(col)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) validateDefinition(def *collections.CollectionDef) error {
	if err := def.Validate(); err != nil {
		return &collections.ValidationError{Message: err.Error()}
	}
	if err := s.rules.ValidateDefinition(def); err != nil {
		return err
	}
	return nil
}

func (s *Server) buildPendingIndexes(ctx context.Context, col string) (*adminCollection, error) {
	def, err := s.collectionByName(col)
	if err != nil {
		return nil, err
	}
	for _, idx := range def.Indexes {
		if idx.State == "ready" {
			continue
		}
		if err := s.store.BuildIndex(ctx, col, idx.Name); err != nil {
			return nil, err
		}
	}
	var out adminCollection
	err = s.store.View(func(tx storage.ReadTx) error {
		updated, err := tx.Collection(col)
		if err != nil {
			return err
		}
		count, err := tx.CountDocs(col)
		if err != nil {
			return err
		}
		out = adminCollection{CollectionDef: updated, DocumentCount: count}
		return nil
	})
	return &out, err
}

// ---- rules and diagnostics ----

type ruleTestRequest struct {
	Rule       string         `json:"rule"`
	Collection string         `json:"collection"`
	Operation  string         `json:"operation"`
	Auth       map[string]any `json:"auth"`
	Resource   map[string]any `json:"resource"`
	Body       map[string]any `json:"body"`
	Query      map[string]any `json:"query"`
}

func (s *Server) handleRuleTester(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var req ruleTestRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	expr := strings.TrimSpace(req.Rule)
	locked := false
	if expr == "" && req.Collection != "" {
		def, err := s.collectionByName(req.Collection)
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		op := req.Operation
		if op == "" {
			op = "view"
		}
		rule, ok := def.RuleSet(op)
		if !ok {
			locked = true
		} else {
			expr = strings.TrimSpace(rule)
		}
	} else if expr == "" && req.Operation == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "provide a rule or a collection and operation", nil)
		return
	}
	if locked {
		writeJSON(w, http.StatusOK, map[string]any{
			"allowed": false,
			"rule":    "",
			"locked":  true,
			"message": "no rule is configured for this operation; only superusers are allowed",
		})
		return
	}
	allowed, err := s.rules.CheckExpression(expr, rules.Context{
		Operation: req.Operation,
		Identity:  req.Auth,
		Resource:  req.Resource,
		Body:      req.Body,
		Query:     req.Query,
	})
	if err != nil {
		var ruleErr *rules.RuleError
		if errors.As(err, &ruleErr) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_rule", err.Error(), nil)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "rule_evaluation_failed", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"allowed": allowed, "rule": expr, "locked": false})
}

func (s *Server) handleAdminCheck(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	report, err := s.store.Check(ctx)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleAdminBackup(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	tmp, err := os.CreateTemp("", "doketfire-backup-*.db")
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := s.store.Backup(tmp); err != nil {
		tmp.Close()
		s.writeServiceError(w, r, err)
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		s.writeServiceError(w, r, err)
		return
	}
	defer tmp.Close()
	st, err := tmp.Stat()
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="doketfire-`+time.Now().UTC().Format("20060102-150405")+`.db"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "backup.db", st.ModTime(), tmp)
}
