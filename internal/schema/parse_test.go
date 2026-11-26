package schema

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const blogYAML = `
version: 1
project: blog

tables:
  profiles:
    fields:
      email: { type: text, required: true, unique: true }
    permissions:
      list: "true"
  posts:
    fields:
      title:     { type: text, required: true, min: 1, max: 200 }
      slug:      { type: text, required: true, unique: true, pattern: "^[a-z0-9-]+$" }
      body:      { type: text }
      published: { type: boolean, default: false }
      author_id: { type: uuid, references: users.id }
      status:    { type: enum, enum: [draft, published] }
    indexes:
      - { fields: [published, created_at] }
      - { name: posts_slug_idx, fields: [slug], unique: true, where: "published = 1" }
    permissions:
      list: "resource.published == true"
    realtime: true
`

func TestParseValid(t *testing.T) {
	s, err := Parse(strings.NewReader(blogYAML))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if s.Version != 1 || s.Project != "blog" {
		t.Fatalf("header = %d/%q", s.Version, s.Project)
	}
	if len(s.Tables) != 2 || s.Tables[0].Name != "posts" || s.Tables[1].Name != "profiles" {
		t.Fatalf("tables are not sorted by name: %v", tableNames(s))
	}

	posts, ok := s.Table("posts")
	if !ok {
		t.Fatal("posts table not indexed")
	}
	if got, want := fieldNames(posts), []string{"title", "slug", "body", "published", "author_id", "status"}; !equalStrings(got, want) {
		t.Fatalf("field order = %v, want %v", got, want)
	}

	title, _ := posts.Field("title")
	if title.Type != TypeText || !title.Required || title.Min == nil || *title.Min != 1 || title.Max == nil || *title.Max != 200 {
		t.Fatalf("title = %+v", title)
	}
	slug, _ := posts.Field("slug")
	if !slug.Unique || slug.Pattern != "^[a-z0-9-]+$" {
		t.Fatalf("slug = %+v", slug)
	}
	published, _ := posts.Field("published")
	if published.Type != TypeBoolean || published.Default != false {
		t.Fatalf("published = %+v", published)
	}
	author, _ := posts.Field("author_id")
	if author.Ref == nil {
		t.Fatal("author_id has no reference")
	}
	if author.Ref.Table != "_dk_users" || author.Ref.Field != "id" || author.Ref.OnDelete != "SET NULL" {
		t.Fatalf("reference = %+v", author.Ref)
	}
	status, _ := posts.Field("status")
	if status.Type != TypeEnum || !equalStrings(status.Enum, []string{"draft", "published"}) {
		t.Fatalf("status = %+v", status)
	}

	if len(posts.Indexes) != 2 {
		t.Fatalf("indexes = %+v", posts.Indexes)
	}
	first := posts.Indexes[0]
	if first.Name != "idx_posts_published_created_at" || first.Unique || first.Where != "" {
		t.Fatalf("generated index = %+v", first)
	}
	if !equalStrings(indexFieldNames(first), []string{"published", "created_at"}) {
		t.Fatalf("index fields = %v", indexFieldNames(first))
	}
	second := posts.Indexes[1]
	if second.Name != "posts_slug_idx" || !second.Unique || second.Where != "published = 1" {
		t.Fatalf("explicit index = %+v", second)
	}

	if !posts.Realtime {
		t.Fatal("realtime not parsed")
	}
	if posts.Permissions[ActionList] != "resource.published == true" {
		t.Fatalf("permissions = %v", posts.Permissions)
	}

	profiles, _ := s.Table("profiles")
	if got, want := fieldNames(profiles), []string{"email"}; !equalStrings(got, want) {
		t.Fatalf("profiles field order = %v, want %v", got, want)
	}
}

func TestParseEmptyTables(t *testing.T) {
	s, err := Parse(strings.NewReader("version: 1\ntables: {}\n"))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(s.Tables) != 0 {
		t.Fatalf("tables = %v", s.Tables)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		key  string
	}{
		{
			name: "top level",
			doc:  "version: 1\ntables: {}\nserver:\n  addr: :8090\n",
			key:  "server",
		},
		{
			name: "table",
			doc:  "version: 1\ntables:\n  posts:\n    field: []\n",
			key:  "field",
		},
		{
			name: "field",
			doc:  "version: 1\ntables:\n  posts:\n    fields:\n      title: { type: text, primary: true }\n",
			key:  "primary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.doc))
			if err == nil {
				t.Fatalf("expected an unknown-key error for %q", tt.key)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("error = %q, want mention of %q", err, tt.key)
			}
		})
	}
}

