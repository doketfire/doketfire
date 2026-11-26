/**
 * Cliente HTTP del panel.
 *
 * - Adjunta el Bearer del access token guardado en sessionStorage/espejo en memoria.
 * - Anima una única renovación concurrentemente (cola de refresh): todas las
 *   peticiones que reciben un 401 esperan la misma promesa y reintentan una vez.
 * - Permite abortar peticiones obsoletas mediante `key` o una señal externa.
 * - Normaliza los errores del servidor `{error:{code,message,details}}`.
 */
import { clearSession, getSession, saveSession } from './session.js'

export class ApiError extends Error {
  constructor(message, { status = 0, code = 'unknown', details = null, requestId = '', cause = null } = {}) {
    super(message || 'Error inesperado')
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details
    this.requestId = requestId
    if (cause) this.cause = cause
  }
}

/** Devuelve un mensaje legible para cualquier error lanzado por el cliente. */
export function errorMessage(error) {
  return error instanceof Error ? error.message : String(error)
}

let fetchImpl = (...args) => globalThis.fetch(...args)
let refreshPromise = null
const activeByKey = new Map()

/** Permite inyectar `fetch` en las pruebas unitarias. */
export function setFetchImplementation(fn) {
  fetchImpl = typeof fn === 'function' ? fn : (...args) => globalThis.fetch(...args)
}

/** Reinicia estado interno (solo para pruebas). */
export function __resetApiState() {
  if (refreshPromise) refreshPromise.catch(() => {})
  refreshPromise = null
  for (const controller of activeByKey.values()) controller.abort()
  activeByKey.clear()
}

/** Construye una URL relativa con parámetros, respetando los ya presentes. */
export function buildURL(path, params = {}) {
  const absolute = /^[a-z][a-z\d+.-]*:\/\//i.test(path)
  const url = new URL(path, absolute ? undefined : 'http://doketfire.local')
  for (const [key, value] of Object.entries(params || {})) {
    if (value === undefined || value === null || value === '') continue
    if (Array.isArray(value)) {
      for (const item of value) url.searchParams.append(key, String(item))
    } else {
      url.searchParams.set(key, String(value))
    }
  }
  return absolute ? url.href : `${url.pathname}${url.search}${url.hash}`
}

/** Devuelve la URL de la siguiente página usando el cursor del servidor. */
export function withCursor(path, params = {}, cursor = '') {
  return buildURL(path, { ...params, cursor: cursor || undefined })
}

function isBodyInit(body) {
  return (
    body instanceof FormData ||
    body instanceof Blob ||
    body instanceof URLSearchParams ||
    typeof body === 'string' ||
    (typeof ArrayBuffer !== 'undefined' && (body instanceof ArrayBuffer || ArrayBuffer.isView(body)))
  )
}

function dispatchSessionExpired() {
  try {
    globalThis.dispatchEvent?.(new CustomEvent('dp:session-expired'))
  } catch {
    // Entorno sin DOM (pruebas): el estado local ya se ha limpiado.
  }
}

async function readBody(res) {
  const text = await res.text()
  if (!text) return null
  try {
    return JSON.parse(text)
  } catch {
    return text
  }
}

async function toApiError(res, data) {
  const payload = data && typeof data === 'object' && data.error ? data.error : {}
  const fallback = typeof data === 'string' && data.trim() ? data.trim().slice(0, 300) : ''
  const message = payload.message || fallback || res.statusText || `HTTP ${res.status}`
  return new ApiError(message, {
    status: res.status,
    code: payload.code || 'http_error',
    details: payload.details || null,
    requestId: res.headers?.get?.('X-Request-ID') || '',
  })
}

async function doRefresh() {
  const { refresh: refreshToken } = getSession()
  if (!refreshToken) throw new ApiError('La sesión ha caducado.', { status: 401, code: 'unauthorized' })

  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), 15000)
  let res
  try {
    res = await fetchImpl('/api/admin/auth-refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refreshToken }),
      signal: controller.signal,
    })
  } catch (error) {
    // Un fallo de red NO invalida la sesión local: puede ser transitorio.
    throw new ApiError('No se pudo renovar la sesión.', { code: 'network_error', cause: error })
  } finally {
    clearTimeout(timer)
  }

  const data = await readBody(res)
  if (!res.ok) {
    const error = await toApiError(res, data)
    // Solo una respuesta 401 definitiva invalida los tokens locales.
    if (res.status === 401) {
      clearSession()
      dispatchSessionExpired()
    }
    throw error
  }

  const previous = getSession()
  saveSession({
    token: data?.token || '',
    refreshToken: data?.refreshToken || refreshToken,
    email: previous.email || data?.record?.email || '',
  })
  if (!getSession().token) {
    throw new ApiError('El servidor no devolvió un token de acceso.', { code: 'invalid_session', status: 401 })
  }
  return true
}

