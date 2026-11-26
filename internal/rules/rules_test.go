package rules

import (
	"testing"

	"github.com/doketfire/doketfire/internal/collections"
)

func mustEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func defWith(rules collections.Rules) *collections.CollectionDef {
	return &collections.CollectionDef{Name: "posts", Rules: rules}
}

func TestRuleSemantics(t *testing.T) {
	e := mustEngine(t)
	owner := map[string]any{"id": "u1", "role": "user"}
	admin := map[string]any{"id": "a1", "role": "admin"}
	resource := map[string]any{"id": "p1", "owner": "u1", "title": "hello"}
	body := map[string]any{"owner": "u1", "title": "hello"}

	cases := []struct {
		name    string
		def     *collections.CollectionDef
		op      string
		admin   bool
		ident   map[string]any
		res     map[string]any
		body    map[string]any
		want    bool
		wantErr bool
	}{
		{name: "missing rule is locked", def: defWith(collections.Rules{}), op: "list", want: false},
		{name: "empty rule is public", def: defWith(collections.Rules{"list": ""}), op: "list", want: true},
		{name: "true rule is public", def: defWith(collections.Rules{"view": "true"}), op: "view", want: true},
		{name: "false rule is locked", def: defWith(collections.Rules{"view": "false"}), op: "view", want: false},
		{name: "admin bypasses", def: defWith(collections.Rules{"view": "false"}), op: "view", admin: true, want: true},
		{
			name: "anonymous denied when auth required",
			def:  defWith(collections.Rules{"view": "request.auth != null"}),
			op:   "view", want: false,
		},
		{
			name: "owner can view",
			def:  defWith(collections.Rules{"view": "request.auth != null && resource.owner == request.auth.id"}),
			op:   "view", ident: owner, res: resource, want: true,
		},
		{
			name: "non-owner cannot view",
			def:  defWith(collections.Rules{"view": "request.auth != null && resource.owner == request.auth.id"}),
			op:   "view", ident: map[string]any{"id": "u2"}, res: resource, want: false,
		},
		{
			name: "create checks body",
			def:  defWith(collections.Rules{"create": "request.auth != null && request.body.owner == request.auth.id"}),
			op:   "create", ident: owner, body: body, want: true,
		},
		{
			name: "create rejects other owner",
			def:  defWith(collections.Rules{"create": "request.auth != null && request.body.owner == request.auth.id"}),
			op:   "create", ident: map[string]any{"id": "u2"}, body: body, want: false,
		},
		{
			name: "update cannot change owner",
			def:  defWith(collections.Rules{"update": "resource.owner == request.auth.id && !('owner' in request.body)"}),
			op:   "update", ident: owner, res: resource, body: map[string]any{"title": "x"}, want: true,
		},
		{
			name: "update rejects owner field",
			def:  defWith(collections.Rules{"update": "resource.owner == request.auth.id && !('owner' in request.body)"}),
			op:   "update", ident: owner, res: resource, body: body, want: false,
		},
		{
			name: "admin role rule",
			def:  defWith(collections.Rules{"delete": "request.auth != null && request.auth.role == 'admin'"}),
			op:   "delete", ident: admin, res: resource, want: true,
		},
		{
			name: "list rule evaluated per document",
			def:  defWith(collections.Rules{"list": "request.auth != null && resource.public == true"}),
			op:   "list", ident: owner, res: map[string]any{"public": true}, want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Allowed(tc.def, tc.op, tc.admin, Context{
				Operation: tc.op,
				Identity:  tc.ident,
				Resource:  tc.res,
				Body:      tc.body,
			})
			if tc.wantErr != (err != nil) {
				t.Fatalf("error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("allowed = %v, want %v (err=%v)", got, tc.want, err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	e := mustEngine(t)
	for _, rule := range []string{"", "true", "request.auth != null", "resource.n > 3 && request.auth.id == 'x'"} {
		if err := e.Validate(rule); err != nil {
			t.Errorf("Validate(%q): %v", rule, err)
		}
	}
	for _, rule := range []string{
		"request.auth ==",
		"1 + 1",
		"resource.n + 'x' == 1",
		"unknown.variable == 1",
	} {
		if err := e.Validate(rule); err == nil {
			t.Errorf("Validate(%q) should fail", rule)
		}
	}
	def := defWith(collections.Rules{"view": "resource.owner == request.auth.id"})
	if err := e.ValidateDefinition(def); err != nil {
		t.Fatal(err)
	}
	def.Rules["bad"] = "this is not valid"
	if err := e.ValidateDefinition(def); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestCheckExpression(t *testing.T) {
	e := mustEngine(t)
	ok, err := e.CheckExpression("request.auth.id == 'u1' && resource.public == true", Context{
		Identity: map[string]any{"id": "u1"},
		Resource: map[string]any{"public": true},
	})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
