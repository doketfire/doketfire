package collections

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Document is the in-memory representation of a stored JSON document.
// Numbers are decoded as json.Number to avoid losing integer precision.
type Document map[string]any

// System field names.
const (
	FieldID      = "id"
	FieldCreated = "created"
	FieldUpdated = "updated"

	// Auth collection system fields.
	FieldEmail        = "email"
	FieldPasswordHash = "passwordHash"
	FieldVerified     = "verified"
	FieldTokenKey     = "tokenKey"
)

// TimeFormat is the canonical timestamp representation used by Doketfire.
// Fixed-width UTC timestamps sort lexicographically in the same order as time.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// NewID returns a new time-sortable ULID string.
func NewID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

// NewIDAt is like NewID but uses the supplied time. Useful in tests.
func NewIDAt(t time.Time) string {
	return ulid.MustNew(ulid.Timestamp(t), rand.Reader).String()
}

// ValidID reports whether id is a syntactically valid ULID.
func ValidID(id string) bool {
	if len(id) != 26 {
		return false
	}
	_, err := ulid.ParseStrict(id)
	return err == nil
}

// IDBytes returns the binary (16-byte) representation of a document id.
func IDBytes(id string) ([]byte, error) {
	u, err := ulid.ParseStrict(id)
	if err != nil {
		return nil, fmt.Errorf("invalid document id %q: %w", id, err)
	}
	b := make([]byte, 16)
	copy(b, u[:])
	return b, nil
}

// IDBytesMust panics on an invalid id. Only use it for ids that were already
// validated by the storage layer.
func IDBytesMust(id string) []byte {
	b, err := IDBytes(id)
	if err != nil {
		panic(err)
	}
	return b
}

// FormatTime renders a canonical UTC timestamp.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeFormat)
}

// ParseTime parses a canonical timestamp (and tolerates RFC3339Nano).
func ParseTime(s string) (time.Time, error) {
	if t, err := time.Parse(TimeFormat, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// Now returns the current canonical timestamp.
func Now() string { return FormatTime(time.Now()) }

// IsTimestamp reports whether s is a valid canonical or RFC3339Nano timestamp.
func IsTimestamp(s string) bool {
	_, err := ParseTime(s)
	return err == nil
}

// ID returns the document id.
func (d Document) ID() string {
	s, _ := d[FieldID].(string)
	return s
}

// GetString returns a string field.
func (d Document) GetString(key string) string {
	s, _ := d[key].(string)
	return s
}

// Get returns a shallow copy of a field.
func (d Document) Get(key string) any { return d[key] }

// Clone returns a deep copy of the document.
func (d Document) Clone() Document {
	if d == nil {
		return nil
	}
	out := make(Document, len(d))
	for k, v := range d {
		out[k] = cloneValue(v)
	}
	return out
}

// cloneValue deep-copies the JSON-compatible values produced by Decode. Scalar
// values are immutable and are returned as-is.
func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = cloneValue(item)
		}
		return out
	case Document:
		out := make(Document, len(x))
		for k, item := range x {
			out[k] = cloneValue(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cloneValue(item)
		}
		return out
	case []string:
		return append([]string(nil), x...)
	default:
		return v
	}
}

