# model-generation-api Specification

## Purpose
TBD - created by archiving change add-models3d-mode. Update Purpose after archive.

## Requirements

### Requirement: A model can be generated with one call

`POST /api/models3d/generations` SHALL accept `{prompt, engine?, detail?, name?, client?, adult?}` as JSON, or `multipart/form-data` with an `image` part (a `.png`, `.jpg`, `.jpeg` or `.webp`, up to 20 MB) and the same fields as text, and answer `202` with the generation. It SHALL make a managed project (`api-m3d-…`, kind `model_generation`) with one entry, labelled with `client` as the other generation APIs do, and run it. A prompt and an image together, neither, an empty or over-long prompt, an unknown engine or detail, a name that is not plain, or a `client` that is not valid SHALL be `400` naming the field; an engine that cannot be used SHALL be `503` with its reason and nothing is queued; `adult: true` on a censored server SHALL be refused as the other APIs refuse it. `name` SHALL default to a name made from the prompt.

#### Scenario: From a prompt
- **WHEN** `{"prompt": "a golden trophy cup", "client": "studio"}` is posted and the engine is available
- **THEN** it answers `202`, and the run goes through the picture and the model and ends with the model in the library

#### Scenario: A picture
- **WHEN** an image is posted with `detail: "low"`
- **THEN** no picture is made by the server and the model is generated from it

#### Scenario: The engine is down
- **WHEN** a generation is posted and the container is stopped
- **THEN** the answer is `503` with `not_running`

### Requirement: Generations are read, listed, stopped and removed

`GET /api/models3d/generations/{id}` SHALL answer `{id, client, state (queued, running, done, failed, cancelled), step (picture, model), prompt, engine, detail, name, model (the <pack>/<name> made, when done), error, created_at, finished_at, run}`; `GET /api/models3d/generations` SHALL list them newest first, pageable (`limit`, `offset`, `sort`) and filterable by `client`; `POST .../{id}/cancel` SHALL stop one that is queued or running (`409` otherwise) and `DELETE .../{id}` SHALL remove the generation and its managed project but not the model it made; an unknown id SHALL be `404`. The managed projects SHALL appear in `GET /api/projects` only with `managed=include`, with their `client` and `kind: "model_generation"`, and their runs in the run history and the statistics with the mode `models3d`.

#### Scenario: Read and list
- **WHEN** two generations were made by two clients
- **THEN** the list shows both newest first and `client=studio` keeps one

#### Scenario: Delete keeps the model
- **WHEN** a finished generation is deleted
- **THEN** the generation is gone and its model is still in the catalog
