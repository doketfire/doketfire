package query

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/storage"
)

func benchDoc() collections.Document {
	return collections.Document{
		"id":     "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"status": "active",
		"views":  json.Number("120"),
		"owner":  "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"title":  "Hello world",
		"author": map[string]any{"name": "Ana", "email": "ana@example.com"},
		"tags":   []any{"go", "db", "api"},
	}
}

func BenchmarkParseFilter(b *testing.B) {
	f := `status = "active" && views >= 100 && author.name = "Ana"`
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(f); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseSort(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseSort("-views,title"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEval(b *testing.B) {
	n, err := Parse(`status = "active" && views >= 100 && author.name = "Ana"`)
	if err != nil {
		b.Fatal(err)
	}
	doc := benchDoc()
	ctx := &EvalContext{Auth: map[string]any{"id": "u1"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, err := Eval(n, doc, ctx)
		if err != nil || !ok {
			b.Fatal(ok, err)
		}
	}
}

func benchQueryStore(b *testing.B, n int) (*storage.BoltStore, *Engine) {
	b.Helper()
	dir := b.TempDir()
	store, err := storage.Open(dir, storage.Options{NoSync: true})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	def := &collections.CollectionDef{
		Name: "posts",
		Indexes: []collections.Index{
			{Name: "status_idx", Field: "status", State: "ready"},
			{Name: "views_idx", Field: "views", State: "ready"},
		},
		Rules: collections.Rules{"list": ""},
	}
	if err := store.Update(func(tx storage.WriteTx) error { return tx.SaveCollection(def) }); err != nil {
		b.Fatal(err)
	}
	const batch = 500
	for i := 0; i < n; i += batch {
		if err := store.Update(func(tx storage.WriteTx) error {
			for j := i; j < i+batch && j < n; j++ {
				status := "active"
				if j%3 == 0 {
					status = "archived"
				}
				doc := collections.Document{
					"status": status,
					"views":  json.Number(fmt.Sprint(j)),
					"owner":  fmt.Sprintf("user-%d", j%50),
					"title":  fmt.Sprintf("Post number %d", j),
					"tags":   []any{"go", "db"},
				}
				if err := tx.PutDoc("posts", doc); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	return store, NewEngine(store, DefaultLimits())
}

func BenchmarkListIndexEq(b *testing.B) {
	_, eng := benchQueryStore(b, 5000)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := eng.List(ctx, "posts", ListOptions{Filter: `status = "active"`, Limit: 50})
		if err != nil || len(res.Items) != 50 {
			b.Fatal(len(res.Items), err)
		}
	}
}

func BenchmarkListIndexRange(b *testing.B) {
	_, eng := benchQueryStore(b, 5000)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := eng.List(ctx, "posts", ListOptions{Filter: `views > 1000`, Sort: "views", Limit: 50})
		if err != nil || len(res.Items) != 50 {
			b.Fatal(len(res.Items), err)
		}
	}
}

func BenchmarkListSortIndex(b *testing.B) {
	_, eng := benchQueryStore(b, 5000)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := eng.List(ctx, "posts", ListOptions{Sort: "-views", Limit: 50})
		if err != nil || len(res.Items) != 50 {
			b.Fatal(len(res.Items), err)
		}
	}
}

func BenchmarkListScanFirstPage(b *testing.B) {
	_, eng := benchQueryStore(b, 5000)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := eng.List(ctx, "posts", ListOptions{Limit: 50})
		if err != nil || len(res.Items) != 50 {
			b.Fatal(len(res.Items), err)
		}
	}
}

func BenchmarkListFilterAuthorize(b *testing.B) {
	_, eng := benchQueryStore(b, 5000)
	ctx := context.Background()
	owner := "user-7"
	opts := ListOptions{
		Filter: `owner = @auth.id`,
		Limit:  50,
		Auth:   map[string]any{"id": owner},
		Authorize: func(doc collections.Document) (bool, error) {
			return doc.GetString("owner") == owner, nil
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := eng.List(ctx, "posts", opts)
		if err != nil || len(res.Items) == 0 {
			b.Fatal(len(res.Items), err)
		}
	}
}

func BenchmarkListMemorySort(b *testing.B) {
	_, eng := benchQueryStore(b, 5000)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := eng.List(ctx, "posts", ListOptions{Sort: "title", Limit: 50})
		if err != nil || len(res.Items) != 50 {
			b.Fatal(len(res.Items), err)
		}
	}
}
