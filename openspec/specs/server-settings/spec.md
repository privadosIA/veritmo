# server-settings Specification

## Purpose
Where the settings of the server are kept, what stays in the settings file because it is read before the database is opened, where the secrets are, and how the server and the command line see a change of a setting.

## Requirements

### Requirement: The settings are kept in the database

The server SHALL keep its settings in the database: the Ollama host, the browser, the model of the motion graphics, the routing of the tasks, the consent given to remote providers and their limit of calls, the hosts and the backends (`dotnet` or `docker`) of HeartMuLa and DiffRhythm2, the monitoring tools, the origins and networks of CORS, the review of drafts and the ctxpress connection. `settings.json` SHALL keep only the database settings and the name of the server, which are read before the database is opened. The routes that read and save settings SHALL answer as before. The settings kept in `settings.json` SHALL be imported when the server starts (see `data-store`), without changing that file. A setting changed in `settings.json` by hand, other than the database settings and the server name, SHALL have no effect.

#### Scenario: After the import

- **WHEN** the server starts with settings kept in the file
- **THEN** `GET /api/settings`, `GET /api/routing`, `GET /api/settings/monitoring` and `GET /api/settings/ctxpress` answer the same bodies as before

#### Scenario: The database settings

- **WHEN** the database settings are saved
- **THEN** they are written to `settings.json` as before, and nothing of them is in the database

### Requirement: A change of a setting is seen by every process

A setting saved through the API or by a command SHALL be used by the next operation of the server and of any command, without a restart; the CORS policy SHALL follow a change within 5 seconds.

#### Scenario: A routing changed while a run waits

- **WHEN** the routing of a task is saved and a run that uses that task starts
- **THEN** the run uses the new routing

#### Scenario: A music backend changed

- **WHEN** the backend of a generator is saved while no run uses it
- **THEN** the next start, stop and availability use the new backend, without a restart

#### Scenario: A command changes a setting

- **WHEN** `veritmo cors` adds an origin while the server runs
- **THEN** within 5 seconds the server answers CORS requests of that origin

### Requirement: Every secret is in the private keys file

The tokens of HeartMuLa and DiffRhythm2 and the secrets of the monitoring tools SHALL be kept in the private keys file (mode 600) with the keys of the providers and the ctxpress token, and SHALL be copied there from `settings.json` when the settings are imported. No secret SHALL be in the database, in an export, in an answer of the API or in a log. The password of the database SHALL stay in `settings.json` (mode 600). Cleaning the files left behind of the settings SHALL rewrite `settings.json` with only the database settings and the server name, never delete it.

#### Scenario: A token saved before the upgrade

- **WHEN** the server starts with a HeartMuLa token in `settings.json`
- **THEN** the HeartMuLa test still works with that token, and the token is in the keys file and not in the database

#### Scenario: Cleaning

- **WHEN** the files left behind of the settings are cleaned
- **THEN** `settings.json` holds the database settings and the server name only, and the server starts with it
