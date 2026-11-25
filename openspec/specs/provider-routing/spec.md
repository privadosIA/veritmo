# provider-routing Specification

## Purpose
Chooses, per task (prompt assistant, describing a picture, describing a clip, clip quality check), which provider runs it, local by default. Sending data to a remote provider needs the user's explicit consent tied to its address, is shown with a badge, is capped in calls, and never switches provider by itself: after a refusal the user may retry elsewhere with one click.

## Requirements

### Requirement: Each task has its own provider, local by default

The settings SHALL hold a routing from four tasks to provider ids: `assistant` (the prompt assistant for pictures, movement, songs and videoclip storyboards), `describe_image`, `describe_video` (a clip, as one scene or one per shot) and `quality` (the clip quality check). Without a routing every task SHALL use the built-in provider `ollama`. A task SHALL use its provider's text model when it is `assistant` and its vision model otherwise. Saving a routing SHALL be refused, with the reason, when the provider does not exist or lacks the model the task needs, and removing a provider that a task uses SHALL be refused naming the task. `GET /api/routing` SHALL answer, for each task, `{provider, name, remote, model}`, and `PUT /api/routing` SHALL take the tasks to change.

#### Scenario: the default
- **WHEN** nothing has been routed
- **THEN** every task is routed to `ollama`, local, and behaves as before

#### Scenario: routing the assistant to a cloud provider
- **WHEN** `assistant` is routed to an `openai` provider that has a text model
- **THEN** `GET /api/routing` reports that provider, its name, `remote` true and its text model for `assistant`

#### Scenario: a missing model
- **WHEN** `describe_image` is routed to a provider with no vision model
- **THEN** the request is refused with a message that says which model is missing

#### Scenario: a provider in use cannot be removed
- **WHEN** a provider that `quality` uses is deleted
- **THEN** the deletion is refused and names the task

### Requirement: Sending data to a remote provider needs consent tied to its address

Routing a task to a remote provider, and using one through the `provider` parameter of a single call, SHALL require an explicit acknowledgement that the pictures, clips or texts of that task will leave this computer for that provider. The acknowledgement SHALL be stored with the provider's id and base address and SHALL stop counting when the base address changes. Without it the API SHALL answer `400` with the code `consent_required` and the message SHALL name the provider and what is sent. A provider that is not remote SHALL need no acknowledgement. The acknowledgement SHALL be given in the same request as the routing (`acknowledge_remote: true`) or in a request of its own for the provider.

#### Scenario: consent is required
- **WHEN** `describe_image` is routed to a remote provider without acknowledgement
- **THEN** the answer is 400 `consent_required`, naming the provider, and the routing is unchanged

#### Scenario: consent given
- **WHEN** the same request carries `acknowledge_remote: true`
- **THEN** the routing is saved and the acknowledgement is stored with the provider's address

#### Scenario: the address changes
- **WHEN** a provider's base address is edited after consent
- **THEN** the tasks routed to it need a new acknowledgement before they run

#### Scenario: a local provider
- **WHEN** a task is routed to a provider on this machine
- **THEN** no acknowledgement is asked

### Requirement: The interface shows when a task uses a remote provider

The interface SHALL show a badge "external · <provider name>" next to every control that runs a task on a remote provider: Improve with AI and Suggest motion (assistant), Describe a reference (describe), writing a song with AI, the videoclip plan with AI, and the quality check. It SHALL show nothing when the provider is local. The availability of those controls SHALL follow the routed provider (a provider without the needed model disables the control with the reason).

#### Scenario: a remote assistant
- **WHEN** `assistant` is routed to a remote provider
- **THEN** Improve with AI and Suggest motion carry the badge with the provider's name

#### Scenario: local shows nothing
- **WHEN** a task is routed to the built-in Ollama
- **THEN** its controls carry no badge

### Requirement: The Settings screen routes tasks

The Settings screen SHALL have a **Tasks** card with one row per task showing a provider selector, the model that will be used, and the badge when that provider is remote. Providers without the model the task needs SHALL be disabled with the reason. Choosing a remote provider SHALL open a consent dialog that names the provider and what the task sends, and the choice SHALL be saved only when the user accepts. A server refusal SHALL be shown next to the row.

#### Scenario: choosing a remote provider
- **WHEN** the user selects a remote provider for a task
- **THEN** a dialog asks for consent naming the provider, and cancelling leaves the routing unchanged

#### Scenario: a provider without the model
- **WHEN** a provider has no vision model
- **THEN** it is disabled in the rows of the describe and quality tasks, with the reason

### Requirement: A single call can use another provider, and providers never switch by themselves

`POST /api/describe` and `POST /api/assist` SHALL accept an optional `provider` that replaces the routing for that call only, with the same consent rule when it is remote. After `describe_refused`, the alternatives offered (the API's message and the interface's "Try with" buttons) SHALL include every other provider that has a vision model, as well as the other vision models of the provider that refused. The application SHALL NOT retry, fall back or switch provider on its own after a refusal, an error or a rate limit: only the user's action does.

