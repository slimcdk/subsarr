# Subsarr

[![Build](https://github.com/slimcdk/subsarr/actions/workflows/docker-build.yml/badge.svg)](https://github.com/slimcdk/subsarr/actions/workflows/docker-build.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io-slimcdk%2Fsubsarr-blue?logo=docker)](https://github.com/slimcdk/subsarr/pkgs/container/subsarr)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[Subscene](https://subscene.com) shut down in 2023, taking millions of community subtitles with it. subsarr lets you self-host the full Subscene archive and use it as a subtitle source in [Bazarr](https://www.bazarr.media) — so your media server can automatically find and download subtitles without depending on any external service.

It imports the community-preserved Subscene V2 dump (~2.7 million entries) into a local database and exposes a search API that Bazarr can query by IMDB ID, title, language, season/episode, and more.

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
  -v /srv/subsarr/pb_data:/app/pb_data \
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

### Step 3 — Create a superuser

```bash
docker run --rm \
  -v /srv/subsarr/pb_data:/app/pb_data \
  ghcr.io/slimcdk/subsarr:latest \
  superuser create [email] [pass]
```

### Step 4 — Run the server

```bash
docker run -d \
  --name subsarr \
  --restart unless-stopped \
  -v /srv/subsarr/pb_data:/app/pb_data \
  -p 8090:8090 \
  ghcr.io/slimcdk/subsarr:latest
```

The admin UI is available at `http://localhost:8090/_/`.

---

## Docker Compose

```yaml
# docker-compose.yml
services:
  subsarr:
    image: ghcr.io/slimcdk/subsarr:latest
    volumes:
      - subsarr-data:/app/pb_data
      - /path/to/subscene/archive:/tmp/subscene-archive:ro
    ports:
      - 8090:8090
    # command: import-dump --archive "/tmp/subscene-archive/Subscene V2.7z.001"
    restart: unless-stopped

volumes:
  subsarr-data:
```

```bash
docker compose up -d
docker compose run --rm subsarr superuser create [email] [password]
docker compose run --rm subsarr import-dump --archive "/tmp/subscene-archive/Subscene V2.7z.001"
```
