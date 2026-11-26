package query

import (
	"context"
	"testing"

	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/storage"
)

func TestTextMatch(t *testing.T) {
	cases := []struct {
		doc   string
		query string
		want  bool
	}{
		{"El rápido zorro marrón", "rápido", true},
		{"El rápido zorro marrón", "RÁPIDO", true},
		{"El rápido zorro marrón", "zorro", true},
		{"El rápido zorro marrón", "elefante", false},
		{"La casa de papel", "papel", true},
		{"La casa de papel", "la", false},
		{"documento importante", "import", true},
		{"", "algo", false},
	}
	for _, tc := range cases {
		if got := collections.TextMatch(tc.doc, tc.query); got != tc.want {
			t.Errorf("TextMatch(%q, %q) = %v, want %v", tc.doc, tc.query, got, tc.want)
		}
	}
}

func TestFulltextIndexQuery(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(dir, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	def := &collections.CollectionDef{
		Name:    "docs",
		Fields:  []collections.Field{{Name: "body", Type: "string"}},
		Indexes: []collections.Index{{Name: "body_ft", Field: "body", Fulltext: true, State: "ready"}},
		Rules:   collections.Rules{"list": "", "view": ""},
	}
	if err := store.Update(func(tx storage.WriteTx) error { return tx.SaveCollection(def) }); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(store, Limits{DefaultLimit: 50, MaxLimit: 500, MaxSortDocs: 1000})
	insert := func(body string) {
		t.Helper()
		if err := store.Update(func(tx storage.WriteTx) error {
			return tx.PutDoc("docs", collections.Document{"body": body})
		}); err != nil {
			t.Fatal(err)
		}
	}
	insert("El rápido zorro salta")
	insert("Un elefante tranquilo")

	res, err := engine.List(context.Background(), "docs", ListOptions{Filter: `body ~ "rápido"`})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 item, got %d (%v)", len(res.Items), res.Items)
	}
	if res.Items[0]["body"] != "El rápido zorro salta" {
		t.Fatalf("unexpected document: %v", res.Items[0])
	}
}
