import { describe, expect, it } from 'bun:test'
import {
  formatBytes,
  formatDuration,
  formatNumber,
  formatUptime,
  parseJson,
  parsePrometheus,
  tokenizeRule,
  truncate,
} from './format.js'

describe('formateadores', () => {
  it('formatea bytes con unidades binarias', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(1048576)).toBe('1.0 MB')
    expect(formatBytes(null)).toBe('—')
    expect(formatBytes('no-numero')).toBe('—')
  })

  it('formatea duraciones y tiempos de actividad', () => {
    expect(formatDuration(250)).toBe('250 ms')
    expect(formatDuration(1500)).toBe('1.50 s')
    expect(formatUptime(65)).toBe('1 min 5 s')
    expect(formatUptime(3661)).toBe('1 h 1 min')
    expect(formatUptime(90000)).toBe('1 d 1 h')
  })

  it('formatea números en español y trunca texto', () => {
    expect(formatNumber(1234.5)).toBe('1.234,5')
    expect(formatNumber(undefined)).toBe('—')
    expect(truncate('abcdef', 4)).toBe('abc…')
    expect(truncate('abc', 4)).toBe('abc')
  })
})

describe('análisis de texto', () => {
  it('parsea métricas Prometheus simples e ignora comentarios', () => {
    const text = [
      '# HELP doketfire_requests Total',
      'doketfire_requests_total 12',
      'doketfire_latency_seconds{quantile="0.5"} 0.25',
      'bad_line',
    ].join('\n')
    expect(parsePrometheus(text)).toEqual({
      doketfire_requests_total: 12,
      doketfire_latency_seconds: 0.25,
    })
  })

  it('parsea JSON sin lanzar excepciones', () => {
    expect(parseJson('{"a":1}')).toEqual({ ok: true, value: { a: 1 } })
    const bad = parseJson('{', 'fallback')
    expect(bad.ok).toBe(false)
    expect(bad.value).toBe('fallback')
    expect(typeof bad.error).toBe('string')
  })

  it('tokeniza reglas CEL en fragmentos seguros', () => {
    const tokens = tokenizeRule("request.method == 'GET' && user.active")
    const kinds = tokens.map((t) => t.kind)
    expect(kinds).toContain('operator')
    expect(kinds).toContain('string')
    expect(kinds).toContain('word')
    expect(tokens.map((t) => t.text).join('')).toBe("request.method == 'GET' && user.active")
  })
})
