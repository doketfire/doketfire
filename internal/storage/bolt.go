package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/doketfire/doketfire/internal/codec"
	"github.com/doketfire/doketfire/internal/collections"
	bolt "go.etcd.io/bbolt"
)

const (
	metaBucket        = "_meta"
	collectionsBucket = "_collections"
	refreshBucket     = "_refresh_tokens"
	adminsBucket      = "_admins"
	schemaVersion     = 1
	indexBuildBatch   = 1000
	maxIndexEntries   = 2000
)

// BoltStore is the embedded Bbolt implementation of Storage.
type BoltStore struct {
	db   *bolt.DB
	dir  string
	path string
	opts Options

	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error

	sizeMu  sync.Mutex
	sizeVal int64
	sizeAt  time.Time

	defCache *defCache
}

type defCache struct {
	mu    sync.RWMutex
	items map[string]*collections.CollectionDef
}

// Open opens (or creates) <dir>/data.db and ensures the system buckets exist.
func Open(dir string, opts Options) (*BoltStore, error) {
	opts = opts.withDefaults()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dir, "data.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{
		Timeout:  opts.Timeout,
		ReadOnly: opts.ReadOnly,
		NoSync:   opts.NoSync,
	})
	if err != nil {
		return nil, fmt.Errorf("open database %s (is another Doketfire instance running?): %w", path, err)
	}
	s := &BoltStore{db: db, dir: dir, path: path, opts: opts, defCache: &defCache{items: make(map[string]*collections.CollectionDef)}}
	if !opts.ReadOnly {
		if err := s.initialize(); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *BoltStore) initialize() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{metaBucket, collectionsBucket, refreshBucket, adminsBucket} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		meta := tx.Bucket([]byte(metaBucket))
		if meta.Get([]byte("schema_version")) == nil {
			if err := meta.Put([]byte("schema_version"), []byte(fmt.Sprint(schemaVersion))); err != nil {
				return err
			}
		}
		return nil
	})
}

// Dir returns the data directory.
func (s *BoltStore) Dir() string { return s.dir }

// Path returns the data.db path.
func (s *BoltStore) Path() string { return s.path }

// CollectionCount returns the number of collections without decoding their
// definitions. Used by health and metrics.
func (s *BoltStore) CollectionCount() int {
	var n int
	_ = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(collectionsBucket))
		if b != nil {
			n = b.Stats().KeyN
		}
		return nil
	})
	return n
}

// Size returns the database file size. The value is cached briefly: health
// and metrics endpoints poll it, and stat calls are expensive on some
// platforms.
func (s *BoltStore) Size() int64 {
	const ttl = 5 * time.Second
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	if !s.sizeAt.IsZero() && time.Since(s.sizeAt) < ttl {
		return s.sizeVal
	}
	st, err := os.Stat(s.path)
	if err != nil {
		return 0
	}
	s.sizeVal = st.Size()
	s.sizeAt = time.Now()
	return s.sizeVal
}

// View runs fn in a read-only transaction.
func (s *BoltStore) View(fn func(ReadTx) error) error {
	if s.closed.Load() {
		return ErrClosed
	}
	return s.db.View(func(tx *bolt.Tx) error {
		return fn(&readTx{tx: tx, store: s})
	})
}

// Update runs fn in a read-write transaction. fn may be retried only when the
// caller uses bbolt batches; View/Update here run exactly once.
func (s *BoltStore) Update(fn func(WriteTx) error) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if s.opts.ReadOnly {
		return ErrReadOnly
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return fn(&writeTx{readTx: readTx{tx: tx, store: s}, maxDocSize: s.opts.MaxDocSize})
	})
}

// Backup writes a consistent snapshot of the database to w.
func (s *BoltStore) Backup(w io.Writer) error {
	if s.closed.Load() {
		return ErrClosed
	}
	return s.db.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(w)
		return err
	})
}

// Close closes the database. It is safe to call multiple times.
func (s *BoltStore) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