/** Renovación compartida: varias peticiones 401 comparten la misma llamada. */
export function refreshSession() {
  if (!refreshPromise) {
    refreshPromise = doRefresh().finally(() => {
      refreshPromise = null
    })
  }
  return refreshPromise
}

/**
 * Ejecuta una petición y devuelve la Response. No parsea el cuerpo.
 * `auth: false` para login/setup/refresh/logout y métricas públicas.
 */
export async function apiFetch(path, options = {}) {
  const {
    params,
    method = 'GET',
    body,
    headers = {},
    signal,
    auth = true,
    retried = false,
    timeoutMs = 30000,
  } = options
  const url = params ? buildURL(path, params) : path
  const controller = new AbortController()
  const onCallerAbort = () => controller.abort(signal?.reason)
  if (signal) {
    if (signal.aborted) controller.abort(signal.reason)
    else signal.addEventListener('abort', onCallerAbort, { once: true })
  }
  const timer = timeoutMs > 0 ? setTimeout(() => controller.abort(new DOMException('Timeout', 'TimeoutError')), timeoutMs) : null

  const init = { method, headers: { ...headers }, signal: controller.signal }
  if (auth) {
    const { token } = getSession()
    if (token) init.headers.Authorization = `Bearer ${token}`
  }
  let payload = body
  if (body !== undefined && body !== null && !isBodyInit(body)) {
    init.headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  if (payload !== undefined && payload !== null) init.body = payload

  try {
    const res = await fetchImpl(url, init)
    if (res.status === 401 && auth && !retried && getSession().refresh) {
      await refreshSession()
      return apiFetch(url, { ...options, params: undefined, retried: true, signal: controller.signal })
    }
    return res
  } catch (error) {
    if (error instanceof ApiError) throw error
    if (controller.signal.aborted) {
      const reason = controller.signal.reason
      const isTimeout = reason instanceof DOMException && reason.name === 'TimeoutError'
      throw new ApiError(isTimeout ? 'El servidor no respondió a tiempo.' : 'Petición cancelada.', {
        code: isTimeout ? 'timeout' : 'aborted',
        cause: error,
      })
    }
    throw new ApiError('No se pudo conectar con el servidor.', { code: 'network_error', cause: error })
  } finally {
    if (timer) clearTimeout(timer)
    if (signal) signal.removeEventListener('abort', onCallerAbort)
  }
}

/** Analiza una Response y lanza ApiError uniforme cuando no es ok. */
export async function parseApiResponse(res) {
  const data = await readBody(res)
  if (!res.ok) throw await toApiError(res, data)
  return data
}

/**
 * Petición JSON principal. `key` aborta la petición anterior con la misma clave
 * (útil para búsquedas y paneles que se recargan rápido).
 */
export async function api(path, options = {}) {
  const { key, signal, ...rest } = options
  let controller = null
  if (key) {
    activeByKey.get(key)?.abort()
    controller = new AbortController()
    activeByKey.set(key, controller)
    if (signal) {
      if (signal.aborted) controller.abort(signal.reason)
      else signal.addEventListener('abort', () => controller.abort(signal.reason), { once: true })
    }
  }
  try {
    const res = await apiFetch(path, { ...rest, signal: controller?.signal || signal })
    return await parseApiResponse(res)
  } finally {
    if (key && activeByKey.get(key) === controller) activeByKey.delete(key)
  }
}

/** Texto plano (p. ej. /metrics), sin parseo JSON. */
export async function apiText(path, options = {}) {
  const res = await apiFetch(path, options)
  if (!res.ok) throw await toApiError(res, await readBody(res))
  return res.text()
}

/**
 * Descarga un archivo con Authorization en la cabecera (nunca en la URL) y
 * dispara un guardado local mediante un object URL.
 */
export async function downloadBlob(path, filename, options = {}) {
  const res = await apiFetch(path, { ...options, timeoutMs: options.timeoutMs ?? 120000 })
  if (!res.ok) throw await toApiError(res, await readBody(res))
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename || 'descarga'
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  setTimeout(() => URL.revokeObjectURL(url), 30000)
}

/** Cierra la sesión en el servidor ANTES de borrar los tokens locales. */
export async function serverLogout() {
  const { refresh: refreshToken } = getSession()
  if (!refreshToken) {
    clearSession()
    return { remote: false }
  }
  let lastError = null
  for (let attempt = 0; attempt < 2; attempt += 1) {
    try {
      const res = await fetchImpl('/api/auth/logout', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refreshToken }),
      })
      if (res.ok || res.status === 204 || res.status === 401) {
        // 401 significa que el refresh ya no era válido: tampoco queda nada que revocar.
        clearSession()
        return { remote: true }
      }
      lastError = await toApiError(res, await readBody(res))
    } catch (error) {
      lastError = error
    }
  }
  // Best effort: se limpia la sesión local para no dejar tokens accesibles en
  // la pestaña, y se informa al llamador de que no se pudo confirmar en servidor.
  clearSession()
  return { remote: false, error: lastError }
}
