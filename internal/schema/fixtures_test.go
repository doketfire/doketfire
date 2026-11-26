package schema

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFixturesValid(t *testing.T) {
	paths := globFixtures(t, "testdata/valid/*.yaml")
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			s, err := LoadFile(path)
			if err != nil {
				t.Fatalf("LoadFile() error: %v", err)
			}
			if s.Version != 1 {
				t.Fatalf("version = %d", s.Version)
			}
			if s.Project == "" {
				t.Fatal("project is empty")
			}
		})
	}
}

func TestBlogFixture(t *testing.T) {
	s, err := LoadFile(filepath.Join("testdata", "valid", "blog.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error: %v", err)
	}
	if got, want := tableNames(s), []string{"comments", "posts", "profiles"}; !equalStrings(got, want) {
		t.Fatalf("table order = %v, want %v", got, want)
	}

	posts, ok := s.Table("posts")
	if !ok {
		t.Fatal("posts table not found")
	}
	wantFields := []string{"title", "slug", "body", "published", "author_id", "status", "metadata"}
	if got := fieldNames(posts); !equalStrings(got, wantFields) {
		t.Fatalf("posts field order = %v, want %v", got, wantFields)
	}
	author, _ := posts.Field("author_id")
	if author.Ref == nil || author.Ref.Table != "_dk_users" || author.Ref.Field != "id" {
		t.Fatalf("author reference = %+v", author.Ref)
	}
	if !posts.Realtime {
		t.Fatal("posts realtime = false")
	}
	if posts.Permissions[ActionDelete] != "request.auth != null && request.auth.role == 'admin'" {
		t.Fatalf("delete permission = %q", posts.Permissions[ActionDelete])
	}

	if len(posts.Indexes) != 3 {
		t.Fatalf("indexes = %+v", posts.Indexes)
	}
	if posts.Indexes[0].Name != "idx_posts_published_created_at" {
		t.Fatalf("generated index = %q", posts.Indexes[0].Name)
	}
	if posts.Indexes[1].Name != "posts_slug_idx" || !posts.Indexes[1].Unique {
		t.Fatalf("slug index = %+v", posts.Indexes[1])
	}
	if posts.Indexes[2].Where != "published = 1" {
		t.Fatalf("partial index = %+v", posts.Indexes[2])
	}

	comments, _ := s.Table("comments")
	postID, _ := comments.Field("post_id")
	if postID.Ref == nil || postID.Ref.Table != "posts" || postID.Ref.Field != "id" {
		t.Fatalf("post reference = %+v", postID.Ref)
	}
}

func TestFixturesInvalid(t *testing.T) {
	want := map[string]string{
		"unknown_key.yaml":                   "field server not found",
		"bad_version.yaml":                   "unsupported schema version 2",
		"bad_table_name.yaml":                "invalid table name",
		"reserved_table.yaml":                "reserved table name",
		"users_table.yaml":                   "users is the _dk_users alias",
		"bad_field_type.yaml":                "invalid type",
		"bad_reference.yaml":                 "invalid reference",
		"unknown_reference.yaml":             "references unknown table",
		"bad_enum.yaml":                      "enum requires values",
		"bad_pattern.yaml":                   "invalid pattern",
		"bad_json_schema.yaml":               "invalid json_schema",
		"bad_index.yaml":                     "unknown field",
		"bad_permission.yaml":                "invalid permission action",
		"duplicate_index_across_tables.yaml": "duplicate index",
	}

	paths := globFixtures(t, "testdata/invalid/*.yaml")
	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			wantErr, ok := want[name]
			if !ok {
				t.Fatalf("no expectation for %s", name)
			}
			_, err := LoadFile(path)
			if err == nil {
				t.Fatalf("LoadFile(%s) accepted an invalid fixture", name)
			}
			if !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("error = %q, want substring %q", err, wantErr)
			}
		})
	}
	if len(paths) != len(want) {
		t.Fatalf("%d invalid fixtures but %d expectations", len(paths), len(want))
	}
}

func globFixtures(t *testing.T, pattern string) []string {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("Glob(%q) error: %v", pattern, err)
	}
	if len(paths) == 0 {
		t.Fatalf("Glob(%q) found no fixtures", pattern)
	}
	return paths
}