type readTx struct {
	tx    *bolt.Tx
	store *BoltStore
}

func (r *readTx) Collection(name string) (*collections.CollectionDef, error) {
	cache := r.store.defCache
	cache.mu.RLock()
	if def, ok := cache.items[name]; ok {
		cache.mu.RUnlock()
		return def.Clone(), nil
	}
	cache.mu.RUnlock()

	b := r.tx.Bucket([]byte(collectionsBucket))
	if b == nil {
		return nil, ErrCollectionNotFound
	}
	raw := b.Get([]byte(name))
	if raw == nil {
		return nil, fmt.Errorf("%w: %s", ErrCollectionNotFound, name)
	}
	var def collections.CollectionDef
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil, fmt.Errorf("decode collection definition %q: %w", name, err)
	}
	ptr := &def
	cache.mu.Lock()
	cache.items[name] = ptr
	cache.mu.Unlock()
	return ptr.Clone(), nil
}

func (r *readTx) Collections() ([]*collections.CollectionDef, error) {
	b := r.tx.Bucket([]byte(collectionsBucket))
	if b == nil {
		return nil, nil
	}
	cache := r.store.defCache
	var out []*collections.CollectionDef
	err := b.ForEach(func(k, v []byte) error {
		var def collections.CollectionDef
		if err := json.Unmarshal(v, &def); err != nil {
			return fmt.Errorf("decode collection definition %q: %w", k, err)
		}
		ptr := &def
		cache.mu.Lock()
		cache.items[string(k)] = ptr
		cache.mu.Unlock()
		out = append(out, ptr.Clone())
		return nil
	})
	return out, err
}

func (r *readTx) GetDoc(col, id string) (Doc, bool, error) {
	idb, err := collections.IDBytes(id)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s", ErrInvalidID, id)
	}
	b := r.tx.Bucket(docBucket(col))
	if b == nil {
		return nil, false, nil
	}
	raw := b.Get(idb)
	if raw == nil {
		return nil, false, nil
	}
	d, err := collections.Decode(raw)
	if err != nil {
		return nil, false, fmt.Errorf("decode document %s/%s: %w", col, id, err)
	}
	return d, true, nil
}

func (r *readTx) ScanDocs(col string, opts ScanOpts) Iterator {
	b := r.tx.Bucket(docBucket(col))
	if b == nil {
		return emptyIterator()
	}
	return newIterator(b.Cursor(), b, false, KeyRange{}, opts)
}

func (r *readTx) ScanIndex(col, index string, kr KeyRange, opts ScanOpts) Iterator {
	ib := r.tx.Bucket(indexBucket(col, index))
	if ib == nil {
		return emptyIterator()
	}
	db := r.tx.Bucket(docBucket(col))
	if db == nil {
		return emptyIterator()
	}
	return newIterator(ib.Cursor(), db, true, kr, opts)
}

func (r *readTx) CountDocs(col string) (int, error) {
	b := r.tx.Bucket(docBucket(col))
	if b == nil {
		return 0, nil
	}
	return b.Stats().KeyN, nil
}

func (r *readTx) RawGet(bucket string, key []byte) ([]byte, error) {
	if err := validateSystemBucket(bucket); err != nil {
		return nil, err
	}
	b := r.tx.Bucket([]byte(bucket))
	if b == nil {
		return nil, nil
	}
	v := b.Get(key)
	if v == nil {
		return nil, nil
	}
	return append([]byte(nil), v...), nil
}

func (r *readTx) RawScan(bucket string, prefix []byte) ([]RawEntry, error) {
	if err := validateSystemBucket(bucket); err != nil {
		return nil, err
	}
	b := r.tx.Bucket([]byte(bucket))
	if b == nil {
		return nil, nil
	}
	var out []RawEntry
	c := b.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		out = append(out, RawEntry{
			Key:   append([]byte(nil), k...),
			Value: append([]byte(nil), v...),
		})
	}
	return out, nil
}

