# models3d-datasets Specification

## Purpose
TBD - created by archiving change add-models3d-datasets. Update Purpose after archive.

## Requirements

### Requirement: A dataset of prompts can be imported as a project that makes 3D models

`POST /api/import/preview` and `POST /api/import/confirm` (and `veritmo import`) SHALL accept `mode: "models3d"` and turn the entries of a dataset (the tuples `[id, title, lora_info, text]` the other modes read) into a project of the mode `models3d` of `add-models3d-mode`: the `id` of the tuple is the entry's id (made plain and unique when it is not), the first lines of `text` up to the first marker are its `prompt`, and the optional lines `Engine: triposr|sf3d`, `Detail: low|normal|high`, `Variants: 1..4`, `Seed: <n>`, `Image: <file of the sources>` and `Adult: true|false` set one field each. The preview SHALL show the project that would be made and its warnings (an entry that is empty, an id that was changed, a line that was not understood, an `Image:` that is not in the sources); the confirmation SHALL write it, validated as a hand-written project is. An entry with no prompt and no `Image:`, an `Engine:` or `Detail:` that is not valid, or a `mode` that is not `video`, `motion`, `music` or `models3d` SHALL be refused naming the entry and the line or the field. A dataset with `Adult:` entries SHALL be refused with `content_not_allowed` by a server that hides adult content.

#### Scenario: A dataset of three prompts
- **WHEN** a dataset of three tuples is previewed and confirmed with `mode: "models3d"`
- **THEN** a project of mode `models3d` is made with three entries whose prompts are the tuples' texts

#### Scenario: The lines
- **WHEN** an entry's text is "a golden trophy cup" followed by `Detail: high` and `Variants: 2`
- **THEN** its prompt is "a golden trophy cup", its detail `high` and its variants 2

#### Scenario: A line that is wrong
- **WHEN** an entry has `Engine: dall-e`
- **THEN** the preview refuses it naming the entry and the line

#### Scenario: Adult on a censored server
- **WHEN** a dataset with `Adult: true` is imported on a censored server
- **THEN** it is refused with `content_not_allowed`

### Requirement: A project of the mode can be exported as a dataset

The export of a project of the mode `models3d` (`GET /api/projects/{id}/export?format=dataset`, and the command) SHALL write one tuple per entry in the format above, with its optional lines only when they differ from the project's defaults, so that importing the result makes the same entries; and SHALL list in `X-Export-Notes` what the format does not carry (the project's defaults, the library, the models themselves). The package export (`format=zip`) is the one that carries a project whole.

#### Scenario: A round trip
- **WHEN** a project of this mode is exported as a dataset and the dataset is imported
- **THEN** the new project has the same ids, prompts, engines, details, variants and seeds
