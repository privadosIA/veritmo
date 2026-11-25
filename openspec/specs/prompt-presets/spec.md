# prompt-presets Specification

## Purpose
TBD - created by archiving change add-dataset-studio-api. Update Purpose after archive.

## Requirements

### Requirement: Presets are kept as files under the media directory
The server SHALL keep prompt presets as rows of the database, with `kind` one of `image`, `video`, `motion`, `music`, `animation`. A preset has `id`, `kind`, `title`, `prompt`, `negative`, `tags`, `engine`, `language`, `source`, `source_id`, `licence`, `nsfw`, `own`, `created_at`. The presets kept as JSON files under `<MEDIA_DIR>/prompts/<kind>/` (under `prompts/` beside the projects directory when no media directory is set) SHALL be imported when the server starts (see `data-store`); the `.cache` folder SHALL NOT be read as presets and SHALL stay where it is. The server SHALL NOT hold every preset in memory.

#### Scenario: Restart
- **WHEN** the server restarts
- **THEN** the presets stored before are listed again

#### Scenario: After the import
- **WHEN** the server starts with 10,197 presets kept as files
- **THEN** the database has 10,197 presets and `GET /api/prompts` answers what it answered from the files

### Requirement: Presets can be searched
`GET /api/prompts` SHALL accept `kind`, `q` (matches title, prompt and tags, case-insensitively), `tags` (comma-separated, all must be present), `limit` (default 50, at most 200) and answer `{items, total}`, sorted by match then by `created_at` descending. An unknown `kind` SHALL be `400`. Presets marked `nsfw` SHALL NOT be listed while the server is censored.

#### Scenario: Search
- **WHEN** a client asks `?kind=image&q=neon&tags=night`
- **THEN** only image presets that match `neon` and carry `night` come back, with the full `total`

### Requirement: A person can add and remove their own presets
`POST /api/prompts` SHALL create one preset with `own: true` and `source: "user"` and answer `201` with it; `kind` and `prompt` (at most 2000 characters) are required. `DELETE /api/prompts/{id}` SHALL remove a preset with `own: true` and answer `204`; a bundled one SHALL be `403` and an unknown id `404`.

#### Scenario: Delete a bundled preset
- **WHEN** a client deletes a preset imported from a bundle
- **THEN** it is `403` and the preset stays

### Requirement: Bundles are imported with their origin and licence
`POST /api/prompts/import` SHALL accept `{format: "prompt-presets", version: 1, items: [...]}` and answer `{added, already, refused}`. An item already present with the same `source` and `source_id` SHALL count in `already` and not be added again. An item without `source` or `licence`, with an invalid `kind`, or with a prompt over 2000 characters SHALL be refused with its `index` and a `reason`; the rest is still added. Another `format` or `version` SHALL be `400`.

#### Scenario: Mixed bundle
- **WHEN** a bundle has one good item, one without a licence and one already imported
- **THEN** the answer is `added: 1`, `already: 1` and one `refused` entry with its index

### Requirement: The store sees presets written to its folder by another process
The server SHALL list, without a restart, the presets the command `veritmo prompts fetch` adds while it runs: the command writes them to the database the server uses. A preset file put in the folder by hand SHALL be listed only after an import is asked for.

#### Scenario: Presets fetched while the server runs
- **WHEN** the command writes presets while the server runs
- **THEN** the next `GET /api/prompts` lists them

### Requirement: The server and the command write presets the same way
What a preset is and how it is saved SHALL live in one package used by both the server and the command, so that what the command writes is exactly what the server reads (`own: false`, an id, `created_at`, one row per preset). Saving a preset whose id exists SHALL replace it; the command SHALL save a batch of presets as one change.

#### Scenario: Round trip
- **WHEN** the command writes a preset
- **THEN** the server lists it with every field the import would have given it

### Requirement: The presets can be paged, read and changed one by one

`GET /api/prompts` SHALL be a list that can be paged (see `api-listing`), combined with the filters and the search it has today: sort by `title` or `created_at`, and `q` searched in the title, the prompt and the tags. `GET /api/prompts/{id}` SHALL answer one preset or `404`. `PUT /api/prompts/{id}` SHALL replace the fields of a preset of the person's own (`own: true`) with the validation a new preset passes, and SHALL be `403` for a preset that is not the person's own and `404` for an unknown one.

#### Scenario: A page of a search

- **WHEN** the list is asked with `kind=image&q=neon&limit=50`
- **THEN** at most 50 image presets that contain `neon` come back, with the total in `X-Total-Count`

#### Scenario: Changing a fetched preset

- **WHEN** a preset with `own: false` is put
- **THEN** the answer is `403` and the preset is unchanged

### Requirement: Prompt presets have a kind for 3D models

The kinds of prompt presets SHALL include `model3d`: `POST /api/prompts`, `GET /api/prompts?kind=model3d`, the search, the update, the deletion and the import and export of bundles SHALL treat it like the other kinds (a prompt of up to 2000 characters, tags, source and licence for a bundled preset), a kind that is not known SHALL be `400` naming the accepted ones, which include it, and the presets of this kind SHALL be the ones given to the assistant as style examples for the mode and the kind `model3d`. `veritmo prompts fetch` SHALL have no source for this kind and SHALL say so when it is asked for it (`--kind model3d`), fetching nothing; presets of the kind come from the person or from a bundle.

#### Scenario: Save and find
- **WHEN** a preset "a glossy pink roller skate, studio render" is saved with kind `model3d` and tag `props`
- **THEN** `GET /api/prompts?kind=model3d&q=skate` finds it

#### Scenario: Bundles
- **WHEN** a bundle (format `prompt-presets`) with items of kind `model3d` is imported
- **THEN** they are added, and listed by `GET /api/prompts?kind=model3d`

#### Scenario: Fetch has no source
- **WHEN** `veritmo prompts fetch` is asked for the kind `model3d`
- **THEN** it says there is no public source for it and fetches nothing
