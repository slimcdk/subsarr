# Runbook: re-importing on the NAS

Upgrading an existing installation to the catalogue model. The service keeps
answering throughout; the only step that takes real time is the import itself.

**Budget 8–12 hours.** Every archive entry is decompressed, hashed and written.

Paths below are the reference installation's; substitute your own.

| | |
|---|---|
| Database | `/mnt/cache/subsarr/db/subsarr.db` |
| Storage | `/mnt/array/subsarr/storage` |
| Archive | `/mnt/array/dumps/subscene/Subscene V2.7z.001` |
| Service | `docker compose -f /srv/subsarr/docker-compose.yaml` |

---

## 0. Check Bazarr first

subsarr stores the language names Bazarr's converter produces. A Bazarr older
than its 2026-08-16 converter change spells two of them differently and would
drop those results.

```bash
# In Bazarr: System → Status → Version
```

If Bazarr predates that change, upgrade it before or shortly after this import.
Everything except Brazilian Portuguese and Chinese works either way.

---

## 1. Back up

The database is the only thing that cannot be rebuilt from the archive.

```bash
docker compose -f /srv/subsarr/docker-compose.yaml stop subsarr

sqlite3 /mnt/cache/subsarr/db/subsarr.db ".backup '/mnt/array/backups/subsarr-$(date +%F).db'"
ls -lh /mnt/array/backups/

docker compose -f /srv/subsarr/docker-compose.yaml start subsarr
```

For PostgreSQL or MariaDB use `pg_dump` / `mariadb-dump` instead.

Storage is not backed up: it is rebuilt from the archive, and the import only
adds to it until the old objects are no longer referenced.

**Free space.** The data migration writes the catalogue model alongside the flat
table before dropping it, so keep roughly the size of the database free — about
4 GB for the reference installation — plus a gigabyte for the write-ahead log.
SQLite does not shrink the file after dropping the old table; the space is reused
by the import, and `VACUUM` reclaims it if you would rather have it back.

---

## 2. Look at the dump

Check that your copy of the archive is read the way it should be — in particular
that the catalogue is found and its columns are mapped sensibly.

```bash
docker compose -f /srv/subsarr/docker-compose.yaml run --rm subsarr \
  inspect-archive --archive "/tmp/subscene-archive/Subscene V2.7z.001" --limit 50000
```

Expect: a `.sql` catalogue, `subscene_id`, `file_path`, `title`, `imdb_id`,
`language` and `releases` all mapped to a column, and around 90 % IMDB coverage.
If a role says `— not found —`, stop and open an issue with the printed column
list: importing would lose that field for every row.

---

## 3. Upgrade the image and migrate

```bash
docker compose -f /srv/subsarr/docker-compose.yaml pull subsarr
docker compose -f /srv/subsarr/docker-compose.yaml run --rm subsarr migrate status
docker compose -f /srv/subsarr/docker-compose.yaml run --rm subsarr migrate
docker compose -f /srv/subsarr/docker-compose.yaml run --rm subsarr migrate status
```

The first `status` shows every migration pending; the second shows all five
applied. The data migration between them moves the flat table into the catalogue
model, keeping every file id. On the reference database — 4.9 million rows — it
takes about a quarter of an hour and prints its progress as it goes.

Start the service again and confirm it still answers:

```bash
docker compose -f /srv/subsarr/docker-compose.yaml up -d subsarr
curl -s http://localhost:8090/api/v1/info | jq
```

`search_by_imdb_id` will be **false** at this point: the flat table had no IMDB
ids, and `/info` reports what the data actually holds. The import fixes that.

---

## 4. Rehearse

Measure the rate on a scratch database before touching production.

```bash
docker compose -f /srv/subsarr/docker-compose.yaml run --rm \
  -e SUBSARR_DB_DSN=/tmp/rehearsal.db \
  -e SUBSARR_STORAGE_PATH=/tmp/rehearsal-storage \
  subsarr import-dump \
    --archive "/tmp/subscene-archive/Subscene V2.7z.001" \
    --limit 20000
```

