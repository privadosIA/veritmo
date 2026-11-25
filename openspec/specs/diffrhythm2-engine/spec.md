# diffrhythm2-engine Specification

## Purpose
DiffRhythm2 makes full songs from lyrics and a style text. It runs as a .NET service on this machine, or as the same service on another machine, and behaves like the HeartMuLa engine from the point of view of the settings, the availability and the studio.

## Requirements

### Requirement: DiffRhythm2 runs as a service that speaks one small HTTP API

The engine SHALL be a service with `GET /health` (`{ready, version, busy, queued, gpu: {name, free_mb, total_mb}}`), `POST /generate` (`{lyrics, style, max_secs, seed, cfg_strength, steps}`, answering `202 {id}`), `GET /jobs/{id}` (`{id, state, progress, error, seconds}`), `GET /jobs/{id}/audio` (the song as a 48 kHz stereo WAV, `409` until the job is done) and `DELETE /jobs/{id}`. It SHALL make one song at a time and queue the others, SHALL fix the random generator with the seed it is given (the model has no seed of its own) so that the same request gives the same song, and SHALL accept an optional bearer token. `lyrics` is the text with its structure tags; the service SHALL write the tags in lower case, keep only those the model knows (`start`, `intro`, `verse`, `chorus`, `bridge`, `outro`, `inst`, `solo`, `hook`, `break`, `end`, `stop`, `space`), drop the others and add `[start]` when it is missing. A request with no lyrics, with lyrics that have no line of text, or with an empty style SHALL be refused with `400` and the field named.

#### Scenario: A song
- **WHEN** `POST /generate` is sent with English lyrics, a style and a seed
- **THEN** the answer is `202` with an id, the job reaches `done` and `/audio` returns a 48 kHz stereo WAV

#### Scenario: The same seed
- **WHEN** the same request is sent twice with the same seed
- **THEN** the two songs are the same

#### Scenario: A tag the model does not know
- **WHEN** the lyrics have `[Pre-Chorus]` and `[Verse]`
- **THEN** the first is dropped, the second is written as `[verse]` and `[start]` is added

#### Scenario: No lyrics
- **WHEN** the request has no lyrics
- **THEN** it is refused with `400` and the message names `lyrics`

### Requirement: The service is a .NET program built from this repository

The service SHALL be the .NET program published by `scripts/get-services.sh` into `bin/services/diffrhythm2`, with the weights read from `models/music/diffrhythm2/` (the converted files). Its version SHALL be what `/health` answers. Starting it SHALL give it one GPU and a port, SHALL never listen beyond `127.0.0.1` and SHALL stop it when the run that started it ends.

#### Scenario: Not ready
- **WHEN** the service starts without the weights
- **THEN** `/health` answers `ready: false` with the missing files

#### Scenario: Weights
- **WHEN** the weights are there
- **THEN** `/health` answers `ready: true` and the name and free memory of the card

#### Scenario: Installing the weights from the catalog
- **WHEN** the `music-diffrhythm2` catalog entry is installed
- **THEN** the server downloads its raw checkpoint files and then runs `diffrhythm2 install --model <folder> --accept-licenses` from `bin/services/diffrhythm2`, which fetches and verifies the MuQ and XLM-RoBERTa models and converts everything into the files the service reads (no Python)
- **AND** the entry counts as installed only when the files the service reads are all there
- **AND** the download fails with the reason when the executable is not built or the command fails, and installing the entry again resumes it

### Requirement: A local engine is started on demand and stopped when the run ends

The engine SHALL use the external service when an address is saved, and the local service otherwise. For the local service it SHALL check, in this order, that `dotnet` and the published service are there, that the weights are complete and that a card has the 8 GB the model needs, and SHALL name the first that fails with its reason code. The service (named `vt-diffrhythm2`) is started by the user from the Environment screen on the card chosen there, or on the one with most free memory, and stays up (see `music-containers`); a run made through the server uses the one that is running and is refused when there is none. A run made with the command line and no server SHALL start the service on the card the run asks for, or on the one with most free memory, wait up to three minutes for `ready`, and stop it when the run ends; a service that was already running is left alone. The service SHALL keep its models in RAM between songs, move them to the card only while a song is made and bring them back, emptying the card's cache, when the song ends or fails.

#### Scenario: Everything is there
- **WHEN** a run is made with the command line (no server) with `diffrhythm2` and the engine is local
- **THEN** the service is started on a card with enough memory, the song is made and the service is stopped

#### Scenario: A card that is too small
- **WHEN** no card has 8 GB
- **THEN** the engine is unavailable with the reason `no_gpu`

#### Scenario: Through the server
- **WHEN** a run is launched through the server and the service is not running
- **THEN** the run is refused and tells the user to start the service from the Environment screen

#### Scenario: At rest
- **WHEN** the service is up and no song is being made
- **THEN** the models are in RAM and the card holds no memory of the service

### Requirement: An external engine is configured like the HeartMuLa one

The settings SHALL accept the address of a DiffRhythm2 service on another machine (`diffrhythm2_host`), an optional token (`diffrhythm2_token`, saved privately, never shown again) and the acceptance that the lyrics and the style are sent there (`diffrhythm2_accept`), with the same validation, the same default port rule, the same acceptance rule for any address that is not this machine and the same removal by sending an empty value as the HeartMuLa address. `GET /api/settings` SHALL carry `diffrhythm2` (`{saved, token_set, remote, consented}`, never the token) and `POST /api/settings/diffrhythm2/test` SHALL ask `GET /health` and answer whether the engine is ready or why not. The Settings screen SHALL have a card with the same fields, the same test button and the same state line as the HeartMuLa card. When an address is saved the engine SHALL be used there and the local one SHALL not be started.

#### Scenario: Saved and tested
- **WHEN** the user saves `192.168.1.50:8788`, accepts it and tests it
- **THEN** the setting is stored, the test says ready or why not, and later songs go to that machine

#### Scenario: No acceptance
- **WHEN** the address is not this machine and has not been accepted
- **THEN** the engine is unavailable with the reason `no_consent` and nothing is sent

#### Scenario: The token
- **WHEN** the settings are read
- **THEN** only `token_set` says whether there is a token

### Requirement: The engines available are reported

`GET /api/music/engines` SHALL carry `diffrhythm2` with `{available, mode: "local" | "external", reason?, detail?, missing?, version?}` and the same reasons as `heartmula`, asking the service nothing but `/health`, starting no service, keeping the answer 30 seconds and dropping it when the settings change. The music screens SHALL offer the engine only when it is available, and SHALL say how to install it when only the service or the weights are missing.

#### Scenario: Available
- **WHEN** the local engine is ready
- **THEN** the generator selector offers DiffRhythm2

#### Scenario: Nothing installed
- **WHEN** the service is not installed and there is no address
- **THEN** the option is hidden and no notice is shown
