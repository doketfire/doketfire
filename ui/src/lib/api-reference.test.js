import { describe, expect, it } from 'bun:test'
import {
  buildApiEndpoints,
  documentStructure,
  patchSample,
  sampleBody,
  sampleResponse,
} from './api-reference.js'

const blogFields = [
  { name: 'titulo', type: 'string', required: true, maxLen: 120 },
  { name: 'contenido', type: 'string' },
  { name: 'publicado', type: 'bool' },
  { name: 'views', type: 'number' },
  { name: 'etiquetas', type: 'array' },
  { name: 'meta', type: 'object' },
  { name: 'portada', type: 'file' },
]

const authFields = [
  { name: 'nombre', type: 'string' },
  { name: 'email', type: 'string', required: true },
  { name: 'passwordHash', type: 'string' },
  { name: 'verified', type: 'bool' },
  { name: 'tokenKey', type: 'string' },
]

describe('cuerpos de ejemplo', () => {
  it('genera un valor representativo por tipo de campo', () => {
    expect(sampleBody(blogFields)).toEqual({
      titulo: 'texto de ejemplo',
      contenido: 'texto de ejemplo',
      publicado: false,
      views: 0,
      etiquetas: ['uno', 'dos'],
      meta: { clave: 'valor' },
      portada: '',
    })
  })

  it('ignora los campos del sistema y cae en un mensaje si no hay campos', () => {
    const body = sampleBody([{ name: 'id', type: 'string' }, { name: 'created', type: 'string' }])
    expect(body).toEqual({ mensaje: 'Hola mundo' })
    expect(sampleBody()).toEqual({ mensaje: 'Hola mundo' })
  })

  it('usa solo el primer campo editable para un PATCH', () => {
    expect(patchSample(blogFields)).toEqual({ titulo: 'nuevo valor' })
    expect(patchSample([{ name: 'views', type: 'number' }])).toEqual({ views: 0 })
    expect(patchSample([])).toEqual({ mensaje: 'nuevo valor' })
  })
})

describe('estructura del documento', () => {
  it('empieza por los campos del sistema y conserva tipos y límites', () => {
    const rows = documentStructure(blogFields)
    expect(rows.slice(0, 3).map((row) => row.name)).toEqual(['id', 'created', 'updated'])
    expect(rows.find((row) => row.name === 'titulo')).toEqual({
      name: 'titulo',
      type: 'string',
      required: true,
      note: 'máx. 120',
    })
    expect(rows.find((row) => row.name === 'views')?.required).toBe(false)
  })

  it('no duplica email ni verified en colecciones auth', () => {
    const rows = documentStructure(authFields, 'auth')
    const names = rows.map((row) => row.name)
    expect(names.filter((name) => name === 'email')).toHaveLength(1)
    expect(names.filter((name) => name === 'verified')).toHaveLength(1)
    expect(new Set(names).size).toBe(names.length)
    expect(rows.find((row) => row.name === 'verified')?.note).toBe('Solo lectura')
  })

  it('añade email y verified cuando la colección auth no los declara', () => {
    const rows = documentStructure([{ name: 'nombre', type: 'string' }], 'auth')
    const names = rows.map((row) => row.name)
    expect(names).toContain('email')
    expect(names).toContain('verified')
  })

  it('no añade campos auth a colecciones base', () => {
    const names = documentStructure(blogFields).map((row) => row.name)
    expect(names).not.toContain('email')
    expect(names).not.toContain('verified')
  })
})

describe('respuesta de ejemplo', () => {
  it('incluye id, created, updated y los campos del documento', () => {
    const response = sampleResponse([{ name: 'titulo', type: 'string' }])
    expect(response.id).toBeTruthy()
    expect(response.created).toBeTruthy()
    expect(response.updated).toBeTruthy()
    expect(response.titulo).toBe('texto de ejemplo')
  })

  it('usa un correo legible en colecciones auth', () => {
    const response = sampleResponse(authFields, 'auth')
    expect(response.email).toBe('usuario@ejemplo.com')
    expect(response.verified).toBe(false)
  })
})

describe('endpoints de la API', () => {
  const baseOptions = {
    collection: 'articulos',
    baseURL: 'http://127.0.0.1:8090',
    fields: blogFields,
  }

  it('documenta los ocho endpoints base con ejemplos en cURL y JavaScript', () => {
    const endpoints = buildApiEndpoints(baseOptions)
    expect(endpoints.map((endpoint) => endpoint.id)).toEqual([
      'list',
      'one',
      'create',
      'patch',
      'put',
      'delete',
      'batch',
      'aggregate',
    ])
    for (const endpoint of endpoints) {
      expect(endpoint.method).toBeTruthy()
      expect(endpoint.title).toBeTruthy()
      expect(endpoint.desc).toBeTruthy()
      expect(endpoint.examples.curl).toContain('http://127.0.0.1:8090')
      expect(endpoint.examples.js).toContain('fetch(')
    }
  })

  it('añade login y registro solo a colecciones auth', () => {
    expect(buildApiEndpoints(baseOptions).some((endpoint) => endpoint.group === 'auth')).toBe(false)

    const auth = buildApiEndpoints({ ...baseOptions, type: 'auth', fields: authFields })
    expect(auth.filter((endpoint) => endpoint.group === 'auth').map((endpoint) => endpoint.id)).toEqual([
      'login',
      'register',
    ])
    expect(auth.find((endpoint) => endpoint.id === 'login')?.path).toBe(
      '/api/collections/articulos/auth-with-password'
    )
  })

  it('codifica el nombre de la colección en las rutas', () => {
    const endpoints = buildApiEndpoints({ ...baseOptions, collection: 'mi colección' })
    expect(endpoints[0].path).toBe('/api/collections/mi%20colecci%C3%B3n/records')
  })

  it('incluye los campos reales en el ejemplo de creación', () => {
    const create = buildApiEndpoints(baseOptions).find((endpoint) => endpoint.id === 'create')
    expect(create.examples.curl).toContain('"titulo":"texto de ejemplo"')
    expect(create.examples.js).toContain('"views":0')
  })

  it('devuelve una lista vacía si falta la colección o la URL base', () => {
    expect(buildApiEndpoints({ baseURL: 'http://localhost' })).toEqual([])
    expect(buildApiEndpoints({ collection: 'x' })).toEqual([])
  })
})
