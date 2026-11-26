package query

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/doketfire/doketfire/internal/codec"
	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/storage"
)

var (
	// ErrNoIndex is returned in strict mode when no usable index exists.
	ErrNoIndex = errors.New("no usable index for this query (strict index mode)")
	// ErrSortLimit is returned when a non-indexed sort would need too many documents in memory.
	ErrSortLimit = errors.New("too many documents to sort in memory; add an index or narrow the filter")
	// ErrInvalidCursor is returned for malformed or incompatible cursors.
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrLimit is returned when the requested page size is out of range.
	ErrLimit = errors.New("invalid limit")
)

// Limits configures query execution.
type Limits struct {
	DefaultLimit int
	MaxLimit     int
	MaxSortDocs  int
	Timeout      time.Duration
}

// DefaultLimits returns conservative defaults.
func DefaultLimits() Limits {
	return Limits{
		DefaultLimit: 50,
		MaxLimit:     500,
		MaxSortDocs:  10000,
		Timeout:      5 * time.Second,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.DefaultLimit <= 0 {
		l.DefaultLimit = d.DefaultLimit
	}
	if l.MaxLimit <= 0 {
		l.MaxLimit = d.MaxLimit
	}
	if l.MaxSortDocs <= 0 {
		l.MaxSortDocs = d.MaxSortDocs
	}
	if l.Timeout <= 0 {
		l.Timeout = d.Timeout
	}
	if l.DefaultLimit > l.MaxLimit {
		l.DefaultLimit = l.MaxLimit
	}
	return l
}

// Engine executes filter, sort and pagination plans against storage.
type Engine struct {
	Store         storage.Storage
	Limits        Limits
	StrictIndexes bool
}

// NewEngine creates an engine with sane defaults.
func NewEngine(store storage.Storage, limits Limits) *Engine {
	return &Engine{Store: store, Limits: limits.withDefaults()}
}

// ListOptions are the inputs for a list operation.
type ListOptions struct {
	Filter string
	Sort   string
	Limit  int
	Cursor string
	Fields []string

	// Auth is exposed to filters as @auth.* variables.
	Auth map[string]any

	// Authorize is applied after the filter and before pagination. Returning
	// false hides the document. It is used for collection security rules.
	Authorize func(collections.Document) (bool, error)

	// Include are dot-separated field paths to resolve from other collections.
	// Ejemplo: ["author.name", "project.title"]
	Include []string

	// IncludeAuthorize, when set, is called for every document resolved by an
	// include. It receives the target collection definition and document so the
	// caller can apply that collection's "view" rule. Returning false omits the
	// included field (sets it to nil) instead of leaking the data.
	IncludeAuthorize func(col string, def *collections.CollectionDef, doc collections.Document) (bool, error)
}

// ListResult is a page of documents.
type ListResult struct {
	Items      []collections.Document `json:"items"`
	NextCursor string                 `json:"nextCursor"`
}

type plan struct {
	index      *collections.Index
	keyRange   storage.KeyRange
	reverse    bool
	memorySort bool
	sort       []SortField
}

// List executes a query.
func (e *Engine) List(ctx context.Context, collection string, opts ListOptions) (*ListResult, error) {
	if e.Store == nil {
		return nil, errors.New("query: storage is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limits := e.Limits.withDefaults()
	limit := opts.Limit
	if limit == 0 {
		limit = limits.DefaultLimit
	}
	if limit < 0 || limit > limits.MaxLimit {
		return nil, fmt.Errorf("%w: must be between 1 and %d", ErrLimit, limits.MaxLimit)
	}
	filter, err := Parse(opts.Filter)
	if err != nil {
		return nil, err
	}
	sortFields, err := ParseSort(opts.Sort)
	if err != nil {
		return nil, err
	}
	cursor, err := decodeCursor(opts.Cursor)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}

	if limits.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limits.Timeout)
		defer cancel()
	}

	var result ListResult
	err = e.Store.View(func(tx storage.ReadTx) error {
		def, err := tx.Collection(collection)
		if err != nil {
			return err
		}
		ctxEval := &EvalContext{Now: time.Now(), Auth: opts.Auth}
		pl, err := e.plan(def, filter, sortFields, ctxEval)
		if err != nil {
			return err
		}

		var it storage.Iterator
		switch {
		case pl.memorySort:
			if cursor != nil && cursor.Type != "sort" {
				return fmt.Errorf("%w: expected a sort cursor", ErrInvalidCursor)
			}
			if pl.index != nil {
				it = tx.ScanIndex(collection, pl.index.Name, pl.keyRange, storage.ScanOpts{Reverse: pl.reverse})
			} else {
				it = tx.ScanDocs(collection, storage.ScanOpts{Reverse: pl.reverse})
			}
		case pl.index != nil:
			if cursor != nil && cursor.Type != "idx" {
				return fmt.Errorf("%w: expected an index cursor", ErrInvalidCursor)
			}
			var after []byte
			if cursor != nil {
				after = cursor.Key
			}
			it = tx.ScanIndex(collection, pl.index.Name, pl.keyRange, storage.ScanOpts{Reverse: pl.reverse, After: after})
		default:
			if cursor != nil && cursor.Type != "id" {
				return fmt.Errorf("%w: expected a document cursor", ErrInvalidCursor)
			}
			var after []byte
			if cursor != nil {
				after = cursor.Key
			}
			it = tx.ScanDocs(collection, storage.ScanOpts{Reverse: pl.reverse, After: after})
		}
		defer it.Close()

		var items []collections.Document
		if pl.memorySort {
			dedupe := pl.index != nil && !pl.index.Unique
			items, result.NextCursor, err = executeSorted(ctx, it, filter, ctxEval, opts.Authorize, pl.sort, limit, limits.MaxSortDocs, cursor, dedupe)
		} else {
			items, result.NextCursor, err = executeNatural(ctx, it, filter, ctxEval, opts.Authorize, limit, pl)
		}
		if err != nil {
			return err
		}
		// Conserva los documentos originales: si se combinan ?fields e ?include,
		// la proyección puede recortar el campo de referencia, así que los
		// includes se resuelven desde el documento sin proyectar.
		var originals []collections.Document
		if len(opts.Include) > 0 {
			originals = items
		}
		if len(opts.Fields) > 0 {
			projected := make([]collections.Document, len(items))
			for i, d := range items {
				projected[i] = Project(d, opts.Fields)
			}
			items = projected
		}
		if len(opts.Include) > 0 {
			includeItems := make([]collections.Document, len(items))
			for i, d := range items {
				includeItems[i] = resolveInclude(d, originals[i], opts.Include, e.Store, opts.IncludeAuthorize)
			}
			items = includeItems
		}
		if items == nil {
			items = []collections.Document{}
		}
		result.Items = items
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (e *Engine) plan(def *collections.CollectionDef, filter Node, sortFields []SortField, evalCtx *EvalContext) (*plan, error) {
	pl := &plan{sort: sortFields}
	comparisons := flattenAnd(filter)

	// Sorting by id uses the primary key order directly.
	if len(sortFields) == 1 && sortFields[0].Path == collections.FieldID {
		pl.reverse = sortFields[0].Desc
		return pl, nil
	}

	// Prefer an index matching the requested sort to avoid in-memory sorting.
	if len(sortFields) == 1 {
		if idx, ok := def.IndexForField(sortFields[0].Path, true); ok {
			pl.index = idx
			pl.reverse = sortFields[0].Desc
			pl.keyRange = rangeForIndex(idx, comparisons, evalCtx)
			return pl, nil
		}
	}

	idx := chooseIndex(def, comparisons)
	if idx != nil {
		pl.index = idx
		pl.keyRange = rangeForIndex(idx, comparisons, evalCtx)
		if len(sortFields) == 0 || (len(sortFields) == 1 && sortFields[0].Path == idx.Field) {
			if len(sortFields) == 1 {
				pl.reverse = sortFields[0].Desc
			}
			return pl, nil
		}
		pl.memorySort = true
		return pl, nil
	}

	if e.StrictIndexes {
		return nil, ErrNoIndex
	}
	if len(sortFields) > 0 {
		pl.memorySort = true
	}
	return pl, nil
}

func chooseIndex(def *collections.CollectionDef, comparisons []*Comparison) *collections.Index {
	var best *collections.Index
	bestScore := 0
	for _, c := range comparisons {
		var score int
		switch c.Op {
		case "=":
			score = 100
		case "~":
			score = 80
		case ">", ">=", "<", "<=":
			score = 50
		default:
			continue
		}
		path := joinPath(c.Path)
		idx, ok := def.IndexForField(path, true)
		if !ok {
			continue
		}
		if c.Op == "~" && !idx.Fulltext {
			continue
		}
		if idx.Unique {
			score += 10
		}
		if score > bestScore || (score == bestScore && best != nil && idx.Name < best.Name) {
			best = idx
			bestScore = score
		}
	}
	return best
}

func rangeForIndex(idx *collections.Index, comparisons []*Comparison, ctx *EvalContext) storage.KeyRange {
	var lower, upper []byte
	for _, c := range comparisons {
		if joinPath(c.Path) != idx.Field {
			continue
		}
		if c.Op == "in" || c.Op == "contains" || c.Op == "!=" {
			continue
		}
		expected := resolveValue(c.Value, ctx)
		if c.Op == "~" {
			if !idx.Fulltext {
				continue
			}
			text, _ := expected.(string)
			tokens := collections.Tokenize(text)
			if len(tokens) == 0 {
				continue
			}
			enc, err := codec.EncodeIndexValue(tokens[0])
			if err != nil {
				continue
			}
			lo := enc
			hi := codec.PrefixSuccessor(enc)
			if len(lo) > 0 && (len(lower) == 0 || bytes.Compare(lo, lower) > 0) {
				lower = append([]byte(nil), lo...)
			}
			if len(hi) > 0 && (len(upper) == 0 || bytes.Compare(hi, upper) < 0) {
				upper = append([]byte(nil), hi...)
			}
			continue
		}
		enc, err := codec.EncodeIndexValue(expected)
		if err != nil {
			continue
		}
		var lo, hi []byte
		switch c.Op {
		case "=":
			lo = enc
			hi = codec.PrefixSuccessor(enc)
		case ">":
			lo = codec.PrefixSuccessor(enc)
		case ">=":
			lo = enc
		case "<":
			hi = enc
		case "<=":
			hi = codec.PrefixSuccessor(enc)
		}
		if len(lo) > 0 && (len(lower) == 0 || bytes.Compare(lo, lower) > 0) {
			lower = append([]byte(nil), lo...)
		}
		if len(hi) > 0 && (len(upper) == 0 || bytes.Compare(hi, upper) < 0) {
			upper = append([]byte(nil), hi...)
		}
	}
	return storage.KeyRange{Start: lower, End: upper}
}

func flattenAnd(n Node) []*Comparison {
	var out []*Comparison
	var walk func(Node)
	walk = func(node Node) {
		switch x := node.(type) {
		case *Logical:
			if x.Op == "&&" {
				walk(x.Left)
				walk(x.Right)
			}
		case *Comparison:
			out = append(out, x)
		}
	}
	walk(n)
	return out
}

func joinPath(path []string) string {
	out := ""
	for i, p := range path {
		if i > 0 {
			out += "."
		}
		out += p
	}
	return out
}

func executeNatural(
	ctx context.Context,
	it storage.Iterator,
	filter Node,
	evalCtx *EvalContext,
	authorize func(collections.Document) (bool, error),
	limit int,
	pl *plan,
) ([]collections.Document, string, error) {
	out := make([]collections.Document, 0, limit+1)
	// Document scans and unique indexes cannot yield the same document twice.
	// Only non-unique (possibly multi-key) index scans need deduplication.
	var seen map[string]struct{}
	if pl.index != nil && !pl.index.Unique {
		seen = make(map[string]struct{})
	}
	var lastKey []byte
	steps := 0
	for it.Next() {
		if steps&255 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
		}
		steps++
		doc := it.Doc()
		if seen != nil {
			id := doc.ID()
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
		}
		ok, err := Eval(filter, doc, evalCtx)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		if authorize != nil {
			ok, err := authorize(doc)
			if err != nil {
				return nil, "", err
			}
			if !ok {
				continue
			}
		}
		out = append(out, doc)
		if len(out) == limit {
			// Remember the boundary key while the cursor is still valid; it is
			// only needed if another match proves there is a next page.
			lastKey = append(lastKey[:0], it.Key()...)
		}
		if len(out) > limit {
			break
		}
	}
	if err := it.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > limit {
		out = out[:limit]
		cursorType := "id"
		if pl.index != nil {
			cursorType = "idx"
		}
		next = encodeCursor(cursorData{Type: cursorType, Key: lastKey})
	}
	return out, next, nil
}

type sortedCandidate struct {
	doc    collections.Document
	values []any
}

func executeSorted(
	ctx context.Context,
	it storage.Iterator,
	filter Node,
	evalCtx *EvalContext,
	authorize func(collections.Document) (bool, error),
	sortFields []SortField,
	limit, maxDocs int,
	cursor *cursorData,
	dedupe bool,
) ([]collections.Document, string, error) {
	candidates := make([]sortedCandidate, 0, minInt(limit*2, maxDocs))
	var seen map[string]struct{}
	if dedupe {
		seen = make(map[string]struct{})
	}
	steps := 0
	for it.Next() {
		if steps&255 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
		}
		steps++
		doc := it.Doc()
		if seen != nil {
			id := doc.ID()
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
		}
		ok, err := Eval(filter, doc, evalCtx)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		if authorize != nil {
			ok, err := authorize(doc)
			if err != nil {
				return nil, "", err
			}
			if !ok {
				continue
			}
		}
		if len(candidates) >= maxDocs {
			return nil, "", ErrSortLimit
		}
		candidates = append(candidates, sortedCandidate{doc: doc, values: SortValues(doc, sortFields)})
	}
	if err := it.Err(); err != nil {
		return nil, "", err
	}

	sort.Slice(candidates, func(i, j int) bool {
		return compareCandidates(candidates[i], candidates[j], sortFields) < 0
	})

	if cursor != nil {
		if len(cursor.Values) != len(sortFields) {
			return nil, "", fmt.Errorf("%w: sort values do not match the current sort", ErrInvalidCursor)
		}
		filtered := candidates[:0]
		for _, c := range candidates {
			if compareCandidateCursor(c, sortFields, cursor) > 0 {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}

	var next string
	if len(candidates) > limit {
		last := candidates[limit-1]
		candidates = candidates[:limit]
		next = encodeCursor(cursorData{
			Type:   "sort",
			ID:     last.doc.ID(),
			Values: last.values,
		})
	}
	out := make([]collections.Document, len(candidates))
	for i, c := range candidates {
		out[i] = c.doc
	}
	return out, next, nil
}

func compareCandidates(a, b sortedCandidate, sortFields []SortField) int {
	for i, f := range sortFields {
		cmp := codec.Compare(a.values[i], b.values[i])
		if f.Desc {
			cmp = -cmp
		}
		if cmp != 0 {
			return cmp
		}
	}
	return stringsCompare(a.doc.ID(), b.doc.ID())
}

func compareCandidateCursor(c sortedCandidate, sortFields []SortField, cursor *cursorData) int {
	for i, f := range sortFields {
		cmp := codec.Compare(c.values[i], cursor.Values[i])
		if f.Desc {
			cmp = -cmp
		}
		if cmp != 0 {
			return cmp
		}
	}
	return stringsCompare(c.doc.ID(), cursor.ID)
}

func stringsCompare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// includeSystemFields are fields that must never be exposed through ?include=.
// They are either internal identifiers, server-managed timestamps or auth
// credentials, and copying them into a related collection would leak data that
// sanitizeRecord normally strips from the response.
var includeSystemFields = map[string]bool{
	collections.FieldID:           true,
	collections.FieldCreated:      true,
	collections.FieldUpdated:      true,
	collections.FieldPasswordHash: true,
	collections.FieldTokenKey:     true,
	collections.FieldEmail:        true,
	collections.FieldVerified:     true,
}

// resolveInclude resuelve campos de otras colecciones basándose en el valor
// de un campo de referencia en el documento actual.
//
// Formato: "reference.field", donde "reference" es a la vez el nombre de la
// colección destino y el campo del documento actual que guarda el ID referenciado.
// Ejemplo: "author.name" → toma doc["author"] como ID, busca ese documento en la
// colección "author" y copia su campo "name" en la clave "author.name".
//
// Comportamiento:
//   - Los campos originales del documento se conservan.
//   - Todas las referencias se resuelven dentro de una única ReadTx por documento
//     (no una transacción por include), para no multiplicar el coste.
//   - Los campos de sistema (id/created/updated/passwordHash/tokenKey/email/
//     verified) nunca se exponen: la clave incluida queda a nil.
//   - Si el documento referenciado existe pero el callback de autorización lo
//     rechaza (regla "view" de la colección destino), la clave queda a nil.
//   - Si la referencia no existe o el campo no está, la clave queda a nil.
func resolveInclude(
	doc collections.Document,
	original collections.Document,
	include []string,
	store storage.Storage,
	authorize func(col string, def *collections.CollectionDef, doc collections.Document) (bool, error),
) collections.Document {
	out := doc.Clone()
	// Los includes con formato inválido o campo de sistema se dejan a nil sin
	// llegar a abrir la transacción.
	valid := make([]string, 0, len(include))
	seen := make(map[string]bool, len(include))
	for _, inc := range include {
		parts := strings.Split(inc, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		if includeSystemFields[parts[1]] {
			out[inc] = nil
			continue
		}
		if seen[inc] {
			continue
		}
		seen[inc] = true
		valid = append(valid, inc)
	}
	if len(valid) == 0 || store == nil {
		return out
	}

	// Una sola transacción de lectura resuelve todas las referencias del documento.
	_ = store.View(func(tx storage.ReadTx) error {
		for _, inc := range valid {
			parts := strings.Split(inc, ".")
			col, field := parts[0], parts[1]

			refStr, ok := collections.GetPath(original, col).(string)
			if !ok || refStr == "" {
				out[inc] = nil
				continue
			}
			def, err := tx.Collection(col)
			if err != nil {
				out[inc] = nil
				continue
			}
			refDoc, found, err := tx.GetDoc(col, refStr)
			if err != nil || !found {
				out[inc] = nil
				continue
			}
			if authorize != nil {
				allowed, err := authorize(col, def, refDoc)
				if err != nil || !allowed {
					out[inc] = nil
					continue
				}
			}
			if v := collections.GetPath(refDoc, field); v != nil {
				out[inc] = v
			} else {
				out[inc] = nil
			}
		}
		return nil
	})
	return out
}

// Project returns a copy of d containing only the requested dot-separated
// fields (and always id). Unknown fields are omitted.
func Project(d collections.Document, fields []string) collections.Document {
	out := collections.Document{collections.FieldID: d.ID()}
	for _, f := range fields {
		if f == collections.FieldID {
			continue
		}
		v := collections.GetPath(d, f)
		if v != nil {
			collections.SetPath(out, f, v)
		}
	}
	return out
}