func TestParseRejectsInvalidReferences(t *testing.T) {
	values := []string{
		"users",
		".id",
		"users.",
		"users.id.extra",
		"Users.id",
		"2users.id",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			doc := "version: 1\ntables:\n  posts:\n    fields:\n      author_id: { type: uuid, references: \"" + value + "\" }\n"
			_, err := Parse(strings.NewReader(doc))
			if err == nil {
				t.Fatalf("reference %q accepted", value)
			}
			if !strings.Contains(err.Error(), "invalid reference") {
				t.Fatalf("error = %q", err)
			}
		})
	}
}

func TestParseAcceptsSystemReferences(t *testing.T) {
	for _, value := range []string{"users.id", "_dk_users.id"} {
		t.Run(value, func(t *testing.T) {
			doc := "version: 1\ntables:\n  posts:\n    fields:\n      author_id: { type: uuid, references: " + value + " }\n"
			s, err := Parse(strings.NewReader(doc))
			if err != nil {
				t.Fatalf("Parse() error: %v", err)
			}
			posts, _ := s.Table("posts")
			author, _ := posts.Field("author_id")
			if author.Ref == nil || author.Ref.Table != "_dk_users" {
				t.Fatalf("reference = %+v", author.Ref)
			}
		})
	}
}

func TestParseRejectsUnknownReferenceTargets(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "table",
			doc:  "version: 1\ntables:\n  posts:\n    fields:\n      author_id: { type: uuid, references: missing.id }\n",
			want: "references unknown table",
		},
		{
			name: "field",
			doc:  "version: 1\ntables:\n  posts:\n    fields:\n      title: { type: text }\n  comments:\n    fields:\n      post_id: { type: uuid, references: posts.missing }\n",
			want: "references unknown field",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.doc))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestParseRejectsUsersTable(t *testing.T) {
	doc := "version: 1\ntables:\n  users:\n    fields:\n      email: { type: text }\n"
	_, err := Parse(strings.NewReader(doc))
	if err == nil || !strings.Contains(err.Error(), "users is the _dk_users alias") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{"missing", "tables: {}\n"},
		{"unsupported", "version: 2\ntables: {}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.doc))
			if err == nil || !strings.Contains(err.Error(), "unsupported schema version") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseRejectsEmptyAndMultipleDocuments(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{"empty", "", "decode yaml"},
		{"multiple", "version: 1\ntables: {}\n---\nversion: 1\ntables: {}\n", "multiple documents"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseRejectsDuplicateKeysAndInvalidIndexes(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "duplicate field key",
			doc:  "version: 1\ntables:\n  posts:\n    fields:\n      title: { type: text }\n      title: { type: text }\n",
			want: "already defined",
		},
		{
			name: "index without fields",
			doc:  "version: 1\ntables:\n  posts:\n    fields:\n      title: { type: text }\n    indexes:\n      - { unique: true }\n",
			want: "expected 1 to 5 fields",
		},
		{
			name: "duplicate generated index",
			doc:  "version: 1\ntables:\n  posts:\n    fields:\n      title: { type: text }\n    indexes:\n      - { fields: [title] }\n      - { fields: [title] }\n",
			want: "duplicate index",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doketfire.yaml")
	if err := os.WriteFile(path, []byte(blogYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error: %v", err)
	}
	if _, ok := s.Table("posts"); !ok {
		t.Fatal("posts table not loaded")
	}

	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(blogYAML))
	f.Add([]byte("version: 1\ntables: {}\n"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse(bytes.NewReader(data))
	})
}

func tableNames(s *Schema) []string {
	names := make([]string, 0, len(s.Tables))
	for _, table := range s.Tables {
		names = append(names, table.Name)
	}
	return names
}

func fieldNames(t *Table) []string {
	names := make([]string, 0, len(t.Fields))
	for _, field := range t.Fields {
		names = append(names, field.Name)
	}
	return names
}

func indexFieldNames(idx Index) []string {
	names := make([]string, 0, len(idx.Fields))
	for _, field := range idx.Fields {
		names = append(names, field.Name)
	}
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
