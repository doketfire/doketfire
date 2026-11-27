package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

// OpenAPI generates a deterministic OpenAPI 3.1.0 JSON document from the
// validated Schema IR.
func OpenAPI(s *Schema) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	title := strings.TrimSpace(s.Project)
	if title == "" {
		title = "Doketfire API"
	}

	paths := orderedObject{}
	paths.set("/api/health", orderedObject{
		{Key: "get", Value: healthOperation()},
	})

	schemas := orderedObject{}
	schemas.set("ErrorResponse", errorResponseSchema())

	for _, t := range s.Tables {
		baseName := exportTypeName(t.Name)
		recordName := baseName + "Record"
		inputName := baseName + "Input"
		patchName := baseName + "Patch"
		listName := baseName + "ListResponse"

		schemas.set(recordName, recordSchema(t))
		schemas.set(inputName, inputSchema(t, true))
		schemas.set(patchName, inputSchema(t, false))
		schemas.set(listName, listResponseSchema(recordName))

		colPath := "/api/collections/" + t.Name + "/records"
		paths.set(colPath, orderedObject{
			{Key: "get", Value: listOperation(t, baseName, listName)},
			{Key: "post", Value: createOperation(t, baseName, inputName, recordName)},
		})

		itemPath := colPath + "/{id}"
		paths.set(itemPath, orderedObject{
			{Key: "get", Value: readOperation(t, baseName, recordName)},
			{Key: "patch", Value: updateOperation(t, baseName, patchName, recordName)},
			{Key: "delete", Value: deleteOperation(t, baseName)},
		})
	}

	securitySchemes := orderedObject{
		{Key: "bearerAuth", Value: orderedObject{
			{Key: "type", Value: "http"},
			{Key: "scheme", Value: "bearer"},
			{Key: "bearerFormat", Value: "JWT"},
		}},
	}

	doc := orderedObject{
		{Key: "openapi", Value: "3.1.0"},
		{Key: "info", Value: orderedObject{
			{Key: "title", Value: title},
			{Key: "version", Value: fmt.Sprintf("%d.0.0", s.Version)},
		}},
		{Key: "paths", Value: paths},
		{Key: "components", Value: orderedObject{
			{Key: "schemas", Value: schemas},
			{Key: "securitySchemes", Value: securitySchemes},
		}},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("marshal openapi: %w", err)
	}
	return buf.Bytes(), nil
}

// orderedField is a single key/value pair in an ordered JSON object.
type orderedField struct {
	Key   string
	Value any
}

// orderedObject serializes JSON objects in deterministic insertion order so
// OpenAPI output preserves YAML field order and path ordering.
type orderedObject []orderedField

