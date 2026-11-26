package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doketfire/doketfire/internal/codec"
	"github.com/doketfire/doketfire/internal/collections"
	bolt "go.etcd.io/bbolt"
)

func openTestStore(t *testing.T, opts Options) *BoltStore {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func createPosts(t *testing.T, s Storage) {
	t.Helper()
	def := &collections.CollectionDef{
		Name: "posts",
		Fields: []collections.Field{
			{Name: "title", Type: "string", Required: true},
			{Name: "views", Type: "number"},
			{Name: "tags", Type: "array"},
			{Name: "author", Type: "object"},
		},
		Indexes: []collections.Index{
			{Name: "title_idx", Field: "title", Unique: true, State: "ready"},
			{Name: "views_idx", Field: "views", State: "ready"},
			{Name: "tags_idx", Field: "tags", State: "ready"},
			{Name: "created_idx", Field: "created", State: "ready"},
		},
		Rules: collections.Rules{"list": "", "view": ""},
	}
	if err := s.Update(func(tx WriteTx) error { return tx.SaveCollection(def) }); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func put(t *testing.T, s Storage, d Doc) Doc {
	t.Helper()
	err := s.Update(func(tx WriteTx) error { return tx.PutDoc("posts", d) })
	if err != nil {
		t.Fatal(err)
	}
	var got Doc
	if err := s.View(func(tx ReadTx) error {
		var ok bool
		var err error
		got, ok, err = tx.GetDoc("posts", d.ID())
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("document not found after write")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCRUDAndIndexMaintenance(t *testing.T) {
	s := openTestStore(t, Options{})
	createPosts(t, s)

	d1 := put(t, s, Doc{"title": "alpha", "views": json.Number("10"), "tags": []any{"go", "db"}})
	d2 := put(t, s, Doc{"title": "beta", "views": json.Number("5"), "tags": []any{"go"}})
	_ = put(t, s, Doc{"title": "gamma", "views": json.Number("20")})

	if d1["created"] == "" || d1["updated"] == "" {
		t.Fatal("storage did not set timestamps")
	}
	created := d1["created"]
	time.Sleep(2 * time.Millisecond)

	// Update title and views; old index entries must disappear.
	d1["title"] = "alpha2"
	d1["views"] = json.Number("7")
	d1 = put(t, s, d1)
	if d1["created"] != created {
		t.Fatal("created timestamp changed on update")
	}

	// Unique index must reject a duplicate title.
	err := s.Update(func(tx WriteTx) error {
		return tx.PutDoc("posts", Doc{"title": "beta", "views": json.Number("1")})
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}

	// Unique title range must find exactly one document.
	var titles []string
	if err := s.View(func(tx ReadTx) error {
		prefix, _ := codec.EncodeIndexValue("beta")
		start, end := codec.PrefixRange(prefix)
		it := tx.ScanIndex("posts", "title_idx", KeyRange{Start: start, End: end}, ScanOpts{})
		defer it.Close()
		for it.Next() {
			titles = append(titles, it.Doc().GetString("title"))
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(titles) != 1 || titles[0] != "beta" {
		t.Fatalf("titles = %v", titles)
	}

	// Ascending and descending scans on views.
	var asc, desc []float64
	if err := s.View(func(tx ReadTx) error {
		it := tx.ScanIndex("posts", "views_idx", KeyRange{}, ScanOpts{})
		defer it.Close()
		for it.Next() {
			v, _ := codec.Number(it.Doc()["views"])
			asc = append(asc, v)
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.View(func(tx ReadTx) error {
		it := tx.ScanIndex("posts", "views_idx", KeyRange{}, ScanOpts{Reverse: true})
		defer it.Close()
		for it.Next() {
			v, _ := codec.Number(it.Doc()["views"])
			desc = append(desc, v)
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}
	wantAsc := []float64{5, 7, 20}
	if len(asc) != len(wantAsc) {
		t.Fatalf("asc = %v", asc)
	}
	for i := range wantAsc {
		if asc[i] != wantAsc[i] || desc[len(desc)-1-i] != wantAsc[i] {
			t.Fatalf("asc=%v desc=%v", asc, desc)
		}
	}

	// Multikey: both go documents are reachable through the tags index.
	var goDocs int
	if err := s.View(func(tx ReadTx) error {
		prefix, _ := codec.EncodeIndexValue("go")
		start, end := codec.PrefixRange(prefix)
		it := tx.ScanIndex("posts", "tags_idx", KeyRange{Start: start, End: end}, ScanOpts{})
		defer it.Close()
		for it.Next() {
			goDocs++
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if goDocs != 2 {
		t.Fatalf("expected 2 go docs, got %d", goDocs)
	}

	// Update tags: remove "go" from d2 and re-check.
	d2["tags"] = []any{"db"}
	if err := s.Update(func(tx WriteTx) error { return tx.PutDoc("posts", d2) }); err != nil {
		t.Fatal(err)
	}
	goDocs = 0
	if err := s.View(func(tx ReadTx) error {
		prefix, _ := codec.EncodeIndexValue("go")
		start, end := codec.PrefixRange(prefix)
		it := tx.ScanIndex("posts", "tags_idx", KeyRange{Start: start, End: end}, ScanOpts{})
		defer it.Close()
		for it.Next() {
			goDocs++
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if goDocs != 1 {
		t.Fatalf("expected 1 go doc after update, got %d", goDocs)
	}

	// Delete removes the document and all its index entries.
	if err := s.Update(func(tx WriteTx) error { return tx.DeleteDoc("posts", d1.ID()) }); err != nil {
		t.Fatal(err)
	}
	if err := s.View(func(tx ReadTx) error {
		_, ok, err := tx.GetDoc("posts", d1.ID())
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("document still present")
		}
		prefix, _ := codec.EncodeIndexValue("alpha2")
		start, end := codec.PrefixRange(prefix)
		it := tx.ScanIndex("posts", "title_idx", KeyRange{Start: start, End: end}, ScanOpts{})
		defer it.Close()
		if it.Next() {
			t.Fatal("index entry still present")
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}

	report, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK() {
		t.Fatalf("check found issues: %+v", report.Issues)
	}
	if report.Documents != 2 {
		t.Fatalf("documents = %d", report.Documents)
	}
}

func TestScanPagination(t *testing.T) {
	s := openTestStore(t, Options{})
	createPosts(t, s)
	for i := 0; i < 10; i++ {
		put(t, s, Doc{"title": "t" + itoa(i), "views": json.Number(itoa(i))})
	}
	var (
		firstPage  []string
		lastKey    []byte
		secondPage []string
	)
	if err := s.View(func(tx ReadTx) error {
		it := tx.ScanIndex("posts", "views_idx", KeyRange{}, ScanOpts{Limit: 4})
		defer it.Close()
		for it.Next() {
			firstPage = append(firstPage, it.Doc().GetString("title"))
			lastKey = append([]byte(nil), it.Key()...)
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 4 {
		t.Fatalf("first page = %v", firstPage)
	}
	if err := s.View(func(tx ReadTx) error {
		it := tx.ScanIndex("posts", "views_idx", KeyRange{}, ScanOpts{After: lastKey, Limit: 4})
		defer it.Close()
		for it.Next() {
			secondPage = append(secondPage, it.Doc().GetString("title"))
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}
	for _, title := range firstPage {
		for _, other := range secondPage {
			if title == other {
				t.Fatalf("duplicate across pages: %v / %v", firstPage, secondPage)
			}
		}
	}
	if len(secondPage) != 4 {
		t.Fatalf("second page = %v", secondPage)
	}
}

func TestCollectionLifecycleAndBuildIndex(t *testing.T) {
	s := openTestStore(t, Options{})
	def := &collections.CollectionDef{
		Name:   "items",
		Fields: []collections.Field{{Name: "name", Type: "string"}},
	}
	if err := s.Update(func(tx WriteTx) error { return tx.SaveCollection(def) }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if err := s.Update(func(tx WriteTx) error {
			return tx.PutDoc("items", Doc{"name": "n" + itoa(i)})
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Add a new index to an existing collection. New writes are maintained
	// immediately, but the index starts as building.
	def.Indexes = []collections.Index{{Name: "name_idx", Field: "name", State: "building"}}
	if err := s.Update(func(tx WriteTx) error { return tx.SaveCollection(def) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(tx WriteTx) error {
		return tx.PutDoc("items", Doc{"name": "new-doc"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.BuildIndex(context.Background(), "items", "name_idx"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.View(func(tx ReadTx) error {
		it := tx.ScanIndex("items", "name_idx", KeyRange{}, ScanOpts{})
		defer it.Close()
		for it.Next() {
			count++
		}
		return it.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if count != 26 {
		t.Fatalf("index count = %d", count)
	}
	report, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK() {
		t.Fatalf("check issues: %+v", report.Issues)
	}

	if err := s.Update(func(tx WriteTx) error { return tx.DeleteCollection("items") }); err != nil {
		t.Fatal(err)
	}
	if err := s.View(func(tx ReadTx) error {
		if _, err := tx.Collection("items"); !errors.Is(err, ErrCollectionNotFound) {
			t.Fatalf("expected collection not found, got %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	createPosts(t, s)
	put(t, s, Doc{"title": "a", "views": json.Number("1")})
	put(t, s, Doc{"title": "b", "views": json.Number("2")})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Corrupt the index directly, as if a bug had skipped index maintenance.
	db, err := bolt.Open(filepath.Join(dir, "data.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("i:posts:views_idx"))
		if b == nil {
			t.Fatal("index bucket missing")
		}
		c := b.Cursor()
		k, _ := c.First()
		if k == nil {
			t.Fatal("index is empty")
		}
		return b.Delete(k)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	report, err := s2.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.OK() {
		t.Fatal("expected check to detect the missing index entry")
	}
	foundMissing := false
	for _, issue := range report.Issues {
		if issue.Kind == "missing_index_entry" {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("issues = %+v", report.Issues)
	}
}

func TestBackup(t *testing.T) {
	s := openTestStore(t, Options{})
	createPosts(t, s)
	put(t, s, Doc{"title": "backup", "views": json.Number("3")})

	var buf bytes.Buffer
	if err := s.Backup(&buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("backup is empty")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "data.db")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("d:posts"))
		if b == nil || b.Stats().KeyN != 1 {
			t.Fatalf("backup missing documents")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{ReadOnly: true})
	if err != nil {
		// A read-only open on a missing file may fail depending on platform.
		s, err = Open(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		s, err = Open(dir, Options{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	defer s.Close()
	if err := s.Update(func(tx WriteTx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("expected read-only error, got %v", err)
	}
	if err := s.View(func(tx ReadTx) error {
		_, _, err := tx.GetDoc("nope", "bad-id")
		if !errors.Is(err, ErrInvalidID) {
			t.Fatalf("expected invalid id, got %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("double close should be safe: %v", err)
	}
	if err := s.View(func(tx ReadTx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected closed error, got %v", err)
	}
}

func TestPutDocValidation(t *testing.T) {
	s := openTestStore(t, Options{MaxDocSize: 512})
	createPosts(t, s)
	err := s.Update(func(tx WriteTx) error {
		return tx.PutDoc("posts", Doc{"views": json.Number("1")})
	})
	if err == nil {
		t.Fatal("missing required title should fail")
	}
	err = s.Update(func(tx WriteTx) error {
		return tx.PutDoc("posts", Doc{"title": "big", "data": string(bytes.Repeat([]byte("x"), 1024))})
	})
	if err == nil {
		t.Fatal("oversized document should fail")
	}
	// Failed writes must not leave index entries behind.
	report, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Documents != 0 || !report.OK() {
		t.Fatalf("rollback failed: %+v %+v", report, report.Issues)
	}
}
