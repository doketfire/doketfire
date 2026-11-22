/**
 * Referencia REST de una colección.
 *
 * Genera la estructura del documento y los ejemplos de la pestaña API.
 * Son funciones puras (sin DOM ni red) para poder probarlas con `bun test`.
 */

/** Campos que gestiona el servidor o que no forman parte del JSON público. */
export const SYSTEM_FIELDS = new Set([
  'id',
  'created',
  'updated',
  'passwordHash',
  'tokenKey',
  'verified',
])

/** Campos de sistema que siempre aparecen en la estructura del documento. */
const SYSTEM_STRUCTURE = [
  { name: 'id', type: 'string (ULID)', required: false, note: 'Generado por el servidor' },
  { name: 'created', type: 'string (ISO 8601)', required: false, note: 'Generado por el servidor' },
  { name: 'updated', type: 'string (ISO 8601)', required: false, note: 'Generado por el servidor' },
]

const AUTH_STRUCTURE = [
  { name: 'email', type: 'string', required: false, note: 'Cuenta de la colección' },
  { name: 'verified', type: 'bool', required: false, note: 'Solo lectura' },
]

/** Valor de ejemplo para un campo según su tipo. */
export function exampleValue(type) {
  switch (type) {
    case 'number':
      return 0
    case 'bool':
      return false
    case 'array':
      return ['uno', 'dos']
    case 'object':
      return { clave: 'valor' }
    case 'file':
      return ''
    default:
      return 'texto de ejemplo'
  }
}

/** Campos que el usuario puede enviar (excluye los del sistema). */
function userFields(fields) {
  return (fields || []).filter((field) => field?.name && !SYSTEM_FIELDS.has(field.name))
}

/** Cuerpo de ejemplo para crear o reemplazar un documento. */
export function sampleBody(fields = []) {
  const body = {}
  for (const field of userFields(fields)) {
    body[field.name] = exampleValue(field.type)
  }
  return Object.keys(body).length ? body : { mensaje: 'Hola mundo' }
}

/** Cuerpo de ejemplo para un PATCH: solo el primer campo editable. */
export function patchSample(fields = []) {
  const first = userFields(fields)[0]
  if (!first) return { mensaje: 'nuevo valor' }
  if (first.type === 'string' || first.type === undefined) {
    return { [first.name]: 'nuevo valor' }
  }
  return { [first.name]: exampleValue(first.type) }
}

/** Filas de la tabla "Estructura del documento". */
export function documentStructure(fields = [], type = 'base') {
  const rows = SYSTEM_STRUCTURE.map((row) => ({ ...row }))
  const names = new Set(rows.map((row) => row.name))

  for (const field of userFields(fields)) {
    if (names.has(field.name)) continue
    names.add(field.name)
    rows.push({
      name: field.name,
      type: field.type || 'any',
      required: Boolean(field.required),
      note: field.maxLen ? `máx. ${field.maxLen}` : '',
    })
  }

  if (type === 'auth') {
    for (const row of AUTH_STRUCTURE) {
      if (names.has(row.name)) continue
      names.add(row.name)
      rows.push({ ...row })
    }
  }

  return rows
}

/** Documento de ejemplo tal como lo devuelve la API. */
export function sampleResponse(fields = [], type = 'base') {
  const body = {
    id: '01J8Z9XK2M4N6P8Q0R2S4T6V8',
    created: '2026-01-01T12:00:00.000Z',
    updated: '2026-01-01T12:00:00.000Z',
    ...sampleBody(fields),
  }
  if (type === 'auth') {
    body.email = 'usuario@ejemplo.com'
    body.verified = false
  }
  return body
}

