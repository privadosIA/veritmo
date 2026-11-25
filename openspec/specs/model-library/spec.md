# model-library Specification

## Purpose
TBD - created by archiving change add-model-library. Update Purpose after archive.

## Requirements

### Requirement: The server knows a list of 3D model packs

The server SHALL ship a list of 3D model packs that a person can install, each with an `id`, a `name`, its `author`, the `licence` (only `CC0` packs are in the list), the page the pack is published on, its `version`, its size, a short description of what it holds (characters, props) and the SHA-256 of the zip of that version. Only packs whose zip holds binary glTF (`.glb`) files SHALL be listed. `GET /api/models3d/packs` SHALL answer `{packs: [...]}` with those of the list and the ones the person imported, each with its `state` (`available`, `installing`, `installed` or `failed`, with the `error` of a failure), its `source` (`catalog` or `user`), and, when installed, the number of models it has. The folder the library lives in SHALL be `models3d/` or the value of `MODELS3D_DIR`, and nothing in it SHALL be committed.

#### Scenario: The list
- **WHEN** the packs are listed on a server that has installed nothing
- **THEN** every pack of the list is `available` with its size, author, licence and page

#### Scenario: A pack that is not in the list
- **WHEN** a pack is asked for by an id that is neither in the list nor imported
- **THEN** the answer is `404`

### Requirement: A pack is installed on demand, checked and unpacked safely

`POST /api/models3d/packs/{id}/install` SHALL queue the download of a pack of the list and answer `202` with the pack in state `installing`; `GET /api/models3d/packs/{id}` SHALL report its progress (bytes done and total) and `POST /api/models3d/packs/{id}/cancel` SHALL stop it and leave nothing behind. The download SHALL only be made from the host the list names for that pack, SHALL refuse a zip larger than 200 MB, and SHALL check the SHA-256 of the zip against the one in the list: a zip that does not match SHALL be refused with the state `failed` saying that the pack changed and is no longer the version that was checked, and nothing is installed. The zip SHALL be unpacked into a temporary folder and moved into place only when it is complete: an entry that is a link, an absolute path or leaves the folder (`..`), more than 5,000 entries or more than 1 GB unpacked SHALL refuse the whole pack, and only the files a library uses (`.glb`, the pictures they name, previews, the licence text) SHALL be kept. Installing an installed pack SHALL be `409`. A server with no network, a page that no longer has the pack's download or any other failure SHALL leave the pack `failed` with the reason and no half-installed files, and the person can retry.

#### Scenario: Install
- **WHEN** a pack of the list is installed
- **THEN** it is queued, reports its progress, ends `installed`, its models are in the catalog and the folder holds its `.glb` files, its pictures and its licence text

#### Scenario: A pack that changed
- **WHEN** the zip that is downloaded has another hash than the one in the list
- **THEN** the pack is `failed` saying so and no file of it is in the library

#### Scenario: A zip that tries to leave its folder
- **WHEN** a zip has an entry named `../x.glb` or a link
- **THEN** the pack is refused and nothing is installed

#### Scenario: Cancel
- **WHEN** an install is cancelled while it downloads
- **THEN** the pack goes back to `available` and the temporary files are removed

### Requirement: A pack can be imported from a zip the person has

`POST /api/models3d/packs/import` with a zip (`application/zip`, up to 200 MB) and the query `id` (a plain lower-case name), `name`, `author`, `licence` (a text, required: what the pack allows) and `source_url` (optional) SHALL unpack it with the same safety rules, keep what a library uses, index it and answer `201` with the pack, marked `source: "user"` and carrying the licence text given. A pack with no `.glb` SHALL be refused with `422` saying so; an `id` already in use SHALL be `409`; a missing `licence` SHALL be `400`. `DELETE /api/models3d/packs/{id}` SHALL remove an installed or imported pack and its files, and a pack of the list goes back to `available`.

#### Scenario: A pack from Google Drive
- **WHEN** a person uploads a zip of `.glb` files with `licence=CC0 by Quaternius`
- **THEN** it is `installed` with `source: "user"` and its models are in the catalog with that licence

#### Scenario: No licence
- **WHEN** a zip is imported with no `licence`
- **THEN** it is refused with `400` and nothing is stored

#### Scenario: Remove
- **WHEN** an installed pack is deleted
- **THEN** its files and its models disappear from the library, and a pack of the list is `available` again

### Requirement: The models of the installed packs are a catalog

