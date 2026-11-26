// Package query implements the Doketfire filter language: lexer, parser, AST,
// evaluator and index-aware execution engine.
package query

import (
	"fmt"
	"strings"
)

// Node is a filter AST node.
type Node interface {
	String() string
	node()
}

// Logical combines two expressions with "&&" or "||".
type Logical struct {
	Op    string // && or ||
	Left  Node
	Right Node
}

func (*Logical) node() {}
func (n *Logical) String() string {
	return fmt.Sprintf("(%s %s %s)", n.Left, n.Op, n.Right)
}

// Not negates an expression.
type Not struct{ X Node }

func (*Not) node()            {}
func (n *Not) String() string { return "!" + n.X.String() }

// Comparison is path <op> value.
type Comparison struct {
	Path  []string
	Op    string
	Value Value

	// Cached at parse time. pathStr avoids re-joining the path for every
	// document; staticValue holds values that do not depend on @now/@auth.
	pathStr     string
	staticValue any
	isStatic    bool
}

func (*Comparison) node() {}
func (n *Comparison) String() string {
	return strings.Join(n.Path, ".") + " " + n.Op + " " + n.Value.String()
}

// ValueKind identifies the kind of a filter value.
type ValueKind int

const (
	ValueNull ValueKind = iota
	ValueString
	ValueNumber
	ValueBool
	ValueVariable
	ValueList
)

// Value is a scalar, variable or list in a comparison.
type Value struct {
	Kind  ValueKind
	Str   string
	Num   float64
	Bool  bool
	Vars  []string // @now -> ["now"], @auth.id -> ["auth","id"]
	Items []Value
}

func (v Value) String() string {
	switch v.Kind {
	case ValueString:
		return fmt.Sprintf("%q", v.Str)
	case ValueNumber:
		return fmt.Sprintf("%g", v.Num)
	case ValueBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case ValueNull:
		return "null"
	case ValueVariable:
		return "@" + strings.Join(v.Vars, ".")
	case ValueList:
		parts := make([]string, len(v.Items))
		for i, it := range v.Items {
			parts[i] = it.String()
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return "<invalid>"
	}
}
