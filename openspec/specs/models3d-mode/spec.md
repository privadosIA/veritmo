# models3d-mode Specification

## Purpose
TBD - created by archiving change add-models3d-mode. Update Purpose after archive.

## Requirements

### Requirement: A project can make 3D models

A project SHALL be able to have `output.mode: models3d` and a top-level `models:` list in its `scenes.yaml`, each entry with an `id` (a plain lower-case name, unique), a `prompt` (a text of up to 1000 characters) or an `image` (a picture of the project's `sources/`), and optionally `engine` (`triposr` or `sf3d`), `detail` (`low`, `normal` or `high`), `variants` (1 to 4, the number of models made with different seeds), `seed` and `adult` (a boolean). `defaults.models3d` SHALL give the `engine` and `detail` of the entries that do not say. The configuration SHALL be refused, by the same checks as a run and naming the entry and the field, when `models` is given in another mode, when an entry has neither a prompt nor an image or has both, when an id repeats or is not plain, when an `engine`, `detail` or `variants` is not valid, or when an `image` is not in the sources. A project in this mode SHALL NOT have `scenes` or `tracks`.

#### Scenario: A valid project
- **WHEN** a project has `output: {mode: models3d}` and `models: [{id: patin, prompt: "a retro roller skate, glossy pink"}]`
- **THEN** its configuration is accepted and its mode is listed as `models3d`

#### Scenario: Entries that cannot be
- **WHEN** an entry has a prompt and an image, or `variants: 9`, or `engine: "x"`
- **THEN** the configuration is refused naming the entry and the field

#### Scenario: Models in a video project
- **WHEN** a video project has a `models:` list
- **THEN** it is refused saying that `models` belongs to the mode `models3d`

### Requirement: A run makes each model and keeps it

A run of a project in this mode SHALL, for each entry (and each variant), make the picture with the project's image engine unless the entry has its own `image`, generate the model with the engine of `model-generation`, write it as `out/<project>/<id>.glb` (`<id>-<n>.glb` for the variants after the first) with a `manifest.json` that says for each model its entry, prompt, engine and its version, detail, seed, triangles and the time it took, and add it to the pack `generated` of the library with the project and the entry recorded. It SHALL report events by phase for each entry (the picture, the model) like the other modes, SHALL check before it starts that the engine is available and refuse the run with its reason when it is not, and SHALL stop cleanly when cancelled, leaving the models already made. Something that decides a model (the prompt, the seed, the engine and its version, the detail, the hash of the picture) SHALL be part of its cache key, so that an unchanged entry is not made again and a changed one is; a model that is made again SHALL replace the file and the entry of the library with the same name.

#### Scenario: Two entries
- **WHEN** a project with two entries is run
- **THEN** `out/<project>/` holds their `.glb` files and a manifest, and both are in the catalog with the project recorded

#### Scenario: Run again
- **WHEN** the same project is run again with one prompt changed
- **THEN** only that entry is made again

#### Scenario: The engine is down
- **WHEN** the project is run and the 3D service is stopped
- **THEN** the run is refused with `not_running` and nothing is made

#### Scenario: Variants
- **WHEN** an entry has `variants: 3`
- **THEN** three models with different seeds are made, `<id>.glb`, `<id>-2.glb` and `<id>-3.glb`

### Requirement: Adult models are marked and not shown by a censored server

An entry with `adult: true`, and every entry of a project marked `adult: true`, SHALL make models marked `adult` in the manifest and in the library. A server that hides adult content (the default) SHALL NOT list such models in the catalog, SHALL answer `404` for them, and SHALL refuse a generation that asks for `adult: true`, as it does for the other categories; `--strict` hides the projects that were not marked.

#### Scenario: A censored server
- **WHEN** a model was made with `adult: true` and the server is censored
- **THEN** the catalog does not list it and its detail is `404`
