# Gate 0 benchmark — Bbolt vs modernc SQLite

Arnés reproducible para decidir el motor de Doketfire v1.0 con datos, no con intuición. Mide la misma carga lógica sobre **Bbolt** (baseline v0.x) y **`modernc.org/sqlite`** (candidato default de v1.0), cada uno en un proceso separado.

> Este directorio es herramienta de validación, no código de producto. No importa `Doketfire/` ni define la API de la v1.0.

## Qué mide

| Workload | Descripción |
|---|---|
| `seed` | Inserción por lotes del dataset completo. Reporta filas/s. |
| `point_get` | Lectura por clave primaria. |
| `list_published` | `published = true AND created_at < ? ORDER BY created_at DESC LIMIT 20` con cursor. |
| `count_published` | `COUNT(*)` de los registros publicados. |
| `update_title` | Actualización por clave primaria. |

También reporta:

- Apertura del motor, `HeapAlloc`, `HeapSys`, `Sys`, RSS y tamaño en disco.
- Tamaño de binarios mínimos que solo enlazan cada motor (`cmd/size-*`).
- Cross-compile opcional con `CGO_ENABLED=0` para Linux y macOS.

## Dataset

- 100.000 registros por defecto, generados de forma determinista (`-seed 42`).
- Campos: `id` (ULID), `title`, `slug` único, `body` (~300 B), `published` (~60 %), `author_id`, `created_at`, `updated_at`.
- Timestamps en milisegundos Unix, uno por segundo, para que la paginación por cursor sea representativa.
- Los IDs y el orden de inserción son idénticos en cada ejecución.

## Cómo ejecutar

```powershell
cd bench
go run . -records 100000 -out RESULTS.md -json RESULTS.json
```

Ejecución rápida para validar el arnés:

```powershell
go run . -records 10000 -point-reads 200 -lists 100 -counts 10 -updates 100
```

Solo un motor:

```powershell
go run . -engines sqlite
```

Verificar cross-compile (más lento, descarga toolchains de módulos):

```powershell
go run . -cross -out RESULTS.md
```

Conservar las bases de datos para inspección:

```powershell
go run . -keep
```

## Interpretación

- **p95 de `list_published` con 100k filas < 50 ms** es el criterio de abandono de Gate 0 para `modernc`. Si no se cumple y CGO no es aceptable, el pivote a SQLite no se justifica.
- La comparación es entre motores, no una promesa absoluta: el hardware, el sistema de archivos y la caché cambian los números.
- SQLite incluye `-wal` y `-shm` en el tamaño en disco; Bbolt solo el `.db`.
- Bbolt serializa JSON por registro; SQLite escanea columnas. Es el coste real de cada enfoque documental.
- La memoria se mide por proceso hijo para evitar que un motor contamine al siguiente.

## CGO SQLite

El build con CGO no está incluido porque el entorno de referencia (Windows, `CGO_ENABLED=0`) no tiene toolchain C/C++. Para medirlo:

1. Instalar un compilador C compatible (MinGW-w64 en Windows, `build-essential` en Linux).
2. Añadir un motor `cgosqlite` que use `github.com/mattn/go-sqlite3` con el mismo esquema y workloads.
3. Ejecutarlo con `CGO_ENABLED=1 go run . -engines bbolt,cgosqlite`.

El resultado debe publicarse junto a los demás en `RESULTS.md` antes de cerrar Gate 0.

## Limitaciones conocidas

- Un solo proceso y un solo escritor: no mide concurrencia multi-cliente.
- No mide réplicas libSQL ni latencia de red.
- No mide el panel, SSE, auth ni autorización; esos presupuestos se validan en fases posteriores.
- `tasklist`/`powershell` se usan solo para reportar CPU/RAM/RSS; si fallan, el benchmark continúa con valores en cero.

## Estructura

```text
bench/
├── main.go          # CLI, proceso padre e hijos
├── dataset.go       # generador determinista
├── engine.go        # contrato de motores
├── bolt.go          # adaptador Bbolt + índices secundarios
├── sqlite.go        # adaptador modernc + WAL + STRICT
├── runner.go        # workloads y métricas
├── stats.go         # p50/p95/p99
├── report.go        # Markdown y comparación
├── extras.go        # tamaños de binario y cross-compile
├── hardware.go      # CPU, RAM y RSS best-effort
├── cmd/size-bbolt/  # binario mínimo Bbolt
└── cmd/size-sqlite/ # binario mínimo SQLite
```
