package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/storage"
)

// Supported server-side aggregation operations.
const (
	AggregateOpCount = "count"
	AggregateOpSum   = "sum"
)

// AggregateOptions are the inputs for a server-side aggregation.
type AggregateOptions struct {
	// Filter is a CEL-style query expression evaluated against each document.
	Filter string
	// Op is the aggregation operation: "count" or "sum".
	Op string
	// Field is the numeric document field used by "sum".
	Field string
	// Auth is exposed to filters as @auth.* variables.
	Auth map[string]any
	// Authorize hides documents rejected by collection rules. It mirrors the
	// list authorization flow so aggregations respect the same permissions.
	Authorize func(collections.Document) (bool, error)
}

// Aggregate computes a count or a sum over the documents that match the filter.
// It replaces the client-side aggregation that Doketfire lacked at the product
// level while keeping the storage layer untouched.
func (e *Engine) Aggregate(ctx context.Context, collection string, opts AggregateOptions) (float64, error) {
	if e.Store == nil {
		return 0, errors.New("query: storage is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Op != AggregateOpCount && opts.Op != AggregateOpSum {
		return 0, fmt.Errorf("unsupported aggregate op %q", opts.Op)
	}
	if opts.Op == AggregateOpSum && strings.TrimSpace(opts.Field) == "" {
		return 0, errors.New("aggregate sum requires a field")
	}
	limits := e.Limits.withDefaults()
	if limits.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limits.Timeout)
		defer cancel()
	}
	var result float64
	err := e.Store.View(func(tx storage.ReadTx) error {
		def, err := tx.Collection(collection)
		if err != nil {
			return err
		}
		ctxEval := &EvalContext{Now: time.Now(), Auth: opts.Auth}
		filter, err := Parse(opts.Filter)
		if err != nil {
			return err
		}
		pl, err := e.plan(def, filter, nil, ctxEval)
		if err != nil {
			return err
		}
		var it storage.Iterator
		switch {
		case pl.index != nil:
			it = tx.ScanIndex(collection, pl.index.Name, pl.keyRange, storage.ScanOpts{Reverse: pl.reverse})
		default:
			it = tx.ScanDocs(collection, storage.ScanOpts{Reverse: pl.reverse})
		}
		defer it.Close()
		var seen map[string]struct{}
		if pl.index != nil && !pl.index.Unique {
			seen = make(map[string]struct{})
		}
		steps := 0
		for it.Next() {
			if steps&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
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
			ok, err := Eval(filter, doc, ctxEval)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if opts.Authorize != nil {
				allowed, err := opts.Authorize(doc)
				if err != nil {
					return err
				}
				if !allowed {
					continue
				}
			}
			result += aggregateValue(doc, opts.Op, opts.Field)
		}
		return it.Err()
	})
	if err != nil {
		return 0, err
	}
	return result, nil
}

// aggregateValue accumulates the value of a document for the requested
// operation. Count always adds one; sum adds the numeric value of the field.
func aggregateValue(doc collections.Document, op string, field string) float64 {
	if op == AggregateOpCount {
		return 1
	}
	v := collections.GetPath(doc, field)
	f := toFloat(v)
	if f == nil {
		return 0
	}
	return *f
}

// toFloat converts the common numeric representations used by documents
// (float64, json.Number, integers and numeric strings) into float64.
func toFloat(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case float32:
		f := float64(x)
		return &f
	case int:
		f := float64(x)
		return &f
	case int32:
		f := float64(x)
		return &f
	case int64:
		f := float64(x)
		return &f
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return nil
		}
		return &f
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return nil
		}
		return &f
	}
	return nil
}
