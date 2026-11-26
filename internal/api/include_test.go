package api

import (
	"fmt"
	"net/http"
	"testing"
)

// TestIncludeJoin verifica el caso A del diseño: un post con autor y proyecto,
// varios includes en la misma petición (parámetros repetidos).
func TestIncludeJoin(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	api.createCollection(publicBaseCollection("posts"))
	api.createCollection(publicBaseCollection("author"))
	api.createCollection(publicBaseCollection("project"))

	author := api.json("POST", "/api/collections/author/records", map[string]any{
		"name": "Ana García",
	}, "", http.StatusCreated)
	authorID, _ := author["id"].(string)
	if authorID == "" {
		t.Fatalf("missing author id: %v", author)
	}

	project := api.json("POST", "/api/collections/project/records", map[string]any{
		"title": "Doketfire Blog",
	}, "", http.StatusCreated)
	projectID, _ := project["id"].(string)
	if projectID == "" {
		t.Fatalf("missing project id: %v", project)
	}

	api.json("POST", "/api/collections/posts/records", map[string]any{
		"title": "Hola mundo", "author": authorID, "project": projectID,
	}, "", http.StatusCreated)

	// Caso A: dos includes con parámetros repetidos.
	list := api.json("GET", "/api/collections/posts/records?include=author.name&include=project.title", nil, "", http.StatusOK)
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item: %v", list)
	}
	item, _ := items[0].(map[string]any)
	if item["author.name"] != "Ana García" {
		t.Fatalf("expected author.name = Ana García, got %v (item: %v)", item["author.name"], item)
	}
	if item["project.title"] != "Doketfire Blog" {
		t.Fatalf("expected project.title = Doketfire Blog, got %v (item: %v)", item["project.title"], item)
	}
	if item["author"] != authorID {
		t.Fatalf("expected author reference to remain %v, got %v", authorID, item["author"])
	}
	if item["title"] != "Hola mundo" {
		t.Fatalf("expected original field title to remain, got %v", item["title"])
	}
}

// TestIncludeJoinSingleAndComma verifica el caso B del diseño (un solo include)
// y que también se acepten includes separados por coma en un único parámetro.
func TestIncludeJoinSingleAndComma(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	api.createCollection(publicBaseCollection("posts"))
	api.createCollection(publicBaseCollection("author"))

	author := api.json("POST", "/api/collections/author/records", map[string]any{
		"name": "Ana García",
	}, "", http.StatusCreated)
	authorID, _ := author["id"].(string)

	api.json("POST", "/api/collections/posts/records", map[string]any{
		"title": "Caso B", "author": authorID,
	}, "", http.StatusCreated)

	// Caso B: un solo include.
	list := api.json("GET", "/api/collections/posts/records?include=author.name", nil, "", http.StatusOK)
	items, _ := list["items"].([]any)
	item, _ := items[0].(map[string]any)
	if item["author.name"] != "Ana García" {
		t.Fatalf("single include: expected Ana García, got %v", item["author.name"])
	}

	// Varios includes separados por coma en un único parámetro.
	list = api.json("GET", "/api/collections/posts/records?include=author.name,title", nil, "", http.StatusOK)
	items, _ = list["items"].([]any)
	item, _ = items[0].(map[string]any)
	if item["author.name"] != "Ana García" {
		t.Fatalf("comma include: expected Ana García, got %v", item["author.name"])
	}
}

// TestIncludeJoinMissingReference cubre el caso edge: referencia inexistente → null.
func TestIncludeJoinMissingReference(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	api.createCollection(publicBaseCollection("posts"))
	api.createCollection(publicBaseCollection("author"))

	api.json("POST", "/api/collections/posts/records", map[string]any{
		"title": "Sin autor", "author": "01JX0000000000000000000000",
	}, "", http.StatusCreated)

	list := api.json("GET", "/api/collections/posts/records?include=author.name", nil, "", http.StatusOK)
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item: %v", list)
	}
	item, _ := items[0].(map[string]any)
	if v, ok := item["author.name"]; !ok || v != nil {
		t.Fatalf("expected author.name = null for missing reference, got %v (present=%v)", v, ok)
	}
}

// TestIncludeJoinSystemFieldsBlocked verifica la consideración 3 del diseño:
// los campos de sistema nunca se exponen a través de ?include=.
func TestIncludeJoinSystemFieldsBlocked(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	api.createCollection(publicBaseCollection("posts"))
	api.createCollection(publicBaseCollection("author"))

	author := api.json("POST", "/api/collections/author/records", map[string]any{
		"name": "Ana García", "email": "ana@example.com", "tokenKey": "super-secret", "passwordHash": "hash",
	}, "", http.StatusCreated)
	authorID, _ := author["id"].(string)

	api.json("POST", "/api/collections/posts/records", map[string]any{
		"title": "Seguro", "author": authorID,
	}, "", http.StatusCreated)

	list := api.json("GET", "/api/collections/posts/records?include=author.tokenKey&include=author.email&include=author.passwordHash&include=author.id", nil, "", http.StatusOK)
	items, _ := list["items"].([]any)
	item, _ := items[0].(map[string]any)
	for _, field := range []string{"author.tokenKey", "author.email", "author.passwordHash", "author.id"} {
		if v, ok := item[field]; ok && v != nil {
			t.Fatalf("system field %q leaked via include: %v", field, v)
		}
	}
}