// ShallowCopy returns a new top-level map sharing nested values. Use it when
// the result is only read (for example JSON serialization or CEL evaluation).
func (d Document) ShallowCopy() Document {
	if d == nil {
		return nil
	}
	out := make(Document, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

// Decode parses JSON into a Document using json.Number for all numbers.
// Decoders are pooled because listing many documents is the hottest path in
// the server.
func Decode(raw []byte) (Document, error) {
	buf := decodePool.Get().(*decodeBuffer)
	buf.reader.Reset(raw)
	var d Document
	err := buf.decoder.Decode(&d)
	decodePool.Put(buf)
	if err != nil {
		return nil, err
	}
	return d, nil
}

type decodeBuffer struct {
	reader  *bytes.Reader
	decoder *json.Decoder
}

var decodePool = sync.Pool{
	New: func() any {
		r := bytes.NewReader(nil)
		dec := json.NewDecoder(r)
		dec.UseNumber()
		return &decodeBuffer{reader: r, decoder: dec}
	},
}

// Encode serializes a document to JSON.
func (d Document) Encode() ([]byte, error) { return json.Marshal(d) }

var pathSegment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidPath reports whether p is a valid dot-separated field path.
func ValidPath(p string) bool {
	if p == "" {
		return false
	}
	for _, seg := range strings.Split(p, ".") {
		if !pathSegment.MatchString(seg) {
			return false
		}
	}
	return true
}

// GetPath resolves a dot-separated path inside a document. Missing parts
// return nil. Only maps are traversed; array indexes are not supported in
// paths.
func GetPath(d Document, path string) any {
	if path == "" {
		return nil
	}
	if !strings.Contains(path, ".") {
		return d[path]
	}
	return GetPathSegments(d, strings.Split(path, "."))
}

// GetPathSegments resolves an already split path. It avoids re-splitting the
// path for every document when evaluating filter and sort expressions.
func GetPathSegments(d Document, path []string) any {
	if len(path) == 0 {
		return nil
	}
	if len(path) == 1 {
		return d[path[0]]
	}
	var cur any = map[string]any(d)
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			if dm, ok := cur.(Document); ok {
				m = map[string]any(dm)
			} else {
				return nil
			}
		}
		cur = m[seg]
	}
	return cur
}

// GetPathAny resolves a path in a generic map.
func GetPathAny(m map[string]any, path string) any {
	var cur any = m
	for _, seg := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[seg]
	}
	return cur
}

// SetPath sets a dot-separated path, creating intermediate maps as needed.
func SetPath(d Document, path string, value any) {
	parts := strings.Split(path, ".")
	cur := map[string]any(d)
	for i, seg := range parts[:len(parts)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
		_ = i
	}
	cur[parts[len(parts)-1]] = value
}

// DeletePath removes the value at a dot-separated path, if present.
func DeletePath(d Document, path string) {
	parts := strings.Split(path, ".")
	cur := map[string]any(d)
	for _, seg := range parts[:len(parts)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
}

// HiddenFields must never be returned to API clients.
var HiddenFields = map[string]struct{}{
	FieldPasswordHash: {},
	FieldTokenKey:     {},
}

// Sanitize returns a deep copy without secret fields. It is safe to use on
// documents that came from the auth collection.
func (d Document) Sanitize() Document {
	if d == nil {
		return Document{}
	}
	out := make(Document, len(d))
	for k, v := range d {
		if _, hidden := HiddenFields[k]; hidden {
			continue
		}
		out[k] = cloneValue(v)
	}
	return out
}

// SanitizeShallow returns a top-level copy without secret fields. Nested
// values are shared, so the result must only be read. This is the fast path
// used for JSON responses and rule evaluation.
func (d Document) SanitizeShallow() Document {
	if d == nil {
		return nil
	}
	out := make(Document, len(d))
	for k, v := range d {
		if _, hidden := HiddenFields[k]; hidden {
			continue
		}
		out[k] = v
	}
	return out
}

// SanitizeView returns d itself when it has no hidden fields, otherwise a
// top-level sanitized copy. It avoids a copy on the common path of ordinary
// documents. The result must not be mutated.
func (d Document) SanitizeView() Document {
	if d == nil {
		return nil
	}
	for k := range HiddenFields {
		if _, ok := d[k]; ok {
			return d.SanitizeShallow()
		}
	}
	return d
}

// SetTimestamps initializes or updates system timestamps.
func (d Document) SetTimestamps(now time.Time, create bool) {
	ts := FormatTime(now)
	if create {
		d[FieldCreated] = ts
		d[FieldUpdated] = ts
		return
	}
	if _, ok := d[FieldCreated].(string); !ok {
		d[FieldCreated] = ts
	}
	d[FieldUpdated] = ts
}

// EnsureIdentity fills a missing id with a new ULID.
func (d Document) EnsureIdentity() error {
	if id, ok := d[FieldID].(string); ok && id != "" {
		if !ValidID(id) {
			return fmt.Errorf("invalid id: %q is not a ULID", id)
		}
		return nil
	}
	d[FieldID] = NewID()
	return nil
}