#### Scenario: retry on local after a cloud refusal
- **WHEN** a description routed to a cloud provider is refused and the user presses "Try with" the local Ollama
- **THEN** the call is made with `provider` set to the local one and nothing is sent to the cloud again

#### Scenario: no automatic fallback
- **WHEN** a remote provider fails or refuses
- **THEN** the error is reported and no request goes to any other provider

#### Scenario: a remote provider chosen for one call
- **WHEN** a call names a remote provider the user never consented to
- **THEN** the answer is 400 `consent_required`

### Requirement: Remote jobs have a call cap and show their size first

The settings SHALL hold `remote.max_calls` (default 60, at least 1) as the most calls one job may make to a remote provider. The server SHALL count the calls a describe job plans (a picture: 2; a clip as one scene: 2 per frame plus 1; one scene per shot: up to 3 per shot) and SHALL refuse, before sending anything, a job on a remote provider whose planned calls exceed the cap, with a message that gives both numbers. Local providers SHALL have no cap. Before running on a remote provider the interface SHALL show "up to N calls to <provider>", N being the worst case of the chosen mode.

#### Scenario: over the cap
- **WHEN** a clip split into 12 shots (up to 36 calls) runs on a remote provider with a cap of 30
- **THEN** the job is refused before any call, saying 36 calls exceed the cap of 30

#### Scenario: local has no cap
- **WHEN** the same job runs on a local provider
- **THEN** it runs

#### Scenario: the size is shown first
- **WHEN** a remote provider is routed for describing a clip as one scene per shot
- **THEN** the interface shows "up to 36 calls to <provider>" before the user starts

### Requirement: Every safeguard holds with any provider

The age check, the detection of refusals, the deletion of refused files and the minors rule SHALL apply unchanged with any provider. A refusal or an answer that is not a description from a remote provider SHALL be reported as it is for Ollama. A provider or model that does not accept pictures (for example a 400 that says so) SHALL give a message that says the model cannot read pictures and to choose a vision model. The Ollama-specific behaviour (staying on the CPU, verifying VRAM, the memory and installed-model checks, unloading) SHALL apply only when the task's provider is of type `ollama`.

#### Scenario: a minor with a cloud provider
- **WHEN** a cloud provider answers that someone in the picture is a minor
- **THEN** the file is refused and deleted, exactly as with Ollama

#### Scenario: a model without vision
- **WHEN** the provider answers 400 saying images are not supported
- **THEN** the message says the model cannot read pictures

#### Scenario: Ollama-only checks are not applied elsewhere
- **WHEN** the assistant runs on an `openai` provider
- **THEN** no memory check, VRAM verification or unload is attempted and the request carries no `num_gpu`

### Requirement: The command line uses the same routing

`veritmo assist`, `describe` and `check` SHALL read the routing from the same settings and SHALL accept `--provider <id>` to use another provider for that run, with the same consent rule when it is remote: a remote provider without a stored acknowledgement SHALL make the command stop with a message that says to accept it in the Settings screen.

#### Scenario: the terminal follows the routing
- **WHEN** `assistant` is routed to a provider and `veritmo assist` runs
- **THEN** the command uses that provider

#### Scenario: no consent in the terminal
- **WHEN** `--provider` names a remote provider with no acknowledgement
- **THEN** the command exits with an error that says to accept it in Settings

### Requirement: The routing answer suggests an external provider for the creative tasks

`GET /api/routing` SHALL carry, for the tasks `motion_graphics` and `quality` (the review of frames), under a top-level `suggested` object keyed by task, a `suggested` field naming the external provider that best fits them when one is saved, tested and not blocked (the first saved provider whose `type` is `openai` and whose preset is `anthropic`, `gemini`, `openrouter` or `openai`, in that order of preference), with `{provider_id, name, reason}` where the reason is the fixed sentence of the task ("the local model reaches its ceiling on the metaphor; an external one scores higher on the same rubric" for `motion_graphics`, "the vision reading of frames is more reliable on an external model" for `quality`). The suggestion SHALL change nothing by itself: the task keeps its provider until `PUT /api/routing` changes it, with the consent rules already in force for remote providers. When no such provider exists the field SHALL be absent.

#### Scenario: a tested external provider exists
- **WHEN** a provider created from the `anthropic` preset has been tested with success and `motion_graphics` still routes to the local provider
- **THEN** `GET /api/routing` shows `suggested` for `motion_graphics` and `quality` pointing at it, and the tasks keep routing to the local provider

#### Scenario: no external provider
- **WHEN** only the built-in Ollama is configured
- **THEN** the routing answer has no `suggested` field

#### Scenario: taking the suggestion
- **WHEN** `PUT /api/routing` routes `motion_graphics` to the suggested provider with the consent the remote rules require
- **THEN** the next scenes batch is written by it and the answer of the batch names the provider used
