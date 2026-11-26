package storage

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/doketfire/doketfire/internal/collections"
)

func benchStore(b *testing.B, n int) *BoltStore {
	b.Helper()
	s, err := Open(b.TempDir(), Options{NoSync: true})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	def := &collections.CollectionDef{
		Name: "posts",
		Indexes: []collections.Index{
			{Name: "status_idx", Field: "status", State: "ready"},
			{Name: "views_idx", Field: "views", State: "ready"},
		},
		Rules: collections.Rules{"list": ""},
	}
	if err := s.Update(func(tx WriteTx) error { return tx.SaveCollection(def) }); err != nil {
		b.Fatal(err)
	}
	const batch = 500
	for i := 0; i < n; i += batch {
		if err := s.Update(func(tx WriteTx) error {
			for j := i; j < i+batch && j < n; j++ {
				status := "active"
				if j%3 == 0 {
					status = "archived"
				}
				doc := Doc{
					"status": status,
					"views":  json.Number(fmt.Sprint(j)),
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
	return s
}

func benchDocID(s *BoltStore, skip int) string {
	var id string
	_ = s.View(func(tx ReadTx) error {
		it := tx.ScanDocs("posts", ScanOpts{})
		defer it.Close()
		i := 0
		for it.Next() {
			if i == skip {
				id = it.Doc().ID()
				return nil
			}
			i++
		}
		return nil
	})
	return id
}

func BenchmarkGetDoc(b *testing.B) {
	s := benchStore(b, 5000)
	id := benchDocID(s, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.View(func(tx ReadTx) error {
			_, ok, err := tx.GetDoc("posts", id)
			if err != nil || !ok {
				return fmt.Errorf("not found: %w", err)
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanDocsLimit(b *testing.B) {
	s := benchStore(b, 5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.View(func(tx ReadTx) error {
			it := tx.ScanDocs("posts", ScanOpts{Limit: 50})
			defer it.Close()
			n := 0
			for it.Next() {
				n++
			}
			if n != 50 {
				return fmt.Errorf("got %d docs", n)
			}
			return it.Err()
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanIndexEq(b *testing.B) {
	s := benchStore(b, 5000)
	start, end := []byte{0x04}, []byte{0xFF}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.View(func(tx ReadTx) error {
			it := tx.ScanIndex("posts", "status_idx", KeyRange{Start: start, End: end}, ScanOpts{Limit: 50})
			defer it.Close()
			n := 0
			for it.Next() {
				n++
			}
			if n != 50 {
				return fmt.Errorf("got %d docs", n)
			}
			return it.Err()
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPutDocInsert(b *testing.B) {
	s := benchStore(b, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc := Doc{
			"status": "active",
			"views":  json.Number(fmt.Sprint(i)),
			"title":  fmt.Sprintf("Benchmark post %d", i),
			"tags":   []any{"go", "db"},
		}
		if err := s.Update(func(tx WriteTx) error { return tx.PutDoc("posts", doc) }); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPutDocUpdate(b *testing.B) {
	s := benchStore(b, 100)
	id := benchDocID(s, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Update(func(tx WriteTx) error {
			return tx.PutDoc("posts", Doc{
				"id":     id,
				"status": "active",
				"views":  json.Number(fmt.Sprint(i)),
				"title":  "Benchmark post",
				"tags":   []any{"go", "db"},
			})
		}); err != nil {
			b.Fatal(err)
		}
	}
}
