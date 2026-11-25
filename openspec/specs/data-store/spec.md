# data-store Specification

## Purpose
The database that holds the state of the server: what lives in it and what stays in files, how its schema is created and upgraded, how the state that was kept in files is imported, and the tools to see it, export it, restore it and clean the files left behind.

## Requirements

### Requirement: The state of the server lives in one database

The server SHALL keep its state in the database chosen in Settings (the SQLite file at `--db` by default, or MySQL or Postgres): the run history and every entity a later change moves share that database. The schema SHALL be changed only by migrations that are part of the program, each with an identifier that orders it by date. The migrations the database has not applied SHALL be applied, in order and each completely or not at all, only by the command `veritmo data migrate`, which the deployment workflow runs; the server SHALL never apply a migration: it SHALL refuse to start while any is pending, saying which and what to run. Each migration SHALL be recorded in the database with its identifier and when it was applied, SHALL be applied once and never again, and SHALL give the same schema on a new database as on one upgraded step by step. Two programs migrating the same database at once SHALL take turns, so that each migration is applied once. A program SHALL refuse to start or to migrate, naming them, on a database that has applied migrations it does not know. The same state and the same answers SHALL be given on the three engines.

#### Scenario: A new database

- **WHEN** `veritmo data migrate` runs on an empty database
- **THEN** the tables exist and every migration is recorded with its date

#### Scenario: The server on a database with pending migrations

- **WHEN** the server starts on a database that has migrations not applied
- **THEN** it does not start, it changes nothing, and the error names the pending migrations and says to run `veritmo data migrate`

#### Scenario: A database of a newer server

- **WHEN** the server starts on a database that has applied a migration the server does not have
- **THEN** it does not start and the error names that migration

#### Scenario: An upgrade

- **WHEN** `veritmo data migrate` of a program with two new migrations runs on a database of the version before
- **THEN** the two are applied in order and recorded, the rows that existed are intact, and a second run applies nothing

#### Scenario: The database of before this capability

- **WHEN** `veritmo data migrate` runs on a database that only has the tables and rows of the run history
- **THEN** the history is intact and its migration is recorded as applied

#### Scenario: Two programs at once

- **WHEN** two programs run `veritmo data migrate` on the same MySQL or Postgres database at the same time
- **THEN** each migration is applied once and both end well

#### Scenario: The same on every engine

- **WHEN** the tests of the data layer run on SQLite, MySQL and Postgres
- **THEN** they pass on the three

### Requirement: What stays in files

Generated and binary media SHALL stay in files and never be stored in the database: videos, frames, audio, pictures, previews, thumbnails, the sources of a project, the working folders `tmp/` and `out/`, and model weights. The database SHALL keep, for a file it refers to, its path relative to the folder it belongs to, never its content. The bootstrap files SHALL stay in files because they are read before the database is opened or by the scripts that start the server: the database settings and the server name in `settings.json`, the pointer files `media-dir`, `models-dir` and `web-dir`, and the pid file. Secrets SHALL stay in files of mode 600 and never be written to the database, to an export or to a log: `keys.json`, the server token, the Hugging Face token and the Civitai token.

#### Scenario: A row with a picture

- **WHEN** an entity has a picture or a video
- **THEN** its row holds the relative path of the file and the file is on disk

#### Scenario: Secrets

- **WHEN** every table of the database and an export are searched for a saved key or token
- **THEN** it is in none of them

### Requirement: The state kept in files is imported once

When the server starts, each entity that was kept in files and whose import is not recorded as done SHALL be imported: every valid file becomes a row, in one transaction per entity. The import SHALL never overwrite a row that exists (the database wins), never change or delete a file, and skip a file it cannot read, recording its path and the reason. An entity whose import had errors SHALL stay usable with the rows that were imported. After its import an entity SHALL be read and written in the database only: a file changed or added by hand afterwards has no effect until an import is asked for. The record of each import SHALL say the entity, when it ran, how many rows it created, how many it skipped because they existed and each file it could not read.

