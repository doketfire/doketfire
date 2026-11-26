// Package storage defines the persistence contract and its embedded Bbolt
// implementation. Everything outside this package works with documents and
// transactions, never with Bbolt directly.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/doketfire/doketfire/internal/collections"
)

// Doc is an alias for a stored JSON document.
type Doc = collections.Document

// DefaultMaxDocSize is the default per-document size limit (1 MiB).
const DefaultMaxDocSize = 1 << 20

var (
	// ErrCollectionNotFound indicates an unknown collection.
	ErrCollectionNotFound = errors.New("collection not found")
	// ErrNotFound indicates a missing document.
	ErrNotFound = errors.New("document not found")
	// ErrConflict indicates a unique-index or document-id conflict.
	ErrConflict = errors.New("conflict")
	// ErrInvalidID indicates a malformed document identifier.
	ErrInvalidID = errors.New("invalid document id")
	// ErrIndexNotFound indicates an unknown index.
	ErrIndexNotFound = errors.New("index not found")
	// ErrReadOnly indicates a write against a read-only store.
	ErrReadOnly = errors.New("storage is read-only")
	// ErrClosed indicates use after Close.
	ErrClosed = errors.New("storage is closed")
)

// ConflictError carries context about the violated constraint.
type ConflictError struct {
	Collection string
	Index      string
	DocID      string
	Reason     string
}

func (e *ConflictError) Error() string {
	if e.Index != "" {
		return fmt.Sprintf("conflict on %s.%s: %s", e.Collection, e.Index, e.Reason)
	}
	return fmt.Sprintf("conflict on %s: %s", e.Collection, e.Reason)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// KeyRange is a half-open byte range: Start is inclusive, End is exclusive.
// A nil bound means unbounded.
type KeyRange struct {
	Start []byte
	End   []byte
}

// ScanOpts controls a forward or reverse iteration.
type ScanOpts struct {
	// Reverse iterates from high keys to low keys.
	Reverse bool
	// Limit stops after this many yielded entries. Zero means no limit.
	Limit int
	// After is an exclusive cursor in scan direction. It is a raw document id
	// for ScanDocs and a raw index key for ScanIndex.
	After []byte
}

// Iterator walks documents inside an open transaction. It is not safe for
// concurrent use and must not outlive the View/Update callback that created it.
type Iterator interface {
	Next() bool
	Key() []byte
	Doc() Doc
	Err() error
	Close() error
}

// ReadTx exposes read operations inside a consistent snapshot.
type ReadTx interface {
	Collection(name string) (*collections.CollectionDef, error)
	Collections() ([]*collections.CollectionDef, error)
	GetDoc(col, id string) (Doc, bool, error)
	ScanDocs(col string, opts ScanOpts) Iterator
	ScanIndex(col, index string, r KeyRange, opts ScanOpts) Iterator
	CountDocs(col string) (int, error)

	// Raw operations are reserved for system buckets (names starting with "_").
	// They are used by the auth package for refresh tokens and admins.
	RawGet(bucket string, key []byte) ([]byte, error)
	RawScan(bucket string, prefix []byte) ([]RawEntry, error)
}

// RawEntry is a copy of a raw key/value pair.
type RawEntry struct {
	Key   []byte
	Value []byte
}

// WriteTx adds mutations. Index maintenance is performed inside the same
// transaction as the document write, so a crash can never leave them out of
// sync.
type WriteTx interface {
	ReadTx
	PutDoc(col string, d Doc) error
	DeleteDoc(col, id string) error
	SaveCollection(def *collections.CollectionDef) error
	DeleteCollection(name string) error
	RawPut(bucket string, key, value []byte) error
	RawDelete(bucket string, key []byte) error
}

// Storage is the database engine contract.
type Storage interface {
	View(fn func(ReadTx) error) error
	Update(fn func(WriteTx) error) error
	Backup(w io.Writer) error
	Close() error

	// Dir is the data directory containing data.db.
	Dir() string
	// Path is the absolute path of data.db.
	Path() string
	// Size reports the current database file size in bytes.
	Size() int64
	// CollectionCount returns the number of collections without decoding their
	// definitions.
	CollectionCount() int
	// BuildIndex builds (or rebuilds) a secondary index in bounded batches.
	BuildIndex(ctx context.Context, collection, index string) error
	// Check verifies document/index consistency.
	Check(ctx context.Context) (*CheckReport, error)
}

// Options configures the Bbolt store.
type Options struct {
	// Timeout is how long Open waits for the file lock. Defaults to 2s.
	Timeout time.Duration
	// ReadOnly opens the database without write access.
	ReadOnly bool
	// NoSync disables fsync after commits. Never use it in production.
	NoSync bool
	// MaxDocSize is the maximum serialized document size. Defaults to 1 MiB.
	MaxDocSize int
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Second
	}
	if o.MaxDocSize <= 0 {
		o.MaxDocSize = DefaultMaxDocSize
	}
	return o
}

// Issue is a consistency problem found by Check.
type Issue struct {
	Collection string `json:"collection"`
	Index      string `json:"index,omitempty"`
	DocID      string `json:"docId,omitempty"`
	Kind       string `json:"kind"`
	Message    string `json:"message"`
}

// CheckReport summarizes a consistency scan.
type CheckReport struct {
	Collections  int     `json:"collections"`
	Documents    int     `json:"documents"`
	IndexEntries int     `json:"indexEntries"`
	Issues       []Issue `json:"issues"`
	Truncated    bool    `json:"truncated,omitempty"`
}

// OK reports whether the database is consistent.
func (r *CheckReport) OK() bool { return r != nil && len(r.Issues) == 0 }
