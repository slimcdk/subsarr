# Subsarr

[![Build](https://github.com/slimcdk/subsarr/actions/workflows/docker-build.yml/badge.svg)](https://github.com/slimcdk/subsarr/actions/workflows/docker-build.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io-slimcdk%2Fsubsarr-blue?logo=docker)](https://github.com/slimcdk/subsarr/pkgs/container/subsarr)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[Subscene](https://subscene.com) shut down in 2023, taking millions of community subtitles with it. subsarr lets you self-host the preserved Subscene archive and use it as a subtitle source in [Bazarr](https://www.bazarr.media) — so your media server keeps finding subtitles without depending on any external service.

It loads the archive's own catalogue into a database, extracts every usable subtitle file into storage, and answers Bazarr's searches in milliseconds.

---

## Features

- **Complete metadata** — title, IMDB id, release names, uploader, comment and upload date come from the archive's catalogue, not from guessing at file names
- **Fast search** — IMDB lookups are index seeks; title searches go through a full-text title index. Milliseconds, not seconds
- **Every format the archive holds** — zip, RAR and raw `.srt`/`.sub`/`.ssa`/`.ass`/`.smi`/`.vtt`/MicroDVD `.txt`, recognised by content, not by extension
- **Configurable database** — SQLite (default), PostgreSQL, or MySQL/MariaDB, with identical search behaviour on all three
- **Configurable storage** — local filesystem (default) or any S3-compatible object store, content-addressed and deduplicated
- **Idempotent import** — re-run it after an interruption, a new dump or an upgrade; nothing is duplicated and download URLs keep working
- **Language whitelist** — store only the languages you will ever ask for
- **Single binary** — statically compiled Go, migrations included

---

## Getting started

### Step 1 — Get the archive

The full Subscene V2 dump (~97 GB, 12-part split 7z) was preserved and shared by the community on Reddit:

> **r/DataHoarder — "Subscene.com full dump"**
> https://www.reddit.com/r/DataHoarder/comments/1b5rxc2/subscenecom_full_dump/

Download all 12 parts (`Subscene V2.7z.001` … `Subscene V2.7z.012`) into the same directory. The importer reads them as one split archive — no extraction needed, and no disk space beyond the database and the subtitle files.

### Step 2 — Look at it first (optional, recommended)

`inspect-archive` reads a dump without importing anything and reports what it found — including how it read the catalogue's columns:

```bash
docker run --rm \
  -v /path/to/subscene/archive:/tmp/subscene-archive:ro \
  ghcr.io/slimcdk/subsarr:latest \
  inspect-archive --archive "/tmp/subscene-archive/Subscene V2.7z.001"
```

### Step 3 — Import

```bash
docker run --rm \
  -v /srv/subsarr/db:/app/db \
  -v /mnt/array/subsarr/storage:/app/storage \
  -v /path/to/subscene/archive:/tmp/subscene-archive:ro \
  ghcr.io/slimcdk/subsarr:latest \
  import-dump --archive "/tmp/subscene-archive/Subscene V2.7z.001"
```

**Budget 8–12 hours** for the full archive: every entry is decompressed, hashed and written. Progress is printed every 10,000 entries and the counters explain every entry that produced no subtitle:

```
[import] pass 1/2: loading the catalogue …
[import] catalogue Subscene_Metadata.sql: table "all_subs", 2556454 rows
[import] columns read as: subscene_id=id file_path=file_path title=title imdb_id=imdb …
[import] 250000 entries  312400 files (312400 new, 0 updated, 0 unchanged)  298100 stored  0 migrated
         skipped: 0 language, 0 resumed, 2 empty, 9800 truncated, 41 error bodies, 351 not subtitles, … (74/s)
```

The import is safe to interrupt. Re-running continues where it stopped; add `--resume` to skip re-reading the entries a previous run already stored.

### Step 4 — Run the server

```bash
docker run -d \
  --name subsarr \
  --restart unless-stopped \
  -v /srv/subsarr/db:/app/db \
  -v /mnt/array/subsarr/storage:/app/storage \
  -p 8090:8090 \
  ghcr.io/slimcdk/subsarr:latest
```

The API is at `http://localhost:8090/api/v1/`, and the specification it was built from is at `/api/v1/openapi.yaml`.

### Step 5 — Point Bazarr at it

In Bazarr, enable the **subsarr** provider and set its base URL. **The scheme is required** — `http://192.168.1.10:8090`, not `192.168.1.10:8090`. Bazarr refuses a URL without one.

---

## Docker Compose

```yaml
services:
  subsarr:
    image: ghcr.io/slimcdk/subsarr:latest
    volumes:
      - /mnt/cache/subsarr/db:/app/db            # SSD/cache — fast reads
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

## What the data looks like

Two tables, joined by every search:

| Table | One row per | Holds |
|---|---|---|
| `uploads` | Subscene upload | subscene id, archive path, slug, title, IMDB id, language, hearing-impaired flag, year, uploader, comment, release names, upload date |
| `files` | downloadable subtitle file | id (the download URL), upload id, file name, format, content hash, storage key, size, download count |

One upload can hold several files — a season zip is one upload and one file per episode — and a file only exists when its content is stored. **Every id a search returns is downloadable.**

Storage keys are content-addressed (`content/<aa>/<bb>/<sha256>.srt`), so identical subtitles uploaded under different ids occupy storage once.

### Reference numbers

Measured on the V2 dump (97 GB, all parts CRC-verified):

| | |
|---|---|
| Archive entries | ~2,556,800 |
| Covered by the catalogue | 2,556,454 (99.99 %) |
| Uploads with an IMDB id | 89.9 % |
| Distinct titles | ~150,000 |
| Subtitle files after import | ~4.9 million |
| Languages | 90 requestable, ~120 present |
| SQLite database | ~3.5 GB |
| Free space needed to migrate an existing one | ~1× the database, plus ~1 GB |
| Subtitle storage | ~40 GB (after deduplication; 12.9 % of the raw files are duplicates) |
| Import duration | 8–12 h on a NAS |

An entry that produces no file is counted, not hidden: about 4 % of the archive was truncated when it was collected, a handful of entries are zero bytes, and a few are JSON or HTML error bodies a scraper saved under a `.zip` name. Their catalogue rows are still loaded, so the data model records that Subscene had them.

---

## Configuration

All settings are environment variables. The defaults are the simplest setup (SQLite + local filesystem).

| Variable | Default | Description |
|---|---|---|
| `SUBSARR_DB_DRIVER` | `sqlite` | `sqlite`, `postgres`, or `mysql` |
| `SUBSARR_DB_DSN` | `subsarr.db` | Connection string (a file path for SQLite) |
| `SUBSARR_STORAGE_BACKEND` | `filesystem` | `filesystem` or `s3` |
| `SUBSARR_STORAGE_PATH` | `./storage` | Local filesystem root |
| `SUBSARR_S3_ENDPOINT` | | S3-compatible endpoint URL |
| `SUBSARR_S3_BUCKET` | `subsarr` | Bucket name |
| `SUBSARR_S3_REGION` | `us-east-1` | Region |
| `SUBSARR_S3_ACCESS_KEY` | | Access key |
| `SUBSARR_S3_SECRET_KEY` | | Secret key |
| `SUBSARR_S3_PATH_STYLE` | `false` | Path-style URLs (required by most self-hosted S3) |
| `SUBSARR_LISTEN` | `0.0.0.0:8090` | HTTP listen address |
| `SUBSARR_IMPORT_LANGUAGES` | | Default language whitelist for imports (see below) |

### MySQL / MariaDB

The DSN is the go-sql-driver form, and **`parseTime=true` is required** — subsarr adds it if you leave it out:

```
SUBSARR_DB_DSN=subsarr:password@tcp(mariadb:3306)/subsarr?parseTime=true
```

### PostgreSQL

```
SUBSARR_DB_DSN=postgres://subsarr:password@postgres:5432/subsarr?sslmode=disable
```

Title search uses PostgreSQL's full-text index. Substring search additionally uses `pg_trgm` when the extension can be created; without it that fallback still works, only slower.

---

## The language whitelist

The archive holds subtitles in about 120 languages. If you only ever ask for two of them, storing the rest is millions of small files you will never open.

```bash
# Import, but only store English and Danish subtitle files
subsarr import-dump --archive "…/Subscene V2.7z.001" --languages english,danish

# Or set it for the deployment
SUBSARR_IMPORT_LANGUAGES=english,danish
```

The flag wins when both are set. Values are accepted in any spelling the canonicaliser understands (`Brazilian Portuguese`, `brazillian-portuguese`, `brazilian_portuguese`), and a value that matches no language stops the import immediately rather than silently importing nothing.

**What the whitelist does and does not do:**

- The **catalogue is always loaded in full**. Every upload is recorded whatever its language.
- Only **file extraction and storage** are filtered. Entries outside the list are skipped before they are opened, which is also why a whitelist makes the import faster.
- `/languages` lists only languages that actually have files, so Bazarr sees what is really available.
- **Widening the list later is a plain re-run**: the metadata is already there, and only the newly allowed files are added.
- Uploads whose language is unknown are stored only if the list is empty or names `unknown`.

Narrowing the list does **not** delete anything. Reclaiming space is a separate, explicit command:

```bash
# See what would go
subsarr prune --keep-languages english,danish --dry-run

# Delete it
subsarr prune --keep-languages english,danish
```

`prune` deletes `files` rows outside the list and then the storage objects nothing points at any more. Catalogue rows are kept, so a later import restores the files without re-reading the metadata. Nothing else ever prunes: not an import, not a server start.

---

## Commands

| Command | Purpose |
|---|---|
| `subsarr serve` | Run the HTTP API |
| `subsarr import-dump` | Import a dump (see `--help` for every flag) |
| `subsarr inspect-archive` | Summarise a dump without importing it |
| `subsarr prune` | Delete stored files for languages you do not keep |
| `subsarr migrate` | Bring the schema up to date |
| `subsarr migrate status` | Show which schema versions are applied |

Import flags worth knowing:

| Flag | Effect |
|---|---|
| `--dry-run` | Read and count, write nothing — use it to measure rate and ETA |
| `--limit N` | Stop after N entries — use it to rehearse |
| `--languages` | The whitelist above |
| `--resume` | Skip entries a previous run already stored |
| `--reload-metadata` | Re-read the catalogue even though it is already loaded |
| `--skip-metadata` | Import file names only, for a dump with no catalogue |
| `--catalogue PATH` | Read the catalogue from a file instead of from the archive |
| `--batch N` | Entries per transaction (default 500) |

---

## Schema migrations

Migrations are versioned, embedded in the binary and applied automatically by every command, so upgrading the container is a pull and a restart.

An installation from before this release is **baselined**: its existing schema is recorded as version 1, and the migrations after it carry it forward — including the data migration that moves the flat `subtitles` table into `uploads` + `files`. That migration:

- keeps every file id, so Bazarr history and in-flight downloads keep working
- canonicalises the language column, which is what made 3 % of rows unreachable
- recovers the hearing-impaired flag and the year
- drops the placeholder rows that answered downloads with a 404

Storage keys are moved to their content-addressed form by the next import, which also removes the old objects.

`subsarr migrate status` shows where a database stands:

```
VER   MIGRATION                APPLIED
1     00001_legacy_schema      2026-08-22 19:45:56Z
2     00002_uploads_files      2026-08-22 19:45:56Z
3     legacy data migration    2026-08-22 19:45:57Z
4     00004_drop_legacy        2026-08-22 19:45:57Z
5     00005_normalised_titles  2026-08-22 19:45:57Z
```

---

## API

| Endpoint | Description |
|---|---|
| `GET /api/v1/info` | Provider metadata and capabilities |
| `GET /api/v1/languages` | Languages that have subtitle files, with counts |
| `GET /api/v1/subtitles/search` | Search |
| `GET /api/v1/subtitles/{id}/download` | Download a subtitle file |
| `GET /api/v1/openapi.yaml` | The specification this build implements |

### Search parameters

| Param | Example | Description |
|---|---|---|
| `imdb_id` | `tt0468569` | Film, or series for an episode |
| `language` | `english` | Any spelling the canonicaliser understands |
| `query` | `The Dark Knight` | Title search — never matches file names |
| `slug` | `the-dark-knight` | Exact Subscene slug |
| `hi` | `true` / `false` | Hearing impaired; omit for both |
| `year` | `2008` | Matches that year and uploads with no year |
| `season` | `2` | Matched as `S02` against release and file names |
| `episode` | `5` | Combined with `season` into `S02E05`; ignored on its own |
| `page` | `1` | 1-based |
| `per_page` | `50` | Clamped to 200 |

Results are ordered by exact title first, then the closest titles, then downloads. `total_items` is the exact number of matches, so a client can page through it. A `400` is returned only for a value that cannot be read at all.

---

## Development

```bash
make build           # compile
make run             # build and serve
make test            # test suite (SQLite only)
make test-dialects   # start PostgreSQL and MariaDB in Docker and test all three
make lint            # golangci-lint
make docker-build    # production image
```

The suite runs against SQLite always, and against PostgreSQL and MariaDB when `SUBSARR_TEST_POSTGRES_DSN` and `SUBSARR_TEST_MYSQL_DSN` are set — which is what `make test-dialects` and CI do. The same conformance suite runs against all three, so the dialects cannot drift apart.

The language names subsarr stores are Bazarr's own, vendored in `internal/lang/bazarr_languages.json`. After a Bazarr rename, regenerate them and let the tests tell you what broke:

```bash
python3 scripts/vendor-bazarr-languages.py
```

### Runbooks

- [Re-importing on the NAS](docs/runbooks/reimport.md) — backup, rehearse, run, verify, roll back
