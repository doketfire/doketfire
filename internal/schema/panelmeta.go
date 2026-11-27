package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// PanelMeta describes the deterministic admin-panel navigation, table views,
// and form fields derived from a Schema IR.
type PanelMeta struct {
	Version     int               `json:"version"`
	Project     string            `json:"project"`
	Navigation  []PanelNavItem    `json:"navigation"`
	Collections []PanelCollection `json:"collections"`
}

// PanelNavItem is one entry in the admin-panel sidebar navigation.
type PanelNavItem struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Path     string `json:"path"`
	Realtime bool   `json:"realtime,omitempty"`
}

// PanelCollection defines list columns, form fields, and permissions for a collection.
type PanelCollection struct {
	Name        string            `json:"name"`
	Label       string            `json:"label"`
	Realtime    bool              `json:"realtime,omitempty"`
	Columns     []PanelColumn     `json:"columns"`
	Fields      []PanelField      `json:"fields"`
	Permissions []PanelPermission `json:"permissions,omitempty"`
}

// PanelColumn describes one column in a collection's list table.
type PanelColumn struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	System   bool   `json:"system,omitempty"`
	Sortable bool   `json:"sortable"`
}

// PanelField describes one editable field in a collection's form.
type PanelField struct {
	Name     string         `json:"name"`
	Label    string         `json:"label"`
	Type     FieldType      `json:"type"`
	Widget   string         `json:"widget"`
	Required bool           `json:"required,omitempty"`
	Unique   bool           `json:"unique,omitempty"`
	Default  any            `json:"default,omitempty"`
	Min      *float64       `json:"min,omitempty"`
	Max      *float64       `json:"max,omitempty"`
	Pattern  string         `json:"pattern,omitempty"`
	Options  []string       `json:"options,omitempty"`
	Relation *PanelRelation `json:"relation,omitempty"`
}

// PanelRelation describes a foreign-key picker target in the admin form.
type PanelRelation struct {
	Table    string `json:"table"`
	Field    string `json:"field"`
	OnDelete string `json:"on_delete"`
}

// PanelPermission is a deterministic action/rule entry for display in the panel.
type PanelPermission struct {
	Action     Action `json:"action"`
	Expression string `json:"expression"`
}

var canonicalActions = []Action{
	ActionList,
	ActionRead,
	ActionCreate,
	ActionUpdate,
	ActionDelete,
	ActionCount,
	ActionAggregate,
}

// BuildPanelMeta derives deterministic admin-panel metadata from a validated Schema IR.
func BuildPanelMeta(s *Schema) (*PanelMeta, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	meta := &PanelMeta{
		Version:     s.Version,
		Project:     s.Project,
		Navigation:  make([]PanelNavItem, 0, len(s.Tables)),
		Collections: make([]PanelCollection, 0, len(s.Tables)),
	}

	for _, t := range s.Tables {
		label := humanizeIdent(t.Name)
		meta.Navigation = append(meta.Navigation, PanelNavItem{
			Name:     t.Name,
			Label:    label,
			Path:     "/collections/" + t.Name,
			Realtime: t.Realtime,
		})

		col := PanelCollection{
			Name:     t.Name,
			Label:    label,
			Realtime: t.Realtime,
			Columns:  buildPanelColumns(t),
			Fields:   make([]PanelField, 0, len(t.Fields)),
		}
		for _, f := range t.Fields {
			pf := PanelField{
				Name:     f.Name,
				Label:    humanizeIdent(f.Name),
				Type:     f.Type,
				Widget:   widgetForField(f),
				Required: f.Required,
				Unique:   f.Unique,
				Default:  f.Default,
				Min:      f.Min,
				Max:      f.Max,
				Pattern:  f.Pattern,
				Options:  f.Enum,
			}
			if f.Ref != nil {
				pf.Relation = &PanelRelation{
					Table:    f.Ref.Table,
					Field:    f.Ref.Field,
					OnDelete: f.Ref.OnDelete,
				}
			}
			col.Fields = append(col.Fields, pf)
		}
		for _, act := range canonicalActions {
			if expr, ok := t.Permissions[act]; ok {
				col.Permissions = append(col.Permissions, PanelPermission{
					Action:     act,
					Expression: expr,
				})
			}
		}
		meta.Collections = append(meta.Collections, col)
	}
	return meta, nil
}

// PanelMetaJSON serializes BuildPanelMeta output as deterministic, indented JSON.
func PanelMetaJSON(s *Schema) ([]byte, error) {
	meta, err := BuildPanelMeta(s)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return nil, fmt.Errorf("marshal panel metadata: %w", err)
	}
	return buf.Bytes(), nil
}

func buildPanelColumns(t *Table) []PanelColumn {
	cols := make([]PanelColumn, 0, len(t.Fields)+3)
	cols = append(cols, PanelColumn{
		Name:     "id",
		Label:    "ID",
		Type:     string(TypeUUID),
		System:   true,
		Sortable: true,
	})
	for _, f := range t.Fields {
		sortable := f.Type != TypeJSON && f.Type != TypeBlob
		cols = append(cols, PanelColumn{
			Name:     f.Name,
			Label:    humanizeIdent(f.Name),
			Type:     string(f.Type),
			Sortable: sortable,
		})
	}
	cols = append(cols,
		PanelColumn{
			Name:     "created_at",
			Label:    "Created At",
			Type:     string(TypeTimestamp),
			System:   true,
			Sortable: true,
		},
		PanelColumn{
			Name:     "updated_at",
			Label:    "Updated At",
			Type:     string(TypeTimestamp),
			System:   true,
			Sortable: true,
		},
	)
	return cols
}

func widgetForField(f *Field) string {
	if f.Ref != nil {
		return "relation"
	}
	switch f.Type {
	case TypeInteger, TypeReal:
		return "number"
	case TypeBoolean:
		return "checkbox"
	case TypeTimestamp:
		return "datetime"
	case TypeUUID:
		return "uuid"
	case TypeEnum:
		return "select"
	case TypeJSON:
		return "json"
	case TypeBlob:
		return "file"
	default:
		return "text"
	}
}

func humanizeIdent(name string) string {
	parts := strings.Split(name, "_")
	words := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		if p == "id" {
			words = append(words, "ID")
			continue
		}
		words = append(words, strings.ToUpper(p[:1])+p[1:])
	}
	if len(words) == 0 {
		return name
	}
	return strings.Join(words, " ")
}
