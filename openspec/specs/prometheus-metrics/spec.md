# prometheus-metrics Specification

## Purpose
What the server tells Prometheus about itself, its machine and its runs at `GET /api/metrics`, including which client asked for the runs and what kind of run they are.

## Requirements

### Requirement: The run metrics say which client asked and what kind of run it is

`veritmo_runs` and `veritmo_run_duration_seconds` SHALL carry the labels `client` and `kind` besides the ones they already have. `kind` is `video` for a generation of the API, `edit` for an edit of the API and `project` for a run of an ordinary project; it is the way the run was asked for and not the type of the project. `client` is the client the generation or edit was requested with (`unknown` without one) and `embedded` for a project. To keep the number of series bounded, `client` SHALL only take values that fit the form of a client name and, in one reading, at most 20 different clients (the ones with most runs); the runs of any other SHALL be counted under `other`. The labels SHALL NOT name a project, nor its mode.

#### Scenario: a dashboard's runs

- **WHEN** `video-studio` has two finished generations and the dashboard itself one project run
- **THEN** `veritmo_runs{state="succeeded",client="video-studio",kind="video"}` is 2 and `{client="embedded",kind="project"}` is 1

#### Scenario: the duration by client

- **WHEN** `video-studio` has successful generations
- **THEN** `veritmo_run_duration_seconds` has its own series (buckets, sum and count) for `client="video-studio",kind="video"`

#### Scenario: a request without client

- **WHEN** a generation was requested without a client
- **THEN** its runs are under `client="unknown"`

#### Scenario: too many clients

- **WHEN** more than 20 different clients have runs
- **THEN** the 20 with most runs have their own series and the rest are added under `client="other"`

### Requirement: The server exposes its state for Prometheus

`GET /api/metrics` SHALL answer `text/plain; version=0.0.4` in the Prometheus exposition format, with a HELP and a TYPE for every family: `veritmo_info{server,database}`, `veritmo_uptime_seconds`, `veritmo_database_up`, `veritmo_runs{state}` (the runs of this server by state), `veritmo_run_duration_seconds` (a histogram of the latest 1000 successful runs of this server), the RAM of the machine, `veritmo_disk_free_bytes{folder}`, for each GPU the video memory used and total, the use (0 to 1), the temperature and the power (left out without `nvidia-smi`; a counter it cannot give reads 0), and `veritmo_music_service_{running,starting}{service}` for each music service that runs on this machine. It SHALL be worked out when asked and count only the runs of this server. It SHALL need the same access token as the rest of `/api`, and SHALL NOT name a project, nor split by mode, in any label.

#### Scenario: Two servers, one history

- **WHEN** another server has runs in the same database
- **THEN** its runs are not in this server's counts or histogram

#### Scenario: No GPU

- **WHEN** `nvidia-smi` is not installed
- **THEN** the GPU families are absent and the rest is there

#### Scenario: The token

- **WHEN** the server has a token and the call carries none
- **THEN** the answer is `401`

#### Scenario: A hidden project

- **WHEN** a run belongs to a project hidden by the content mode
- **THEN** no label names it
