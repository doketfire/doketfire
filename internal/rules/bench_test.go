package rules

import (
	"testing"

	"github.com/doketfire/doketfire/internal/collections"
)

func benchRuleEngine(b *testing.B) *Engine {
	b.Helper()
	e, err := New()
	if err != nil {
		b.Fatal(err)
	}
	return e
}

func BenchmarkAllowedPublic(b *testing.B) {
	e := benchRuleEngine(b)
	def := &collections.CollectionDef{Name: "posts", Rules: collections.Rules{"list": ""}}
	ctx := Context{Operation: "list"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, err := e.Allowed(def, "list", false, ctx)
		if err != nil || !ok {
			b.Fatal(ok, err)
		}
	}
}

func BenchmarkAllowedCELAuth(b *testing.B) {
	e := benchRuleEngine(b)
	def := &collections.CollectionDef{Name: "posts", Rules: collections.Rules{
		"view": "request.auth != null && resource.id == request.auth.id",
	}}
	ctx := Context{
		Operation: "view",
		Identity:  map[string]any{"id": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "email": "ana@example.com", "name": "Ana"},
		Resource:  map[string]any{"id": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "owner": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "title": "Hello"},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, err := e.Allowed(def, "view", false, ctx)
		if err != nil || !ok {
			b.Fatal(ok, err)
		}
	}
}

func BenchmarkAllowedCELComplex(b *testing.B) {
	e := benchRuleEngine(b)
	def := &collections.CollectionDef{Name: "posts", Rules: collections.Rules{
		"update": `request.auth != null && (request.auth.admin == true || (resource.owner == request.auth.id && request.body.status in ["draft", "review"]))`,
	}}
	ctx := Context{
		Operation: "update",
		Identity:  map[string]any{"id": "u1", "email": "ana@example.com", "admin": false},
		Resource:  map[string]any{"id": "d1", "owner": "u1", "status": "draft", "title": "Hello"},
		Body:      map[string]any{"status": "review", "title": "Hello 2"},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, err := e.Allowed(def, "update", false, ctx)
		if err != nil || !ok {
			b.Fatal(ok, err)
		}
	}
}