type writeTx struct {
	readTx
	maxDocSize int
}

func (w *writeTx) PutDoc(col string, d Doc) error {
	def, err := w.Collection(col)
	if err != nil {
		return err
	}
	if d == nil {
		return &collections.ValidationError{Message: "document must be a JSON object"}
	}
	if err := d.EnsureIdentity(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidID, err)
	}
	id := d.ID()
	idb, err := collections.IDBytes(id)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidID, err)
	}

	docs := w.tx.Bucket(docBucket(col))
	if docs == nil {
		return fmt.Errorf("%w: %s", ErrCollectionNotFound, col)
	}

	var old Doc
	if raw := docs.Get(idb); raw != nil {
		old, err = collections.Decode(raw)
		if err != nil {
			return fmt.Errorf("decode existing document %s: %w", id, err)
		}
	}

	now := time.Now()
	// System timestamps default to "now", but a client-provided valid value is
	// honored (backdating). On update, a missing created falls back to the
	// stored value so the document keeps a stable origin.
	if !collections.IsTimestamp(d.GetString(collections.FieldCreated)) {
		if old != nil {
			if v, ok := old[collections.FieldCreated].(string); ok && collections.IsTimestamp(v) {
				d[collections.FieldCreated] = v
			} else {
				d[collections.FieldCreated] = collections.FormatTime(now)
			}
		} else {
			d[collections.FieldCreated] = collections.FormatTime(now)
		}
	}
	if !collections.IsTimestamp(d.GetString(collections.FieldUpdated)) {
		d[collections.FieldUpdated] = collections.FormatTime(now)
	}
	if err := def.ValidateDocument(d); err != nil {
		return err
	}

	raw, err := d.Encode()
	if err != nil {
		return fmt.Errorf("encode document: %w", err)
	}
	limit := w.maxDocSize
	if limit <= 0 {
		limit = DefaultMaxDocSize
	}
	if len(raw) > limit {
		return &collections.ValidationError{Message: fmt.Sprintf("document exceeds the %d byte limit", limit)}
	}

	// Remove old index entries before writing new ones. If any later step
	// fails, Bbolt rolls the whole transaction back.
	if old != nil {
		for _, idx := range def.Indexes {
			if err := w.deleteIndexEntries(col, idx, old, idb); err != nil {
				return err
			}
		}
	}
	for _, idx := range def.Indexes {
		if err := w.putIndexEntries(col, def, idx, d, idb); err != nil {
			return err
		}
	}
	if err := docs.Put(idb, raw); err != nil {
		return err
	}
	return nil
}

func (w *writeTx) DeleteDoc(col, id string) error {
	idb, err := collections.IDBytes(id)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidID, err)
	}
	docs := w.tx.Bucket(docBucket(col))
	if docs == nil {
		return ErrNotFound
	}
	raw := docs.Get(idb)
	if raw == nil {
		return ErrNotFound
	}
	old, err := collections.Decode(raw)
	if err != nil {
		return fmt.Errorf("decode document %s: %w", id, err)
	}
	def, err := w.Collection(col)
	if err != nil {
		return err
	}
	for _, idx := range def.Indexes {
		if err := w.deleteIndexEntries(col, idx, old, idb); err != nil {
			return err
		}
	}
	if err := docs.Delete(idb); err != nil {
		return err
	}
	return w.cascadeDelete(col, id, def)
}

