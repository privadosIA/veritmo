# project-identity Specification

## Purpose
TBD - created by archiving change rename-to-veritmo. Update Purpose after archive.

## Requirements

### Requirement: The program is called Veritmo

The project SHALL be named `Veritmo` everywhere it names itself: the Go module SHALL be `veritmo` and every import path SHALL start with `veritmo/`; the command SHALL be `veritmo` (built from `cmd/veritmo/`), with `veritmo-api` accepted as an alias name of the same binary; the user directories SHALL be `~/.config/veritmo/` and `~/.cache/veritmo/` (honouring `XDG_CONFIG_HOME` and `XDG_CACHE_HOME`); environment variables SHALL be prefixed `VERITMO_`; Docker images SHALL be `veritmo/<component>`, the compose project `veritmo` and the music containers `vt-<engine>`; Prometheus families SHALL be prefixed `veritmo_`; the OpenAPI `title` SHALL be `Veritmo API`; and the `client` the server sends to the compression service SHALL be `veritmo`. `README.md` SHALL open with the tagline *"Veritmo — ver el ritmo"* and one sentence saying what the program does. No file of the repository outside `.orchestrator/` SHALL contain `mediaOrquestator` or `mediaorquestator`, except the code that migrates the old directory and reads the old variables, and the history notes that mention the former name.

#### Scenario: The binary names itself
- **WHEN** `veritmo --version` runs
- **THEN** it prints `veritmo` and its version, and `veritmo-api --version` prints the same

#### Scenario: Nothing old remains
- **WHEN** the repository is searched for the old name, case-insensitively, outside `.orchestrator/`, `third_party/` and the migration code
- **THEN** nothing is found

#### Scenario: The metrics carry the new prefix
- **WHEN** `GET /api/metrics` is read
- **THEN** every family starts with `veritmo_` and none with `mediaorquestator_`

### Requirement: The old user directories are moved once

When `~/.config/veritmo/` does not exist and `~/.config/mediaorquestator/` does, the first process that opens the configuration (the server or any command) SHALL rename the old directory to the new one and SHALL log one line naming both paths; the same SHALL hold for `~/.cache/`. When both exist, the new one SHALL be used and a warning SHALL say the old one is ignored and may be deleted. Nothing SHALL be copied and nothing SHALL be deleted. `scripts/serve.sh` and `scripts/install.sh` SHALL apply the same rename before reading their files (token, `media-dir`, `web-dir`, pid files, `install.log`).

#### Scenario: A server updated in place
- **WHEN** the new binary starts on a machine whose `~/.config/mediaorquestator/` holds `token`, `settings.json`, `keys.json` and `models-dir`
- **THEN** `~/.config/veritmo/` holds those files, the old directory is gone, the server answers to the same token and lists the same providers, and the log says what was moved

#### Scenario: Both directories exist
- **WHEN** the old and the new directory both exist
- **THEN** the new one is used, the old one is untouched and a warning names it

#### Scenario: A fresh machine
- **WHEN** neither directory exists
- **THEN** the new one is created as before and nothing is logged about a move

### Requirement: The old environment variables are still read, with a warning

Every setting read from a `VERITMO_<X>` variable SHALL fall back to `MO_<X>` when the new one is unset, and the first use of a fallback in a process SHALL log `MO_<X> is obsolete, use VERITMO_<X>`. When both are set, the new one SHALL win. The compose files, the workflow and the documentation SHALL use only the new names.

#### Scenario: An old deploy
- **WHEN** the server starts with `MO_DB_DRIVER`, `MO_DB_HOST`, `MO_DB_NAME`, `MO_DB_USER` and `MO_DB_PASSWORD` in its environment and no `VERITMO_*`
- **THEN** it opens that database and logs one obsolete-variable line per variable used

#### Scenario: Both set
- **WHEN** `VERITMO_DB_NAME=a` and `MO_DB_NAME=b`
- **THEN** the database is `a`
