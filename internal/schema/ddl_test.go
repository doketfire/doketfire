package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"posts", `"posts"`},
		{"author_id", `"author_id"`},
		{`a"b`, `"a""b"`},
	}
	for _, tt := range tests {
		if got := QuoteIdent(tt.in); got != tt.want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildDDLAllTypesAndConstraints(t *testing.T) {
	s := &Schema{
		Version: 1,
		Project: "types",
		Tables: []*Table{
			{
				Name: "items",
				Fields: []*Field{
					{Name: "title", Type: TypeText, Required: true, Min: floatPtr(2), Max: floatPtr(80)},
					{Name: "subtitle", Type: TypeText, Min: floatPtr(3)},
					{Name: "note", Type: TypeText, Max: floatPtr(50)},
					{Name: "qty", Type: TypeInteger, Default: 1, Min: floatPtr(0), Max: floatPtr(100)},
					{Name: "score", Type: TypeReal, Default: 0.5, Min: floatPtr(0), Max: floatPtr(10.5)},
					{Name: "active", Type: TypeBoolean, Required: true, Default: true},
					{Name: "published_at", Type: TypeTimestamp},
					{Name: "external_id", Type: TypeUUID, Unique: true},
					{Name: "payload", Type: TypeJSON},
					{Name: "kind", Type: TypeEnum, Enum: []string{"a", "b'c"}},
					{Name: "raw", Type: TypeBlob},
					{Name: "owner_id", Type: TypeUUID, Ref: &Ref{Table: "_dk_users", Field: "id", OnDelete: "CASCADE"}},
				},
				Indexes: []Index{
					{
						Name:   "idx_items_kind_created",
						Fields: []IndexField{{Name: "kind"}, {Name: "created_at", Desc: true}},
						Where:  "active = 1",
					},
				},
			},
		},
	}

	stmts, err := Build(s)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if len(stmts) != 2 {
		t.Fatalf("got %d statements, want 2: %v", len(stmts), stmts)
	}

	wantTable := strings.Join([]string{
		`CREATE TABLE "items" (`,
		`  "id" TEXT PRIMARY KEY,`,
		`  "created_at" INTEGER NOT NULL,`,
		`  "updated_at" INTEGER NOT NULL,`,
		`  "version" INTEGER NOT NULL DEFAULT 1,`,
		`  "title" TEXT NOT NULL CHECK (length("title") BETWEEN 2 AND 80),`,
		`  "subtitle" TEXT CHECK (length("subtitle") >= 3),`,
		`  "note" TEXT CHECK (length("note") <= 50),`,
		`  "qty" INTEGER DEFAULT 1 CHECK ("qty" BETWEEN 0 AND 100),`,
		`  "score" REAL DEFAULT 0.5 CHECK ("score" BETWEEN 0 AND 10.5),`,
		`  "active" INTEGER NOT NULL DEFAULT 1 CHECK ("active" IN (0, 1)),`,
		`  "published_at" INTEGER,`,
		`  "external_id" TEXT UNIQUE,`,
		`  "payload" TEXT,`,
		`  "kind" TEXT CHECK ("kind" IN ('a', 'b''c')),`,
		`  "raw" BLOB,`,
		`  "owner_id" TEXT REFERENCES "_dk_users"("id") ON DELETE CASCADE`,
		`) STRICT;`,
	}, "\n")

	if stmts[0] != wantTable {
		t.Fatalf("table DDL mismatch:\ngot:\n%s\nwant:\n%s", stmts[0], wantTable)
	}

	wantIndex := `CREATE INDEX "idx_items_kind_created" ON "items" ("kind", "created_at" DESC) WHERE active = 1;`
	if stmts[1] != wantIndex {
		t.Fatalf("index DDL = %q, want %q", stmts[1], wantIndex)
	}
}

func TestBuildDDLDeterministic(t *testing.T) {
	s1, err := LoadFile(filepath.Join("testdata", "valid", "blog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := LoadFile(filepath.Join("testdata", "valid", "blog.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	ddl1, err := Build(s1)
	if err != nil {
		t.Fatal(err)
	}
	ddl2, err := Build(s2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ddl1, "\n\n") != strings.Join(ddl2, "\n\n") {
		t.Fatal("Build() output was not deterministic across loads")
	}
}

func TestBuildDDLErrors(t *testing.T) {
	if _, err := Build(nil); err == nil {
		t.Fatal("expected error for nil schema")
	}

	s := validSchema()
	s.Tables[0].Fields[0].Default = map[string]any{"bad": 1}
	if _, err := Build(s); err == nil {
		t.Fatal("expected error for unsupported default value type")
	}
}

func TestValidateIndexWhereRejectsUnsafe(t *testing.T) {
	for _, where := range []string{"1 = 1; DROP TABLE posts", "1 = 1 -- comment", "1 = 1 /* x */", "a\nb"} {
		s := validSchema()
		s.Tables[0].Indexes[0].Where = where
		if err := s.Validate(); err == nil {
			t.Fatalf("expected Validate() error for where=%q", where)
		}
	}
}

func TestDDLGoldenFixtures(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		golden string
	}{
		{"blog", "testdata/valid/blog.yaml", "testdata/golden/blog.sql"},
		{"minimal", "testdata/valid/minimal.yaml", "testdata/golden/minimal.sql"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := LoadFile(tt.schema)
			if err != nil {
				t.Fatalf("LoadFile(%s) error: %v", tt.schema, err)
			}
			stmts, err := Build(s)
			if err != nil {
				t.Fatalf("Build(%s) error: %v", tt.schema, err)
			}
			got := ""
			if len(stmts) > 0 {
				got = strings.Join(stmts, "\n\n") + "\n"
			}

			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(tt.golden), 0o755); err != nil {
					t.Fatalf("create golden dir: %v", err)
				}
				if err := os.WriteFile(tt.golden, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(tt.golden)
			if err != nil {
				t.Fatalf("read golden (run go test ./internal/schema -update): %v", err)
			}
			if got != string(want) {
				t.Fatalf("DDL golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", tt.schema, got, want)
			}
		})
	}
}
