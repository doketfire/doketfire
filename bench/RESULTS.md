# Gate 0 benchmark — Bbolt vs modernc SQLite

> Generado el 2026-11-26T03:16:44Z por `bench` (Doketfire).

## Entorno

| Campo | Valor |
|---|---|
| Go | `go1.27.3` |
| OS/arch | `windows/amd64` |
| CPU | Intel(R) Core(TM) 3 N355 (8 hilos) |
| RAM total | 15.68 GiB |

## Dataset y configuración

| Parámetro | Valor |
|---|---:|
| Registros | 100000 |
| Seed determinista | 42 |
| Batch de escritura | 1000 |
| Point reads | 2000 |
| Listas | 500 |
| Counts | 50 |
| Updates | 500 |
| Warmup por workload | 50 |

## Resultados por motor

### bbolt

- Apertura: 5.423 ms
- Seed: 100000 filas en 4272.7 ms (23404 filas/s)

| Workload | n | p50 | p95 | p99 | ops/s |
|---|---:|---:|---:|---:|---:|
| point_get | 2000 | 0.012 ms | 0.062 ms | 0.134 ms | 51177 |
| list_published | 500 | 0.126 ms | 0.531 ms | 1.334 ms | 5162 |
| count_published | 50 | 4.883 ms | 8.119 ms | 8.905 ms | 194.6 |
| update_title | 500 | 2.426 ms | 5.010 ms | 7.387 ms | 352.8 |

### sqlite

- Apertura: 14.94 ms
- Seed: 100000 filas en 14159.5 ms (7062 filas/s)

| Workload | n | p50 | p95 | p99 | ops/s |
|---|---:|---:|---:|---:|---:|
| point_get | 2000 | 0.133 ms | 0.527 ms | 1.253 ms | 4913 |
| list_published | 500 | 0.404 ms | 1.756 ms | 3.196 ms | 1685 |
| count_published | 50 | 12.46 ms | 19.81 ms | 27.60 ms | 79.2 |
| update_title | 500 | 0.187 ms | 1.017 ms | 2.945 ms | 3010 |

## Comparación directa

Δ = variación porcentual del motor secundario frente al primero. En latencia, negativo es mejor; en filas/s, positivo es mejor.

| Workload | bbolt p50 | sqlite p50 | Δ p50 | bbolt p95 | sqlite p95 | Δ p95 |
|---|---:|---:|---:|---:|---:|---:|
| point_get | 0.012 ms | 0.133 ms | +1020.2% | 0.062 ms | 0.527 ms | +755.8% |
| list_published | 0.126 ms | 0.404 ms | +219.6% | 0.531 ms | 1.756 ms | +230.5% |
| count_published | 4.883 ms | 12.46 ms | +155.2% | 8.119 ms | 19.81 ms | +144.0% |
| update_title | 2.426 ms | 0.187 ms | -92.3% | 5.010 ms | 1.017 ms | -79.7% |
| seed (filas/s) | 23404 | 7062 | -69.8% | — | — | — |

## Lectura rápida

- **Point reads** (p95): gana **bbolt**, 8.6× más rápido (0.062 ms vs 0.527 ms).
- **Listas indexadas** (p95): gana **bbolt**, 3.3× más rápido (0.531 ms vs 1.756 ms).
- **Counts** (p95): gana **bbolt**, 2.4× más rápido (8.119 ms vs 19.81 ms).
- **Updates** (p95): gana **sqlite**, 4.9× más rápido (1.017 ms vs 5.010 ms).
- **Seed**: gana **bbolt**, 3.3× más rápido (23404 vs 7062 filas/s).
- **Disco**: gana **sqlite**, 4.3× menos espacio (59.10 MiB vs 256.00 MiB).
- **Binario**: el build mínimo con SQLite añade 5.99 MiB frente a Bbolt (9.43 MiB vs 3.44 MiB).
- **Criterio Gate 0**: p95 de `list_published` con 100000 filas = 1.756 ms (< 50 ms) → ✅ cumple.

## Recursos

| Motor | HeapAlloc | HeapSys | Sys | RSS | DB en disco |
|---|---:|---:|---:|---:|---:|
| bbolt | 1000.68 KiB | 111.81 MiB | 120.33 MiB | 166.89 MiB | 256.00 MiB |
| sqlite | 619.12 KiB | 100.56 MiB | 110.95 MiB | 120.59 MiB | 59.10 MiB |

## Tamaño de binario (dependencia aislada)

Programas mínimos que solo importan el motor, compilados con `CGO_ENABLED=0` y `-trimpath`.

| Build | Tamaño |
|---|---:|
| bbolt-only | 3.44 MiB |
| sqlite-only | 9.43 MiB |

## Cross-compile (`CGO_ENABLED=0`)

| Target | Resultado | Duración | Binario |
|---|---|---:|---:|
| linux/amd64 | ✅ | 3440.8 ms | 11.77 MiB |
| linux/arm64 | ✅ | 2404.8 ms | 11.26 MiB |
| darwin/arm64 | ✅ | 1978.3 ms | 11.21 MiB |

## Entradas para el go/no-go

Criterio de abandono de Gate 0: p95 de listas indexadas con 100k filas < 50 ms.

| Motor | p95 `list_published` | Cumple |
|---|---:|---|
| bbolt | 0.531 ms | ✅ |
| sqlite | 1.756 ms | ✅ |

## Notas metodológicas

- Cada motor se ejecuta en un proceso hijo separado para que la memoria de uno no contamine al otro.
- Bbolt guarda documentos JSON y mantiene índices secundarios; SQLite usa columnas, `STRICT`, WAL y `synchronous=NORMAL`.
- Bbolt confirma una transacción por lote; SQLite usa `_txlock=immediate` y una transacción por lote.
- El tamaño en disco de SQLite incluye `-wal` y `-shm` en el momento de la medición; Bbolt solo el archivo `.db`.
- No se midió CGO SQLite: requiere un toolchain C/C++ que este entorno no tiene. Queda como referencia pendiente para Gate 0.
- Una sola ejecución por motor: la varianza entre runs puede ser alta. Para cerrar Gate 0, repetir al menos 3 veces y usar la mediana.
- Las duraciones de cross-compile dependen del estado de la caché de Go; no son un benchmark de compilación en frío.
- Los números dependen del hardware, del sistema de archivos y de la caché; se publican para comparar motores, no como promesa absoluta.
