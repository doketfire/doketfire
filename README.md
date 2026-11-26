# Doketfire

[![Go Version](https://img.shields.io/badge/go-1.26+-00ADD8.svg?style=flat-square&logo=go)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)](LICENSE)
[![Embedded UI](https://img.shields.io/badge/ui-Svelte_5-FF3E00.svg?style=flat-square&logo=svelte)](ui)
[![Database](https://img.shields.io/badge/storage-Bbolt-333333.svg?style=flat-square)](https://github.com/etcd-io/bbolt)

**Doketfire** is an open-source, self-hosted Backend-as-a-Service (BaaS) packaged in a **single Go binary**. It provides a document database powered by embedded [Bbolt](https://github.com/etcd-io/bbolt), a full REST API, JWT authentication with session rotation, CEL access control, real-time Server-Sent Events (SSE), local file storage, and an embedded administrative dashboard.

**Zero external dependencies**: One binary, one database file, and one admin panel.

---

## Highlights

- **Single Binary**: Self-contained server, database engine, static asset hosting, and admin UI.
- **Embedded Storage**: ACID-compliant key-value document store using Bbolt; no external database servers to manage.
- **Dynamic Collections**: Schemas with field-level validation, multi-field indexes, and relational cascades.
- **Expressive REST API**: Rich querying (`filter`, `sort`, `fields`, `include`), cursor pagination, server-side aggregations, and atomic multi-document transactions.
- **CEL Security Rules**: Granular authorization powered by Google's Common Expression Language (CEL).
- **Real-Time SSE**: Built-in Server-Sent Events stream for instant updates on document mutations.
- **Robust Auth**: HS256 JWTs, rotating refresh tokens, session family revocation, and superuser accounts.
- **Embedded Admin Dashboard**: Responsive, secure web UI built with Svelte 5 and embedded directly at `/_/`.
- **Operational Tooling**: Built-in CLI commands for online backups, atomic restore, compaction, integrity checks, and index rebuilding.

---

## Quick Start

### 1. Build and Run

```bash
# Build the binary
go build -o doketfire ./cmd/doketfire

# Start the server (defaults to ./data on 0.0.0.0:8090)
./doketfire serve --dir=./data --http=127.0.0.1:8090
```

### 2. Create Initial Superuser

Open **`http://127.0.0.1:8090/_/`** in your browser to complete the initial setup, or create the first superuser directly via the CLI:

```bash
./doketfire superuser create --dir=./data admin@example.com "secure-password"
```

Once running:
- **Admin Dashboard**: `http://127.0.0.1:8090/_/`
- **REST API Base**: `http://127.0.0.1:8090/api/`
- **Health Check**: `http://127.0.0.1:8090/api/health`

---

## API Usage

### Create a Record

```bash
curl -X POST http://127.0.0.1:8090/api/collections/posts/records \
  -H "Authorization: Bearer <TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Getting Started with Doketfire",
    "published": true,
    "tags": ["database", "go"]
  }'
```

### Query Records

Filter using an expressive query syntax, sort fields, and paginate with opaque cursors:

```bash
curl -G http://127.0.0.1:8090/api/collections/posts/records \
  -H "Authorization: Bearer <TOKEN>" \
  --data-urlencode 'filter=published = true && tags contains "go"' \
  --data-urlencode 'sort=-created,title' \
  --data-urlencode 'limit=20'
```

### Real-Time Subscriptions (SSE)

Subscribe to real-time events over a single HTTP connection:

```bash
curl -N http://127.0.0.1:8090/api/realtime \
  -H "Authorization: Bearer <TOKEN>" \
  -H "Accept: text/event-stream"
```

Events are streamed as records are created, updated, or deleted:

```text
id: 1042
event: posts
data: {"seq":1042,"action":"create","collection":"posts","id":"01JB...","record":{...}}
```

---

## Security Rules (CEL)

Control record-level access using Google's [CEL (Common Expression Language)](https://github.com/google/cel-spec). Define rules per action (`list`, `view`, `create`, `update`, `delete`):

```json
{
  "rules": {
    "list": "true",
    "view": "true",
    "create": "request.auth != null",
    "update": "request.auth != null && resource.author == request.auth.id",
    "delete": "request.auth != null && resource.author == request.auth.id"
  }
}
```

- **Empty / omitted rule**: Restricted to superusers only.
- `"true"`: Publicly accessible.
- Superusers always bypass CEL rules.

---

## CLI Reference

```text
Usage:
  doketfire serve [flags]                       Run the HTTP/HTTPS server
  doketfire superuser create <email> <pwd>      Create a superuser account
  doketfire backup [--dir=DIR] [--out=DIR]      Create a database snapshot backup
  doketfire restore [--dir=DIR] <file>          Atomically restore database from backup
  doketfire compact [--dir=DIR]                 Reclaim unused space and compact Bbolt file
  doketfire check [--dir=DIR]                   Validate schema integrity and Bbolt pages
  doketfire reindex [--dir=DIR] <col> [idx]     Rebuild collection index(es)
  doketfire version                             Show version information
```

---

## Configuration

Settings can be specified via command-line flags or environment variables:

| Flag | Environment Variable | Default | Description |
| --- | --- | --- | --- |
| `--dir` | `DOKET_DIR` | `./data` | Path to storage directory |
| `--http` | `DOKET_HTTP` | `0.0.0.0:8090` | HTTP listen address |
| `--https` | `DOKET_HTTPS_DOMAIN` | *(empty)* | Domain for automatic Let's Encrypt TLS |
| `--tls-cert` | `DOKET_TLS_CERT` | *(empty)* | Custom TLS certificate file |
| `--tls-key` | `DOKET_TLS_KEY` | *(empty)* | Custom TLS private key file |
| `--cors` | `DOKET_CORS` | *(empty)* | Allowed CORS origins (comma-separated or `*`) |
| `--read-only` | `DOKET_READ_ONLY` | `false` | Open database in read-only mode |
| `--strict-indexes` | `DOKET_STRICT_INDEXES` | `false` | Reject queries that cannot use an index |
| `--max-body` | `DOKET_MAX_BODY` | `2097152` | Maximum JSON request body (2 MiB) |
| `--max-upload` | `DOKET_MAX_UPLOAD` | `26214400` | Maximum file upload size (25 MiB) |
| `--log-level` | `DOKET_LOG_LEVEL` | `info` | Log verbosity (`debug`, `info`, `warn`, `error`) |

`DOKET_*` is the canonical prefix. `DOCPOCKET_*` is accepted as a legacy alias for one release.

---

## Docker Deployment

Run with Docker using a persistent volume for the data directory:

```bash
docker run -d \
  --name doketfire \
  -p 8090:8090 \
  -v doketfire_data:/data \
  ghcr.io/doketfire/doketfire:latest
```

Create a superuser inside the container:

```bash
docker exec -it doketfire doketfire superuser create --dir=/data admin@example.com "secure-password"
```

Or build the image locally with the provided [Dockerfile](Dockerfile):

```bash
docker build -t doketfire .
```

---

## Development

Requirements:
- **Go 1.26+** (for the backend server)
- **Bun** (only needed when compiling the UI in `ui/`)

```bash
# Run backend tests
go test ./...

# Build the embedded frontend (outputs to ui/build/)
cd ui
bun install
bun run build
cd ..

# Rebuild server with newly built UI assets
go build -o doketfire ./cmd/doketfire
```

---

## License

Doketfire is released under the [MIT License](LICENSE).
