package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/doketfire/doketfire/internal/storage"
)

const (
	panelSettingsBucket = "_meta"
	panelSettingsKey    = "panel_settings"

	defaultTitle         = "Doketfire"
	defaultAuthRateLimit = 10
	defaultLogRetention  = 500
	minAuthRateLimit     = 1
	maxAuthRateLimit     = 1000
	minLogRetention      = 50
	maxLogRetention      = 5000
	maxPanelTitleRunes   = 80
)

// PanelSettings is the panel configuration persisted in _meta/panel_settings.
type PanelSettings struct {
	Title         string   `json:"title"`
	CORSOrigins   []string `json:"corsOrigins"`
	AuthRateLimit int      `json:"authRateLimit"`
	LogRetention  int      `json:"logRetention"`
	UpdatedAt     string   `json:"updatedAt"`
}

type settingsStore struct {
	mu       sync.RWMutex
	current  PanelSettings
	defaults PanelSettings
}

func defaultPanelSettings(cfg Config) PanelSettings {
	return PanelSettings{
		Title:         defaultTitle,
		CORSOrigins:   append(make([]string, 0, len(cfg.CORSOrigins)), cfg.CORSOrigins...),
		AuthRateLimit: defaultAuthRateLimit,
		LogRetention:  defaultLogRetention,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

func clonePanelSettings(in PanelSettings) PanelSettings {
	in.CORSOrigins = append(make([]string, 0, len(in.CORSOrigins)), in.CORSOrigins...)
	return in
}

func newSettingsStore(store storage.Storage, cfg Config) *settingsStore {
	defaults := defaultPanelSettings(cfg)
	current := clonePanelSettings(defaults)
	if store != nil {
		var raw []byte
		_ = store.View(func(tx storage.ReadTx) error {
			var err error
			raw, err = tx.RawGet(panelSettingsBucket, []byte(panelSettingsKey))
			return err
		})
		if raw != nil {
			current = decodePanelSettings(raw, defaults)
		}
	}
	return &settingsStore{current: current, defaults: defaults}
}

func (s *settingsStore) Get() PanelSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clonePanelSettings(s.current)
}

func (s *settingsStore) Set(v PanelSettings) {
	s.mu.Lock()
	s.current = clonePanelSettings(v)
	s.mu.Unlock()
}

// decodePanelSettings keeps usable stored fields and falls back to defaults for
// absent or invalid ones, so a manually edited database cannot crash the API.
func decodePanelSettings(raw []byte, defaults PanelSettings) PanelSettings {
	out := clonePanelSettings(defaults)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return out
	}
	var full PanelSettings
	if err := json.Unmarshal(raw, &full); err != nil {
		return out
	}
	if v, ok := fields["title"]; ok && len(v) > 0 && string(v) != "null" {
		if utf8.RuneCountInString(full.Title) <= maxPanelTitleRunes {
			out.Title = full.Title
		}
	}
	if v, ok := fields["corsOrigins"]; ok && len(v) > 0 && string(v) != "null" {
		if origins, err := validateCORSOrigins(full.CORSOrigins); err == nil {
			out.CORSOrigins = origins
		}
	}
	if full.AuthRateLimit >= minAuthRateLimit && full.AuthRateLimit <= maxAuthRateLimit {
		out.AuthRateLimit = full.AuthRateLimit
	}
	if full.LogRetention >= minLogRetention && full.LogRetention <= maxLogRetention {
		out.LogRetention = full.LogRetention
	}
	if _, err := time.Parse(time.RFC3339, full.UpdatedAt); err == nil {
		out.UpdatedAt = full.UpdatedAt
	}
	return out
}

func validateCORSOrigins(origins []string) ([]string, error) {
	out := make([]string, 0, len(origins))
	seen := map[string]bool{}
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		if origin != "*" {
			u, err := url.Parse(origin)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
				u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
				(u.Path != "" && u.Path != "/") {
				return nil, fmt.Errorf("invalid CORS origin %q", origin)
			}
			origin = u.Scheme + "://" + u.Host
		}
		if !seen[origin] {
			seen[origin] = true
			out = append(out, origin)
		}
	}
	return out, nil
}

type settingsPatch struct {
	title         *string
	corsOrigins   *[]string
	authRateLimit *int
	logRetention  *int
}