func (o *orderedObject) set(key string, val any) {
	*o = append(*o, orderedField{Key: key, Value: val})
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(f.Key)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(f.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func exportTypeName(name string) string {
	parts := strings.Split(name, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	if b.Len() == 0 {
		return "Collection"
	}
	return b.String()
}

func healthOperation() orderedObject {
	return orderedObject{
		{Key: "operationId", Value: "getHealth"},
		{Key: "summary", Value: "Health check"},
		{Key: "tags", Value: []string{"system"}},
		{Key: "responses", Value: orderedObject{
			{Key: "200", Value: orderedObject{
				{Key: "description", Value: "Service is healthy"},
				{Key: "content", Value: jsonContent(orderedObject{
					{Key: "type", Value: "object"},
					{Key: "properties", Value: orderedObject{
						{Key: "status", Value: orderedObject{{Key: "type", Value: "string"}}},
						{Key: "version", Value: orderedObject{{Key: "type", Value: "string"}}},
					}},
					{Key: "required", Value: []string{"status"}},
				})},
			}},
		}},
	}
}

func errorResponseSchema() orderedObject {
	return orderedObject{
		{Key: "type", Value: "object"},
		{Key: "properties", Value: orderedObject{
			{Key: "error", Value: orderedObject{
				{Key: "type", Value: "object"},
				{Key: "properties", Value: orderedObject{
					{Key: "code", Value: orderedObject{{Key: "type", Value: "string"}}},
					{Key: "message", Value: orderedObject{{Key: "type", Value: "string"}}},
					{Key: "details", Value: orderedObject{{Key: "type", Value: "object"}}},
				}},
				{Key: "required", Value: []string{"code", "message"}},
			}},
		}},
		{Key: "required", Value: []string{"error"}},
	}
}

func recordSchema(t *Table) orderedObject {
	props := orderedObject{
		{Key: "id", Value: orderedObject{
			{Key: "type", Value: "string"},
			{Key: "description", Value: "Record ULID identifier"},
		}},
		{Key: "created_at", Value: orderedObject{
			{Key: "type", Value: "string"},
			{Key: "format", Value: "date-time"},
		}},
		{Key: "updated_at", Value: orderedObject{
			{Key: "type", Value: "string"},
			{Key: "format", Value: "date-time"},
		}},
		{Key: "version", Value: orderedObject{
			{Key: "type", Value: "integer"},
			{Key: "format", Value: "int64"},
		}},
	}
	required := []string{"id", "created_at", "updated_at", "version"}

	for _, f := range t.Fields {
		props.set(f.Name, fieldSchema(f, !f.Required))
		if f.Required {
			required = append(required, f.Name)
		}
	}

	return orderedObject{
		{Key: "type", Value: "object"},
		{Key: "properties", Value: props},
		{Key: "required", Value: required},
	}
}

func inputSchema(t *Table, includeRequired bool) orderedObject {
	props := orderedObject{}
	var required []string

	for _, f := range t.Fields {
		props.set(f.Name, fieldSchema(f, !f.Required))
		if includeRequired && f.Required && f.Default == nil {
			required = append(required, f.Name)
		}
	}

	obj := orderedObject{
		{Key: "type", Value: "object"},
		{Key: "properties", Value: props},
	}
	if len(required) > 0 {
		obj.set("required", required)
	}
	return obj
}

func listResponseSchema(recordName string) orderedObject {
	return orderedObject{
		{Key: "type", Value: "object"},
		{Key: "properties", Value: orderedObject{
			{Key: "items", Value: orderedObject{
				{Key: "type", Value: "array"},
				{Key: "items", Value: refSchema(recordName)},
			}},
			{Key: "nextCursor", Value: orderedObject{
				{Key: "type", Value: []string{"string", "null"}},
			}},
		}},
		{Key: "required", Value: []string{"items"}},
	}
}

func fieldSchema(f *Field, nullable bool) orderedObject {
	if f.Type == TypeJSON && f.JSONSchema != "" {
		var raw any
		if err := json.Unmarshal([]byte(f.JSONSchema), &raw); err == nil {
			if m, ok := raw.(map[string]any); ok {
				return orderedFromJSONMap(m)
			}
		}
	}

	var baseType any
	obj := orderedObject{}
	switch f.Type {
	case TypeText:
		baseType = jsonTypeValue("string", nullable)
		obj.set("type", baseType)
		if f.Min != nil {
			obj.set("minLength", int64(math.Trunc(*f.Min)))
		}
		if f.Max != nil {
			obj.set("maxLength", int64(math.Trunc(*f.Max)))
		}
		if f.Pattern != "" {
			obj.set("pattern", f.Pattern)
		}
	case TypeInteger:
		obj.set("type", jsonTypeValue("integer", nullable))
		obj.set("format", "int64")
		if f.Min != nil {
			obj.set("minimum", int64(math.Trunc(*f.Min)))
		}
		if f.Max != nil {
			obj.set("maximum", int64(math.Trunc(*f.Max)))
		}
	case TypeReal:
		obj.set("type", jsonTypeValue("number", nullable))
		obj.set("format", "double")
		if f.Min != nil {
			obj.set("minimum", *f.Min)
		}
		if f.Max != nil {
			obj.set("maximum", *f.Max)
		}
	case TypeBoolean:
		obj.set("type", jsonTypeValue("boolean", nullable))
	case TypeTimestamp:
		obj.set("type", jsonTypeValue("string", nullable))
		obj.set("format", "date-time")
	case TypeUUID:
		obj.set("type", jsonTypeValue("string", nullable))
		obj.set("format", "uuid")
	case TypeEnum:
		obj.set("type", jsonTypeValue("string", nullable))
		obj.set("enum", f.Enum)
	case TypeJSON:
		obj.set("type", []string{"object", "array"})
	case TypeBlob:
		obj.set("type", jsonTypeValue("string", nullable))
		obj.set("contentEncoding", "base64")
	default:
		obj.set("type", jsonTypeValue("string", nullable))
	}

	if f.Default != nil {
		obj.set("default", f.Default)
	}
	if f.Ref != nil {
		obj.set("x-references", f.Ref.Table+"."+f.Ref.Field)
	}
	return obj
}

func orderedFromJSONMap(m map[string]any) orderedObject {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Deterministic key order for embedded JSON Schema maps.
	slices.Sort(keys)
	out := make(orderedObject, 0, len(keys))
	for _, k := range keys {
		out = append(out, orderedField{Key: k, Value: m[k]})
	}
	return out
}

func jsonTypeValue(typ string, nullable bool) any {
	if nullable {
		return []string{typ, "null"}
	}
	return typ
}

func listOperation(t *Table, baseName, listName string) orderedObject {
	params := []orderedObject{
		queryParam("filter", "Filter expression", "string"),
		queryParam("sort", "Comma-separated sort fields (prefix '-' for descending)", "string"),
		queryParam("limit", "Maximum records per page", "integer"),
		queryParam("cursor", "Opaque pagination cursor", "string"),
		queryParam("fields", "Comma-separated fields to project", "string"),
		queryParam("include", "Comma-separated relations to include", "string"),
	}
	op := orderedObject{
		{Key: "operationId", Value: "list" + baseName},
		{Key: "summary", Value: "List " + t.Name + " records"},
		{Key: "tags", Value: []string{t.Name}},
		{Key: "parameters", Value: params},
		{Key: "responses", Value: orderedObject{
			{Key: "200", Value: schemaResponse("Paginated records", refSchema(listName))},
			{Key: "400", Value: errorRefResponse("Invalid query parameters")},
			{Key: "401", Value: errorRefResponse("Unauthorized")},
			{Key: "403", Value: errorRefResponse("Forbidden")},
		}},
	}
	return withSecurity(op, t, ActionList)
}

func createOperation(t *Table, baseName, inputName, recordName string) orderedObject {
	op := orderedObject{
		{Key: "operationId", Value: "create" + baseName},
		{Key: "summary", Value: "Create a " + t.Name + " record"},
		{Key: "tags", Value: []string{t.Name}},
		{Key: "requestBody", Value: orderedObject{
			{Key: "required", Value: true},
			{Key: "content", Value: jsonContent(refSchema(inputName))},
		}},
		{Key: "responses", Value: orderedObject{
			{Key: "201", Value: schemaResponse("Created record", refSchema(recordName))},
			{Key: "400", Value: errorRefResponse("Invalid request")},
			{Key: "401", Value: errorRefResponse("Unauthorized")},
			{Key: "403", Value: errorRefResponse("Forbidden")},
			{Key: "422", Value: errorRefResponse("Validation failed")},
		}},
	}
	return withSecurity(op, t, ActionCreate)
}

func readOperation(t *Table, baseName, recordName string) orderedObject {
	params := []orderedObject{
		pathIDParam(),
		queryParam("fields", "Comma-separated fields to project", "string"),
		queryParam("include", "Comma-separated relations to include", "string"),
	}
	op := orderedObject{
		{Key: "operationId", Value: "get" + baseName},
		{Key: "summary", Value: "Get a " + t.Name + " record"},
		{Key: "tags", Value: []string{t.Name}},
		{Key: "parameters", Value: params},
		{Key: "responses", Value: orderedObject{
			{Key: "200", Value: schemaResponse("Record", refSchema(recordName))},
			{Key: "401", Value: errorRefResponse("Unauthorized")},
			{Key: "403", Value: errorRefResponse("Forbidden")},
			{Key: "404", Value: errorRefResponse("Not found")},
		}},
	}
	return withSecurity(op, t, ActionRead)
}

func updateOperation(t *Table, baseName, patchName, recordName string) orderedObject {
	op := orderedObject{
		{Key: "operationId", Value: "update" + baseName},
		{Key: "summary", Value: "Update a " + t.Name + " record"},
		{Key: "tags", Value: []string{t.Name}},
		{Key: "parameters", Value: []orderedObject{pathIDParam()}},
		{Key: "requestBody", Value: orderedObject{
			{Key: "required", Value: true},
			{Key: "content", Value: jsonContent(refSchema(patchName))},
		}},
		{Key: "responses", Value: orderedObject{
			{Key: "200", Value: schemaResponse("Updated record", refSchema(recordName))},
			{Key: "400", Value: errorRefResponse("Invalid request")},
			{Key: "401", Value: errorRefResponse("Unauthorized")},
			{Key: "403", Value: errorRefResponse("Forbidden")},
			{Key: "404", Value: errorRefResponse("Not found")},
			{Key: "409", Value: errorRefResponse("Version conflict")},
			{Key: "422", Value: errorRefResponse("Validation failed")},
		}},
	}
	return withSecurity(op, t, ActionUpdate)
}

func deleteOperation(t *Table, baseName string) orderedObject {
	op := orderedObject{
		{Key: "operationId", Value: "delete" + baseName},
		{Key: "summary", Value: "Delete a " + t.Name + " record"},
		{Key: "tags", Value: []string{t.Name}},
		{Key: "parameters", Value: []orderedObject{pathIDParam()}},
		{Key: "responses", Value: orderedObject{
			{Key: "204", Value: orderedObject{{Key: "description", Value: "Deleted"}}},
			{Key: "401", Value: errorRefResponse("Unauthorized")},
			{Key: "403", Value: errorRefResponse("Forbidden")},
			{Key: "404", Value: errorRefResponse("Not found")},
		}},
	}
	return withSecurity(op, t, ActionDelete)
}

func withSecurity(op orderedObject, t *Table, action Action) orderedObject {
	if strings.TrimSpace(t.Permissions[action]) == "true" {
		return op
	}
	op.set("security", []orderedObject{
		{{Key: "bearerAuth", Value: []string{}}},
	})
	return op
}

func pathIDParam() orderedObject {
	return orderedObject{
		{Key: "name", Value: "id"},
		{Key: "in", Value: "path"},
		{Key: "required", Value: true},
		{Key: "schema", Value: orderedObject{{Key: "type", Value: "string"}}},
	}
}

func queryParam(name, desc, typ string) orderedObject {
	return orderedObject{
		{Key: "name", Value: name},
		{Key: "in", Value: "query"},
		{Key: "description", Value: desc},
		{Key: "schema", Value: orderedObject{{Key: "type", Value: typ}}},
	}
}

func jsonContent(schema any) orderedObject {
	return orderedObject{
		{Key: "application/json", Value: orderedObject{
			{Key: "schema", Value: schema},
		}},
	}
}

func schemaResponse(desc string, schema any) orderedObject {
	return orderedObject{
		{Key: "description", Value: desc},
		{Key: "content", Value: jsonContent(schema)},
	}
}

func errorRefResponse(desc string) orderedObject {
	return schemaResponse(desc, refSchema("ErrorResponse"))
}

func refSchema(name string) orderedObject {
	return orderedObject{
		{Key: "$ref", Value: "#/components/schemas/" + name},
	}
}
