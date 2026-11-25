# database-choice Specification

## Purpose
TBD - created by archiving change add-database-choice. Update Purpose after archive.

## Requirements

### Requirement: The history can be kept in SQLite, MySQL or Postgres

The server SHALL keep the run history, the reviews and the speed measurements in the engine saved in Settings: the SQLite file at `--db` when nothing is saved (and when `sqlite` is saved), or a MySQL or Postgres database given by host, port (3306 or 5432 by default), database name, user and password. The engine SHALL be read once, when the server starts. `GET /api/settings/database` SHALL answer `saved` (what is saved) and `active` (what the server started with), `restart_required` when they differ, `server_name`, and `servers` (the names that have runs in the history), and SHALL never return the password (`password_set` instead). `PUT` SHALL validate and save without connecting (`400` naming what is wrong: an unknown engine, an empty host, name or user, a port out of range, a host with a scheme or a path, a name with characters that are not allowed); a password left out SHALL keep the saved one only when the engine, host, port and user are the same; `sqlite` SHALL remove everything saved.

#### Scenario: Saved but not yet in use

- **WHEN** MySQL is saved while the server runs on SQLite
- **THEN** `saved` says mysql, `active` says sqlite and `restart_required` is true

#### Scenario: The password

- **WHEN** the settings are read, or saved again with no password for the same server
- **THEN** the password is not in any answer and the saved one is kept

#### Scenario: Another host

- **WHEN** the host changes and no password is sent
- **THEN** the saved password is not kept

### Requirement: A connection can be tested without changing anything

`POST /api/settings/database/test` SHALL open a connection with the form (the saved password when it is left out) within 10 seconds and answer `{ok, version?, empty?, error?}`; a database nobody answers on SHALL be `ok: false` with the error, not an HTTP error. It SHALL create and write nothing. SQLite SHALL be `400`: it has nothing to test.

#### Scenario: Nobody answers

- **WHEN** the port is closed
- **THEN** the answer is `200` with `ok: false` and the error

#### Scenario: An empty database

- **WHEN** the database answers and has no tables of the history
- **THEN** `ok` is true and `empty` is true

### Requirement: The SQLite history can be copied to the database

`POST /api/settings/database/copy` SHALL create the tables in the saved MySQL or Postgres database and copy into it, in one transaction, the runs, events, samples, reviews, findings and calibration samples of the SQLite file, leaving alone the rows whose id is already there (so it can be repeated) and giving this server's name to the rows that have none. It SHALL answer how many rows of each table were copied; `409` without a saved database, when this server already keeps its history in it, or while a run is queued or running; `502` when it fails, with nothing changed.

#### Scenario: Twice

- **WHEN** the copy is made twice
- **THEN** the second adds no rows

#### Scenario: A run is going

- **WHEN** a run is active
- **THEN** the answer is `409` and nothing is copied

### Requirement: Each row knows which server made it

Runs, reviews and calibration samples SHALL record the server that made them (the saved name, the computer's by default); rows that predate this SHALL be given to the server that opens the history. At start a server SHALL mark as interrupted only its own queued or running runs, and the speed measured for a card SHALL be computed from that server's samples only. When the history is in MySQL or Postgres, the ids of runs and reviews SHALL end with a random suffix of this process so two servers never make the same id. `GET /api/runs` rows carry `server`.

#### Scenario: Another server restarts

- **WHEN** a server starts and another one has a running run in the same history
- **THEN** that run is still running

#### Scenario: A card's speed

- **WHEN** two servers measured a card of the same model
- **THEN** each one's speed is computed from its own samples

### Requirement: The settings card

Settings SHALL have a **Database** card with the engine, the fields of a server, the name of this server, a test button, a save button and, when a server is saved and this one still uses the file, a copy button; it SHALL say what is in use and that a restart is needed to apply what is saved, show the servers that have runs, and never show the password.

#### Scenario: Copy offered

- **WHEN** MySQL is saved and the server uses SQLite
- **THEN** the card offers the copy

### Requirement: The chosen database holds all the state

The database chosen in Settings SHALL hold every table of the data layer (see `data-store`), not only the run history. `POST /api/settings/database/copy` SHALL copy every table of the data layer from the SQLite file to the database of the form, with the rules it has for the history (rows that exist in the destination are not replaced), and its answer SHALL say, per table, how many rows it copied and how many were already there. The copy SHALL never write a secret to the destination.

#### Scenario: Copy to an empty Postgres

- **WHEN** the copy is asked from a SQLite database with history and other state to an empty Postgres database
- **THEN** every table has the same rows in Postgres and the answer lists each table with its count

#### Scenario: Copy twice

- **WHEN** the copy is asked again
- **THEN** nothing is duplicated and each table is reported with zero copied rows

### Requirement: The database can be given by the environment

The server and the commands that open the database SHALL take the engine, host, port, database name, user and password from the environment (`VERITMO_DB_DRIVER`, `VERITMO_DB_HOST`, `VERITMO_DB_PORT`, `VERITMO_DB_NAME`, `VERITMO_DB_USER`, `VERITMO_DB_PASSWORD`; the former `MO_*` names are read as a fallback with a warning, as `project-identity` says) when nothing is saved in the settings file, so that a container needs no hand-written file. What is saved in the settings file SHALL win over the environment. The password SHALL be read only from the environment variable or the file and SHALL NOT appear in any answer of the API, in a log line or in the status of the data layer. `GET /api/settings/database` SHALL say whether what is active comes from the settings file or from the environment.

#### Scenario: A container with no settings file

- **WHEN** the server starts with the variables of a MySQL in its environment and no saved database
- **THEN** it keeps all its state in that MySQL and `GET /api/settings/database` says the source is the environment

#### Scenario: Saved settings win

- **WHEN** a database is saved in the settings file and the environment names another
- **THEN** the server uses the saved one

#### Scenario: The password is never shown

- **WHEN** the settings and the status of the data layer are read
- **THEN** the password of the environment is in none of them