The final line reports the rate. Divide 2,556,800 by it for the ETA. A dry run
(`--dry-run`) measures reading and hashing without writing anything.

---

## 5. Run it

```bash
docker compose -f /srv/subsarr/docker-compose.yaml run --rm -d --name subsarr-import \
  subsarr import-dump --archive "/tmp/subscene-archive/Subscene V2.7z.001"

docker logs -f subsarr-import
```

Add `--languages english,danish` (or whatever you actually request) to store only
those languages: the catalogue is still loaded in full, and widening the list
later is another run of the same command.

The service keeps serving while this runs. It is safe to stop and restart —
re-running continues where it left off, and `--resume` skips re-reading what is
already stored.

Watch for:

- `X new, Y updated, Z unchanged` — a first run is nearly all new; a re-run is
  nearly all unchanged
- `truncated` climbing steadily — expected, roughly 4 % of the archive
- `errors` climbing — not expected; capture the log lines and stop

---

## 6. Verify

```bash
# The catalogue arrived
curl -s http://localhost:8090/api/v1/info | jq '.features.search_by_imdb_id'   # true

# Languages are the names Bazarr sends
curl -s http://localhost:8090/api/v1/languages | jq '.items[:5]'

# A film, by IMDB id
curl -s "http://localhost:8090/api/v1/subtitles/search?imdb_id=tt0468569&language=english&per_page=5" \
  | jq '{total: .total_items, first: .items[0] | {title, releases, author, uploaded_at}}'

# An episode
curl -s "http://localhost:8090/api/v1/subtitles/search?imdb_id=tt0903747&language=english&season=2&episode=5" \
  | jq '.total_items'

# A title, including a variant slug
curl -s "http://localhost:8090/api/v1/subtitles/search?query=The+Dark+Knight&language=english&per_page=5" \
  | jq '[.items[].slug]'

# The language that used to be unreachable
curl -s "http://localhost:8090/api/v1/subtitles/search?language=brazillian-portuguese&per_page=1" \
  | jq '.total_items'

# Every result is downloadable
ID=$(curl -s "http://localhost:8090/api/v1/subtitles/search?imdb_id=tt0468569&per_page=1" | jq -r '.items[0].id')
curl -sf -o /dev/null -w '%{http_code}\n' "http://localhost:8090/api/v1/subtitles/$ID/download"   # 200
```

Each of these should return in milliseconds. If a title search takes seconds, the
title index is missing — run `subsarr migrate` and restart, which rebuilds it.

### Bazarr smoke test

1. **Settings → Providers → subsarr**: the base URL must include the scheme
   (`http://192.168.1.10:8090`). Save; Bazarr pings `/info`.
2. Pick a film with no subtitles and use **Manual search**. Results should appear
   within a second or two, with real release names in the release column.
3. Download one and confirm it lands next to the video file.
4. Repeat for an episode of a series.
5. Let one scheduled search run and check Bazarr's log for provider errors.

---

## 7. Reclaim space (optional)

Only if you imported with a whitelist narrower than what is already stored, or
want to narrow it now:

```bash
docker compose -f /srv/subsarr/docker-compose.yaml run --rm subsarr \
  prune --keep-languages english,danish --dry-run

docker compose -f /srv/subsarr/docker-compose.yaml run --rm subsarr \
  prune --keep-languages english,danish
```

---

## Rolling back

The archive is the source of truth, so a rollback is a restore plus a re-run.

1. Stop the service and any running import.
2. Restore the database backup over `subsarr.db`.
3. Pin the previous image tag in the compose file and start it.

Storage is **not** rolled back: the import will have written content-addressed
objects and removed the legacy ones. The restored database points at the legacy
keys, so downloads will 404 until you re-run the import with the new image. If
you need the old installation working again immediately, restore the storage
directory from the array's snapshot as well.

Every image built from this repository is tagged `sha-<commit>`, and every branch
and pull request has its own tag, so an exact build can always be pinned:

```yaml
image: ghcr.io/slimcdk/subsarr:sha-43261b5
```