`GET /api/models3d/models` SHALL list the models of the installed packs: `{models: [{id, pack, name, tags, clips, triangles, needs, licence, author, attribution}], total}`, where `id` is `<pack>/<name>`, `clips`, `triangles` and `needs` are read from the file (as the sources of a project do), `tags` come from the pack and from the words of the file name (`character`, `female`, `a`…), and `attribution` says whether the licence asks for the author to be named (false for CC0). It SHALL accept `pack`, `q` (searched in the name and the tags without regard to case), `tag`, `has_clips` (`true`), `limit` and `offset`, with `X-Total-Count` and `Link` when paged. `GET /api/models3d/models/{pack}/{name}` SHALL answer one model, `404` when it is not there. A file of a pack that is not a usable binary glTF SHALL be left out of the catalog and named in a warning of the pack, not make the pack fail. A model's thumbnail, `GET /api/models3d/models/{pack}/{name}/thumbnail`, SHALL be the pack's own preview picture when it has one; otherwise it SHALL be one small frame (256 pixels) of the model drawn by Blender on the CPU, kept in the library so it is drawn once, and `503` with how to install Blender when it is missing and the pack has no preview.

#### Scenario: Search
- **WHEN** the catalog is searched with `q=female` and `has_clips=true` on a library with Kenney's *Mini Characters*
- **THEN** the six female characters come back with their 32 clips each

#### Scenario: A pack's own preview
- **WHEN** the thumbnail of a model whose pack has a preview picture is asked
- **THEN** that picture is answered and Blender is not started

#### Scenario: A file that is not a model
- **WHEN** a pack holds a `.glb` that is not a valid binary glTF
- **THEN** it is not in the catalog, the pack is still installed and the pack says which file was left out

### Requirement: The binary glTF of a model can be downloaded

`GET /api/models3d/models/{pack}/{name}/file` SHALL answer the model's `.glb` as the library stores it: `200` with `Content-Type: model/gltf-binary`, `Content-Length`, an `ETag` and `Cache-Control: private, max-age=…`. `If-None-Match` with the current `ETag` SHALL be answered with `304` and no body, and a `Range` with `206`. A model that is not in the library SHALL be `404` with the usual error body, and so SHALL a model marked for adults on a server that does not show them. The route SHALL need the same token as the rest of `/api/models3d/` and SHALL also accept `?ticket=`, and SHALL be described in the OpenAPI document.

#### Scenario: A model
- **WHEN** the file of a model of the library is asked
- **THEN** the answer is `200` with the bytes of the `.glb`, `model/gltf-binary`, its length and an `ETag`

#### Scenario: Not modified
- **WHEN** it is asked again with the `ETag` in `If-None-Match`
- **THEN** the answer is `304` with no body

#### Scenario: A model that is not there
- **WHEN** the file of a model or a pack that is not in the library is asked
- **THEN** the answer is `404`

#### Scenario: A viewer that cannot send headers
- **WHEN** the file is asked with a valid `?ticket=` and no `Authorization` header
- **THEN** it is served; with no ticket and no token the answer is `401`

### Requirement: The library is behind the same access as the rest of the API

The routes under `/api/models3d/` SHALL need the same token as the rest of `/api/`, SHALL NOT be about any project (a hidden project's content mode does not apply to them), and SHALL be described in the OpenAPI document.

#### Scenario: Without the token
- **WHEN** the packs are listed without the token
- **THEN** the answer is `401`

### Requirement: A pack can be a pack of generated models

The library SHALL have a pack `generated` for the models that `model-generation` makes: `source: "generated"`, created when the first one is made, removable like any pack (its models go with it), and not importable over (an import with that id is `409`). Each of its models SHALL carry, besides the fields of every model, the `prompt` it was made from (or the name of the picture), the `engine` and its version, the `licence` of that engine and the `generated_at` date, in the catalog and in `GET /api/models3d/models/{pack}/{name}`; its `attribution` SHALL be false for `triposr` and true for `sf3d`, whose licence asks that its terms are followed.

#### Scenario: The pack appears
- **WHEN** the first model is generated
- **THEN** `GET /api/models3d/packs` lists `generated` as installed with one model and `source: "generated"`

#### Scenario: Removing it
- **WHEN** the pack `generated` is deleted
- **THEN** its models leave the library and a project that copied one of them keeps it

### Requirement: The catalog can be filtered by origin and searched in prompts

`GET /api/models3d/models` SHALL also accept `source` (`catalog`, `user` or `generated`), `engine` (`triposr` or `sf3d`, for generated models) and `project` (the project or generation that made the model), and its `q` SHALL search the prompt of a generated model besides the name and the tags. A generated model SHALL carry `project` and `entry` (what made it) with its prompt, engine and licence. A model marked `adult` SHALL NOT be listed, found by `q` or answered by `GET .../models/{pack}/{name}` (`404`) or its thumbnail by a server that hides adult content.

#### Scenario: What a project made
- **WHEN** the catalog is asked with `project=objetos`
- **THEN** only the models that project made are listed

#### Scenario: Search in prompts
- **WHEN** the catalog is searched with `q=roller skate`
- **THEN** the model made from the prompt "a retro roller skate, glossy pink" is found

#### Scenario: Adult on a censored server
- **WHEN** the catalog is listed by a censored server
- **THEN** the models marked `adult` are not in it
