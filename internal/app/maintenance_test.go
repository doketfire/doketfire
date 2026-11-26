package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doketfire/doketfire/internal/storage"
)

func appJSON(t *testing.T, a *App, method, path, body, token string, want int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: invalid JSON: %v", method, path, err)
	}
	return out
}

func TestCompactAndRestoreThroughMaintenance(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Dir = filepath.Join(dir, "data")
	cfg.HTTPAddr = "127.0.0.1:0"
	cfg.LogLevel = "error"
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	handlerBefore := a.HTTP.Handler
	storeBefore := a.Store
	appJSON(t, a, "GET", "/api/admin/setup", "", "", http.StatusOK)
	appJSON(t, a, "POST", "/api/admin/setup", `{"email":"admin@example.com","password":"password-123"}`, "", http.StatusCreated)
	session := appJSON(t, a, "POST", "/api/admin/auth-with-password", `{"email":"admin@example.com","password":"password-123"}`, "", http.StatusOK)
	token, _ := session["token"].(string)

	backupPath := filepath.Join(dir, "backup.db")
	backup, err := os.Create(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}

	appJSON(t, a, "POST", "/api/admin/collections", `{"name":"temporary","type":"base","rules":{"list":"","view":"","create":"","update":"","delete":""}}`, token, http.StatusCreated)

	compact, err := a.Compact(context.Background())
	if err != nil {
		t.Fatalf("compact failed: %v", err)
	}
	if compact.Before <= 0 || compact.After <= 0 || !strings.Contains(compact.Backup, "pre-compact") {
		t.Fatalf("unexpected compact report: %+v", compact)
	}
	appJSON(t, a, "GET", "/api/admin/me", "", token, http.StatusOK)
	collectionsAfterCompact := appJSON(t, a, "GET", "/api/admin/collections", "", token, http.StatusOK)
	if len(collectionsAfterCompact["items"].([]any)) != 1 {
		t.Fatalf("collection lost after compact: %v", collectionsAfterCompact)
	}

	restore, err := a.Restore(context.Background(), bytes.NewReader(backupBytes))
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if restore.Before <= 0 || restore.After <= 0 || !strings.Contains(restore.Backup, "pre-restore") {
		t.Fatalf("unexpected restore report: %+v", restore)
	}
	if a.HTTP.Handler != handlerBefore {
		t.Fatal("http.Server.Handler must not change during maintenance")
	}
	if a.Store == storeBefore {
		t.Fatal("expected a fresh store generation after restore")
	}
	// The persisted server secret keeps access tokens valid across the swap.
	appJSON(t, a, "GET", "/api/admin/me", "", token, http.StatusOK)
	collectionsAfterRestore := appJSON(t, a, "GET", "/api/admin/collections", "", token, http.StatusOK)
	if items := collectionsAfterRestore["items"].([]any); len(items) != 0 {
		t.Fatalf("restore did not revert the collection: %v", collectionsAfterRestore)
	}
}

func TestMaintenanceRejectsReadOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Dir = filepath.Join(dir, "data")
	cfg.HTTPAddr = "127.0.0.1:0"
	cfg.LogLevel = "error"
	writable, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.ReadOnly = true
	readOnly, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if _, err := readOnly.Compact(context.Background()); !errors.Is(err, storage.ErrReadOnly) {
		t.Fatalf("compact in read-only mode: %v", err)
	}
	if _, err := readOnly.Restore(context.Background(), bytes.NewReader(nil)); !errors.Is(err, storage.ErrReadOnly) {
		t.Fatalf("restore in read-only mode: %v", err)
	}
}