/** Documentación de los endpoints base de una colección. */
function baseEndpoints({ collection, baseURL, bodyInline, patchInline }) {
  const col = encodeURIComponent(collection)
  const listPath = `/api/collections/${col}/records`
  const onePath = `${listPath}/{id}`
  const listURL = `${baseURL}${listPath}`
  const oneURL = `${baseURL}${onePath}`
  const headers = `-H 'Authorization: Bearer TU_TOKEN' -H 'Content-Type: application/json'`

  return [
    {
      id: 'list',
      method: 'GET',
      title: 'Listar documentos',
      path: listPath,
      desc: 'Devuelve una página de documentos. Funciona sin token solo si la regla de lista es pública.',
      params: [
        ['filter', 'Filtro, p. ej. titulo = "Hola"'],
        ['sort', 'Orden: campo, -campo (máximo 3)'],
        ['limit', 'Tamaño de página (por defecto 50, máximo 500)'],
        ['cursor', 'Cursor de la página siguiente'],
        ['fields', 'Proyección: id, titulo, autor'],
        ['include', 'Referencias: autor.nombre'],
        ['view', 'Vista guardada por nombre'],
      ],
      open: true,
      examples: {
        curl: `curl --get '${listURL}' -H 'Authorization: Bearer TU_TOKEN' --data-urlencode 'limit=25' --data-urlencode 'sort=-created'`,
        js: `const res = await fetch('${listURL}?limit=25&sort=-created', {\n  headers: { Authorization: 'Bearer TU_TOKEN' }\n})\nconst { items, nextCursor } = await res.json()`,
      },
    },
    {
      id: 'one',
      method: 'GET',
      title: 'Obtener un documento',
      path: onePath,
      desc: 'Devuelve un documento por su ID. Requiere la regla de ver detalle.',
      examples: {
        curl: `curl '${oneURL}' -H 'Authorization: Bearer TU_TOKEN'`,
        js: `const res = await fetch('${oneURL}', {\n  headers: { Authorization: 'Bearer TU_TOKEN' }\n})\nconst record = await res.json()`,
      },
    },
    {
      id: 'create',
      method: 'POST',
      title: 'Crear documento',
      path: listPath,
      desc: 'Crea un documento. El servidor genera id, created y updated.',
      examples: {
        curl: `curl -X POST '${listURL}' ${headers} -d '${bodyInline}'`,
        js: `const res = await fetch('${listURL}', {\n  method: 'POST',\n  headers: { 'Content-Type': 'application/json', Authorization: 'Bearer TU_TOKEN' },\n  body: JSON.stringify(${bodyInline})\n})\nconst record = await res.json()`,
      },
    },
    {
      id: 'patch',
      method: 'PATCH',
      title: 'Actualizar documento',
      path: onePath,
      desc: 'Mezcla los campos enviados. Envía null para borrar un campo; los arrays se reemplazan.',
      examples: {
        curl: `curl -X PATCH '${oneURL}' ${headers} -d '${patchInline}'`,
        js: `const res = await fetch('${oneURL}', {\n  method: 'PATCH',\n  headers: { 'Content-Type': 'application/json', Authorization: 'Bearer TU_TOKEN' },\n  body: JSON.stringify(${patchInline})\n})\nconst record = await res.json()`,
      },
    },
    {
      id: 'put',
      method: 'PUT',
      title: 'Reemplazar documento',
      path: onePath,
      desc: 'Reemplaza el documento completo conservando id y created.',
      examples: {
        curl: `curl -X PUT '${oneURL}' ${headers} -d '${bodyInline}'`,
        js: `const res = await fetch('${oneURL}', {\n  method: 'PUT',\n  headers: { 'Content-Type': 'application/json', Authorization: 'Bearer TU_TOKEN' },\n  body: JSON.stringify(${bodyInline})\n})\nconst record = await res.json()`,
      },
    },
    {
      id: 'delete',
      method: 'DELETE',
      title: 'Eliminar documento',
      path: onePath,
      desc: 'Borra el documento y, tras confirmar, sus archivos subidos.',
      examples: {
        curl: `curl -X DELETE '${oneURL}' -H 'Authorization: Bearer TU_TOKEN'`,
        js: `await fetch('${oneURL}', {\n  method: 'DELETE',\n  headers: { Authorization: 'Bearer TU_TOKEN' }\n})`,
      },
    },
    {
      id: 'batch',
      method: 'POST',
      title: 'Lote de operaciones',
      path: '/api/batch',
      desc: 'Hasta 100 operaciones en una sola transacción; si una falla, se revierte todo.',
      examples: {
        curl: `curl -X POST '${baseURL}/api/batch' ${headers} -d '{"operations":[{"method":"POST","collection":"${collection}","record":${bodyInline}}]}'`,
        js: `const res = await fetch('${baseURL}/api/batch', {\n  method: 'POST',\n  headers: { 'Content-Type': 'application/json', Authorization: 'Bearer TU_TOKEN' },\n  body: JSON.stringify({\n    operations: [\n      { method: 'POST', collection: '${collection}', record: ${bodyInline} }\n    ]\n  })\n})`,
      },
    },
    {
      id: 'aggregate',
      method: 'POST',
      title: 'Agregaciones',
      path: `/api/collections/${col}/aggregate`,
      desc: 'Cuenta documentos o suma un campo numérico, con filtro opcional.',
      examples: {
        curl: `curl -X POST '${baseURL}/api/collections/${col}/aggregate' ${headers} -d '{"op":"count"}'`,
        js: `const res = await fetch('${baseURL}/api/collections/${col}/aggregate', {\n  method: 'POST',\n  headers: { 'Content-Type': 'application/json', Authorization: 'Bearer TU_TOKEN' },\n  body: JSON.stringify({ op: 'count' })\n})\nconst { result } = await res.json()`,
      },
    },
  ]
}

