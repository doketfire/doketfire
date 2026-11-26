package auth

import (
	"testing"

	"github.com/doketfire/doketfire/internal/collections"
)

func benchIdentity() *Identity {
	return &Identity{
		Kind:       "user",
		ID:         "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Collection: "users",
		Email:      "ana@example.com",
		Record: collections.Document{
			"id":       "01ARZ3NDEKTSV4RRFFQ69G5FAV",
			"email":    "ana@example.com",
			"name":     "Ana",
			"verified": true,
			"role":     "editor",
			"profile":  map[string]any{"bio": "hello", "links": []any{"a", "b"}},
		},
	}
}

func BenchmarkAuthMap(b *testing.B) {
	id := benchIdentity()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if id.AuthMap() == nil {
			b.Fatal("nil map")
		}
	}
}
