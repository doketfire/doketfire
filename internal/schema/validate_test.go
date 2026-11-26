package schema

import (
	"strings"
	"testing"
)

func validSchema() *Schema {
	users := &Table{
		Name: "users",
		Fields: []*Field{
			{Name: "email", Type: TypeText, Required: true, Unique: true},
			{Name: "name", Type: TypeText},
		},
	}
	posts := &Table{
		Name: "posts",
		Fields: []*Field{
			{Name: "title", Type: TypeText, Required: true, Min: floatPtr(1), Max: floatPtr(200)},
			{Name: "author", Type: TypeText, Ref: &Ref{Table: "users", Field: "id", OnDelete: "CASCADE"}},
			{Name: "status", Type: TypeEnum, Enum: []string{"draft", "published"}},
			{Name: "views", Type: TypeInteger, Default: 0},
		},
		Indexes: []Index{
			{Name: "posts_author_idx", Fields: []IndexField{{Name: "author"}}},
		},
		Permissions: map[Action]string{ActionList: "true", ActionRead: "true"},
		Realtime:    true,
	}
	return &Schema{Version: 1, Project: "blog", Tables: []*Table{posts, users}}
}

func floatPtr(v float64) *float64 { return &v }

func TestValidIdent(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"posts", true},
		{"post_2", true},
		{"a", true},
		{"", false},
		{"Posts", false},
		{"2posts", false},
		{"_dk_users", false},
		{strings.Repeat("a", 63), true},
		{strings.Repeat("a", 64), false},
	}
	for _, tt := range tests {
		if got := ValidIdent(tt.name); got != tt.want {
			t.Errorf("ValidIdent(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSchemaValidateValid(t *testing.T) {
	s := validSchema()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
	if _, ok := s.Table("posts"); !ok {
		t.Fatal("posts table not indexed")
	}
	if _, ok := s.Table("missing"); ok {
		t.Fatal("missing table found")
	}
	posts, _ := s.Table("posts")
	if _, ok := posts.Field("title"); !ok {
		t.Fatal("title field not indexed")
	}
	if _, ok := posts.Field("missing"); ok {
		t.Fatal("missing field found")
	}
	if len(s.Tables) != 2 || s.Tables[0].Name != "posts" {
		t.Fatalf("field order changed: %v", s.Tables)
	}
}

func TestSchemaValidateSystemRef(t *testing.T) {
	s := validSchema()
	posts := s.Tables[0]
	posts.Fields = append(posts.Fields, &Field{
		Name: "owner",
		Type: TypeText,
		Ref:  &Ref{Table: "_dk_users", Field: "id", OnDelete: "SET NULL"},
	})
	if err := s.Validate(); err != nil {
		t.Fatalf("system reference rejected: %v", err)
	}
}

func TestSchemaValidateNil(t *testing.T) {
	var s *Schema
	if err := s.Validate(); err == nil {
		t.Fatal("expected error for nil schema")
	}
}

func TestSchemaValidateErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Schema)
		want   string
	}{
		{"version", func(s *Schema) { s.Version = 2 }, "unsupported schema version"},
		{"invalid table", func(s *Schema) { s.Tables[0].Name = "Posts" }, "invalid table name"},
		{"reserved table", func(s *Schema) { s.Tables[0].Name = "version" }, "reserved table name"},
		{"duplicate table", func(s *Schema) { s.Tables = append(s.Tables, s.Tables[0]) }, "duplicate table"},
		{"invalid field", func(s *Schema) { s.Tables[0].Fields[0].Name = "Title" }, "invalid field name"},
		{"reserved field", func(s *Schema) { s.Tables[0].Fields[0].Name = "created_at" }, "reserved field name"},
		{"duplicate field", func(s *Schema) { s.Tables[0].Fields[1].Name = "title" }, "duplicate field"},
		{"invalid type", func(s *Schema) { s.Tables[0].Fields[0].Type = "varchar" }, "invalid type"},
		{"enum empty", func(s *Schema) { s.Tables[0].Fields[2].Enum = nil }, "enum requires values"},
		{"enum on text", func(s *Schema) { s.Tables[0].Fields[0].Enum = []string{"x"} }, "enum values require type"},
		{"min greater", func(s *Schema) {
			s.Tables[0].Fields[0].Min = floatPtr(10)
			s.Tables[0].Fields[0].Max = floatPtr(1)
		}, "min cannot be greater"},
		{"negative text min", func(s *Schema) { s.Tables[0].Fields[0].Min = floatPtr(-1) }, "cannot be negative"},
		{"invalid index", func(s *Schema) { s.Tables[0].Indexes[0].Name = "Bad" }, "invalid index name"},
		{"index no fields", func(s *Schema) { s.Tables[0].Indexes[0].Fields = nil }, "expected 1 to 5 fields"},
		{"index too many", func(s *Schema) {
			idx := &s.Tables[0].Indexes[0]
			idx.Fields = make([]IndexField, 6)
			for i := range idx.Fields {
				idx.Fields[i] = IndexField{Name: "title"}
			}
		}, "expected 1 to 5 fields"},
		{"index unknown field", func(s *Schema) { s.Tables[0].Indexes[0].Fields[0].Name = "missing" }, "unknown field"},
		{"index duplicate field", func(s *Schema) {
			s.Tables[0].Indexes[0].Fields = []IndexField{{Name: "author"}, {Name: "author"}}
		}, "duplicate field"},
		{"duplicate index", func(s *Schema) {
			s.Tables[0].Indexes = append(s.Tables[0].Indexes, s.Tables[0].Indexes[0])
		}, "duplicate index"},
		{"invalid action", func(s *Schema) { s.Tables[0].Permissions["publish"] = "true" }, "invalid permission action"},
		{"unknown ref table", func(s *Schema) { s.Tables[0].Fields[1].Ref.Table = "missing" }, "references unknown table"},
		{"unknown ref field", func(s *Schema) { s.Tables[0].Fields[1].Ref.Field = "missing" }, "references unknown field"},
		{"bad on delete", func(s *Schema) { s.Tables[0].Fields[1].Ref.OnDelete = "NO ACTION" }, "invalid on_delete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSchema()
			tt.mutate(s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}