/** Endpoints extra de las colecciones de tipo auth. */
function authEndpoints({ collection, baseURL }) {
  const col = encodeURIComponent(collection)
  const loginURL = `${baseURL}/api/collections/${col}/auth-with-password`
  const registerURL = `${baseURL}/api/collections/${col}/auth-register`

  return [
    {
      id: 'login',
      group: 'auth',
      method: 'POST',
      title: 'Iniciar sesión (usuario)',
      path: `/api/collections/${col}/auth-with-password`,
      desc: 'Devuelve token, refreshToken y el registro del usuario.',
      examples: {
        curl: `curl -X POST '${loginURL}' -H 'Content-Type: application/json' -d '{"identity":"usuario@ejemplo.com","password":"tu-password"}'`,
        js: `const res = await fetch('${loginURL}', {\n  method: 'POST',\n  headers: { 'Content-Type': 'application/json' },\n  body: JSON.stringify({ identity: 'usuario@ejemplo.com', password: 'tu-password' })\n})\nconst { token, record } = await res.json()`,
      },
    },
    {
      id: 'register',
      group: 'auth',
      method: 'POST',
      title: 'Registrar usuario',
      path: `/api/collections/${col}/auth-register`,
      desc: 'Crea una cuenta nueva en esta colección de usuarios.',
      examples: {
        curl: `curl -X POST '${registerURL}' -H 'Content-Type: application/json' -d '{"email":"nuevo@ejemplo.com","password":"una-password-larga"}'`,
        js: `const res = await fetch('${registerURL}', {\n  method: 'POST',\n  headers: { 'Content-Type': 'application/json' },\n  body: JSON.stringify({ email: 'nuevo@ejemplo.com', password: 'una-password-larga' })\n})\nconst { token, record } = await res.json()`,
      },
    },
  ]
}

/**
 * Lista completa de endpoints documentados para una colección.
 * @param {{ collection: string, type?: string, baseURL: string, fields?: Array }} options
 */
export function buildApiEndpoints({ collection, type = 'base', baseURL, fields = [] } = {}) {
  if (!collection || !baseURL) return []
  const shared = {
    collection,
    baseURL,
    bodyInline: JSON.stringify(sampleBody(fields)),
    patchInline: JSON.stringify(patchSample(fields)),
  }
  const endpoints = baseEndpoints(shared)
  if (type === 'auth') endpoints.push(...authEndpoints(shared))
  return endpoints
}
