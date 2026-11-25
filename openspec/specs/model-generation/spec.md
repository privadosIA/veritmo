# model-generation Specification

## Purpose
TBD - created by archiving change add-model-generation. Update Purpose after archive.

## Requirements

### Requirement: The 3D engines run in an optional container and say whether they can be used

The server SHALL offer two engines for making a 3D model from a picture, `triposr` (TripoSR, MIT licence; the default) and `sf3d` (Stable Fast 3D, Stability AI Community License), run by an optional service in a container that the person starts and stops. `GET /api/models3d/engines` SHALL answer `{engines: {triposr, sf3d}}`, each with `available`, and when it is not, a `reason` and a `detail`: `no_docker`, `no_weights`, `no_gpu`, `not_running` (everything is there but the container is stopped), `unreachable`, `not_built` (the engine is not in the image), and for `sf3d` also `key_missing` (no Hugging Face key is saved) and `licence_not_accepted` (Hugging Face refuses the key for the engine's repository: the detail SHALL name the repository and say that its licence has to be accepted on Hugging Face with the account of the key). `POST /api/models3d/engines/{engine}/start` and `.../stop` SHALL start and stop the container, and `POST .../engines/{engine}/install` SHALL queue the download of the engine's weights once into the models folder (TripoSR's need no account; `sf3d`'s use the saved Hugging Face key and are refused with the reason above when it is missing or rejected). The server SHALL NOT accept licences, create accounts or ask for a key on anyone's behalf: it SHALL only say what is missing.

#### Scenario: Nothing installed
- **WHEN** the engines are listed on a machine with Docker and a card but no weights
- **THEN** `triposr` is `available: false` with `no_weights` and says how to install it

#### Scenario: Stable Fast 3D without a key
- **WHEN** `sf3d` is checked and no Hugging Face key is saved
- **THEN** it is `key_missing` and the detail names `PUT /api/tokens/huggingface` and the repository whose licence has to be accepted

#### Scenario: A key that has not accepted the licence
- **WHEN** a Hugging Face key is saved but its account has not accepted the licence of the repository
- **THEN** `sf3d` is `licence_not_accepted` and its detail gives the address of the repository

#### Scenario: Start and stop
- **WHEN** the engine's weights are installed and it is started
- **THEN** its container answers, the engine is `available`, and stopping it makes it `not_running`

### Requirement: One model is generated from a picture or a prompt

The program SHALL generate a 3D model from one picture or one prompt, as a function of the server and as the command `veritmo models3d generate --image <file> | --prompt "<text>" [--engine triposr|sf3d] [--detail low|normal|high] [--name <name>]`, which needs the engine to be available and answers with the model it made (`<pack>/<name>`) and what it took. The `engine` SHALL be `triposr` by default; `detail` SHALL be `normal` by default. With a picture, it SHALL be used as it is (`.png`, `.jpg`, `.jpeg` or `.webp`, up to 20 MB); with a prompt, the server SHALL make the picture with its image engine, adding to the prompt that it is one object alone on a plain background lit softly. Before the engine, the picture's background SHALL be cut out. A prompt and a picture together, neither, an empty prompt, an unknown engine or detail, or a name that is not a plain name SHALL be refused naming the problem; an engine that cannot be used SHALL be refused with its reason from the availability; a prompt on a machine whose image engine is not available SHALL be refused saying so.

#### Scenario: From a prompt
- **WHEN** `veritmo models3d generate --prompt "a retro roller skate, glossy pink"` is run with the engine available
- **THEN** a picture is made, its background cut out, the model generated, and a model appears in the pack `generated`

#### Scenario: From a picture
- **WHEN** a picture of a lamp is given with `--detail low`
- **THEN** no picture is made and the model has fewer triangles than the same picture at `normal`

#### Scenario: The engine is not there
- **WHEN** it is asked while the container is stopped
- **THEN** it is refused with `not_running` and nothing is made

#### Scenario: Both a prompt and a picture
- **WHEN** both `--prompt` and `--image` are given
- **THEN** it is refused saying to give one of them

### Requirement: A generated model is upright, on its base and within the limits of a scene

Whatever an engine returns, the model the server keeps SHALL be a binary glTF that stands upright in glTF's axes (Y up), has its origin at the middle of its base (the lowest point of its bounding box, centred in the other two axes), and has no more triangles than the limit of a model in a scene (`blender-engine`: 500,000), keeping its colours. The `detail` SHALL map to the resolution of the surface (`low` about 128, `normal` 192, `high` 256 points a side for TripoSR) so that the triangles follow it, and the result SHALL carry in its record the triangles it has. A result that is empty (no surface) SHALL fail the job with that reason.

#### Scenario: Standing on the floor
- **WHEN** a generated model is placed in a scene at `location [0, 0, 0]` with `fit: "largest"`
- **THEN** it stands on the floor with its whole shape above it, and a model whose raw output was lying on its side is upright

#### Scenario: Detail
- **WHEN** the same picture is generated at `low` and at `high`
- **THEN** `low` has fewer triangles than `high` and both are under 500,000

### Requirement: Generated models go to a pack of the library with their origin recorded

A model that a generation makes SHALL be put in the pack `generated` of the library (created on the first one, `source: "generated"`), under its `name` (with a number added when it is taken), and the library SHALL keep with it the prompt (or the name of the picture), the engine, its version and its licence (`MIT` for `triposr`; `Stability AI Community License (free up to US$1M of yearly revenue; see its terms)` for `sf3d`), and show them in the catalog with the model's other fields. From then on it SHALL be found by `GET /api/models3d/models`, drawn by its thumbnail, and added to a project or a piece by the routes that copy a library model, like any other.

#### Scenario: In the catalog
- **WHEN** a generation is done
- **THEN** `GET /api/models3d/models?pack=generated` lists the model with its prompt, engine and licence, and a project can add it with `POST /api/projects/{id}/sources/model`

### Requirement: Stable Fast 3D is supported without being provided

The server SHALL support `sf3d` as an engine when the container image has it (a build argument) and the person has done what its licence asks on their own account, and SHALL NOT include, download or ship its weights or accept its terms. Its models SHALL carry the licence above, so that a dashboard can show it. The documentation SHALL say plainly that the engine was written without an account that has accepted that licence and is **unverified** against the real repository, and what each person has to do (accept the licence on the repository's page, save their Hugging Face key, install the engine).

#### Scenario: Built without it
- **WHEN** the image was built without Stable Fast 3D
- **THEN** `sf3d` is `not_built` and the detail says how to build it

#### Scenario: Not the server's business
- **WHEN** `sf3d` is not usable for any reason about the account
- **THEN** the server only reports the reason and the repository and does not try to accept anything or to work around it
