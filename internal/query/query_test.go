package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/storage"
)

func TestParseFilters(t *testing.T) {
	valid := []string{
		`status = "active"`,
		`views >= 100 && author.name = "Ana"`,
		`tags contains "go" || tags contains "svelte"`,
		`status in ["draft", "review"] && !(archived = true)`,
		`created > @now`,
		`owner = @auth.id`,
		`a = 1 || b = 2 && c = 3`,
		`!(a = 1)`,
		`x = -1.5e3`,
		`name = 'O\'Brien'`,
		`empty = null`,
	}
	for _, f := range valid {
		if _, err := Parse(f); err != nil {
			t.Errorf("Parse(%q): %v", f, err)
		}
	}
	if n, err := Parse("   "); err != nil || n != nil {
		t.Fatalf("empty filter: %v %v", n, err)
	}

	invalid := []string{
		`views >=`,
		`foo`,
		`a = [1]`,
		`a in 1`,
		`(a = 1`,
		`a = 1)`,
		`a = 1 & b = 2`,
		`a == 1 &&`,
		`@now = 1`,
		`a = @bogus`,
		`a .. b = 1`,
		`a = "unterminated`,
	}
	for _, f := range invalid {
		if _, err := Parse(f); err == nil {
			t.Errorf("Parse(%q) should fail", f)
		}
	}
}

func TestParsePrecedence(t *testing.T) {
	n, err := Parse(`a = 1 || b = 2 && c = 3`)
	if err != nil {
		t.Fatal(err)
	}
	logic, ok := n.(*Logical)
	if !ok || logic.Op != "||" {
		t.Fatalf("expected top-level OR, got %#v", n)
	}
	right, ok := logic.Right.(*Logical)
	if !ok || right.Op != "&&" {
		t.Fatalf("expected AND on the right, got %#v", logic.Right)
	}
}

func TestEval(t *testing.T) {
	doc := collections.Document{
		"status": "active",
		"views":  json.Number("120"),
		"author": map[string]any{"name": "Ana"},
		"tags":   []any{"go", "svelte"},
		"empty":  nil,
		"ok":     true,
	}
	cases := []struct {
		filter string
		want   bool
	}{
		{`status = "active"`, true},
		{`status = "draft"`, false},
		{`status != "draft"`, true},
		{`views >= 100`, true},
		{`views < 100`, false},
		{`author.name = "Ana"`, true},
		{`author.name = "Bob"`, false},
		{`tags contains "go"`, true},
		{`tags contains "rust"`, false},
		{`status in ["draft", "active"]`, true},
		{`status in ["draft", "review"]`, false},
		{`empty = null`, true},
		{`missing = null`, true},
		{`missing != null`, false},
		{`ok = true`, true},
		{`!(status = "draft")`, true},
		{`status = "active" && views > 100`, true},
		{`status = "active" || views > 1000`, true},
		{`status = "draft" || views > 1000`, false},
		{`status in ["active"]`, true},
	}
	ctx := &EvalContext{Auth: map[string]any{"id": "u1"}}
	for _, tc := range cases {
		n, err := Parse(tc.filter)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.filter, err)
		}
		got, err := Eval(n, doc, ctx)
		if err != nil {
			t.Fatalf("Eval(%q): %v", tc.filter, err)
		}
		if got != tc.want {
			t.Errorf("Eval(%q) = %v, want %v", tc.filter, got, tc.want)
		}
	}

	// Auth and now variables.
	n, _ := Parse(`owner = @auth.id`)
	doc["owner"] = "u1"
	if ok, _ := Eval(n, doc, ctx); !ok {
		t.Fatal("@auth.id did not resolve")
	}
	n, _ = Parse(`created < @now`)
	doc["created"] = "2020-01-01T00:00:00.000Z"
	if ok, _ := Eval(n, doc, ctx); !ok {
		t.Fatal("@now did not resolve")
	}
}

func TestParseSort(t *testing.T) {
	sf, err := ParseSort("-created,title,+views")
	if err != nil {
		t.Fatal(err)
	}
	if len(sf) != 3 || !sf[0].Desc || sf[1].Desc || sf[2].Desc || sf[2].Path != "views" {
		t.Fatalf("unexpected sort: %+v", sf)
	}
	for _, bad := range []string{"", " ", "-", "a,,b", "a.b.c.d.e.f?", "a,b,c,d"} {
		if bad == "" {
			if sf, err := ParseSort(bad); err != nil || sf != nil {
				t.Fatalf("empty sort should be nil")
			}
			continue
		}
		if _, err := ParseSort(bad); err == nil {
			t.Errorf("ParseSort(%q) should fail", bad)
		}
	}
}

