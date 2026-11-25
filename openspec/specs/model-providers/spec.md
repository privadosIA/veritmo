# model-providers Specification

## Purpose
One interface for the models the application talks to, with an Ollama and an OpenAI-compatible implementation, a registry of providers in the settings, API keys in their own private file that are never shown, sent over plain http to another machine or written to logs, a providers API with a connection test, and the Providers card of the Settings screen.

## Requirements

### Requirement: Every provider speaks one interface

Model providers SHALL implement one interface offering a chat call (system text, user text, optional pictures, an option to ask for JSON, a token limit and a temperature, answering the text and whether it stopped at the limit) and a call listing the provider's models. The assistant, describe and the clip quality check SHALL reach their model only through this interface. Behaviour that exists only for Ollama (running on the CPU, keep-alive, unloading a model, asking where it is loaded) SHALL stay in the Ollama implementation and SHALL NOT be sent to other providers.

#### Scenario: the interface hides the protocol
- **WHEN** a task chats through an Ollama provider and through an OpenAI-compatible one with the same request
- **THEN** each speaks its own protocol and both return the answer's text

#### Scenario: Ollama-only options are not sent elsewhere
- **WHEN** a request that asks to stay off the GPU is sent through an OpenAI-compatible provider
- **THEN** no `num_gpu` or keep-alive option appears in what is sent

### Requirement: The OpenAI-compatible provider follows the chat completions protocol

The `openai` provider SHALL send `POST <base>/chat/completions` with a `Authorization: Bearer <key>` header when a key is set, a `messages` list (system, then user), `max_tokens` and `temperature`, and pictures as `image_url` parts holding a base64 `data:` URI. When JSON is asked for it SHALL send `response_format: {"type": "json_object"}` and, if the server answers 400 because it does not know that option, SHALL repeat the request once without it. It SHALL read the answer from the first choice, SHALL report `finish_reason: length` as truncated, and SHALL list models with `GET <base>/models`. A base ending in `/v1` SHALL be used as it is; a base without a path SHALL get `/v1` added. A `401` or `403` SHALL say the key was rejected, a `429` that the provider asked to slow down, a `404` on a chat call that the model was not found, and any other failure SHALL give the status without echoing the provider's body beyond 200 characters, with the key redacted.

#### Scenario: a text chat
- **WHEN** a chat with system and user text is sent to a simulated server
- **THEN** the request has the system and user messages, the key as a Bearer token, and the answer's text is returned

#### Scenario: a picture
- **WHEN** a chat carries a PNG
- **THEN** the user message holds an `image_url` part whose URL is a `data:image/png;base64,…` URI

#### Scenario: JSON asked for, option unsupported
- **WHEN** the server answers 400 to the request that carries `response_format`
- **THEN** the request is repeated once without it and its answer is used

#### Scenario: a truncated answer
- **WHEN** the first choice ends with `finish_reason` `length`
- **THEN** the answer is reported as truncated

#### Scenario: failures say what to do
- **WHEN** the server answers 401, 429 or an unknown model
- **THEN** the errors say, in turn, that the key was rejected, that the provider asked to slow down, and that the model was not found

#### Scenario: the base address
- **WHEN** the base is `https://openrouter.ai/api/v1`, and when it is `http://127.0.0.1:8000`
- **THEN** chat calls go to `https://openrouter.ai/api/v1/chat/completions` and `http://127.0.0.1:8000/v1/chat/completions`

### Requirement: Providers are registered in the settings, and Ollama is always there

The settings file SHALL hold a list of providers, each with `id` (lowercase letters, digits and hyphens, at most 40 characters, unique), `name` (at most 60 characters), `type` (`ollama` or `openai`), `base_url`, `text_model` and `vision_model` (each at most 200 characters, empty when the provider is not used for that). The built-in provider `ollama` SHALL always exist, SHALL take its address from the Ollama setting of `ollama-settings`, SHALL NOT be stored in that list and SHALL NOT be deletable or editable here. An `ollama` provider's address SHALL be validated as in `ollama-settings`; an `openai` provider's base SHALL be http or https with a host, no credentials, no query or fragment and a path that is empty or ends in `/v1`.

#### Scenario: the built-in provider
- **WHEN** providers are listed with none saved
- **THEN** the list holds the built-in `ollama` provider, flagged as built in, with the address in use

#### Scenario: adding a provider
- **WHEN** an `openai` provider `openrouter` is added with base `https://openrouter.ai/api/v1`
- **THEN** it is stored in the settings, listed with `remote` true, and its address is normalized

#### Scenario: invalid providers are refused
- **WHEN** an id has a capital letter, a base has credentials, a path other than `/v1`, a query, or the id is `ollama`
- **THEN** the request is refused with the reason and nothing is saved

#### Scenario: the built-in cannot be removed
- **WHEN** deleting or editing `ollama` is asked
- **THEN** the answer is a refusal that says to change the address in the Ollama settings

