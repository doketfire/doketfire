package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/doketfire/doketfire/internal/auth"
	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/files"
	"github.com/doketfire/doketfire/internal/realtime"
	"github.com/doketfire/doketfire/internal/rules"
	"github.com/doketfire/doketfire/internal/storage"
)

type benchAPI struct {
	srv     *Server
	handler http.Handler
	store   *storage.BoltStore
	token   string
	docID   string
}

func newBenchAPI(b *testing.B) *benchAPI {
	b.Helper()
	dir := b.TempDir()
	store, err := storage.Open(filepath.Join(dir, "data"), storage.Options{NoSync: true})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	authSvc, err := auth.New(store, auth.Config{
		Params: auth.Argon2Params{Memory: 8 << 10, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
	})
	if err != nil {
		b.Fatal(err)
	}
	ruleEngine, err := rules.New()
	if err != nil {
		b.Fatal(err)
	}
	fileStore, err := files.New(filepath.Join(dir, "files"), 1<<20)
	if err != nil {
		b.Fatal(err)
	}
	srv, err := New(Deps{
		Store:   store,
		Auth:    authSvc,
		Rules:   ruleEngine,
		Files:   fileStore,
		Hub:     realtime.NewHub(64, 32),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "bench",
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(srv.Close)

	cols := []*collections.CollectionDef{
		{
			Name:    "users",
			Type:    collections.TypeAuth,
			Indexes: []collections.Index{{Name: "email_idx", Field: "email", Unique: true, State: "ready"}},
		},
		{
			Name: "posts",
			Indexes: []collections.Index{
				{Name: "status_idx", Field: "status", State: "ready"},
				{Name: "views_idx", Field: "views", State: "ready"},
			},
			Rules: collections.Rules{
				"list":   "request.auth != null",
				"view":   "request.auth != null",
				"create": "request.auth != null",
			},
		},
		{
			Name:  "public_posts",
			Rules: collections.Rules{"list": "", "view": ""},
		},
	}
	for _, def := range cols {
		def := def
		if err := store.Update(func(tx storage.WriteTx) error { return tx.SaveCollection(def) }); err != nil {
			b.Fatal(err)
		}
	}

	session, err := authSvc.Register("users", "bench@example.com", "password-123", collections.Document{"name": "Bench"})
	if err != nil {
		b.Fatal(err)
	}

	const docs = 500
	if err := store.Update(func(tx storage.WriteTx) error {
		for i := 0; i < docs; i++ {
			status := "active"
			if i%3 == 0 {
				status = "archived"
			}
			doc := collections.Document{
				"status": status,
				"views":  json.Number(fmt.Sprint(i)),
				"title":  fmt.Sprintf("Post number %d", i),
				"tags":   []any{"go", "db", "api"},
				"author": map[string]any{"name": "Ana", "email": "ana@example.com"},
			}
			if err := tx.PutDoc("posts", doc); err != nil {
				return err
			}
			pub := collections.Document{
				"status": status,
				"views":  json.Number(fmt.Sprint(i)),
				"title":  fmt.Sprintf("Public post %d", i),
				"tags":   []any{"go", "api"},
			}
			if err := tx.PutDoc("public_posts", pub); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}

	var firstID string
	if err := store.View(func(tx storage.ReadTx) error {
		it := tx.ScanDocs("posts", storage.ScanOpts{Limit: 1})
		defer it.Close()
		if it.Next() {
			firstID = it.Doc().ID()
		}
		return it.Err()
	}); err != nil {
		b.Fatal(err)
	}

	return &benchAPI{srv: srv, handler: srv.Handler(), store: store, token: session.Token, docID: firstID}
}

func (a *benchAPI) do(b *testing.B, req *http.Request) {
	b.Helper()
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		b.Fatalf("%s %s: status %d body %s", req.Method, req.URL, rec.Code, rec.Body.String())
	}
}

func BenchmarkHTTPHealth(b *testing.B) {
	a := newBenchAPI(b)
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.do(b, req)
	}
}

func BenchmarkHTTPListPublic(b *testing.B) {
	a := newBenchAPI(b)
	req := httptest.NewRequest(http.MethodGet, "/api/collections/public_posts/records?limit=50", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.do(b, req)
	}
}

func BenchmarkHTTPListAuth(b *testing.B) {
	a := newBenchAPI(b)
	req := httptest.NewRequest(http.MethodGet, "/api/collections/posts/records?limit=50", nil)
	req.Header.Set("Authorization", "Bearer "+a.token)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.do(b, req)
	}
}

func BenchmarkHTTPListAuthGzip(b *testing.B) {
	a := newBenchAPI(b)
	req := httptest.NewRequest(http.MethodGet, "/api/collections/posts/records?limit=50", nil)
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept-Encoding", "gzip")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.do(b, req)
	}
}

func BenchmarkHTTPListFiltered(b *testing.B) {
	a := newBenchAPI(b)
	q := url.Values{}
	q.Set("filter", `status = "active"`)
	q.Set("sort", "-views")
	q.Set("limit", "50")
	req := httptest.NewRequest(http.MethodGet, "/api/collections/posts/records?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+a.token)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.do(b, req)
	}
}

func BenchmarkHTTPGetRecord(b *testing.B) {
	a := newBenchAPI(b)
	req := httptest.NewRequest(http.MethodGet, "/api/collections/posts/records/"+a.docID, nil)
	req.Header.Set("Authorization", "Bearer "+a.token)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.do(b, req)
	}
}

func BenchmarkHTTPCreateRecord(b *testing.B) {
	a := newBenchAPI(b)
	payload := []byte(`{"title":"bench","status":"active","views":1,"tags":["go"]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/collections/posts/records", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+a.token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			b.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
	}
}

func BenchmarkHTTPListAuthParallel(b *testing.B) {
	a := newBenchAPI(b)
	req := httptest.NewRequest(http.MethodGet, "/api/collections/posts/records?limit=50", nil)
	req.Header.Set("Authorization", "Bearer "+a.token)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rec := httptest.NewRecorder()
			a.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Errorf("status %d", rec.Code)
				return
			}
		}
	})
}
