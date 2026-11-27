# Blog Example

Ejemplo declarativo de `doketfire.yaml` para un blog con `posts`, `comments` y referencias a `users` (`_dk_users`).

## Qué define

- **`posts`**: título, slug único con regex, contenido, flag `published`, referencia `author_id -> users.id`, estado `enum` (`draft` / `published`), índices compuestos y parciales, reglas CEL por acción y `realtime: true`.
- **`comments`**: referencia `post_id -> posts.id`, referencia `author_id -> users.id`, cuerpo validado (`1..2000`), índices por post/fecha y autor, y reglas CEL.
- **Tabla `users`**: en `doketfire.yaml` no se declara `tables.users`; el alias `references: users.id` apunta a la tabla del sistema `_dk_users`.

## Estado de ejecución

Este archivo ya es validado por `internal/schema` (`Parse`, `Validate`, `Build`, `OpenAPI` y `BuildPanelMeta`). Los comandos CLI declarativos (`doketfire init`, `doketfire apply`, `doketfire serve` sobre SQLite) forman parte de las siguientes épicas en `docs/TODO.md`.
