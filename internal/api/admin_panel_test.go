package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doketfire/doketfire/internal/storage"
)

func (a *testAPI) requestWithHeaders(method, path string, body []byte, token string, headers map[string]string) *http.Response {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, a.baseURL+path, reader)
	if err != nil {
		a.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	return resp
}

func jsonMap(t *testing.T, v any) map[string]any {
	t.Helper()
	out, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %T", v)
	}
	return out
}

func errorCode(t *testing.T, body map[string]any) string {
	t.Helper()
	errObj := jsonMap(t, body["error"])
	code, _ := errObj["code"].(string)
	return code
}

func numberValue(t *testing.T, v any) float64 {
	t.Helper()
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			t.Fatalf("invalid number %q: %v", n, err)
		}
		return f
	case float64:
		return n
	default:
		t.Fatalf("expected number, got %T", v)
		return 0
	}
}

func TestAdminSettingsValidationPersistenceAndDynamicCORS(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()

	initial := api.json("GET", "/api/admin/settings", nil, api.token, http.StatusOK)
	if initial["title"] != "Doketfire" {
		t.Fatalf("unexpected default title: %v", initial["title"])
	}
	if _, ok := initial["corsOrigins"].([]any); !ok {
		t.Fatalf("corsOrigins must be an array: %v", initial["corsOrigins"])
	}
	if numberValue(t, initial["authRateLimit"]) != 10 {
		t.Fatalf("unexpected default authRateLimit: %v", initial["authRateLimit"])
	}
	if _, err := time.Parse(time.RFC3339, initial["updatedAt"].(string)); err != nil {
		t.Fatalf("invalid updatedAt: %v", err)
	}

	bad := api.json("PATCH", "/api/admin/settings", map[string]any{"unknown": 1}, api.token, http.StatusUnprocessableEntity)
	if errorCode(t, bad) != "validation_failed" {
		t.Fatalf("unexpected error for unknown setting: %v", bad)
	}
	api.json("PATCH", "/api/admin/settings", map[string]any{"corsOrigins": []string{"not-an-origin"}}, api.token, http.StatusUnprocessableEntity)
	api.json("PATCH", "/api/admin/settings", map[string]any{"authRateLimit": 0}, api.token, http.StatusUnprocessableEntity)
	api.json("PATCH", "/api/admin/settings", map[string]any{"logRetention": 49}, api.token, http.StatusUnprocessableEntity)

	patched := api.json("PATCH", "/api/admin/settings", map[string]any{
		"title":         "Panel",
		"corsOrigins":   []string{"https://app.example.test"},
		"authRateLimit": 25,
		"logRetention":  120,
	}, api.token, http.StatusOK)
	if patched["title"] != "Panel" || numberValue(t, patched["authRateLimit"]) != 25 || numberValue(t, patched["logRetention"]) != 120 {
		t.Fatalf("unexpected patched settings: %v", patched)
	}

	var raw []byte
	if err := api.store.View(func(tx storage.ReadTx) error {
		var err error
		raw, err = tx.RawGet("_meta", []byte("panel_settings"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var persisted PanelSettings
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("settings were not persisted as JSON: %v", err)
	}
	if persisted.Title != "Panel" || persisted.AuthRateLimit != 25 || persisted.LogRetention != 120 ||
		len(persisted.CORSOrigins) != 1 || persisted.CORSOrigins[0] != "https://app.example.test" {
		t.Fatalf("unexpected persisted settings: %+v", persisted)
	}

	resp := api.requestWithHeaders("OPTIONS", "/api/health", nil, "", map[string]string{"Origin": "https://app.example.test"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("new origin preflight status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.test" {
		t.Fatalf("unexpected CORS header: %q", got)
	}
	resp = api.requestWithHeaders("OPTIONS", "/api/health", nil, "", map[string]string{"Origin": "https://old.example.test"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("old origin preflight status = %d, want 403", resp.StatusCode)
	}
}

func TestAuthRateLimitUsesCurrentSettings(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	api.json("PATCH", "/api/admin/settings", map[string]any{"authRateLimit": 1}, api.token, http.StatusOK)

	body := []byte(`{"email":"nobody@example.com","password":"wrong-password"}`)
	first := api.request("POST", "/api/admin/auth-with-password", body, "", "application/json")
	first.Body.Close()
	if first.StatusCode != http.StatusUnauthorized {
		t.Fatalf("first attempt status = %d, want 401", first.StatusCode)
	}
	second := api.request("POST", "/api/admin/auth-with-password", body, "", "application/json")
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second attempt status = %d, want 429", second.StatusCode)
	}
}

func TestAdminLogsFiltering(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	for i := 0; i < 3; i++ {
		api.json("GET", "/api/version", nil, "", http.StatusOK)
	}
	api.json("GET", "/api/health", nil, "", http.StatusOK)

	logs := jsonMap(t, api.json("GET", "/api/admin/logs?q=VERSION&limit=2", nil, api.token, http.StatusOK))
	if numberValue(t, logs["total"]) != 3 || logs["truncated"] != true {
		t.Fatalf("unexpected log metadata: total=%v truncated=%v", logs["total"], logs["truncated"])
	}
	items, ok := logs["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected two log items: %v", logs["items"])
	}
	entry := jsonMap(t, items[0])
	if entry["method"] != "GET" || entry["path"] != "/api/version" || numberValue(t, entry["status"]) != 200 {
		t.Fatalf("unexpected log entry: %v", entry)
	}
	id, _ := entry["id"].(string)
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("log id is not hex: %q", id)
	}
	if _, err := time.Parse(time.RFC3339, entry["time"].(string)); err != nil {
		t.Fatalf("invalid log time: %v", err)
	}
	if entry["ip"] == "" || entry["bytes"] == nil || entry["durationMs"] == nil {
		t.Fatalf("incomplete log entry: %v", entry)
	}

	health := jsonMap(t, api.json("GET", "/api/admin/logs?status=200&q=health", nil, api.token, http.StatusOK))
	if numberValue(t, health["total"]) != 1 {
		t.Fatalf("expected one health request, got %v", health["total"])
	}
	missing := jsonMap(t, api.json("GET", "/api/admin/logs?status=404", nil, api.token, http.StatusOK))
	if numberValue(t, missing["total"]) != 0 {
		t.Fatalf("expected no 404 requests, got %v", missing["total"])
	}
	api.json("GET", "/api/admin/logs?limit=501", nil, api.token, http.StatusBadRequest)
}

func TestAdminSuperuserManagement(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	admins := jsonMap(t, api.json("GET", "/api/admin/admins", nil, api.token, http.StatusOK))
	items, _ := admins["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one superuser: %v", admins)
	}
	first := jsonMap(t, items[0])
	for _, forbidden := range []string{"passwordHash", "tokenKey", "password", "token"} {
		if _, ok := first[forbidden]; ok {
			t.Fatalf("admin response leaked %s: %v", forbidden, first)
		}
	}
	firstID, _ := first["id"].(string)

	last := api.json("DELETE", "/api/admin/admins/"+firstID, nil, api.token, http.StatusConflict)
	if errorCode(t, last) != "admin_last" {
		t.Fatalf("expected admin_last, got %v", last)
	}

	second := api.json("POST", "/api/admin/admins", map[string]any{
		"email": "second@example.com", "password": "password-123",
	}, api.token, http.StatusCreated)
	for _, forbidden := range []string{"passwordHash", "tokenKey"} {
		if _, ok := second[forbidden]; ok {
			t.Fatalf("created admin leaked %s: %v", forbidden, second)
		}
	}
	secondSession := api.json("POST", "/api/admin/auth-with-password", map[string]any{
		"email": "second@example.com", "password": "password-123",
	}, "", http.StatusOK)
	secondToken, _ := secondSession["token"].(string)

	api.json("DELETE", "/api/admin/admins/"+firstID, nil, secondToken, http.StatusNoContent)
	api.json("GET", "/api/admin/admins", nil, secondToken, http.StatusOK)

	third := api.json("POST", "/api/admin/admins", map[string]any{
		"email": "third@example.com", "password": "password-123",
	}, secondToken, http.StatusCreated)
	thirdID, _ := third["id"].(string)
	thirdSession := api.json("POST", "/api/admin/auth-with-password", map[string]any{
		"email": "third@example.com", "password": "password-123",
	}, "", http.StatusOK)
	thirdToken, _ := thirdSession["token"].(string)
	self := api.json("DELETE", "/api/admin/admins/"+thirdID, nil, thirdToken, http.StatusConflict)
	if errorCode(t, self) != "admin_self" {
		t.Fatalf("expected admin_self, got %v", self)
	}
}

func TestAdminSessionsSanitizedAndRevoked(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	body := jsonMap(t, api.json("GET", "/api/admin/sessions", nil, api.token, http.StatusOK))
	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("expected sessions: %v", body)
	}
	session := jsonMap(t, items[0])
	tokenID, _ := session["tokenId"].(string)
	if len(tokenID) != 12 {
		t.Fatalf("tokenId length = %d, want 12: %q", len(tokenID), tokenID)
	}
	if _, err := hex.DecodeString(tokenID); err != nil {
		t.Fatalf("tokenId is not hex: %q", tokenID)
	}
	for _, forbidden := range []string{"token", "refreshToken", "tokenHash", "hash", "passwordHash"} {
		if _, ok := session[forbidden]; ok {
			t.Fatalf("session leaked %s: %v", forbidden, session)
		}
	}
	if session["kind"] != "admin" || session["used"] != false {
		t.Fatalf("unexpected session fields: %v", session)
	}
	family, _ := session["family"].(string)

	revoked := api.json("POST", "/api/admin/sessions/revoke", map[string]any{"family": family}, api.token, http.StatusOK)
	if revoked["revoked"] == nil {
		t.Fatalf("missing revoked count: %v", revoked)
	}
	after := jsonMap(t, api.json("GET", "/api/admin/sessions", nil, api.token, http.StatusOK))
	if len(after["items"].([]any)) != 0 {
		t.Fatalf("expected no sessions after revoke: %v", after)
	}
	bad := api.json("POST", "/api/admin/sessions/revoke", map[string]any{"family": family, "all": true}, api.token, http.StatusUnprocessableEntity)
	if errorCode(t, bad) != "validation_failed" {
		t.Fatalf("unexpected error for ambiguous revocation: %v", bad)
	}

	other := api.json("POST", "/api/admin/admins", map[string]any{
		"email": "revoke@example.com", "password": "password-123",
	}, api.token, http.StatusCreated)
	otherID, _ := other["id"].(string)
	otherSession := api.json("POST", "/api/admin/auth-with-password", map[string]any{
		"email": "revoke@example.com", "password": "password-123",
	}, "", http.StatusOK)
	otherToken, _ := otherSession["token"].(string)
	revoked = api.json("POST", "/api/admin/sessions/revoke", map[string]any{
		"identity": map[string]any{"kind": "admin", "collection": "", "id": otherID},
	}, api.token, http.StatusOK)
	if numberValue(t, revoked["revoked"]) < 1 {
		t.Fatalf("identity revocation removed no sessions: %v", revoked)
	}
	api.json("GET", "/api/admin/me", nil, otherToken, http.StatusUnauthorized)
}

func TestViewsRequireAuthentication(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	api.createCollection(publicBaseCollection("posts"))

	api.json("GET", "/api/collections/posts/views", nil, "", http.StatusUnauthorized)
	api.json("POST", "/api/collections/posts/views", map[string]any{"name": "mine"}, "", http.StatusUnauthorized)
	api.json("DELETE", "/api/collections/posts/views/mine", nil, "", http.StatusUnauthorized)
}

type fakeMaintenance struct {
	compactCalls int
	restoreCalls int
	report       MaintenanceReport
	lastBody     []byte
}

func (f *fakeMaintenance) Compact(context.Context) (*MaintenanceReport, error) {
	f.compactCalls++
	report := f.report
	return &report, nil
}

func (f *fakeMaintenance) Restore(_ context.Context, r io.Reader) (*MaintenanceReport, error) {
	f.restoreCalls++
	f.lastBody, _ = io.ReadAll(r)
	report := f.report
	return &report, nil
}

func TestAdminMaintenanceEndpoints(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	fake := &fakeMaintenance{report: MaintenanceReport{Before: 100, After: 80, Backup: "backup.db"}}
	api.srv.maintenance = fake

	report := api.json("POST", "/api/admin/compact", nil, api.token, http.StatusOK)
	if numberValue(t, report["before"]) != 100 || numberValue(t, report["after"]) != 80 || report["backup"] != "backup.db" {
		t.Fatalf("unexpected compact report: %v", report)
	}
	if fake.compactCalls != 1 {
		t.Fatalf("compact calls = %d, want 1", fake.compactCalls)
	}

	backupPath := filepath.Join(t.TempDir(), "data.db")
	backup, err := os.Create(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.store.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	dbBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "data.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(dbBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	resp := api.request("POST", "/api/admin/restore", body.Bytes(), api.token, "multipart/form-data; boundary="+writer.Boundary())
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore status = %d, want 200: %s", resp.StatusCode, raw)
	}
	if fake.restoreCalls != 1 || !bytes.Equal(fake.lastBody, dbBytes) {
		t.Fatalf("restore did not receive the uploaded database (calls=%d, bytes=%d)", fake.restoreCalls, len(fake.lastBody))
	}

	var badBody bytes.Buffer
	badWriter := multipart.NewWriter(&badBody)
	badPart, _ := badWriter.CreateFormFile("file", "data.db")
	_, _ = badPart.Write([]byte("not a database"))
	_ = badWriter.Close()
	bad := api.request("POST", "/api/admin/restore", badBody.Bytes(), api.token, "multipart/form-data; boundary="+badWriter.Boundary())
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid database status = %d, want 400", bad.StatusCode)
	}
}