#### Scenario: First start after the upgrade

- **WHEN** the server starts and an entity has 100 valid files and no rows
- **THEN** it has 100 rows, the files are unchanged and the import is recorded as done

#### Scenario: Second start

- **WHEN** the server starts again
- **THEN** nothing is imported and the rows changed since are as they were left

#### Scenario: A file that cannot be read

- **WHEN** one of the files is not valid
- **THEN** the others are imported, and the record names that file and why it was skipped

#### Scenario: A file edited by hand later

- **WHEN** a file of an imported entity is edited by hand
- **THEN** the server keeps answering what the database holds

### Requirement: The status of the data

`GET /api/data/status` SHALL answer the engine in use, the migrations applied (identifier and date) and the ones not applied yet, each table with its number of rows, the record of each import and, per entity, the files left behind (`legacy_files`: how many and how many bytes). `veritmo data status` SHALL print the same.

#### Scenario: After an import with a skipped file

- **WHEN** the status is read
- **THEN** the entity is listed with its rows, the skipped file and its reason, and the files left behind with their size

### Requirement: An import can be asked for

`POST /api/data/import` with `{entity?}` SHALL run the import again for that entity, or for all of them without it, with the same rules: files that have no row become rows, existing rows are untouched. It SHALL answer the record of what it did, `404` naming the entities that exist for an unknown one and `409` while another import runs. `veritmo data import [entity]` SHALL do the same.

#### Scenario: A part dropped in the folder by hand

- **WHEN** a folder is added to the files of an imported entity and its import is asked for
- **THEN** that item is a row and the others are unchanged

### Requirement: The data can be exported and restored

`GET /api/data/export` SHALL answer a `.zip` with one file per table (one JSON object per line) and a manifest with the migrations applied, the engine and the date; it SHALL hold no secret and no media. `veritmo data export <file>` SHALL write the same file. `veritmo data restore <file>` SHALL load an export into an empty database of any of the three engines; it SHALL refuse, changing nothing, a database that already has rows and an export made with migrations the program does not know.

#### Scenario: From SQLite to Postgres

- **WHEN** an export made on SQLite is restored on an empty Postgres database
- **THEN** every table has the same rows

#### Scenario: A database that is not empty

- **WHEN** a restore is asked on a database with rows
- **THEN** it is refused and nothing changes

### Requirement: The files left behind can be cleaned

`POST /api/data/legacy/clean` with `{entities: [...]}` SHALL delete the state files left behind of the entities named, and only of those whose import is recorded as done with no skipped file; it SHALL never delete media, a bootstrap file or a secret. It SHALL answer what it deleted and how many bytes, and `409` naming the entity when its import has skipped files or has not run. Nothing SHALL be deleted without this call.

#### Scenario: An entity imported with no errors

- **WHEN** its files are cleaned
- **THEN** its state files are gone, its media is on disk and the server answers the same as before

#### Scenario: An entity with a skipped file

- **WHEN** its files are asked to be cleaned
- **THEN** the answer is `409` and nothing is deleted

### Requirement: A change of several things is all or nothing

An operation that changes more than one row or table SHALL be applied completely or not at all: a failure in the middle SHALL leave the state as it was before the operation.

#### Scenario: A failure half way

- **WHEN** the second write of an operation fails
- **THEN** the first one is not in the database

### Requirement: The last migration can be undone

`veritmo data rollback <migration>` SHALL undo the migration named when it is the last one applied and it can be undone, and remove its record; it SHALL exit with a non-zero code, changing nothing, when the migration is not the last one applied, when it says it cannot be undone, or while a server is running on the same SQLite database. The server SHALL never undo a migration by itself.

#### Scenario: Undoing the last one

- **WHEN** the last migration applied added a table and is rolled back
- **THEN** the table is gone, the migration is no longer recorded and the next start of that program applies it again

#### Scenario: Not the last one

- **WHEN** a migration that is not the last one applied is named
- **THEN** the command exits with a non-zero code and nothing changes
