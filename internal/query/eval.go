package query

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/doketfire/doketfire/internal/codec"
	"github.com/doketfire/doketfire/internal/collections"
)

// EvalContext provides dynamic values available to filters.
type EvalContext struct {
	Now  time.Time
	Auth map[string]any
}

func (c *EvalContext) nowString() string {
	t := c.Now
	if t.IsZero() {
		t = time.Now()
	}
	return collections.FormatTime(t)
}

func (c *EvalContext) authValue(path []string) any {
	if c == nil || c.Auth == nil || len(path) == 0 || path[0] != "auth" {
		return nil
	}
	if len(path) == 1 {
		return c.Auth
	}
	var cur any = c.Auth
	for _, seg := range path[1:] {
		m, ok := cur.(map[string]any)
		if !ok {
			if d, ok := cur.(collections.Document); ok {
				m = map[string]any(d)
			} else {
				return nil
			}
		}
		cur = m[seg]
	}
	return cur
}

// Eval evaluates a filter against a document. A nil filter matches everything.
func Eval(n Node, doc collections.Document, ctx *EvalContext) (bool, error) {
	if n == nil {
		return true, nil
	}
	switch x := n.(type) {
	case *Logical:
		left, err := Eval(x.Left, doc, ctx)
		if err != nil {
			return false, err
		}
		if x.Op == "&&" {
			if !left {
				return false, nil
			}
			return Eval(x.Right, doc, ctx)
		}
		if left {
			return true, nil
		}
		return Eval(x.Right, doc, ctx)
	case *Not:
		v, err := Eval(x.X, doc, ctx)
		if err != nil {
			return false, err
		}
		return !v, nil
	case *Comparison:
		return evalComparison(x, doc, ctx)
	default:
		return false, fmt.Errorf("query: unsupported AST node %T", n)
	}
}

func evalComparison(c *Comparison, doc collections.Document, ctx *EvalContext) (bool, error) {
	actual := collections.GetPathSegments(doc, c.Path)
	var expected any
	if c.isStatic {
		expected = c.staticValue
	} else {
		expected = resolveValue(c.Value, ctx)
	}
	switch c.Op {
	case "=":
		return equal(actual, expected), nil
	case "!=":
		return !equal(actual, expected), nil
	case ">", ">=", "<", "<=":
		cmp, ok := compareOrdered(actual, expected)
		if !ok {
			return false, nil
		}
		switch c.Op {
		case ">":
			return cmp > 0, nil
		case ">=":
			return cmp >= 0, nil
		case "<":
			return cmp < 0, nil
		case "<=":
			return cmp <= 0, nil
		}
	case "in":
		list, ok := expected.([]any)
		if !ok {
			return false, nil
		}
		if arr, ok := actual.([]any); ok {
			for _, el := range arr {
				for _, item := range list {
					if equal(el, item) {
						return true, nil
					}
				}
			}
			return false, nil
		}
		for _, item := range list {
			if equal(actual, item) {
				return true, nil
			}
		}
		return false, nil
	case "contains":
		switch v := actual.(type) {
		case string:
			s, ok := expected.(string)
			return ok && strings.Contains(v, s), nil
		case []any:
			for _, el := range v {
				if equal(el, expected) {
					return true, nil
				}
			}
			return false, nil
		case []string:
			s, ok := expected.(string)
			if !ok {
				return false, nil
			}
			for _, el := range v {
				if el == s {
					return true, nil
				}
			}
			return false, nil
		default:
			return false, nil
		}
	case "~":
		// Full-text matching: token based, case-insensitive, stopword aware.
		s, ok := actual.(string)
		needle, ok2 := expected.(string)
		if !ok || !ok2 {
			return false, nil
		}
		return collections.TextMatch(s, needle), nil
	default:
		return false, fmt.Errorf("query: unsupported operator %q", c.Op)
	}
	return false, nil
}

func resolveValue(v Value, ctx *EvalContext) any {
	switch v.Kind {
	case ValueNull:
		return nil
	case ValueString:
		return v.Str
	case ValueNumber:
		return v.Num
	case ValueBool:
		return v.Bool
	case ValueVariable:
		if len(v.Vars) == 0 {
			return nil
		}
		if v.Vars[0] == "now" {
			if ctx == nil {
				return collections.FormatTime(time.Now())
			}
			return ctx.nowString()
		}
		return ctx.authValue(v.Vars)
	case ValueList:
		out := make([]any, len(v.Items))
		for i, it := range v.Items {
			out[i] = resolveValue(it, ctx)
		}
		return out
	default:
		return nil
	}
}

func equal(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if af, ok := codec.Number(a); ok {
		if bf, ok := codec.Number(b); ok {
			return af == bf
		}
		return false
	}
	if as, ok := a.(string); ok {
		bs, ok := b.(string)
		return ok && as == bs
	}
	if ab, ok := a.(bool); ok {
		bb, ok := b.(bool)
		return ok && ab == bb
	}
	if as, ok := a.([]string); ok {
		bs, ok := b.([]string)
		if !ok {
			return false
		}
		return reflect.DeepEqual(as, bs)
	}
	return reflect.DeepEqual(a, b)
}

func compareOrdered(a, b any) (int, bool) {
	if af, ok := codec.Number(a); ok {
		bf, ok := codec.Number(b)
		if !ok {
			return 0, false
		}
		switch {
		case af < bf:
			return -1, true
		case af > bf:
			return 1, true
		default:
			return 0, true
		}
	}
	if as, ok := a.(string); ok {
		bs, ok := b.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(as, bs), true
	}
	if ab, ok := a.(bool); ok {
		bb, ok := b.(bool)
		if !ok {
			return 0, false
		}
		switch {
		case ab == bb:
			return 0, true
		case !ab:
			return -1, true
		default:
			return 1, true
		}
	}
	return 0, false
}
