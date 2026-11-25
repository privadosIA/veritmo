# heartmula-engine Specification

## Purpose
TBD - created by archiving change add-heartmula-engine. Update Purpose after archive.

## Requirements

### Requirement: HeartMuLa runs as a service that speaks one small HTTP API

The HeartMuLa engine SHALL be a service, whether it runs as a process started by this program or on another machine, with these calls: `GET /health` answering `{ready, version, gpu: {name, free_mb}}`; `POST /generate` taking `{lyrics, tags, max_ms, seed, temperature, topk, cfg_scale}` and answering a job; `GET /jobs/{id}` answering the state (`queued`, `running`, `done`, `failed`), the progress (frames made out of frames expected) and the error; and `GET /jobs/{id}/audio` answering the song as a 48 kHz stereo WAV once it is done. It SHALL make one song at a time and queue the others, SHALL set the random seed it is given (the model has no seed option of its own), SHALL load its stages on demand and free them after each song so that the GPU is free between songs, and SHALL accept an optional bearer token. The generation SHALL size the attention cache of the language model to the song asked for (the prompt, one frame per 80 ms of audio and a margin) and SHALL decode the audio in bf16, so that a song fits a 12 GB card.

#### Scenario: Health
- **WHEN** a client asks `GET /health` and the weights are loaded
- **THEN** it answers ready with the version and the GPU's name and free memory

#### Scenario: A job
- **WHEN** a client posts lyrics and tags with a seed
- **THEN** it gets a job whose progress grows while it runs and whose audio can be fetched when it is done

#### Scenario: Same seed
- **WHEN** the same request is sent twice with the same seed
- **THEN** the sampling starts from the same random state both times

#### Scenario: The GPU is free between songs
- **WHEN** a song has been made and nothing is queued
- **THEN** the service holds no model in GPU memory

#### Scenario: Token
- **WHEN** a token is configured and a request has none
- **THEN** it is answered 401

### Requirement: The service is a .NET program built from this repository

The service SHALL be the .NET program published by `scripts/get-services.sh` into `bin/services/heartmula`. The weights SHALL be read from `models/music/heartmula/`.

#### Scenario: Build
- **WHEN** the user runs `scripts/get-services.sh`
- **THEN** the service is published in `bin/services/heartmula`

### Requirement: A local engine is started on demand and stopped when the run ends

The engine SHALL count as local when `dotnet` and the published service are there and the weights are in `models/music/heartmula/`. The service (a process named `vt-heartmula`, on the card `<N>`, listening only on `127.0.0.1`) is started by the user from the Environment screen and stays up (see `music-containers`); a run made through the server uses the one that is running and is refused when there is none. A run made with the command line and no server SHALL start the service if it is not running, wait for `GET /health` to say ready, and stop it when the run that started it ends (one that was already running before the run is left as it was). The GPU SHALL be the one with the most free memory among those with at least 10.5 GB free, or the one the run or the user asks for; when none has enough free memory the start SHALL fail with a message that says how much each one has free.

#### Scenario: On demand
- **WHEN** a run is made with the command line (no server) and no service is running
- **THEN** the service is started on the chosen GPU and the run waits until it is ready

#### Scenario: The best GPU
- **WHEN** one GPU has 15 GB free and the other 10 GB
- **THEN** the service uses the first

#### Scenario: No room
- **WHEN** every GPU has less than 10.5 GB free
- **THEN** the start fails naming the free memory of each GPU and nothing is started

#### Scenario: End of the run
- **WHEN** the command line run that started the service ends
- **THEN** the service is stopped, and a service that was already running is left running

#### Scenario: Through the server
- **WHEN** a run is launched through the server and the service is not running
- **THEN** the run is refused and tells the user to start the service from the Environment screen

### Requirement: An external engine can be configured like the Ollama address

The settings SHALL accept the address of a HeartMuLa service on another machine (`heartmula_host`) and an optional token, saved privately like the Ollama address, validated (a host or a URL, no spaces) and tested with `GET /health` from the Settings screen, which SHALL show the state of the engine (local or external, ready or why not). When an external address is configured the engine SHALL be used there and the local one SHALL not be started. An empty value SHALL remove the setting. An address that is not on this machine SHALL be flagged as external, and the first use of it SHALL ask the user to accept that lyrics and tags are sent to it, tied to that address, as for the other external providers.

#### Scenario: Saved and tested
- **WHEN** the user saves `192.168.1.50:8787` and tests it
- **THEN** the setting is stored, the test says ready or why not, and later songs go to that machine

#### Scenario: Not configured
- **WHEN** no address is configured
- **THEN** the local engine is used if there is one

#### Scenario: Consent
- **WHEN** the address is not this machine and the user has not accepted it
- **THEN** the engine is not available and the reason says so

### Requirement: The engines available are reported, and the engine is offered only when it is

`GET /api/music/engines` SHALL answer, for the engine `heartmula`, `{available, mode: "local" | "external", reason?}`, where `reason` is `no_service`, `no_weights`, `no_gpu`, `unreachable`, `no_consent` or `key_rejected`. It SHALL ask nothing of the service but `/health`, SHALL start no service and SHALL be kept 30 seconds and dropped when the settings change. The music screens SHALL show the engine choice (the engine selector and the quality level that uses it) only when it is available, SHALL hide it otherwise, and SHALL show a notice with how to install it (build the service, download the weights) only when the cause is something missing on this machine (`no_service`, `no_weights`); an unreachable external address SHALL be shown in Settings but not as a notice in the project.

#### Scenario: Available
- **WHEN** the local engine is ready
- **THEN** the engine selector shows HeartMuLa

#### Scenario: Not anywhere
- **WHEN** the service is not installed and no address is configured
- **THEN** the music project does not show the option and no notice is shown

#### Scenario: Weights missing
- **WHEN** the service is there but the weights are not
- **THEN** the option is hidden and a notice says to download them from Models

#### Scenario: Unknown means hidden
- **WHEN** the answer has not arrived yet
- **THEN** the option is hidden
