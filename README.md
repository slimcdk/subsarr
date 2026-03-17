# Subsarr

[![Build](https://github.com/slimcdk/subsarr/actions/workflows/docker-build.yml/badge.svg)](https://github.com/slimcdk/subsarr/actions/workflows/docker-build.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io-slimcdk%2Fsubsarr-blue?logo=docker)](https://github.com/slimcdk/subsarr/pkgs/container/subsarr)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[Subscene](https://subscene.com) shut down in 2023, taking millions of community subtitles with it. subsarr lets you self-host the full Subscene archive and use it as a subtitle source in [Bazarr](https://www.bazarr.media) — so your media server can automatically find and download subtitles without depending on any external service.

It imports the community-preserved Subscene V2 dump (~2.7 million entries) into a local database and exposes a search API that Bazarr can query by IMDB ID, title, language, season/episode, and more.

---

## Features

- **Configurable database** — SQLite (default), PostgreSQL, or MySQL/MariaDB
- **Configurable storage** — local filesystem (default) or any S3-compatible object store
- **No authentication required** — simple read-only subtitle API
- **Streaming import** — reads directly from the 7z archive, no extraction needed
- **Single binary** — statically compiled Go with zero runtime dependencies

---

## Getting started

### Step 1 — Get the archive

The full Subscene V2 dump (~97 GB, 12-part split 7z) was preserved and shared by the community on Reddit:

> **r/DataHoarder — "Subscene.com full dump"**
> https://www.reddit.com/r/DataHoarder/comments/1b5rxc2/subscenecom_full_dump/

Download all 12 parts (`Subscene V2.7z.001` … `Subscene V2.7z.012`) into the same directory. The importer reads them as a single split archive — no extraction needed.

### Step 2 — Import the dump

This is a one-time step. The import streams directly from the archive and takes roughly 2–3 hours for the full dataset.

```bash
docker run --rm \
  -v /srv/subsarr/db:/app/db \
  -v /mnt/array/subsarr/storage:/app/storage \
  -v /path/to/subscene/archive:/tmp/subscene-archive \
  ghcr.io/slimcdk/subsarr:latest \
  import-dump --archive "/tmp/subscene-archive/Subscene V2.7z.001"
```

Progress is printed to stdout:

```
[import] opening archive /tmp/subscene-archive/Subscene V2.7z.001 …
[import] archive has 2706833 entries across volumes: [...]
[import] detected format: v2
[import] 10000 processed  9987 imported  0 skipped  13 errors  (312/s)
...
[import:V2 stream] done: 2706833 processed  2695441 imported  ...  in 2h28m
```

### Step 3 — Run the server

```bash
docker run -d \
  --name subsarr \
  --restart unless-stopped \
  -v /srv/subsarr/db:/app/db \
  -v /mnt/array/subsarr/storage:/app/storage \
  -p 8090:8090 \
  ghcr.io/slimcdk/subsarr:latest
```

The API is available at `http://localhost:8090/api/v1/`.

---

## Docker Compose

```yaml
# docker-compose.yml
services:
  subsarr:
    image: ghcr.io/slimcdk/subsarr:latest
    volumes:
      - /mnt/cache/subsarr/db:/app/db           # SSD/cache — fast reads
      - /mnt/array/subsarr/storage:/app/storage  # array — bulk file storage
      - /path/to/subscene/archive:/tmp/subscene-archive:ro
    ports:
      - 8090:8090
    restart: unless-stopped
```

```bash
# Import (one-time)
docker compose run --rm subsarr import-dump --archive "/tmp/subscene-archive/Subscene V2.7z.001"

# Start the server
docker compose up -d
```

---

## Configuration

All settings are via environment variables. Defaults are tuned for the simplest setup (SQLite + local filesystem).

| Variable | Default | Description |
|---|---|---|
| `SUBSARR_DB_DRIVER` | `sqlite` | Database backend: `sqlite`, `postgres`, or `mysql` |
| `SUBSARR_DB_DSN` | `subsarr.db` | Connection string (file path for SQLite, URL for others) |
| `SUBSARR_STORAGE_BACKEND` | `filesystem` | File storage: `filesystem` or `s3` |
| `SUBSARR_STORAGE_PATH` | `./storage` | Local filesystem root (when backend=filesystem) |
| `SUBSARR_S3_ENDPOINT` | | S3-compatible endpoint URL |
| `SUBSARR_S3_BUCKET` | `subsarr` | S3 bucket name |
| `SUBSARR_S3_REGION` | `us-east-1` | S3 region |
| `SUBSARR_S3_ACCESS_KEY` | | S3 access key |
| `SUBSARR_S3_SECRET_KEY` | | S3 secret key |
| `SUBSARR_S3_PATH_STYLE` | `false` | Use path-style S3 URLs (required for most self-hosted S3) |
| `SUBSARR_LISTEN` | `0.0.0.0:8090` | HTTP listen address |

### Example: PostgreSQL + S3

```yaml
services:
  subsarr:
    image: ghcr.io/slimcdk/subsarr:latest
    environment:
      SUBSARR_DB_DRIVER: postgres
      SUBSARR_DB_DSN: postgres://user:pass@postgres:5432/subsarr?sslmode=disable
      SUBSARR_STORAGE_BACKEND: s3
      SUBSARR_S3_ENDPOINT: http://s3:3900
      SUBSARR_S3_BUCKET: subsarr
      SUBSARR_S3_ACCESS_KEY: your-access-key
      SUBSARR_S3_SECRET_KEY: your-secret-key
      SUBSARR_S3_PATH_STYLE: "true"
    ports:
      - 8090:8090
```

---

## API

| Endpoint | Description |
|---|---|
| `GET /api/v1/info` | Provider metadata and capabilities |
| `GET /api/v1/languages` | Available languages with counts |
| `GET /api/v1/subtitles/search` | Search subtitles (see params below) |
| `GET /api/v1/subtitles/{id}/download` | Download a subtitle file |

### Search parameters

| Param | Example | Description |
|---|---|---|
| `imdb_id` | `tt0468569` | Filter by IMDB ID |
| `language` | `English` | Filter by language name |
| `slug` | `the-dark-knight` | Filter by Subscene slug |
| `query` | `dark knight` | Free-text search on title/filename |
| `hi` | `true` | Hearing-impaired only |
| `year` | `2008` | Filter by release year |
| `season` | `2` | Filter by season number (S02 pattern) |
| `episode` | `5` | Combined with season for S02E05 pattern |
| `page` | `1` | Page number (1-based) |
| `per_page` | `50` | Results per page (max 200) |

---

## Development

```bash
# Build
make build

# Run server
make run

# Run tests
make test

# Build Docker image
make docker-build
```

### sqlc

Database queries are managed with [sqlc](https://sqlc.dev/). Schema and query files are in `sql/`. To regenerate Go code after modifying queries:

```bash
sqlc generate
```
