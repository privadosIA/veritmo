# The data of the server

[← Back to the README](../README.md)

The server keeps its state in one database: SQLite by default (the file `.orchestrator/orchestrator.db`), or MySQL or Postgres chosen in Settings (`PUT /api/settings/database`, see [http-api.md](http-api.md)). The code is the package `internal/platform/store`, on GORM. The run history was the first thing to live in it; the state that was kept in folders and files has moved there too, in five steps, each with its own section below: [the music motion mode](#the-music-motion-mode-in-the-database), [the projects](#the-projects-in-the-database), [the song library](#the-song-library-in-the-database) and [the settings, the prompt presets, the HyperFrames blocks and the motions](#the-settings-the-prompts-the-blocks-and-the-motions-in-the-database). This page says what lives where, how the schema changes, and the tools to look after the data.

## What lives in the database and what stays in files

| In the database | In files, always |
|---|---|
| The run history, reviews and speed measurements, and every table a later change moves | Generated and binary media: videos, frames, audio, pictures, previews, thumbnails, the sources of a project, `tmp/` and `out/`, model weights |
| The text of what the server keeps (definitions, pages, manifests, scripts) as columns of text | The files a rendering page loads by relative path |
| The record of each import of the state that was kept in files | The bootstrap files, read before the database is opened: the database settings and the server name in `settings.json`, the pointer files `media-dir`, `models-dir` and `web-dir`, the pid file |
| | The secrets: `keys.json`, the server token, the Hugging Face and Civitai tokens. No secret is in a table, an export or a log |

A row that refers to a file holds its path relative to the folder it belongs to, never its content.

## The schema changes only through migrations

Every table, and every later change of a table, is a **versioned migration**: a Go function kept in the repository, applied once, in order, by `veritmo data migrate`, and recorded in the table `schema_migrations` with the time it was applied (it does what Flyway does in Java; the tool is [gormigrate](https://github.com/go-gormigrate/gormigrate)). `AutoMigrate` is never called to create or change a table of the data layer.

## Who migrates: the deployment workflow, never the server

The schema is changed by the command `veritmo data migrate`, before deploying a version that brings migrations (in the Compose stack, the `migrate` step does it). The server **does not migrate**: `veritmo serve` checks the database when it starts and refuses to start while migrations are pending, naming them and the command to run; it also refuses a database that has applied migrations it does not know (a newer version made it). `import`, `export` and `restore` check the same; only `migrate` and `rollback` change the schema. `data status` shows the applied and pending migrations without changing anything.

```bash
veritmo data migrate                       # the database saved in Settings, or the SQLite file of --db
VERITMO_DB_PASSWORD=... veritmo data migrate --driver postgres --host db.example --port 5432 --database app --user app
```

The connection flags are for a workflow, which has no Settings: the password is read only from the environment variable `VERITMO_DB_PASSWORD` (a flag would show in the process list). Two programs migrating the same MySQL or Postgres database at once take a database lock and take turns, so each migration is applied once. The workflow needs, as repository variables, `VERITMO_DB_DRIVER`, `VERITMO_DB_HOST`, `VERITMO_DB_PORT`, `VERITMO_DB_NAME` and `VERITMO_DB_USER`, and as a secret `VERITMO_DB_PASSWORD`; the runner must reach the database server. For a local SQLite database run `veritmo data migrate` by hand after updating.

To add a migration, in the package that owns the tables (nothing registers itself: the package offers a function and `server.DataRegistry()` calls it):

```go
// RegisterStore tells a registry what this area owns.
func RegisterStore(reg *store.Registry) {
    reg.AddModels(&Piece{})                   // the live model: the export, the copy and the status walk it
    reg.AddMigration(&store.Migration{
        ID: "202611191030_pieces",            // YYYYMMDDHHMM_short_name: it sorts by date
        Migrate: func(tx *gorm.DB) error {
            type piece struct {               // a COPY of the struct as it is today
                ID    string `gorm:"primaryKey;size:191"`
                Title string
            }
            if tx.Migrator().HasTable("pieces") { return nil }
            return tx.Table("pieces").Migrator().CreateTable(&piece{})
        },
        Rollback: func(tx *gorm.DB) error { return tx.Migrator().DropTable("pieces") },
    })
}
```

and one line in `server.DataRegistry()` (`internal/server/core_data.go`): `pieces.RegisterStore(r)`. A test builds its own registry (`store.NewRegistry()` plus the `RegisterStore` it needs) and gives it to `store.Open` or `store.OpenMemory`.

Rules:

- **A migration never uses the live model structs.** It declares inside its function a copy of the struct as it is at its date (or calls `tx.Migrator()`), so that it does the same thing forever whatever the models become. The test of each area calls `st.CheckSchema()` after `Migrate`: a model changed without its migration fails.
- Only GORM's migrator is used, no SQL text, so that one migration serves SQLite, MySQL and Postgres. Where an engine needs something of its own, branch on `tx.Dialector.Name()`.
- A migration succeeds on a database that already has what it creates (`HasTable`), because the databases deployed before it exist.
- `Rollback` drops what the migration added. A migration that transforms data, or creates tables that already existed, sets `Rollback: store.CannotUndo`.
- Columns: a string primary key (`size:191`, so that MySQL indexes it), scalar columns for everything a list filters or sorts by, and the rest of the entity as a document in a `store.Text` (long text) or `store.JSON[T]` column. No JSON operator of any engine is used in a query.
- A program refuses to start (or to migrate) on a database that has applied migrations it does not know (a newer version made it), and names them.

## Importing the state that was kept in files

An area that moves an entity to the database adds an importer (`reg.AddImporter`, in its `RegisterStore`) that turns its files into rows. When the server starts, every entity that has not been imported is imported, in one transaction each, before the first request. The import **never overwrites a row, never changes a file and never deletes one**; a file it cannot read is skipped and named in the record; an entity whose import failed is undone and tried again the next time. After its import an entity is read and written in the database only: a file changed or added by hand has no effect until an import is asked for.

```bash
veritmo data status                 # the engine, the migrations, the rows of each table, the imports and the files left behind
veritmo data import                 # every entity not imported yet
veritmo data import music-motion/parts   # one entity, again: files that have no row become rows, rows that exist stay
```

The same through the API: `GET /api/data/status`, `POST /api/data/import {entity?}` (`404` for an unknown entity, `409` while an import runs). The commands work while the server runs on the same SQLite file (WAL), and open the database saved in Settings (`--db` is the SQLite file when none is saved).

## Cleaning the files left behind

The import leaves the files where they are. `POST /api/data/legacy/clean {entities: [...]}` deletes the state files of the entities named, one by one and only those their importer lists (never media, a bootstrap file or a secret, never a folder). It refuses, deleting nothing of any entity (`409`), one whose import has not ended well or skipped a file. Nothing is deleted without this call.

## Export, restore and the copy to another engine

```bash
veritmo data export data.zip        # one JSON-lines file per table and a manifest; no secret, no media
veritmo data restore data.zip       # into an EMPTY database of any engine (SQLite, MySQL or Postgres)
```

`GET /api/data/export` downloads the same zip. A restore is one transaction, and refuses a database that has rows (changing nothing) and an export made with migrations the program does not know. To move a server from the SQLite file to MySQL or Postgres, save the database in Settings and call `POST /api/settings/database/copy`: it copies the run history and every other table, leaves alone the rows that are already there (so it can be repeated) and answers `copied` and `existing` per table.

## Undoing a migration

`veritmo data rollback <migration>` undoes the **last** migration applied, when it can be undone, and removes its record: the next time a program opens the database it is applied again. It is refused when the migration is not the last one, when it says it cannot be undone (the history one cannot), and while a server runs on a SQLite database (a pid file in the settings folder). The server never undoes a migration by itself.

## Lists: `limit`, `offset`, `sort` and `q`

A list of the API that can be paged accepts the four optional parameters, and answers exactly as it did before when none is sent:

- `limit` (1 to 500) and `offset` (0 or more; alone it uses a limit of 100): a page, with the header `X-Total-Count` (how many match in all) and `Link` (`rel="next"` and `rel="prev"`). A bad value is `400` naming the parameter and its range.
- `sort`: a field the list names, with `-` before it for descending order; any other is `400` naming the fields that can be used.
- `q`: a text searched in the text fields the list names, without regard to case. It combines with the filters the list already has.

For a client in a browser: `X-Total-Count` and `Link` are in `Access-Control-Expose-Headers`. The parameters are in the components of [openapi.yaml](../internal/server/openapi.yaml) (`ListLimit`, `ListOffset`, `ListSort`, `ListQuery`).

## Running the store tests on MySQL and Postgres

`go test ./internal/platform/store` always runs on SQLite in memory. The same tests (the migrations, the repositories, the import, the export and the restore) run on the other engines when a database is given, and skip it otherwise (`go test -v` lists what was skipped):

```bash
docker run -d --name store-mysql -e MYSQL_ROOT_PASSWORD=pw -e MYSQL_DATABASE=store_test -p 3307:3306 mysql:8
docker run -d --name store-postgres -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=store_test -p 5433:5432 postgres:16

STORE_TEST_MYSQL_DSN='root:pw@tcp(127.0.0.1:3307)/store_test?charset=utf8mb4&parseTime=True&loc=UTC' \
STORE_TEST_POSTGRES_DSN='host=127.0.0.1 port=5433 user=postgres password=pw dbname=store_test sslmode=disable TimeZone=UTC' \
go test ./internal/platform/store -v
```

Each test drops every table of the database first, so give it a database of its own and never one that holds data.

## The music motion mode in the database

The first area that moved its state to the store is the music motion mode (change `move-music-motion-to-database`). Its eight entities, each imported once from the folders it used to be kept in (`veritmo data import <entity>` brings in what was put there later):

| Entity (importer) | Tables | What stays a file |
|---|---|---|
| `music-motion/parts` | `mm_parts` | the HyperFrames block files (they have their own folder) |
| `music-motion/pieces` | `mm_pieces`, `mm_piece_documents` (design, script, song map, proposal, scene batches, review) | the song, its stems, frames, `out/`, `tmp/`, `scenes.yaml` |
| `music-motion/part-drafts` | `mm_part_drafts` | `frame.jpg` and the preview frames |
| `music-motion/references` | `mm_references` | the key frames and the pictures of the shots |
| `music-motion/learnings` | `mm_learnings` | |
| `music-motion/presets` | `mm_presets` | |
| `music-motion/render-times` | `mm_render_ratios` | |
| `music-motion/library` | `mm_library_items`, `mm_library_files` | |

The migrations are `202611…_mm_*` in the packages `musicmotion` and `motionref`. The user's parts and the user's characters and backgrounds are read through a store that `server.DataRegistry()` builds once (no package-level default), and a test builds its own on a folder or on an in-memory database.

### For dashboards-lab

Everything is behind the routes of [MUSIC_MODE_MOTION.md](MUSIC_MODE_MOTION.md#http-api); the ones that are new, all in `openapi.yaml`:

- Paged lists (`limit`, `offset`, `sort`, `q`; `X-Total-Count` and `Link` in the answer; no parameter, the whole list as before): `GET /api/music-motion/parts`, `/pieces` (`client`), `/part-drafts` (`state`, `piece`), `/references` (`state`), `/learnings` (`reference`, `state`), `/presets`, `/library/characters`, `/library/backgrounds`.
- `POST /api/music-motion/parts`, `PATCH /api/music-motion/pieces/{id}`, `DELETE /api/music-motion/learnings/{id}`, and `POST`, `PUT`, `DELETE` on the library of characters and backgrounds.
- Approving the scenes of a batch is all or nothing now: if one of them cannot be kept, none is and the answer says so.

## The projects in the database

The second area that moved its state is the projects (change `move-projects-to-database`). A project is a **row** with its definition (the text of its `scenes.yaml`, byte for byte) and what the listings say about it, which is derived from the text every time it is saved: name, mode, kind, client, adult label, scene count, whether it is valid (and the error when it is not), and the size of its folder measured when a run ends, when its storage is cleaned, when its sources change and when it is imported. The folder under `projects/<id>/` keeps what is not text: the sources, `tmp/` and `out/`, and a working copy of the definition.

| Entity (importer) | Tables | What stays a file |
|---|---|---|
| `projects` | `projects` (definition and derived columns), `project_versions` (the 20 newest texts a save replaced, with date and reason), `project_pages` (the page of each motion scene and overlay and its provenance), `managed_records` (the record of each generation, picture edit and video edit made in one call) | `sources/`, `tmp/`, `out/`, `.reviews/`, the other files of `motion/` (`*.choice.json`, `*.part.json`, `*.song.json`) |

The importer reads every folder of the projects folder that holds a `scenes.yaml`: an invalid definition is imported too (marked invalid with its error), each `scenes.yaml.*` backup becomes a version (oldest first), each page of `motion/` a page and each `generation.json` a record. `LegacyFiles` lists only the backups and the records: the `scenes.yaml` and the pages stay as the working copy.

### The folder is a working copy: Materialize and Ingest

The pipeline works on files, so the server syncs the folder around what reads it:

- **Materialize** (before a run starts, and again when a queued run begins, before a check, a cache report, an export or a review fix): writes the definition to `scenes.yaml` and each page to `motion/` when the hash of the file differs from the saved one, and removes a page file the database does not have. It touches no other file of `motion/`. A `scenes.yaml` changed by hand is **overwritten**, with one line in the log.
- **Ingest** (when a run ends in any state, after a fix, after the extension of a clip, after a piece writes its project): a `scenes.yaml` that differs from the saved definition becomes the definition and the text it replaces a version (reason `run`, `fix` or `extend`); a page that differs becomes the page; a page file that is gone is **not** a deletion (only the API deletes pages). If someone saved another definition while the run worked, that text stays the definition and the one the run left is kept as a version.

The hash, not the time, decides: the media folder may be on a filesystem with coarse times, and a copy or a restore changes times. A per-project lock makes a save, a materialize and an ingest of one project take turns.

### Before going back to a version of the program that keeps the definitions in the folders

The previous binaries read `projects/<id>/scenes.yaml` and know nothing of the database. The folder holds the definition as of the last Materialize; a definition saved since and never run is only in the database. Write them all out first:

```bash
for id in $(curl -s -H "Authorization: Bearer $(cat ~/.config/veritmo/token)" http://127.0.0.1:4666/api/projects?managed=include | python3 -c 'import json,sys; print(" ".join(p["id"] for p in json.load(sys.stdin)))'); do
  bin/veritmo project pull "$id"      # writes projects/$id/scenes.yaml from the saved definition
done
```

### For dashboards-lab

Nothing breaks: every route answers as before. What is new, all in `openapi.yaml` and [http-api.md](http-api.md):

- Paged lists (`limit`, `offset`, `sort`, and `q` where it makes sense; `X-Total-Count` and `Link` in the answer; no parameter, the whole list as before): `GET /api/projects` (`sort` = `id`, `name`, `updated_at`; `q` in the id and the name), `GET /api/generations`, `GET /api/video-edits` and the new `GET /api/image-edits` (`sort=created_at`, `client`), `GET /api/projects/{id}/versions`.
- `GET /api/projects/{id}` and `PATCH /api/projects/{id}` (`name`, `client`, `adult`), and the versions of a definition: `GET /api/projects/{id}/versions`, `GET .../versions/{version}`, `POST .../versions/{version}/restore`.
- A definition is changed only through `PUT /api/projects/{id}/config`, `PATCH`, a restore or `veritmo project push`: a hand edit of the file has no effect.

## The song library in the database

The third area that moved its state is the song library (change `move-song-library-to-database`). Every song a run exports is a **row** with what the listing says about it (title, caption, lyrics, bpm, key, generator, client, favourite, the file and the cover, and the length of the audio as the song map reads it) and its **stars**; the playlists are two more tables. The audio, the cover and the manifest of the run stay files. `GET /api/library` is one query: it opens no folder and runs no ffmpeg.

| Entity (importer) | Tables | What stays a file |
|---|---|---|
| `library` (after `projects`) | `songs` (one row per exported song, its stars in `rating`), `playlists`, `playlist_songs` (the place of each song in a playlist) | the audio, the cover and `manifest.json` of each project |

The importer makes a row of every song the manifest of a music project lists whose audio is on disk and whose track is still in the project, measuring each length once (a log line says how many); then it puts the stars and the playlists of `projects/.library.json` on those songs. A star for a song that does not exist, and a place in a playlist for one, are **left out and named in the record** of the import. `LegacyFiles` lists only `.library.json`.

### How the rows follow the folder

`SyncProject` makes the rows of one project what its manifest exports now: a new song is added, one that is there is updated (**its stars stay**), one that is no longer exported is removed, and it leaves the playlists, which close the gap. The length of an audio is measured again only when its size or its time changed since its row was written. The server calls it when a run of the project ends, after its definition is saved (the title, the favourites and the client of its songs come from the definition), after a song is deleted, and for every project by `POST /api/library/rescan`. Deleting a project removes its songs, their stars and their places in the playlists in the same transaction.

A run of `veritmo --project <id>` (and `veritmo project push`) tells the library the same way, through the same code. A run of a loose `scenes.yaml` has no project in the database, so its songs are not in the library; audio put or removed by hand is brought in or out by `POST /api/library/rescan`.

### Before going back to a version of the program that keeps the library in files

The previous binaries read the manifests and `projects/.library.json`, which are untouched. The stars and the playlists changed since the upgrade exist only in the database: they are not in the file.

### For dashboards-lab

Nothing breaks: `GET /api/library` and `PUT /api/library/meta` answer the same bodies as before. What is new, all in `openapi.yaml` and [http-api.md](http-api.md):

- `GET /api/library/songs`: the songs in pages (`limit`, `offset`; `X-Total-Count` and `Link`), sorted by `created_at` (newest first, the default), `title`, `rating` or `duration`; `q` in the title, the caption and the lyrics; filters `project`, `client`, `managed`, `min_rating`, `favorite=true` and `playlist` (in the order of the playlist unless `sort` is given). Each song is the item `GET /api/library` shows, plus `rating`.
- `GET /api/library/songs/{key}` and `PUT /api/library/songs/{key}/rating` (`{stars}`, 0 removes them). The key is `project/track/seed`: send its slashes as `%2F`.
- `GET|POST /api/library/playlists` and `GET|PUT|DELETE /api/library/playlists/{id}`: a playlist is made, read, replaced and deleted on its own, with `409` for an id that exists and `422` naming a song key that is not in the library.
- `POST /api/library/rescan` for audio put or removed by hand: it answers how many songs it added, updated and removed.
- `PUT /api/library/meta` leaves out a star or a place in a playlist for a key that is not a song of the library (it used to keep them in the file) and answers what it kept.

## The settings, the prompts, the blocks and the motions in the database

The last state kept in files moved with the change `move-settings-and-catalogs-to-database`. Four entities, each imported once at the first start (`veritmo data import <entity>` brings in what was put there later); none of them deletes or changes a file:

| Entity (importer) | Tables | What stays a file |
|---|---|---|
| `settings` | `settings` (one row per setting: its name and the JSON of its value), `providers` (one row per provider, in the order the user gave them) | `settings.json`, with only the database settings and the server name once it is cleaned; `keys.json` |
| `prompts` | `prompt_presets` | `<prompts>/.cache/` (the downloads and the report of `veritmo prompts fetch`) |
| `hyperframes/blocks` | `hf_blocks` (the record of each block: the columns a list filters by, and the block as a document) | everything in `hyperframes/blocks/<name>/`: the page, what it loads, the thumbnail; `hyperframes/clips/` |
| `motions` | `motions` (the record of each motion) | its `control/` frames and its `preview.mp4`, in `~/.config/veritmo/motions/<id>/` |

### What stays in `settings.json`, and why

`settings.json` keeps only `database` (where the database is, with its password) and `server_name`, because the program reads them **before** the database is open. Everything else is in the database: the Ollama address, the browser, the model of the motion graphics, the routing, the consent to remote providers, the hosts of HeartMuLa and DiffRhythm2, the monitoring tools, the CORS origins and networks, the review of drafts and the ctxpress connection. The first start copies what the file held into the database and **does not change the file**: a previous version of the program can still read it, and a setting changed since the upgrade exists only in the database. A setting edited in `settings.json` by hand, other than those two, has **no effect** (edit it through Settings, the API or a command: `veritmo cors add|remove|list` writes the database). Cleaning the files left behind (`veritmo data clean settings`) rewrites `settings.json` with only those two, and never deletes it; do it when the rollback window is over.

A setting saved by the API or by a command is used by the next operation of the server and of any command with no restart; the CORS policy follows a change within 5 seconds (the origins and networks are read again every 5 seconds). Every command but `data` and `serve` opens the database the first time it needs a setting; before the first start of the server there is no database file and the settings are still the file.

### Where each secret is

Every secret is in the private keys file `keys.json` (mode 600, in a directory of mode 700) **and in no table, export, answer of the API or log**: the keys of the providers (id of the provider), the ctxpress token (`ctxpress`), the HeartMuLa and DiffRhythm2 tokens (`heartmula`, `diffrhythm2`) and the secrets of the monitoring tools (`monitoring:<tool>`). The first start copies the three that used to be in `settings.json` there, where the file has no entry for them. The password of the database stays in `settings.json` (mode 600), as it is read before the database is open.

### Prompt presets, blocks and motions

The command `veritmo prompts fetch` writes the database (`--db`), so a running server lists what it fetched at once; `veritmo blocks` writes the records of the blocks to it too. A preset file or a block folder put by hand enters with an import (`veritmo data import prompts`, `hyperframes/blocks`, `motions`). The tags of a preset, a block and a motion are stored between commas so that the search finds a part of one and a filter by tag matches it whole.

### Before going back to a version of the program that keeps these in files

The previous binaries read `settings.json` (untouched until cleaned), the preset files, the `block.json` and `motion.json` files: all still there. What was changed after the upgrade (a setting, a preset, an edit of a block or a motion's title) is only in the database.

### For dashboards-lab

Nothing breaks: every route answers as before. What is new, all in `openapi.yaml` and [http-api.md](http-api.md):

- `GET /api/prompts/{id}` and `PUT /api/prompts/{id}` (a preset of the person's own; `403` for a bundled one); `GET /api/prompts` also takes `offset` and `sort` (`title`, `created_at`) and answers `X-Total-Count` and `Link` when a page parameter is sent (it was always a page of 50, at most 200: unchanged).
- `GET /api/providers/{id}` (with `key_set`, never the key).
- `GET /api/hyperframes/blocks` can be paged (`limit`, `offset`, `sort` = `name` or `title`; `total` and `states` count everything that matches) and `DELETE /api/hyperframes/blocks/{name}` deletes a block, its folder, and never a part made from it.
- `GET /api/motions` can be paged and searched (`limit`, `offset`, `sort`, `q`, `state`) and `PATCH /api/motions/{id}` changes `title` and `tags`.

## The environment as the default of a connection

A container is given its connections by environment variables instead of a hand-written `settings.json` (the Compose stack does it, see [COMPOSE.md](COMPOSE.md)). The rule is the one the Ollama address has always had: **what is saved in Settings wins, the environment is the default when nothing is saved, and the default of a piece that is not there is empty**. Each answer that shows one of these says where it comes from with `source`: `setting` (saved), `env` (the variable) or `default` (nothing).

| What | Variables | Where it is shown |
|---|---|---|
| The database (MySQL or Postgres) | `VERITMO_DB_DRIVER`, `VERITMO_DB_HOST`, `VERITMO_DB_PORT`, `VERITMO_DB_NAME`, `VERITMO_DB_USER`, `VERITMO_DB_PASSWORD` | `GET /api/settings/database` |
| Ollama | `OLLAMA_HOST` | `GET /api/settings` (`ollama`) |
| The ctxpress connection | `CTXPRESS_URL`, `CTXPRESS_TOKEN`, `CTXPRESS_MIN_CHARS` | `GET /api/settings/ctxpress` |
| HeartMuLa and DiffRhythm2 (an external service) | `HEARTMULA_HOST`, `HEARTMULA_TOKEN`, `DIFFRHYTHM2_HOST`, `DIFFRHYTHM2_TOKEN` | `GET /api/settings` (`heartmula`, `diffrhythm2`) |
| The monitoring tools | `MONITORING_<GRAFANA\|PROMETHEUS\|LOKI\|REDIS>_ADDRESS`, `_USER`, `_SECRET` | `GET /api/settings/monitoring` |
| Browser origins and networks that may call the API | `CORS_ORIGINS`, `CORS_NETWORKS` (comma lists) | `veritmo cors list` |

- **The database in the environment is never a silent file.** If `VERITMO_DB_DRIVER` names a MySQL or a Postgres and the rest does not describe one (no host, a port that is not a number), `serve`, `data` and every command that opens the database stop and name the variable; they do not fall back to SQLite. With the variables unset nothing changes.
- **A token goes with its address.** A saved address never takes the token of the environment. An address given by the environment is accepted without the click that a remote address of the saved settings needs: writing it in `.env` is the person's own choice.
- **The environment is never written to the settings.** Saving anything else in Settings does not copy these values into the file or the database; the origins of `CORS_ORIGINS` are added to the saved ones and to `--cors-origin`, without repeating one.
- **A secret can be a file.** Every secret above (`VERITMO_DB_PASSWORD`, the two music tokens, `MONITORING_*_SECRET`, `CTXPRESS_TOKEN`, and the server's `VERITMO_TOKEN`) also takes `<NAME>_FILE`, the path of a file whose content (trimmed) is the value, the way Docker secrets are given: it does not show in `docker inspect`. The variable wins over the file; a file that cannot be read stops the start and names the file, never a value.

## In the Compose stack

[The stack](COMPOSE.md) has **one** database server, the MySQL the server uses, and no second one: the server's schema (`veritmo`), the schema of Grafana (`grafana`, its own state: users, dashboards saved by hand) and a read-only user for the history dashboard, all in the same MySQL. What the other pieces keep is not in it: Prometheus and Loki keep their data in their own Docker volumes (`prometheus-data`, `loki-data`), Redis keeps nothing. The migration is the `migrate` step, which runs `veritmo data migrate` before the server starts; the server never migrates.
