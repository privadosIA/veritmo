# music-docker-option Specification

## Purpose
The two song generators (HeartMuLa and DiffRhythm2) can run as Python containers instead of the .NET services: the images and the script that runs them by hand, and what the server does with them: the backend each generator uses as a setting, the start and stop of the container, what each backend lacks, and the install of what is missing from the page. LAION-CLAP has no container.

## Requirements

### Requirement: The generators can run as Python containers

The repository SHALL carry the images of HeartMuLa and DiffRhythm2 (Python) with the same HTTP contract as the .NET services (`/health`, `/generate`, `/jobs/{id}`, `/jobs/{id}/audio`), and a script that builds them, starts and stops their containers and says which address to save in Settings. Using them SHALL be optional and SHALL NOT require the .NET services.

#### Scenario: A container as the generator
- **WHEN** a container is started with the script and its address is saved as `heartmula_host`
- **THEN** the engines API lists HeartMuLa as available in `external` mode and a music run can use it

#### Scenario: Nothing leaves the machine
- **WHEN** a container is started
- **THEN** it listens on 127.0.0.1 only and mounts the weights read-only

### Requirement: The Python services are tested without a GPU

The Python servers SHALL keep tests that run with no GPU and no weights, and the script SHALL have a test with a stand-in for the container program.

#### Scenario: Tests without hardware
- **WHEN** the tests of `docker/heartmula` and `docker/diffrhythm2` run on a machine with no GPU
- **THEN** they pass

### Requirement: The backend of each generator is a setting

`PUT /api/settings` SHALL accept `heartmula_backend` and `diffrhythm2_backend`, each `dotnet` or `docker` (an empty text removes the setting, which means `dotnet`), and `GET /api/settings` SHALL show in the `heartmula` and `diffrhythm2` blocks `backend` and `backend_source` (`setting` or `default`). The change SHALL apply to the next operation without a restart. An address saved in `heartmula_host` or `diffrhythm2_host` SHALL win over the backend: the generator is then external. The save SHALL answer `422` for another value and, for `docker`, when Docker does not answer (`reason: no_docker`) and `409` while a run is making songs with the generator or it is starting; it SHALL stop what runs on the old backend and save nothing when it refuses.

#### Scenario: Saved and read back
- **WHEN** `diffrhythm2_backend` is saved as `docker`
- **THEN** the next `GET /api/settings` shows `backend: docker` and `backend_source: setting` for DiffRhythm2 and `dotnet` and `default` for HeartMuLa

#### Scenario: No Docker
- **WHEN** `docker` is saved and Docker does not answer
- **THEN** the answer is `422` with `no_docker` and the setting is not saved

#### Scenario: The old backend is stopped
- **WHEN** the backend changes from `docker` to `dotnet` while the container runs and no run uses it
- **THEN** the container is removed and the new backend is saved

### Requirement: The server runs the container as the script does

With the `docker` backend the server SHALL run `veritmo/heartmula:1` and `veritmo/diffrhythm2:1` with the name `vt-<engine>`, `--restart no`, the card chosen, the port of the engine published on `127.0.0.1` only and the folder of the weights mounted read-only, and SHALL report its state as the state of the .NET service (`stopped`, `starting`, `running`, `gpu`, `port`, `since`). A container that does not say ready in time SHALL be removed and the reason kept in `container.error`. `scripts/music-containers.sh` SHALL run the same images, names, ports and flags, and a test SHALL fail when the two differ. Nothing SHALL start by itself when the machine starts.

#### Scenario: A container that does not get ready
- **WHEN** a container is started and never answers ready
- **THEN** it is removed and `container.error` says why

#### Scenario: Script and server agree
- **WHEN** the image, the port, the name or a flag changes in the script or in the server but not in the other
- **THEN** the test that compares them fails

### Requirement: The engines say what each backend lacks

For `heartmula` and `diffrhythm2`, `GET /api/music/engines` SHALL carry `backends: {dotnet, docker}` and for each `{available, missing?, reason?, detail?, need_bytes?}`. `missing` SHALL list what is lacking in the order a person fixes it: `service` and `weights` for `dotnet`; `docker`, `image`, `weights` and `gpu_runtime` for `docker`. `reason` SHALL be the first of them as the usual code (`no_service`, `no_weights`, `no_docker`, `no_image`, `no_gpu_runtime`) and `need_bytes` the approximate disk the missing parts take. The engines without a container (LAION-CLAP, video vision) SHALL carry none.

#### Scenario: Image missing
- **WHEN** Docker answers, has the NVIDIA runtime, the weights are complete and the image is not built
- **THEN** `backends.docker.missing` is `["image"]`, `reason` is `no_image` and `need_bytes` is the size of the image

#### Scenario: No Docker
- **WHEN** Docker does not answer
- **THEN** `backends.docker.missing` starts with `docker` and `reason` is `no_docker`

### Requirement: What a generator lacks is installed from the server

`POST /api/music/engines/{engine}/install` with `{backend?, parts?, confirm?}`, for `heartmula` and `diffrhythm2` (`404` for another engine), SHALL, without `confirm`, answer `200` with the plan and change nothing: the parts missing (`service`, `image`, `weights`, in that order), the approximate size of each, the licences that confirming accepts, and for each place the disk it needs against what it has free. With `confirm: true` it SHALL answer `202` with a job that runs the parts in order and can be read (`GET /api/music/installs/{id}`), followed (`.../events`, with the log lines of the program that runs) and cancelled (`POST .../cancel`). The request SHALL name no program, image or path: `service` is `scripts/get-services.sh --only <engine>`, `image` is `docker build` of `docker/<engine>`, and `weights` is the catalog download (for DiffRhythm2 with the `docker` backend, `fetch.py` of its image). It SHALL answer `409` while the generator runs, is starting or a run uses it, while another install runs, and when nothing is missing; and `422` when the disk lacks the space (`no_disk`, saying how much), when Docker cannot be used (`no_docker`) or for a part that does not exist for the backend. A part that is there when its turn comes SHALL be skipped, so that repeating an install that failed does only what is missing. Cancelling SHALL leave the job `cancelled`, the part that was running `cancelled` and the ones after it `pending`.

#### Scenario: A plan
- **WHEN** the install of `diffrhythm2` for `docker` is asked without `confirm` and the image and the weights are missing
- **THEN** the answer lists `image` and `weights` with their sizes, the non-commercial licence of the MuQ-MuLan weights and the disk, and nothing is run

#### Scenario: Confirmed
- **WHEN** it is confirmed
- **THEN** the answer is `202`, the image is built and then the weights are fetched, and the job ends `done`

#### Scenario: Not enough disk
- **WHEN** a place has less free space than the parts that go there need
- **THEN** the answer is `422` with `no_disk` and how much is missing, and nothing is run

#### Scenario: A failure and a repeat
- **WHEN** the weights fail after the image was built, and the install is asked again
- **THEN** the first job is `failed` with the part and the reason, and the second runs only the weights
