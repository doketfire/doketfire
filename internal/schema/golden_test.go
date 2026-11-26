package schema

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update golden files")

type goldenSchema struct {
	Version int           `json:"version"`
	Project string        `json:"project"`
	Tables  []goldenTable `json:"tables"`
}

type goldenTable struct {
	Name        string            `json:"name"`
	Fields      []goldenField     `json:"fields"`
	Indexes     []goldenIndex     `json:"indexes,omitempty"`
	Permissions map[string]string `json:"permissions,omitempty"`
	Realtime    bool              `json:"realtime,omitempty"`
}

type goldenField struct {
	Name       string     `json:"name"`
	Type       FieldType  `json:"type"`
	Required   bool       `json:"required,omitempty"`
	Unique     bool       `json:"unique,omitempty"`
	Default    any        `json:"default,omitempty"`
	Min        *float64   `json:"min,omitempty"`
	Max        *float64   `json:"max,omitempty"`
	Pattern    string     `json:"pattern,omitempty"`
	Enum       []string   `json:"enum,omitempty"`
	Ref        *goldenRef `json:"ref,omitempty"`
	JSONSchema string     `json:"json_schema,omitempty"`
}

type goldenRef struct {
	Table    string `json:"table"`
	Field    string `json:"field"`
	OnDelete string `json:"on_delete"`
}

type goldenIndex struct {
	Name   string             `json:"name"`
	Fields []goldenIndexField `json:"fields"`
	Unique bool               `json:"unique,omitempty"`
	Where  string             `json:"where,omitempty"`
}

type goldenIndexField struct {
	Name string `json:"name"`
	Desc bool   `json:"desc,omitempty"`
}

func TestGoldenFixtures(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		golden string
	}{
		{"blog", "testdata/valid/blog.yaml", "testdata/golden/blog.json"},
		{"minimal", "testdata/valid/minimal.yaml", "testdata/golden/minimal.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkGolden(t, tt.schema, tt.golden)
		})
	}
}

func checkGolden(t *testing.T, schemaPath, goldenPath string) {
	t.Helper()

	s, err := LoadFile(schemaPath)
	if err != nil {
		t.Fatalf("LoadFile(%s) error: %v", schemaPath, err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(dumpSchema(s)); err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	got := buf.Bytes()

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run go test ./internal/schema -update): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", schemaPath, got, want)
	}
}

func dumpSchema(s *Schema) goldenSchema {
	out := goldenSchema{
		Version: s.Version,
		Project: s.Project,
		Tables:  make([]goldenTable, 0, len(s.Tables)),
	}
	for _, t := range s.Tables {
		gt := goldenTable{
			Name:     t.Name,
			Fields:   make([]goldenField, 0, len(t.Fields)),
			Realtime: t.Realtime,
		}
		for _, f := range t.Fields {
			gf := goldenField{
				Name:       f.Name,
				Type:       f.Type,
				Required:   f.Required,
				Unique:     f.Unique,
				Default:    f.Default,
				Min:        f.Min,
				Max:        f.Max,
				Pattern:    f.Pattern,
				Enum:       f.Enum,
				JSONSchema: f.JSONSchema,
			}
			if f.Ref != nil {
				gf.Ref = &goldenRef{Table: f.Ref.Table, Field: f.Ref.Field, OnDelete: f.Ref.OnDelete}
			}
			gt.Fields = append(gt.Fields, gf)
		}
		for _, idx := range t.Indexes {
			gi := goldenIndex{Name: idx.Name, Unique: idx.Unique, Where: idx.Where}
			for _, f := range idx.Fields {
				gi.Fields = append(gi.Fields, goldenIndexField(f))
			}
			gt.Indexes = append(gt.Indexes, gi)
		}
		if len(t.Permissions) > 0 {
			gt.Permissions = make(map[string]string, len(t.Permissions))
			for action, expr := range t.Permissions {
				gt.Permissions[string(action)] = expr
			}
		}
		out.Tables = append(out.Tables, gt)
	}
	return out
}
