package schema

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// QuoteIdent quotes a SQLite identifier with double quotes, escaping embedded
// double quotes so identifiers derived from the IR cannot break SQL syntax.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// Build generates deterministic SQLite DDL statements from a validated Schema IR.
func Build(s *Schema) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	var out []string
	for _, t := range s.Tables {
		stmt, err := createTable(t)
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", t.Name, err)
		}
		out = append(out, stmt)
		for _, idx := range t.Indexes {
			out = append(out, createIndex(t, idx))
		}
	}
	return out, nil
}

func createTable(t *Table) (string, error) {
	cols := make([]string, 0, 4+len(t.Fields))
	cols = append(cols,
		`"id" TEXT PRIMARY KEY`,
		`"created_at" INTEGER NOT NULL`,
		`"updated_at" INTEGER NOT NULL`,
		`"version" INTEGER NOT NULL DEFAULT 1`,
	)
	for _, f := range t.Fields {
		col, err := columnSQL(f)
		if err != nil {
			return "", fmt.Errorf("field %q: %w", f.Name, err)
		}
		cols = append(cols, col)
	}
	return fmt.Sprintf("CREATE TABLE %s (\n  %s\n) STRICT;",
		QuoteIdent(t.Name), strings.Join(cols, ",\n  ")), nil
}

func columnSQL(f *Field) (string, error) {
	qName := QuoteIdent(f.Name)
	parts := []string{qName, sqlType(f.Type)}
	if f.Required {
		parts = append(parts, "NOT NULL")
	}
	if f.Unique {
		parts = append(parts, "UNIQUE")
	}
	if f.Default != nil {
		lit, err := literalSQL(f.Default)
		if err != nil {
			return "", err
		}
		parts = append(parts, "DEFAULT "+lit)
	}
	if f.Type == TypeBoolean {
		parts = append(parts, "CHECK ("+qName+" IN (0, 1))")
	}
	if len(f.Enum) > 0 {
		parts = append(parts, "CHECK ("+qName+" IN ("+quoteStringList(f.Enum)+"))")
	}
	if check := rangeCheckSQL(qName, f); check != "" {
		parts = append(parts, check)
	}
	if f.Ref != nil {
		parts = append(parts, fmt.Sprintf("REFERENCES %s(%s) ON DELETE %s",
			QuoteIdent(f.Ref.Table), QuoteIdent(f.Ref.Field), f.Ref.OnDelete))
	}
	return strings.Join(parts, " "), nil
}

func rangeCheckSQL(qName string, f *Field) string {
	if f.Min == nil && f.Max == nil {
		return ""
	}
	var subject string
	var formatVal func(float64) string
	switch f.Type {
	case TypeText:
		subject = "length(" + qName + ")"
		formatVal = func(v float64) string {
			return strconv.FormatInt(int64(math.Trunc(v)), 10)
		}
	case TypeInteger:
		subject = qName
		formatVal = func(v float64) string {
			return strconv.FormatInt(int64(math.Trunc(v)), 10)
		}
	case TypeReal:
		subject = qName
		formatVal = func(v float64) string {
			return strconv.FormatFloat(v, 'g', -1, 64)
		}
	default:
		return ""
	}

	switch {
	case f.Min != nil && f.Max != nil:
		return fmt.Sprintf("CHECK (%s BETWEEN %s AND %s)", subject, formatVal(*f.Min), formatVal(*f.Max))
	case f.Min != nil:
		return fmt.Sprintf("CHECK (%s >= %s)", subject, formatVal(*f.Min))
	default:
		return fmt.Sprintf("CHECK (%s <= %s)", subject, formatVal(*f.Max))
	}
}

func createIndex(t *Table, idx Index) string {
	cols := make([]string, 0, len(idx.Fields))
	for _, f := range idx.Fields {
		col := QuoteIdent(f.Name)
		if f.Desc {
			col += " DESC"
		}
		cols = append(cols, col)
	}
	unique := ""
	if idx.Unique {
		unique = "UNIQUE "
	}
	where := ""
	if idx.Where != "" {
		where = " WHERE " + idx.Where
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)%s;",
		unique, QuoteIdent(idx.Name), QuoteIdent(t.Name), strings.Join(cols, ", "), where)
}

func sqlType(t FieldType) string {
	switch t {
	case TypeInteger, TypeBoolean, TypeTimestamp:
		return "INTEGER"
	case TypeReal:
		return "REAL"
	case TypeBlob:
		return "BLOB"
	case TypeJSON, TypeText, TypeUUID, TypeEnum:
		return "TEXT"
	default:
		return "TEXT"
	}
}

func literalSQL(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return quoteString(x), nil
	case bool:
		if x {
			return "1", nil
		}
		return "0", nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported default value type %T", v)
	}
}

func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func quoteStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = quoteString(v)
	}
	return strings.Join(quoted, ", ")
}