### Requirement: Keys live in their own private file and are never shown

A provider's API key SHALL be stored only in `keys.json`, next to the settings (`~/.config/veritmo/`), in a file of mode 0600 inside a directory of mode 0700, written atomically, keyed by provider id. A key SHALL be accepted only up to 500 characters with no spaces or control characters. A key SHALL NEVER appear in `settings.json`, `scenes.yaml`, exported packages, the environment report, log output, error messages, or any API answer: the API SHALL only say whether one is set (`key_set`). Deleting a provider SHALL delete its key, and removing a key SHALL leave the provider. A key SHALL NOT be sent over plain `http` to an address that is not this machine: creating or testing such a provider with a key SHALL be refused with a message saying to use https.

#### Scenario: where the key goes
- **WHEN** a provider is saved with a key
- **THEN** `keys.json` holds it with mode 0600 and `settings.json` does not contain it

#### Scenario: never in an answer
- **WHEN** providers are listed, created, edited or tested
- **THEN** no response body contains the key and each provider has only `key_set`

#### Scenario: never in an error
- **WHEN** a request to a provider fails and its body echoes the key
- **THEN** the message returned has the key replaced by `***`

#### Scenario: deleting a provider deletes its key
- **WHEN** a provider with a key is deleted
- **THEN** its entry is gone from `keys.json`

#### Scenario: no key over plain http
- **WHEN** a provider at `http://192.168.1.60:8000/v1` is saved or tested with a key
- **THEN** the request is refused with a message that says to use https, and the key is not sent

#### Scenario: a loopback server may use http
- **WHEN** a provider at `http://127.0.0.1:8000/v1` is saved with a key
- **THEN** it is accepted

### Requirement: The providers API manages and tests providers

The server SHALL expose, behind the token rules of the rest of the API and documented in `openapi.yaml`: `GET /api/providers` (the built-in first, then the saved ones, each `{id, name, type, base_url, text_model, vision_model, builtin, remote, key_set, lists_models}`); `POST /api/providers` and `PUT /api/providers/{id}` taking the fields above, an optional `preset` and an optional `key` (absent: unchanged; empty: remove); `DELETE /api/providers/{id}`; and `POST /api/providers/{id}/test`, which SHALL send one short chat to the provider's text model (`"Reply with OK"`, at most 5 tokens) within 10 seconds and, when the provider lists models (`lists_models`), SHALL also list them (`GET /v1/models` or Ollama's tags) within 5 seconds, without following redirects, reading at most 4 MiB and returning at most 200 models, sending nothing of the user's, and answers `{ok, remote, chat_ms?, models?: [{name}], models_note?, error?}` where an unreachable or rejecting provider is `ok: false` with status 200 and a message naming the address, and a provider that does not list models says so in `models_note` without failing. Unknown ids SHALL be 404 and invalid input 400 with the reason.

#### Scenario: create, edit and delete
- **WHEN** a client creates a provider, changes its vision model, and deletes it
- **THEN** each answer reflects the change and the provider is gone from the list

#### Scenario: the key is optional on edit
- **WHEN** a provider is edited without a `key` field
- **THEN** its key is unchanged, and with `"key": ""` it is removed and `key_set` becomes false

#### Scenario: testing a working provider
- **WHEN** the test reaches a server that answers the short chat and whose `/v1/models` lists two models
- **THEN** the answer has `ok` true, `chat_ms` and both names, and the provider's key was sent as a Bearer token

#### Scenario: testing a provider that does not list models
- **WHEN** the provider is the Anthropic preset and the short chat answers
- **THEN** the answer has `ok` true, `chat_ms`, no `models` and a `models_note` saying the models are typed by hand

#### Scenario: a provider with no text model
- **WHEN** the provider has no text model and lists its models
- **THEN** the test answers from the list alone with a `models_note` saying no chat was sent; and when it also does not list models the answer is `ok` false asking for a text model

#### Scenario: an address that ends in /openai
- **WHEN** a provider is added with base `https://generativelanguage.googleapis.com/v1beta/openai/`
- **THEN** it is accepted and chats go to `…/v1beta/openai/chat/completions`, without a `/v1` added

#### Scenario: a rejected key
- **WHEN** the server answers 401 to the chat
- **THEN** the answer is 200 with `ok` false and a message that the key was rejected

#### Scenario: no redirects
- **WHEN** the provider's address redirects elsewhere
- **THEN** the redirect is not followed and the test fails

#### Scenario: the token is required
- **WHEN** the server has a token and a request has none
- **THEN** every providers endpoint answers 401

### Requirement: The Settings screen manages providers

