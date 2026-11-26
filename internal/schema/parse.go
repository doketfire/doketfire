package schema

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// rawSchema mirrors doketfire.yaml. It is deliberately separate from the IR:
// only this file knows about YAML tags and raw field names.
type rawSchema struct {
	Version int                 `yaml:"version"`
	Project string              `yaml:"project"`
	Tables  map[string]rawTable `yaml:"tables"`
}

type rawTable struct {
	Fields      map[string]rawField `yaml:"fields"`
	Indexes     []rawIndex          `yaml:"indexes"`
	Permissions map[string]string   `yaml:"permissions"`
	Realtime    bool                `yaml:"realtime"`
}

type rawField struct {
	Type       string   `yaml:"type"`
	Required   bool     `yaml:"required"`
	Unique     bool     `yaml:"unique"`
	Default    any      `yaml:"default"`
	Min        *float64 `yaml:"min"`
	Max        *float64 `yaml:"max"`
	Pattern    string   `yaml:"pattern"`
	Enum       []string `yaml:"enum"`
	References string   `yaml:"references"`
	JSONSchema string   `yaml:"json_schema"`
}

type rawIndex struct {
	Name   string   `yaml:"name"`
	Fields []string `yaml:"fields"`
	Unique bool     `yaml:"unique"`
	Where  string   `yaml:"where"`
}

// rawOrder decodes the document again to recover field declaration order.
// Go maps cannot preserve it, and the IR guarantees YAML order for fields.
type rawOrder struct {
	Tables map[string]rawTableOrder `yaml:"tables"`
}

type rawTableOrder struct {
	Fields yaml.Node `yaml:"fields"`
}

// refIdentRE accepts identifiers in references. It also allows the leading
// underscore of system tables such as _dk_users.
var refIdentRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Parse decodes and validates a doketfire.yaml document.
//
// The decoder is strict: unknown keys are rejected. Tables are sorted by name
// so DDL generation is deterministic; fields keep their YAML declaration order.
func Parse(r io.Reader) (*Schema, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var raw rawSchema
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode yaml: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode yaml: multiple documents are not supported")
		}
		return nil, fmt.Errorf("decode yaml: %w", err)
	}

	var order rawOrder
	if err := yaml.Unmarshal(data, &order); err != nil {
		return nil, fmt.Errorf("decode yaml: %w", err)
	}

	if raw.Version != 1 {
		return nil, fmt.Errorf("unsupported schema version %d", raw.Version)
	}

	s := &Schema{Version: raw.Version, Project: raw.Project}
	names := make([]string, 0, len(raw.Tables))
	for name := range raw.Tables {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t, err := raw.Tables[name].compile(name, order.Tables[name].Fields)
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", name, err)
		}
		s.Tables = append(s.Tables, t)
	}
	return s, s.Validate()
}

// LoadFile parses a schema file from disk.
func LoadFile(path string) (*Schema, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

func (rt rawTable) compile(name string, fieldsNode yaml.Node) (*Table, error) {
	t := &Table{
		Name:        name,
		Permissions: make(map[Action]string, len(rt.Permissions)),
		Realtime:    rt.Realtime,
	}

	seen := make(map[string]struct{}, len(rt.Fields))
	for _, fieldName := range orderedFieldNames(&fieldsNode, rt.Fields) {
		f, err := rt.Fields[fieldName].compile()
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", fieldName, err)
		}
		f.Name = fieldName
		t.Fields = append(t.Fields, f)
		seen[fieldName] = struct{}{}
	}

	// Defensive fallback: YAML merge keys and aliases may hide fields from the
	// order pass. They are appended in a deterministic order, never dropped.
	rest := make([]string, 0, len(rt.Fields)-len(seen))
	for fieldName := range rt.Fields {
		if _, ok := seen[fieldName]; !ok {
			rest = append(rest, fieldName)
		}
	}
	sort.Strings(rest)
	for _, fieldName := range rest {
		f, err := rt.Fields[fieldName].compile()
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", fieldName, err)
		}
		f.Name = fieldName
		t.Fields = append(t.Fields, f)
	}

	for _, ri := range rt.Indexes {
		t.Indexes = append(t.Indexes, ri.compile(name))
	}
	for action, expr := range rt.Permissions {
		t.Permissions[Action(action)] = expr
	}
	return t, nil
}

func orderedFieldNames(node *yaml.Node, fields map[string]rawField) []string {
	for node != nil && node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	names := make([]string, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := node.Content[i].Value
		if _, ok := fields[name]; ok {
			names = append(names, name)
		}
	}
	return names
}

func (rf rawField) compile() (*Field, error) {
	f := &Field{
		Type:       FieldType(rf.Type),
		Required:   rf.Required,
		Unique:     rf.Unique,
		Default:    rf.Default,
		Min:        rf.Min,
		Max:        rf.Max,
		Pattern:    rf.Pattern,
		Enum:       rf.Enum,
		JSONSchema: rf.JSONSchema,
	}
	if rf.References != "" {
		ref, err := parseReference(rf.References)
		if err != nil {
			return nil, err
		}
		f.Ref = ref
	}
	return f, nil
}

// parseReference compiles "users.id" into the physical _dk_users(id) target.
// The default delete behavior is SET NULL, matching the documented DDL.
func parseReference(value string) (*Ref, error) {
	table, field, ok := strings.Cut(value, ".")
	if !ok || table == "" || field == "" || strings.Contains(field, ".") {
		return nil, fmt.Errorf("invalid reference %q: expected table.field", value)
	}
	if !refIdentRE.MatchString(table) || !refIdentRE.MatchString(field) {
		return nil, fmt.Errorf("invalid reference %q", value)
	}
	if table == "users" {
		table = "_dk_users"
	}
	return &Ref{Table: table, Field: field, OnDelete: "SET NULL"}, nil
}

func (ri rawIndex) compile(table string) Index {
	idx := Index{
		Name:   ri.Name,
		Unique: ri.Unique,
		Where:  ri.Where,
	}
	for _, name := range ri.Fields {
		idx.Fields = append(idx.Fields, IndexField{Name: name})
	}
	if idx.Name == "" {
		parts := make([]string, 0, len(idx.Fields)+1)
		parts = append(parts, table)
		for _, f := range idx.Fields {
			parts = append(parts, f.Name)
		}
		idx.Name = "idx_" + strings.Join(parts, "_")
	}
	return idx
}
