package schema

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var identRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// ValidIdent reports whether s is a valid snake_case identifier.
func ValidIdent(s string) bool { return identRE.MatchString(s) }

var validFieldTypes = map[FieldType]struct{}{
	TypeText:      {},
	TypeInteger:   {},
	TypeReal:      {},
	TypeBoolean:   {},
	TypeTimestamp: {},
	TypeUUID:      {},
	TypeJSON:      {},
	TypeEnum:      {},
	TypeBlob:      {},
}

var validActions = map[Action]struct{}{
	ActionList:      {},
	ActionRead:      {},
	ActionCreate:    {},
	ActionUpdate:    {},
	ActionDelete:    {},
	ActionCount:     {},
	ActionAggregate: {},
}

var validOnDelete = map[string]struct{}{
	"SET NULL": {},
	"CASCADE":  {},
	"RESTRICT": {},
}

// Validate checks structural invariants and rebuilds the name indexes.
// References are resolved after every table has been checked.
func (s *Schema) Validate() error {
	if s == nil {
		return fmt.Errorf("schema is nil")
	}
	if s.Version != 1 {
		return fmt.Errorf("unsupported schema version %d", s.Version)
	}

	s.reindex()
	seenTables := make(map[string]struct{}, len(s.Tables))
	for _, t := range s.Tables {
		if t == nil {
			return fmt.Errorf("table is nil")
		}
		if !ValidIdent(t.Name) {
			return fmt.Errorf("invalid table name %q", t.Name)
		}
		if slices.Contains(systemColumns, t.Name) {
			return fmt.Errorf("reserved table name %q", t.Name)
		}
		if _, ok := seenTables[t.Name]; ok {
			return fmt.Errorf("duplicate table %q", t.Name)
		}
		seenTables[t.Name] = struct{}{}
		if err := t.validate(); err != nil {
			return fmt.Errorf("table %q: %w", t.Name, err)
		}
	}

	for _, t := range s.Tables {
		for _, f := range t.Fields {
			if f.Ref == nil {
				continue
			}
			if err := s.validateRef(t, f); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Table) validate() error {
	t.reindex()
	seenFields := make(map[string]struct{}, len(t.Fields))
	for _, f := range t.Fields {
		if f == nil {
			return fmt.Errorf("field is nil")
		}
		if !ValidIdent(f.Name) {
			return fmt.Errorf("invalid field name %q", f.Name)
		}
		if slices.Contains(systemColumns, f.Name) {
			return fmt.Errorf("reserved field name %q", f.Name)
		}
		if _, ok := seenFields[f.Name]; ok {
			return fmt.Errorf("duplicate field %q", f.Name)
		}
		seenFields[f.Name] = struct{}{}

		if _, ok := validFieldTypes[f.Type]; !ok {
			return fmt.Errorf("field %q: invalid type %q", f.Name, f.Type)
		}
		if f.Type == TypeEnum {
			if len(f.Enum) == 0 {
				return fmt.Errorf("field %q: enum requires values", f.Name)
			}
			seenEnum := make(map[string]struct{}, len(f.Enum))
			for _, value := range f.Enum {
				if value == "" {
					return fmt.Errorf("field %q: enum values cannot be empty", f.Name)
				}
				if _, ok := seenEnum[value]; ok {
					return fmt.Errorf("field %q: duplicate enum value %q", f.Name, value)
				}
				seenEnum[value] = struct{}{}
			}
		} else if len(f.Enum) > 0 {
			return fmt.Errorf("field %q: enum values require type %q", f.Name, TypeEnum)
		}

		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return fmt.Errorf("field %q: min cannot be greater than max", f.Name)
		}
		if f.Type == TypeText {
			if f.Min != nil && *f.Min < 0 {
				return fmt.Errorf("field %q: text min length cannot be negative", f.Name)
			}
			if f.Max != nil && *f.Max < 0 {
				return fmt.Errorf("field %q: text max length cannot be negative", f.Name)
			}
		}
		if f.Ref != nil {
			if f.Ref.Table == "" || f.Ref.Field == "" {
				return fmt.Errorf("field %q: reference requires table and field", f.Name)
			}
			if _, ok := validOnDelete[f.Ref.OnDelete]; !ok {
				return fmt.Errorf("field %q: invalid on_delete %q", f.Name, f.Ref.OnDelete)
			}
		}
	}

	seenIndexes := make(map[string]struct{}, len(t.Indexes))
	for _, idx := range t.Indexes {
		if !ValidIdent(idx.Name) {
			return fmt.Errorf("invalid index name %q", idx.Name)
		}
		if _, ok := seenIndexes[idx.Name]; ok {
			return fmt.Errorf("duplicate index %q", idx.Name)
		}
		seenIndexes[idx.Name] = struct{}{}
		if len(idx.Fields) == 0 || len(idx.Fields) > 5 {
			return fmt.Errorf("index %q: expected 1 to 5 fields", idx.Name)
		}
		seenIndexFields := make(map[string]struct{}, len(idx.Fields))
		for _, idxField := range idx.Fields {
			if _, ok := seenFields[idxField.Name]; !ok {
				return fmt.Errorf("index %q: unknown field %q", idx.Name, idxField.Name)
			}
			if _, ok := seenIndexFields[idxField.Name]; ok {
				return fmt.Errorf("index %q: duplicate field %q", idx.Name, idxField.Name)
			}
			seenIndexFields[idxField.Name] = struct{}{}
		}
	}

	for action := range t.Permissions {
		if _, ok := validActions[action]; !ok {
			return fmt.Errorf("invalid permission action %q", action)
		}
	}
	return nil
}

func (s *Schema) validateRef(t *Table, f *Field) error {
	ref := f.Ref
	if isSystemTable(ref.Table) {
		return nil
	}
	target, ok := s.Table(ref.Table)
	if !ok {
		return fmt.Errorf("table %q field %q: references unknown table %q", t.Name, f.Name, ref.Table)
	}
	if slices.Contains(systemColumns, ref.Field) {
		return nil
	}
	if _, ok := target.Field(ref.Field); !ok {
		return fmt.Errorf("table %q field %q: references unknown field %q.%q", t.Name, f.Name, ref.Table, ref.Field)
	}
	return nil
}

func isSystemTable(name string) bool { return strings.HasPrefix(name, "_dk_") }