func (s *Server) decodeSettingsPatch(w http.ResponseWriter, r *http.Request) (settingsPatch, bool) {
	fields, err := s.decodeJSONObject(w, r)
	if err != nil {
		s.writeServiceError(w, r, err)
		return settingsPatch{}, false
	}
	allowed := map[string]bool{
		"title": true, "corsOrigins": true, "authRateLimit": true, "logRetention": true,
	}
	var patch settingsPatch
	for name := range fields {
		if !allowed[name] {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed",
				fmt.Sprintf("unknown setting %q", name), map[string]any{"field": name})
			return settingsPatch{}, false
		}
	}
	if raw, ok := fields["title"]; ok {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "title must be a string", map[string]any{"field": "title"})
			return settingsPatch{}, false
		}
		if utf8.RuneCountInString(value) > maxPanelTitleRunes {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed",
				fmt.Sprintf("title must be at most %d characters", maxPanelTitleRunes), map[string]any{"field": "title"})
			return settingsPatch{}, false
		}
		value = strings.TrimSpace(value)
		patch.title = &value
	}
	if raw, ok := fields["corsOrigins"]; ok {
		var value []string
		if err := json.Unmarshal(raw, &value); err != nil || string(raw) == "null" {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "corsOrigins must be an array of origins", map[string]any{"field": "corsOrigins"})
			return settingsPatch{}, false
		}
		origins, err := validateCORSOrigins(value)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error(), map[string]any{"field": "corsOrigins"})
			return settingsPatch{}, false
		}
		patch.corsOrigins = &origins
	}
	if raw, ok := fields["authRateLimit"]; ok {
		var value int
		if err := json.Unmarshal(raw, &value); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "authRateLimit must be an integer", map[string]any{"field": "authRateLimit"})
			return settingsPatch{}, false
		}
		if value < minAuthRateLimit || value > maxAuthRateLimit {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed",
				fmt.Sprintf("authRateLimit must be between %d and %d", minAuthRateLimit, maxAuthRateLimit),
				map[string]any{"field": "authRateLimit"})
			return settingsPatch{}, false
		}
		patch.authRateLimit = &value
	}
	if raw, ok := fields["logRetention"]; ok {
		var value int
		if err := json.Unmarshal(raw, &value); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "logRetention must be an integer", map[string]any{"field": "logRetention"})
			return settingsPatch{}, false
		}
		if value < minLogRetention || value > maxLogRetention {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed",
				fmt.Sprintf("logRetention must be between %d and %d", minLogRetention, maxLogRetention),
				map[string]any{"field": "logRetention"})
			return settingsPatch{}, false
		}
		patch.logRetention = &value
	}
	return patch, true
}

func applySettingsPatch(base PanelSettings, patch settingsPatch, updatedAt string) PanelSettings {
	out := clonePanelSettings(base)
	if patch.title != nil {
		out.Title = *patch.title
	}
	if patch.corsOrigins != nil {
		out.CORSOrigins = append([]string(nil), (*patch.corsOrigins)...)
	}
	if patch.authRateLimit != nil {
		out.AuthRateLimit = *patch.authRateLimit
	}
	if patch.logRetention != nil {
		out.LogRetention = *patch.logRetention
	}
	out.UpdatedAt = updatedAt
	return out
}

func (s *Server) handleAdminGetSettings(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	writeJSON(w, http.StatusOK, s.settings.Get())
}

func (s *Server) handleAdminPatchSettings(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	patch, ok := s.decodeSettingsPatch(w, r)
	if !ok {
		return
	}
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	var persisted PanelSettings
	err := s.store.Update(func(tx storage.WriteTx) error {
		raw, err := tx.RawGet(panelSettingsBucket, []byte(panelSettingsKey))
		if err != nil {
			return err
		}
		base := s.settings.defaults
		if raw != nil {
			base = decodePanelSettings(raw, s.settings.defaults)
		}
		persisted = applySettingsPatch(base, patch, updatedAt)
		encoded, err := json.Marshal(persisted)
		if err != nil {
			return err
		}
		return tx.RawPut(panelSettingsBucket, []byte(panelSettingsKey), encoded)
	})
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	s.settings.Set(persisted)
	s.logs.Resize(persisted.LogRetention)
	writeJSON(w, http.StatusOK, persisted)
}

// decodeJSONObject reads a bounded JSON object without discarding unknown keys.
func (s *Server) decodeJSONObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := s.decodeJSON(w, r, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}