// TestIncludeJoinRespectsViewRules verifica que un ?include= no pueda filtrar
// documentos de una colección cuyo regla "view" no permite al solicitante.
func TestIncludeJoinRespectsViewRules(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()

	// posts es pública; author solo permite ver a usuarios autenticados.
	api.createCollection(publicBaseCollection("posts"))
	authorDef := publicBaseCollection("author")
	authorDef["rules"] = map[string]any{
		"list": "", "view": "request.auth != null", "create": "", "update": "", "delete": "",
	}
	api.createCollection(authorDef)

	author := api.json("POST", "/api/collections/author/records", map[string]any{
		"name": "Secreto",
	}, api.token, http.StatusCreated)
	authorID, _ := author["id"].(string)

	api.json("POST", "/api/collections/posts/records", map[string]any{
		"title": "Pública", "author": authorID,
	}, "", http.StatusCreated)

	// Anónimo: la regla view de author falla → el campo incluido queda a nil.
	anon := api.json("GET", "/api/collections/posts/records?include=author.name", nil, "", http.StatusOK)
	items, _ := anon["items"].([]any)
	item, _ := items[0].(map[string]any)
	if v, ok := item["author.name"]; ok && v != nil {
		t.Fatalf("include leaked a document hidden by the view rule: %v", v)
	}

	// Admin: puede ver el autor → el campo se resuelve.
	admin := api.json("GET", "/api/collections/posts/records?include=author.name", nil, api.token, http.StatusOK)
	items, _ = admin["items"].([]any)
	item, _ = items[0].(map[string]any)
	if item["author.name"] != "Secreto" {
		t.Fatalf("admin should resolve include, got %v", item["author.name"])
	}
}

// TestIncludeJoinWithPagination cubre el último item del checklist: el include
// debe aplicarse a cada página de resultados, no solo a la primera.
func TestIncludeJoinWithPagination(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	api.createCollection(publicBaseCollection("posts"))
	api.createCollection(publicBaseCollection("author"))

	author := api.json("POST", "/api/collections/author/records", map[string]any{
		"name": "Ana García",
	}, "", http.StatusCreated)
	authorID, _ := author["id"].(string)

	// Crear 3 posts que referencian al mismo autor.
	for i := 0; i < 3; i++ {
		api.json("POST", "/api/collections/posts/records", map[string]any{
			"title": fmt.Sprintf("Post %d", i), "author": authorID,
		}, "", http.StatusCreated)
	}

	cursor := ""
	total := 0
	for page := 0; page < 5; page++ {
		path := "/api/collections/posts/records?limit=1&include=author.name"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		list := api.json("GET", path, nil, "", http.StatusOK)
		items, _ := list["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("page %d: expected 1 item, got %d", page, len(items))
		}
		item, _ := items[0].(map[string]any)
		if item["author.name"] != "Ana García" {
			t.Fatalf("page %d: include not resolved: %v", page, item)
		}
		total++

		next, _ := list["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if total != 3 {
		t.Fatalf("expected 3 paginated items with include, got %d", total)
	}
}

// TestIncludeJoinWithFields verifica que ?fields e ?include puedan combinarse:
// la proyección recorta el documento principal pero el include se resuelve desde
// el original y se añade al resultado.
func TestIncludeJoinWithFields(t *testing.T) {
	api := newTestAPI(t)
	api.setupAdmin()
	api.createCollection(publicBaseCollection("posts"))
	api.createCollection(publicBaseCollection("author"))

	author := api.json("POST", "/api/collections/author/records", map[string]any{
		"name": "Ana García",
	}, "", http.StatusCreated)
	authorID, _ := author["id"].(string)

	api.json("POST", "/api/collections/posts/records", map[string]any{
		"title": "Combinado", "author": authorID, "extra": "se corta",
	}, "", http.StatusCreated)

	list := api.json("GET", "/api/collections/posts/records?fields=title&include=author.name", nil, "", http.StatusOK)
	items, _ := list["items"].([]any)
	item, _ := items[0].(map[string]any)
	if item["title"] != "Combinado" {
		t.Fatalf("expected title to remain: %v", item)
	}
	if _, ok := item["extra"]; ok {
		t.Fatalf("expected extra to be projected out: %v", item)
	}
	if item["author.name"] != "Ana García" {
		t.Fatalf("include should resolve even with fields projection: %v", item)
	}
}
