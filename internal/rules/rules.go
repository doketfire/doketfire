// Package rules compiles and evaluates collection security rules written in
// Google CEL. Programs are cached by expression text and compiled once.
package rules

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"cel.dev/cel-go/cel"

	"github.com/doketfire/doketfire/internal/collections"
)

// RuleError describes an invalid rule expression.
type RuleError struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

func (e *RuleError) Error() string {
	return fmt.Sprintf("invalid rule %q: %s", e.Rule, e.Message)
}

// Engine owns the CEL environment and the compiled program cache.
type Engine struct {
	env   *cel.Env
	cache sync.Map // expression -> cel.Program
}

// New creates a rule engine.
func New() (*Engine, error) {
	env, err := cel.NewEnv(
		cel.Variable("request", cel.DynType),
		cel.Variable("resource", cel.DynType),
		cel.Variable("now", cel.TimestampType),
	)
	if err != nil {
		return nil, fmt.Errorf("create CEL environment: %w", err)
	}
	return &Engine{env: env}, nil
}

// Validate compiles a rule and checks that it returns a boolean.
func (e *Engine) Validate(rule string) error {
	rule = strings.TrimSpace(rule)
	if rule == "" || rule == "true" || rule == "false" || rule == "null" {
		return nil
	}
	_, err := e.program(rule)
	return err
}

// ValidateDefinition validates every rule in a collection.
func (e *Engine) ValidateDefinition(def *collections.CollectionDef) error {
	for op, rule := range def.Rules {
		if err := e.Validate(rule); err != nil {
			return fmt.Errorf("rule %q: %w", op, err)
		}
	}
	return nil
}

// Context carries the CEL activation values.
type Context struct {
	Operation string
	Identity  map[string]any
	Resource  map[string]any
	Body      map[string]any
	Query     map[string]any
	Now       time.Time
}

// mapOrNil converts a nil map to an untyped nil so CEL sees null instead of
// an empty object. This makes `request.auth != null` behave as expected for
// anonymous requests.
func mapOrNil(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

// Allowed reports whether the operation is permitted.
//
// Semantics:
//   - superusers bypass all rules;
//   - a missing rule is locked;
//   - an empty rule or "true" is public;
//   - "false" and "null" are locked;
//   - any other expression must evaluate to true.
func (e *Engine) Allowed(def *collections.CollectionDef, op string, isAdmin bool, ctx Context) (bool, error) {
	if isAdmin {
		return true, nil
	}
	if def == nil {
		return false, nil
	}
	rule, ok := def.RuleSet(op)
	if !ok {
		return false, nil
	}
	rule = strings.TrimSpace(rule)
	switch rule {
	case "":
		return true, nil
	case "true":
		return true, nil
	case "false", "null":
		return false, nil
	case "request.auth != null":
		// Fast path for the most common rule; avoids a CEL evaluation per
		// document when listing auth collections.
		return ctx.Identity != nil, nil
	case "request.auth != null && resource.id == request.auth.id":
		// Default view/list rule for auth collections.
		if ctx.Identity == nil || ctx.Resource == nil {
			return false, nil
		}
		rid, ok1 := ctx.Resource[collections.FieldID].(string)
		uid, ok2 := ctx.Identity[collections.FieldID].(string)
		return ok1 && ok2 && rid == uid, nil
	}
	prg, err := e.program(rule)
	if err != nil {
		return false, err
	}
	if ctx.Now.IsZero() {
		ctx.Now = time.Now()
	}
	activation := map[string]any{
		"request": map[string]any{
			"auth":   mapOrNil(ctx.Identity),
			"method": ctx.Operation,
			"body":   mapOrNil(ctx.Body),
			"query":  mapOrNil(ctx.Query),
		},
		"resource": mapOrNil(ctx.Resource),
		"now":      ctx.Now,
	}
	out, _, err := prg.Eval(activation)
	if err != nil {
		return false, fmt.Errorf("evaluate rule for %s: %w", op, err)
	}
	allowed, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("rule for %s did not return a boolean", op)
	}
	return allowed, nil
}

// CheckExpression evaluates a standalone expression, used by the admin rule
// tester.
func (e *Engine) CheckExpression(expr string, ctx Context) (bool, error) {
	expr = strings.TrimSpace(expr)
	switch expr {
	case "", "true":
		return true, nil
	case "false", "null":
		return false, nil
	}
	prg, err := e.program(expr)
	if err != nil {
		return false, err
	}
	if ctx.Now.IsZero() {
		ctx.Now = time.Now()
	}
	out, _, err := prg.Eval(map[string]any{
		"request": map[string]any{
			"auth":   mapOrNil(ctx.Identity),
			"method": ctx.Operation,
			"body":   mapOrNil(ctx.Body),
			"query":  mapOrNil(ctx.Query),
		},
		"resource": mapOrNil(ctx.Resource),
		"now":      ctx.Now,
	})
	if err != nil {
		return false, err
	}
	allowed, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("expression did not return a boolean")
	}
	return allowed, nil
}

func (e *Engine) program(rule string) (cel.Program, error) {
	if v, ok := e.cache.Load(rule); ok {
		return v.(cel.Program), nil
	}
	ast, iss := e.env.Compile(rule)
	if iss != nil && iss.Err() != nil {
		return nil, &RuleError{Rule: rule, Message: iss.Err().Error()}
	}
	if ast.OutputType() != cel.BoolType {
		return nil, &RuleError{Rule: rule, Message: "expression must return a boolean"}
	}
	prg, err := e.env.Program(ast)
	if err != nil {
		return nil, &RuleError{Rule: rule, Message: err.Error()}
	}
	actual, _ := e.cache.LoadOrStore(rule, prg)
	return actual.(cel.Program), nil
}

// DocumentMap converts a document to a plain map for CEL.
func DocumentMap(d collections.Document) map[string]any {
	if d == nil {
		return nil
	}
	return map[string]any(d)
}