func setupEngine(t *testing.T) (*Engine, storage.Storage) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(dir, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	def := &collections.CollectionDef{
		Name: "posts",
		Fields: []collections.Field{
			{Name: "title", Type: "string"},
			{Name: "views", Type: "number"},
			{Name: "status", Type: "string"},
			{Name: "author", Type: "object"},
			{Name: "tags", Type: "array"},
		},
		Indexes: []collections.Index{
			{Name: "views_idx", Field: "views", State: "ready"},
			{Name: "title_idx", Field: "title", Unique: true, State: "ready"},
			{Name: "tags_idx", Field: "tags", State: "ready"},
			{Name: "author_name_idx", Field: "author.name", State: "ready"},
		},
		Rules: collections.Rules{"list": "", "view": ""},
	}
	if err := store.Update(func(tx storage.WriteTx) error { return tx.SaveCollection(def) }); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(store, Limits{DefaultLimit: 5, MaxLimit: 100, MaxSortDocs: 1000})
	return engine, store
}

func insertDoc(t *testing.T, store storage.Storage, d collections.Document) {
	t.Helper()
	if err := store.Update(func(tx storage.WriteTx) error { return tx.PutDoc("posts", d) }); err != nil {
		t.Fatal(err)
	}
}

func seedDocs(t *testing.T, store storage.Storage, n int) []collections.Document {
	t.Helper()
	docs := make([]collections.Document, 0, n)
	for i := 0; i < n; i++ {
		d := collections.Document{
			"title":  fmt.Sprintf("post-%02d", i),
			"views":  json.Number(fmt.Sprint(i * 10)),
			"status": []string{"draft", "active", "archived"}[i%3],
			"author": map[string]any{"name": []string{"Ana", "Bob", "Cara"}[i%3]},
			"tags":   []any{[]string{"go", "db", "ui"}[i%3], "common"},
			"rating": json.Number(fmt.Sprint((i * 7) % 13)),
		}
		insertDoc(t, store, d)
		docs = append(docs, d)
	}
	// Fetch to obtain ids and timestamps.
	var out []collections.Document
	if err := store.View(func(tx storage.ReadTx) error {
		it := tx.ScanDocs("posts", storage.ScanOpts{})
		defer it.Close()
		for it.Next() {
			out = append(out, it.Doc())
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestListFilterSortPagination(t *testing.T) {
	engine, store := setupEngine(t)
	seedDocs(t, store, 30)

	res, err := engine.List(context.Background(), "posts", ListOptions{
		Filter: `views >= 100`,
		Sort:   "-views",
		Limit:  5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 5 {
		t.Fatalf("len = %d", len(res.Items))
	}
	for i, d := range res.Items {
		views, _ := json.Number(d["views"].(json.Number).String()).Float64()
		want := float64(290 - i*10)
		if views != want {
			t.Fatalf("item %d views = %v, want %v", i, views, want)
		}
	}
	if res.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}

	// Walk all pages and ensure no duplicates and descending order.
	seen := map[string]bool{}
	prev := 1e9
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		page, err := engine.List(context.Background(), "posts", ListOptions{
			Filter: `views >= 100`,
			Sort:   "-views",
			Limit:  4,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range page.Items {
			id := d.ID()
			if seen[id] {
				t.Fatalf("duplicate %s", id)
			}
			seen[id] = true
			v, _ := codecNumber(d["views"])
			if v > prev {
				t.Fatalf("order broken: %v > %v", v, prev)
			}
			prev = v
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 20 {
		t.Fatalf("expected 20 docs with views >= 100, got %d", len(seen))
	}
}

func codecNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	default:
		return 0, false
	}
}

func TestListMemorySortAndCursor(t *testing.T) {
	engine, store := setupEngine(t)
	seedDocs(t, store, 17)

	cursor := ""
	seen := map[string]bool{}
	prev := -1.0
	for pages := 0; pages < 20; pages++ {
		res, err := engine.List(context.Background(), "posts", ListOptions{
			Sort:   "rating",
			Limit:  3,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range res.Items {
			if seen[d.ID()] {
				t.Fatal("duplicate across memory-sorted pages")
			}
			seen[d.ID()] = true
			v, _ := codecNumber(d["rating"])
			if v < prev {
				t.Fatalf("rating order broken: %v < %v", v, prev)
			}
			prev = v
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if len(seen) != 17 {
		t.Fatalf("seen %d docs", len(seen))
	}
}

func TestListAuthorizeAffectsPagination(t *testing.T) {
	engine, store := setupEngine(t)
	seedDocs(t, store, 20)

	var all []string
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		res, err := engine.List(context.Background(), "posts", ListOptions{
			Filter: `views >= 0`,
			Limit:  3,
			Cursor: cursor,
			Authorize: func(d collections.Document) (bool, error) {
				v, _ := codecNumber(d["views"])
				return int(v)%20 != 0, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range res.Items {
			all = append(all, d.ID())
			v, _ := codecNumber(d["views"])
			if int(v)%20 == 0 {
				t.Fatalf("unauthorized doc leaked: %v", v)
			}
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if len(all) != 10 {
		t.Fatalf("expected 10 authorized docs, got %d", len(all))
	}
}

func TestListProjectionAndErrors(t *testing.T) {
	engine, store := setupEngine(t)
	seedDocs(t, store, 3)

	res, err := engine.List(context.Background(), "posts", ListOptions{
		Limit:  1,
		Fields: []string{"title", "author.name"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Items[0]
	if d[collections.FieldID] == "" || d["title"] == nil {
		t.Fatalf("projection missing fields: %v", d)
	}
	if author, ok := d["author"].(map[string]any); !ok || author["name"] == nil {
		t.Fatalf("nested projection failed: %v", d)
	}
	if _, ok := d["views"]; ok {
		t.Fatal("unrequested field leaked")
	}

	if _, err := engine.List(context.Background(), "posts", ListOptions{Limit: 1000}); !errors.Is(err, ErrLimit) {
		t.Fatalf("expected limit error, got %v", err)
	}
	if _, err := engine.List(context.Background(), "posts", ListOptions{Filter: `views >`}); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := engine.List(context.Background(), "posts", ListOptions{Cursor: "not-base64"}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("expected cursor error, got %v", err)
	}
	if _, err := engine.List(context.Background(), "missing", ListOptions{}); !errors.Is(err, storage.ErrCollectionNotFound) {
		t.Fatalf("expected collection not found, got %v", err)
	}

	engine.StrictIndexes = true
	if _, err := engine.List(context.Background(), "posts", ListOptions{Filter: `status = "active"`}); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("expected no-index error, got %v", err)
	}
	if _, err := engine.List(context.Background(), "posts", ListOptions{Filter: `views > 1`}); err != nil {
		t.Fatalf("indexed query should pass strict mode: %v", err)
	}
}

func TestFiltersAgainstNaiveEvaluator(t *testing.T) {
	engine, store := setupEngine(t)
	docs := seedDocs(t, store, 40)
	filters := []string{
		`views > 100`,
		`views >= 100`,
		`views < 100`,
		`views <= 100`,
		`views = 100`,
		`status = "active"`,
		`status != "active"`,
		`author.name = "Ana"`,
		`tags contains "go"`,
		`status in ["draft", "archived"]`,
		`views > 50 && status = "active"`,
		`views < 50 || author.name = "Cara"`,
		`!(status = "draft")`,
		`title contains "post-1"`,
	}
	for _, f := range filters {
		n, err := Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, d := range docs {
			ok, err := Eval(n, d, &EvalContext{})
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				want[d.ID()] = true
			}
		}
		got := map[string]bool{}
		cursor := ""
		for pages := 0; pages < 100; pages++ {
			res, err := engine.List(context.Background(), "posts", ListOptions{Filter: f, Limit: 7, Cursor: cursor})
			if err != nil {
				t.Fatalf("filter %q: %v", f, err)
			}
			for _, d := range res.Items {
				if got[d.ID()] {
					t.Fatalf("filter %q returned duplicate %s", f, d.ID())
				}
				got[d.ID()] = true
			}
			if res.NextCursor == "" {
				break
			}
			cursor = res.NextCursor
		}
		if len(got) != len(want) {
			t.Fatalf("filter %q: got %d docs, want %d\ngot=%v\nwant=%v", f, len(got), len(want), got, want)
		}
		for id := range want {
			if !got[id] {
				t.Fatalf("filter %q missing %s", f, id)
			}
		}
	}
}

func TestAuthFilter(t *testing.T) {
	engine, store := setupEngine(t)
	docs := seedDocs(t, store, 6)
	insertDoc(t, store, map[string]any{"title": "owned", "views": json.Number("1"), "owner": "u1"})
	_ = docs

	res, err := engine.List(context.Background(), "posts", ListOptions{
		Filter: `owner = @auth.id`,
		Limit:  10,
		Auth:   map[string]any{"id": "u1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].GetString("title") != "owned" {
		t.Fatalf("auth filter result: %v", res.Items)
	}
}

func TestRandomFilterDifferential(t *testing.T) {
	engine, store := setupEngine(t)
	docs := seedDocs(t, store, 60)
	rng := rand.New(rand.NewSource(11))
	statuses := []string{"draft", "active", "archived"}
	for n := 0; n < 60; n++ {
		views := rng.Intn(600)
		status := statuses[rng.Intn(len(statuses))]
		var filter string
		switch rng.Intn(5) {
		case 0:
			filter = fmt.Sprintf("views >= %d", views)
		case 1:
			filter = fmt.Sprintf("views < %d", views)
		case 2:
			filter = fmt.Sprintf("status = %q", status)
		case 3:
			filter = fmt.Sprintf("views >= %d && status = %q", views, status)
		case 4:
			filter = fmt.Sprintf("views <= %d || status = %q", views, status)
		}
		nNode, err := Parse(filter)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, d := range docs {
			ok, _ := Eval(nNode, d, &EvalContext{})
			if ok {
				want[d.ID()] = true
			}
		}
		got := map[string]bool{}
		res, err := engine.List(context.Background(), "posts", ListOptions{Filter: filter, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range res.Items {
			got[d.ID()] = true
		}
		if len(got) != len(want) {
			t.Fatalf("filter %q: got %d, want %d", filter, len(got), len(want))
		}
	}
}
