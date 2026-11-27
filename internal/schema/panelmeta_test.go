package schema

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPanelMetaValid(t *testing.T) {
	s, err := LoadFile(filepath.Join("testdata", "valid", "blog.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	meta, err := BuildPanelMeta(s)
	if err != nil {
		t.Fatalf("BuildPanelMeta() error: %v", err)
	}
	if meta.Version != 1 || meta.Project != "blog" {
		t.Fatalf("header = %d/%q", meta.Version, meta.Project)
	}
	if len(meta.Navigation) != 3 || meta.Navigation[0].Name != "comments" || meta.Navigation[1].Name != "posts" || meta.Navigation[2].Name != "profiles" {
		t.Fatalf("unexpected navigation order: %+v", meta.Navigation)
	}
	if meta.Navigation[1].Path != "/collections/posts" || !meta.Navigation[1].Realtime {
		t.Fatalf("posts nav = %+v", meta.Navigation[1])
	}

	posts := meta.Collections[1]
	wantFields := []string{"title", "slug", "body", "published", "author_id", "status", "metadata"}
	if len(posts.Fields) != len(wantFields) {
		t.Fatalf("posts fields = %d, want %d", len(posts.Fields), len(wantFields))
	}
	for i, wantName := range wantFields {
		if posts.Fields[i].Name != wantName {
			t.Fatalf("posts field[%d] = %q, want %q", i, posts.Fields[i].Name, wantName)
		}
	}

	if posts.Fields[3].Widget != "checkbox" || posts.Fields[4].Widget != "relation" || posts.Fields[5].Widget != "select" || posts.Fields[6].Widget != "json" {
		t.Fatalf("unexpected widgets: %+v", posts.Fields)
	}
	if posts.Fields[4].Relation == nil || posts.Fields[4].Relation.Table != "_dk_users" {
		t.Fatalf("author_id relation = %+v", posts.Fields[4].Relation)
	}
	if len(posts.Permissions) != 5 || posts.Permissions[0].Action != ActionList || posts.Permissions[4].Action != ActionDelete {
		t.Fatalf("unexpected permissions order: %+v", posts.Permissions)
	}
}

func TestPanelMetaDeterministic(t *testing.T) {
	s1, err := LoadFile(filepath.Join("testdata", "valid", "blog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := LoadFile(filepath.Join("testdata", "valid", "blog.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	b1, err := PanelMetaJSON(s1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := PanelMetaJSON(s2)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatal("PanelMetaJSON() output was not deterministic")
	}
}

func TestPanelMetaErrors(t *testing.T) {
	if _, err := BuildPanelMeta(nil); err == nil {
		t.Fatal("expected error for nil schema")
	}
	if _, err := PanelMetaJSON(nil); err == nil {
		t.Fatal("expected error for nil schema JSON")
	}
}

func TestPanelMetaGoldenFixtures(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		golden string
	}{
		{"blog", "testdata/valid/blog.yaml", "testdata/golden/blog.panel.json"},
		{"minimal", "testdata/valid/minimal.yaml", "testdata/golden/minimal.panel.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := LoadFile(tt.schema)
			if err != nil {
				t.Fatalf("LoadFile(%s) error: %v", tt.schema, err)
			}
			got, err := PanelMetaJSON(s)
			if err != nil {
				t.Fatalf("PanelMetaJSON(%s) error: %v", tt.schema, err)
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
				t.Fatalf("panel metadata golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", tt.schema, got, want)
			}
		})
	}
}