// cascadeDelete removes documents in declared child collections that
// reference the deleted document. It runs inside the same Bbolt transaction
// as the parent delete, so the cascade is atomic. Child documents keep their
// index entries consistent by reusing deleteIndexEntries.
func (w *writeTx) cascadeDelete(col, id string, def *collections.CollectionDef) error {
	for _, child := range def.Cascade {
		childDef, err := w.Collection(child.Collection)
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				continue
			}
			return err
		}
		bucket := w.tx.Bucket(docBucket(child.Collection))
		if bucket == nil {
			continue
		}
		var doomed [][]byte
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			doc, err := collections.Decode(v)
			if err != nil {
				continue
			}
			if ref, ok := collections.GetPath(doc, child.Field).(string); ok && ref == id {
				doomed = append(doomed, append([]byte(nil), k...))
			}
		}
		for _, key := range doomed {
			raw := bucket.Get(key)
			if raw == nil {
				continue
			}
			doc, err := collections.Decode(raw)
			if err != nil {
				continue
			}
			idb, err := collections.IDBytes(doc.ID())
			if err != nil {
				continue
			}
			for _, idx := range childDef.Indexes {
				if err := w.deleteIndexEntries(child.Collection, idx, doc, idb); err != nil {
					return err
				}
			}
			if err := bucket.Delete(key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *writeTx) SaveCollection(def *collections.CollectionDef) error {
	def = def.Clone()
	def.Normalize()
	if err := def.Validate(); err != nil {
		return err
	}
	b := w.tx.Bucket([]byte(collectionsBucket))
	if b == nil {
		return fmt.Errorf("internal: collections bucket missing")
	}
	var previous *collections.CollectionDef
	if raw := b.Get([]byte(def.Name)); raw != nil {
		var old collections.CollectionDef
		if err := json.Unmarshal(raw, &old); err != nil {
			return err
		}
		previous = &old
	}

	// Drop buckets belonging to indexes that no longer exist.
	if previous != nil {
		for _, old := range previous.Indexes {
			if _, ok := def.Index(old.Name); !ok {
				_ = w.tx.DeleteBucket(indexBucket(def.Name, old.Name))
			}
		}
	}

	for i := range def.Indexes {
		idx := &def.Indexes[i]
		if previous != nil {
			if old, ok := previous.Index(idx.Name); ok {
				if old.Field != idx.Field || old.Unique != idx.Unique || old.Fulltext != idx.Fulltext {
					// Changing the shape of an index requires a rebuild. Keep
					// the index out of the planner until it is complete.
					idx.State = "building"
				}
			}
		}
		if idx.State == "" {
			idx.State = "building"
		}
		if _, err := w.tx.CreateBucketIfNotExists(indexBucket(def.Name, idx.Name)); err != nil {
			return fmt.Errorf("create index bucket %s/%s: %w", def.Name, idx.Name, err)
		}
	}

	raw, err := json.Marshal(def)
	if err != nil {
		return err
	}
	if err := b.Put([]byte(def.Name), raw); err != nil {
		return err
	}
	if _, err := w.tx.CreateBucketIfNotExists(docBucket(def.Name)); err != nil {
		return fmt.Errorf("create document bucket %s: %w", def.Name, err)
	}
	// Invalidate the cached definition so the next read reloads it.
	if w.readTx.store != nil && w.readTx.store.defCache != nil {
		w.readTx.store.defCache.mu.Lock()
		delete(w.readTx.store.defCache.items, def.Name)
		w.readTx.store.defCache.mu.Unlock()
	}
	return nil
}

func (w *writeTx) DeleteCollection(name string) error {
	b := w.tx.Bucket([]byte(collectionsBucket))
	if b == nil || b.Get([]byte(name)) == nil {
		return fmt.Errorf("%w: %s", ErrCollectionNotFound, name)
	}
	def, err := w.Collection(name)
	if err != nil {
		return err
	}
	for _, idx := range def.Indexes {
		_ = w.tx.DeleteBucket(indexBucket(name, idx.Name))
	}
	_ = w.tx.DeleteBucket(docBucket(name))
	if err := b.Delete([]byte(name)); err != nil {
		return err
	}
	if w.readTx.store != nil && w.readTx.store.defCache != nil {
		w.readTx.store.defCache.mu.Lock()
		delete(w.readTx.store.defCache.items, name)
		w.readTx.store.defCache.mu.Unlock()
	}
	return nil
}

func (w *writeTx) RawPut(bucket string, key, value []byte) error {
	if err := validateSystemBucket(bucket); err != nil {
		return err
	}
	b, err := w.tx.CreateBucketIfNotExists([]byte(bucket))
	if err != nil {
		return err
	}
	return b.Put(key, value)
}

func (w *writeTx) RawDelete(bucket string, key []byte) error {
	if err := validateSystemBucket(bucket); err != nil {
		return err
	}
	b := w.tx.Bucket([]byte(bucket))
	if b == nil {
		return nil
	}
	return b.Delete(key)
}

func (w *writeTx) putIndexEntries(col string, def *collections.CollectionDef, idx collections.Index, d Doc, idb []byte) error {
	value := collections.GetPath(d, idx.Field)
	var entries [][]byte
	if idx.Fulltext {
		s, _ := value.(string)
		for _, tok := range collections.Tokenize(s) {
			enc, err := codec.EncodeIndexValue(tok)
			if err != nil {
				return &collections.ValidationError{Field: idx.Field, Message: "cannot index value: " + err.Error()}
			}
			entries = append(entries, enc)
		}
	} else {
		var err error
		entries, err = codec.EncodeIndexValues(value)
		if err != nil {
			return &collections.ValidationError{Field: idx.Field, Message: "cannot index value: " + err.Error()}
		}
	}
	if len(entries) > maxIndexEntries {
		return &collections.ValidationError{Field: idx.Field, Message: fmt.Sprintf("too many array elements to index (max %d)", maxIndexEntries)}
	}
	if idx.Unique && len(entries) > 1 {
		return &collections.ValidationError{Field: idx.Field, Message: "a unique index cannot be created on an array field"}
	}
	b := w.tx.Bucket(indexBucket(col, idx.Name))
	if b == nil {
		return fmt.Errorf("internal: index bucket %s/%s missing", col, idx.Name)
	}
	for _, enc := range entries {
		if idx.Unique {
			if existing := b.Get(enc); existing != nil && !bytes.Equal(existing, idb) {
				return &ConflictError{
					Collection: col,
					Index:      idx.Name,
					DocID:      d.ID(),
					Reason:     fmt.Sprintf("duplicate value for unique index on %q", idx.Field),
				}
			}
			if err := b.Put(enc, idb); err != nil {
				return err
			}
			continue
		}
		key := make([]byte, 0, len(enc)+16)
		key = append(key, enc...)
		key = append(key, idb...)
		if len(key) > codec.MaxKeySize {
			return codec.ErrValueTooLarge
		}
		if err := b.Put(key, idb); err != nil {
			return err
		}
	}
	return nil
}

func (w *writeTx) deleteIndexEntries(col string, idx collections.Index, d Doc, idb []byte) error {
	var entries [][]byte
	if idx.Fulltext {
		s, _ := collections.GetPath(d, idx.Field).(string)
		for _, tok := range collections.Tokenize(s) {
			enc, err := codec.EncodeIndexValue(tok)
			if err != nil {
				return err
			}
			entries = append(entries, enc)
		}
	} else {
		var err error
		entries, err = codec.EncodeIndexValues(collections.GetPath(d, idx.Field))
		if err != nil {
			// A document that was writable earlier might now be unindexable after
			// an index definition change. Rebuild will repair the bucket.
			if errors.Is(err, codec.ErrNotIndexable) {
				return nil
			}
			return err
		}
	}
	b := w.tx.Bucket(indexBucket(col, idx.Name))
	if b == nil {
		return nil
	}
	for _, enc := range entries {
		if idx.Unique {
			if err := b.Delete(enc); err != nil {
				return err
			}
			continue
		}
		key := make([]byte, 0, len(enc)+16)
		key = append(key, enc...)
		key = append(key, idb...)
		if err := b.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

func validateSystemBucket(name string) error {
	if !strings.HasPrefix(name, "_") || strings.Contains(name, ":") {
		return fmt.Errorf("raw access is restricted to system buckets, got %q", name)
	}
	return nil
}

func docBucket(col string) []byte        { return []byte("d:" + col) }
func indexBucket(col, idx string) []byte { return []byte("i:" + col + ":" + idx) }

// BuildIndex rebuilds a secondary index in bounded transactions and marks it
// ready when finished.
func (s *BoltStore) BuildIndex(ctx context.Context, collection, index string) error {
	def, err := s.collectionDef(collection)
	if err != nil {
		return err
	}
	idx, ok := def.Index(index)
	if !ok {
		return fmt.Errorf("%w: %s", ErrIndexNotFound, index)
	}
	idxCopy := *idx

	// Mark as building while the bucket is cleared.
	if err := s.Update(func(tx WriteTx) error {
		d, err := tx.Collection(collection)
		if err != nil {
			return err
		}
		if target, ok := d.Index(index); ok {
			target.State = "building"
		}
		if err := tx.SaveCollection(d); err != nil {
			return err
		}
		wt := tx.(*writeTx)
		_ = wt.tx.DeleteBucket(indexBucket(collection, index))
		_, err = wt.tx.CreateBucket(indexBucket(collection, index))
		return err
	}); err != nil {
		return err
	}

	var after []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var docs []Doc
		var last []byte
		if err := s.View(func(rt ReadTx) error {
			it := rt.ScanDocs(collection, ScanOpts{After: after, Limit: indexBuildBatch})
			defer it.Close()
			for it.Next() {
				docs = append(docs, it.Doc())
				last = append(last[:0], it.Key()...)
			}
			return it.Err()
		}); err != nil {
			return err
		}
		if len(docs) == 0 {
			break
		}
		if err := s.Update(func(tx WriteTx) error {
			wt := tx.(*writeTx)
			for _, d := range docs {
				idb, err := collections.IDBytes(d.ID())
				if err != nil {
					return err
				}
				if err := wt.putIndexEntries(collection, def, idxCopy, d, idb); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		after = append([]byte(nil), last...)
		if len(docs) < indexBuildBatch {
			break
		}
	}

	// Mark ready.
	return s.Update(func(tx WriteTx) error {
		d, err := tx.Collection(collection)
		if err != nil {
			return err
		}
		if target, ok := d.Index(index); ok {
			target.State = "ready"
		}
		return tx.SaveCollection(d)
	})
}

func (s *BoltStore) collectionDef(name string) (*collections.CollectionDef, error) {
	var def *collections.CollectionDef
	err := s.View(func(tx ReadTx) error {
		var err error
		def, err = tx.Collection(name)
		return err
	})
	return def, err
}

// Check verifies that every ready index has exactly the entries implied by
// the stored documents.
func (s *BoltStore) Check(ctx context.Context) (*CheckReport, error) {
	report := &CheckReport{Issues: []Issue{}}
	const maxIssues = 200
	addIssue := func(issue Issue) {
		if len(report.Issues) >= maxIssues {
			report.Truncated = true
			return
		}
		report.Issues = append(report.Issues, issue)
	}
	err := s.View(func(rt ReadTx) error {
		cols, err := rt.Collections()
		if err != nil {
			return err
		}
		report.Collections = len(cols)
		r := rt.(*readTx)
		for _, def := range cols {
			if err := ctx.Err(); err != nil {
				return err
			}
			docsB := r.tx.Bucket(docBucket(def.Name))
			if docsB == nil {
				addIssue(Issue{Collection: def.Name, Kind: "missing_bucket", Message: "documents bucket is missing"})
				continue
			}
			expected := map[string]map[string]struct{}{} // doc id -> index name -> keys
			c := docsB.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				report.Documents++
				if len(k) != 16 {
					addIssue(Issue{Collection: def.Name, Kind: "bad_document_key", Message: fmt.Sprintf("invalid document key length %d", len(k))})
					continue
				}
				doc, err := collections.Decode(v)
				if err != nil {
					addIssue(Issue{Collection: def.Name, DocID: fmt.Sprintf("%x", k), Kind: "bad_document", Message: err.Error()})
					continue
				}
				id := doc.ID()
				if id == "" {
					addIssue(Issue{Collection: def.Name, Kind: "bad_document", Message: "document has no id"})
					continue
				}
				idb, err := collections.IDBytes(id)
				if err != nil || !bytes.Equal(idb, k) {
					addIssue(Issue{Collection: def.Name, DocID: id, Kind: "id_mismatch", Message: "document id does not match its key"})
				}
				exp := map[string]struct{}{}
				for _, idx := range def.Indexes {
					if idx.State != "ready" {
						continue
					}
					keys, err := indexKeysFor(idx, idb, doc)
					if err != nil {
						addIssue(Issue{Collection: def.Name, Index: idx.Name, DocID: id, Kind: "unindexable_document", Message: err.Error()})
						continue
					}
					b := r.tx.Bucket(indexBucket(def.Name, idx.Name))
					for _, key := range keys {
						exp[idx.Name+"\x00"+string(key)] = struct{}{}
						if b == nil {
							addIssue(Issue{Collection: def.Name, Index: idx.Name, DocID: id, Kind: "missing_index_bucket", Message: "index bucket is missing"})
							continue
						}
						val := b.Get(key)
						if val == nil {
							addIssue(Issue{Collection: def.Name, Index: idx.Name, DocID: id, Kind: "missing_index_entry", Message: "document is not present in a ready index"})
						} else if !bytes.Equal(val, idb) {
							addIssue(Issue{Collection: def.Name, Index: idx.Name, DocID: id, Kind: "wrong_index_entry", Message: "index entry points to a different document"})
						}
					}
				}
				expected[id] = exp
			}
			// Orphan pass.
			for _, idx := range def.Indexes {
				if idx.State != "ready" {
					continue
				}
				b := r.tx.Bucket(indexBucket(def.Name, idx.Name))
				if b == nil {
					addIssue(Issue{Collection: def.Name, Index: idx.Name, Kind: "missing_index_bucket", Message: "index bucket is missing"})
					continue
				}
				ic := b.Cursor()
				for k, v := ic.First(); k != nil; k, v = ic.Next() {
					report.IndexEntries++
					if len(v) != 16 {
						addIssue(Issue{Collection: def.Name, Index: idx.Name, Kind: "bad_index_entry", Message: "index value is not a document id"})
						continue
					}
					idb := v
					docRaw := docsB.Get(idb)
					if docRaw == nil {
						addIssue(Issue{Collection: def.Name, Index: idx.Name, DocID: fmt.Sprintf("%x", idb), Kind: "orphan_index_entry", Message: "index entry points to a missing document"})
						continue
					}
					doc, err := collections.Decode(docRaw)
					if err != nil {
						continue
					}
					id := doc.ID()
					exp, ok := expected[id]
					if !ok {
						continue
					}
					if _, ok := exp[idx.Name+"\x00"+string(k)]; !ok {
						addIssue(Issue{Collection: def.Name, Index: idx.Name, DocID: id, Kind: "stale_index_entry", Message: "index entry is not implied by the document"})
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

func indexKeysFor(idx collections.Index, idb []byte, doc Doc) ([][]byte, error) {
	entries, err := codec.EncodeIndexValues(collections.GetPath(doc, idx.Field))
	if err != nil {
		return nil, err
	}
	keys := make([][]byte, 0, len(entries))
	for _, enc := range entries {
		if idx.Unique {
			keys = append(keys, append([]byte(nil), enc...))
			continue
		}
		key := make([]byte, 0, len(enc)+16)
		key = append(key, enc...)
		key = append(key, idb...)
		keys = append(keys, key)
	}
	return keys, nil
}
