// Package schema defines the intermediate representation (IR) of a
// doketfire.yaml schema. DDL, OpenAPI and panel metadata are generated from
// this IR, never from the raw YAML.
package schema

// FieldType is the logical type of a field.
type FieldType string

const (
	TypeText      FieldType = "text"
	TypeInteger   FieldType = "integer"
	TypeReal      FieldType = "real"
	TypeBoolean   FieldType = "boolean"
	TypeTimestamp FieldType = "timestamp"
	TypeUUID      FieldType = "uuid"
	TypeJSON      FieldType = "json"
	TypeEnum      FieldType = "enum"
	TypeBlob      FieldType = "blob"
)

// Action is an operation that can be authorized on a table.
type Action string

const (
	ActionList      Action = "list"
	ActionRead      Action = "read"
	ActionCreate    Action = "create"
	ActionUpdate    Action = "update"
	ActionDelete    Action = "delete"
	ActionCount     Action = "count"
	ActionAggregate Action = "aggregate"
)

// Ref points to another table's field.
type Ref struct {
	Table    string // "users" in YAML; "_dk_users" after compilation.
	Field    string
	OnDelete string // SET NULL | CASCADE | RESTRICT
}

// Field is a column in the logical schema.
type Field struct {
	Name       string
	Type       FieldType
	Required   bool
	Unique     bool
	Default    any
	Min        *float64 // length for text; value for numbers
	Max        *float64
	Pattern    string
	Enum       []string
	Ref        *Ref
	JSONSchema string
}

// IndexField is one field in an index, optionally descending.
type IndexField struct {
	Name string
	Desc bool
}

// Index is a table index.
type Index struct {
	Name   string
	Fields []IndexField
	Unique bool
	Where  string
}

// Table is a logical collection.
type Table struct {
	Name        string
	Fields      []*Field
	fieldByName map[string]*Field
	Indexes     []Index
	Permissions map[Action]string
	Realtime    bool
}

// Schema is the compiled representation of doketfire.yaml.
type Schema struct {
	Version int
	Project string
	Tables  []*Table
	byName  map[string]*Table
}

// Table returns a table by name.
func (s *Schema) Table(name string) (*Table, bool) {
	t, ok := s.byName[name]
	return t, ok
}

// Field returns a field by name.
func (t *Table) Field(name string) (*Field, bool) {
	f, ok := t.fieldByName[name]
	return f, ok
}

func (s *Schema) reindex() {
	s.byName = make(map[string]*Table, len(s.Tables))
	for _, t := range s.Tables {
		if t != nil {
			s.byName[t.Name] = t
		}
	}
}

func (t *Table) reindex() {
	t.fieldByName = make(map[string]*Field, len(t.Fields))
	for _, f := range t.Fields {
		if f != nil {
			t.fieldByName[f.Name] = f
		}
	}
}

// System columns are always present and cannot be redefined.
var systemColumns = []string{"id", "created_at", "updated_at", "version"}
