# content-mode Specification

## Purpose
The server shows or hides adult projects according to a mode that is fixed when it starts. By default it is censored: a project marked for adults does not exist for the API, the dashboard or anything that lists projects, runs or files.

## Requirements

### Requirement: The server starts censored, or strict, and keeps that mode

`veritmo serve` SHALL start in the `censored` mode unless it is given `--strict`, which starts the `strict` mode. The mode SHALL be fixed for the life of the process: no request, setting, environment variable or file SHALL change it while the server runs, and no request header or query SHALL ask for another. `scripts/serve.sh` SHALL start the server in the `censored` mode and in the `strict` mode when `STRICT=1`.

#### Scenario: the default
- **WHEN** the server starts with no flag
- **THEN** its mode is `censored`

#### Scenario: strict
- **WHEN** the server starts with `--strict`
- **THEN** its mode is `strict`

#### Scenario: nothing changes it
- **WHEN** a request carries a header, a query or a body that asks for another mode
- **THEN** it is ignored and the mode stays

### Requirement: A hidden project does not exist for the API

In the `censored` mode a project with `adult: true` is hidden; in the `strict` mode a project with no `adult` is hidden as well. A hidden project SHALL be left out of every list and total, and every request that names it, by id, by the id of one of its runs, reviews or songs, or by a path into its folders, SHALL answer `404` with the same body as for a project that does not exist. This covers the project list and its sizes, the project's configuration, cache, sources, peaks, loudness, artifacts, thumbnails and previews, storage and its cleaning, reviews and their frames, exports and duplication, scene motion, the video-clip plan and creation, runs (launch, list, detail, cancel, delete, resources, statistics and the event stream), the clip and image listings of the gallery, the global storage report and the music library. A bulk call (`POST /api/runs/delete`) SHALL ignore the ids of hidden runs as if they were unknown.

#### Scenario: the list
- **WHEN** the projects `a` (`adult: true`), `b` (`adult: false`) and `c` (no label) exist and the mode is `censored`
- **THEN** `GET /api/projects` lists `b` and `c` and not `a`

#### Scenario: strict
- **WHEN** the same projects exist and the mode is `strict`
- **THEN** only `b` is listed

#### Scenario: a call that names a hidden project
- **WHEN** `GET /api/projects/a/config` is called in the `censored` mode
- **THEN** the answer is the `404` that an unknown project gets, with the same body

#### Scenario: a file of a hidden project
- **WHEN** `GET /api/projects/a/artifacts/out/a/s1.png` is called in the `censored` mode
- **THEN** the answer is `404`

#### Scenario: a run
- **WHEN** a run of `a` exists and its detail, events or resources are asked for in the `censored` mode
- **THEN** each answers `404`, and `GET /api/runs` does not list it

#### Scenario: totals
- **WHEN** runs of `a` and `b` exist
- **THEN** the statistics, the storage report and the gallery listings count and list only `b`'s

#### Scenario: nothing can be launched on it
- **WHEN** `POST /api/projects/a/runs` is called in the `censored` mode
- **THEN** the answer is `404` and no run is queued

### Requirement: Nothing for adults is created or entered while censored

In the `censored` mode and the `strict` mode the server SHALL refuse, with `422` and the code `content_not_allowed`, every request that would make a project with `adult: true`: creating a project, saving a configuration, importing a dataset or a package, duplicating, or changing the label to `true`. The message SHALL say that this server does not keep content for adults and SHALL NOT say how the mode could be changed. A project with no label or with `adult: false` is not affected.

#### Scenario: saving the label
- **WHEN** `PUT /api/projects/b/config` carries `adult: true` in the `censored` mode
- **THEN** the answer is `422 content_not_allowed` and the file is unchanged

#### Scenario: importing a package
- **WHEN** a package whose `scenes.yaml` has `adult: true` is imported in the `censored` mode
- **THEN** the answer is `422 content_not_allowed` and no project is created

### Requirement: A project that cannot be read is hidden by the strict mode and shown by the censored one

A project whose `scenes.yaml` cannot be read SHALL be treated as not reviewed: the `censored` mode lists it as invalid like any broken project and the `strict` mode hides it. The label SHALL be read from the file on each request (or from a cache keyed by the file's modification time), so that a change on disk is seen at once.

#### Scenario: the file changes on disk
- **WHEN** a project's `scenes.yaml` gets `adult: true` while the server runs in the `censored` mode
- **THEN** the next request treats the project as hidden

### Requirement: The dashboard follows the mode

The dashboard SHALL read `content_mode` from the environment report and SHALL show it in the environment view. In the `censored` and `strict` modes the label control SHALL offer only "reviewed, fine for everyone" and "not reviewed", and SHALL say that this server keeps no content for adults.

#### Scenario: the control
- **WHEN** the editor opens in the `censored` mode
- **THEN** the "for adults" choice is not offered