The Settings screen SHALL have a **Providers** card listing the built-in Ollama (read only, with a pointer to the Ollama card) and the saved providers, each with its name, type, address, models, a mark when its address is another machine, and a mark when a key is saved. The card SHALL let the user add a provider (name, type, base address, text model, vision model, key), edit one, test one (showing whether it answers, or the reason, and its models) and delete one after confirming. The key SHALL be a write-only field: it SHALL never be shown, editing SHALL leave it unchanged when the field is empty, and a button SHALL remove a saved key. A refusal by the server SHALL be shown next to the form with its reason.

#### Scenario: adding and testing
- **WHEN** the user adds a provider and presses its test button
- **THEN** the card lists it and shows whether it answered and its models

#### Scenario: the key is never displayed
- **WHEN** a provider with a saved key is opened for editing
- **THEN** the key field is empty, a "key saved" mark is shown, and saving without typing keeps the key

#### Scenario: removing a key
- **WHEN** the user presses the button that removes the key
- **THEN** the mark disappears and the provider remains

#### Scenario: a refusal
- **WHEN** the server refuses an address
- **THEN** its reason appears next to the form and the list does not change

### Requirement: Moving behind the interface changes nothing for the user

With only the built-in provider, the prompt assistant, describe and the clip quality check SHALL behave exactly as before: the assistant stays on the CPU, verifies that its model holds no VRAM and unloads it; describe checks ages first, reports refusals with alternatives and unloads its model; the quality check unloads its model. Their existing tests SHALL pass unchanged.

#### Scenario: the assistant is still CPU-only
- **WHEN** the assistant runs
- **THEN** its Ollama request carries `num_gpu` 0, the VRAM check runs and the model is unloaded afterwards

#### Scenario: describe is unchanged
- **WHEN** a picture of an apparent minor is described
- **THEN** it is refused before any description, as before

### Requirement: The public edition accepts only listed provider hosts

In the public edition an `openai` provider SHALL be added or edited only when its host is on a list kept in the program (the hosts of the OpenAI API, OpenRouter, Anthropic's OpenAI-compatible layer, Google's Gemini OpenAI-compatible endpoint and the like, written in one place with the presets); any other host SHALL be refused with the reason and nothing saved. The built-in Ollama keeps its free address. A provider already saved with a host not on the list SHALL be listed as `blocked` and SHALL NOT be used by any task, as if it were unreachable. The private edition accepts any host that the existing rules accept.

#### Scenario: host not on the list
- **WHEN** a public build is asked to add an `openai` provider with base `https://my-server.example/v1`
- **THEN** the request is refused saying the host is not allowed in this edition and nothing is saved

#### Scenario: a preset's host
- **WHEN** a public build is asked to add the `gemini` preset
- **THEN** it is accepted

#### Scenario: a saved provider after moving to the public build
- **WHEN** the settings hold a provider whose host is not on the list and the build is public
- **THEN** it is listed as blocked and a task routed to it fails as unavailable

#### Scenario: private edition
- **WHEN** the same request is made in a private build
- **THEN** it follows the existing rules

### Requirement: Known providers come as presets

`GET /api/providers/presets` SHALL list the OpenAI-compatible providers the program knows, each `{id, name, base_url, lists_models, text_models[], vision_models[], key_hint, notes}`: at least `anthropic` (`https://api.anthropic.com/v1/`, the Claude API key as the Bearer token, `lists_models: false`, the note that the layer is for testing, that `response_format` is not honoured and that thinking is off), `gemini` (`https://generativelanguage.googleapis.com/v1beta/openai/`, the Google AI Studio key, `lists_models: true`), `openrouter` and `openai`, with suggested text and vision model names dated in the docs. `POST /api/providers` SHALL accept `preset: <id>` and fill `type`, `base_url`, `text_model`, `vision_model` and `name` from it when the request leaves them empty; every field SHALL stay editable. The presets SHALL be written in one place in the program, next to the public edition's host list, and their hosts SHALL be on that list.

#### Scenario: Creating from a preset
- **WHEN** `POST /api/providers` carries `{"preset": "anthropic", "key": "…"}`
- **THEN** the provider is saved with Anthropic's base address, a suggested text and vision model and the key, and the answer shows them

#### Scenario: Editing what the preset gave
- **WHEN** the same request also names `text_model`
- **THEN** that model is kept and the rest comes from the preset

### Requirement: The providers are kept in the database

The server SHALL keep each provider as a row of the database, in the order the user gave them, and its key in the private keys file as before; Ollama SHALL always be there. The providers kept in `settings.json` SHALL be imported when the server starts (see `data-store`). The providers API SHALL answer as before, and `GET /api/providers/{id}` SHALL answer one provider as the list shows it (never its key), or `404`. Deleting a provider SHALL delete its row and its key and take it out of the routing in the same change.

#### Scenario: After the import

- **WHEN** the server starts with providers kept in the file
- **THEN** `GET /api/providers` answers the same providers, in the same order, and each one that had a key still has it

#### Scenario: One provider

- **WHEN** `GET /api/providers/{id}` is asked
- **THEN** the answer is that provider with `has_key` and without the key
