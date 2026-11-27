package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAPIValidStructureAndOrder(t *testing.T) {
	s, err := LoadFile(filepath.Join("testdata", "valid", "blog.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := OpenAPI(s)
	if err != nil {
		t.Fatalf("OpenAPI() error: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %v, want 3.1.0", doc["openapi"])
	}

	info, _ := doc["info"].(map[string]any)
	if info["title"] != "blog" || info["version"] != "1.0.0" {
		t.Fatalf("info = %v", info)
	}

	paths, _ := doc["paths"].(map[string]any)
	for _, wantPath := range []string{
		"/api/health",
		"/api/collections/comments/records",
		"/api/collections/comments/records/{id}",
		"/api/collections/posts/records",
		"/api/collections/posts/records/{id}",
		"/api/collections/profiles/records",
		"/api/collections/profiles/records/{id}",
	} {
		if _, ok := paths[wantPath]; !ok {
			t.Fatalf("missing path %q", wantPath)
		}
	}

	// Verify field order inside PostsRecord matches YAML declaration order (not alphabetical).
	text := string(raw)
	postsStart := strings.Index(text, `"PostsRecord":`)
	if postsStart < 0 {
		t.Fatal("missing PostsRecord schema")
	}
	postsSection := text[postsStart:]
	titleIdx := strings.Index(postsSection, `"title":`)
	slugIdx := strings.Index(postsSection, `"slug":`)
	bodyIdx := strings.Index(postsSection, `"body":`)
	authorIdx := strings.Index(postsSection, `"author_id":`)
	if !(titleIdx > 0 && titleIdx < slugIdx && slugIdx < bodyIdx && bodyIdx < authorIdx) {
		t.Fatalf("PostsRecord field order did not follow YAML order (%d, %d, %d, %d)", titleIdx, slugIdx, bodyIdx, authorIdx)
	}

	// Public "list" on comments omits bearerAuth; private "list" on posts requires bearerAuth.
	commentsPath, _ := paths["/api/collections/comments/records"].(map[string]any)
	commentsList, _ := commentsPath["get"].(map[string]any)
	if _, hasSec := commentsList["security"]; hasSec {
		t.Fatalf("public comments list should not require security: %v", commentsList["security"])
	}
	postsPath, _ := paths["/api/collections/posts/records"].(map[string]any)
	postsList, _ := postsPath["get"].(map[string]any)
	if _, hasSec := postsList["security"]; !hasSec {
		t.Fatal("non-public posts list must require bearerAuth")
	}
}

func TestOpenAPIDeterministic(t *testing.T) {
	s1, err := LoadFile(filepath.Join("testdata", "valid", "blog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := LoadFile(filepath.Join("testdata", "valid", "blog.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	b1, err := OpenAPI(s1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := OpenAPI(s2)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatal("OpenAPI() output was not deterministic")
	}
}

func TestOpenAPIErrors(t *testing.T) {
	if _, err := OpenAPI(nil); err == nil {
		t.Fatal("expected error for nil schema")
	}
}

func TestOpenAPIGoldenFixtures(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		golden string
	}{
		{"blog", "testdata/valid/blog.yaml", "testdata/golden/blog.openapi.json"},
		{"minimal", "testdata/valid/minimal.yaml", "testdata/golden/minimal.openapi.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := LoadFile(tt.schema)
			if err != nil {
				t.Fatalf("LoadFile(%s) error: %v", tt.schema, err)
			}
			got, err := OpenAPI(s)
			if err != nil {
				t.Fatalf("OpenAPI(%s) error: %v", tt.schema, err)
			}

			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(tt.golden), 0o755); err != nil {
					t.Fatalf("create golden dir: %v", err)
				}
				if err := os.WriteFile(tt.golden, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(tt.golden)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("OpenAPI golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", tt.schema, got, want)
			}
		})
	}
}
