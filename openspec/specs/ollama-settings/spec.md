# ollama-settings Specification

## Purpose
The address of Ollama that the assistant, describe and the quality check use: one resolver (saved setting, then OLLAMA_HOST, then the default) evaluated at each use, a private settings file, endpoints to read, change and test it, a flag when it is not this machine, and the Settings screen that edits it without a restart.

## Requirements

### Requirement: The Ollama address is resolved in one place, on every use

A single resolver SHALL give the address of Ollama to every consumer (the prompt assistant, describe, the clip quality check, the vision-model list, the unload endpoint, the environment report and the command line) and SHALL be evaluated at each request, never cached for the life of the process. Its precedence SHALL be: the address saved in the settings file, then the environment variable `OLLAMA_HOST`, then `http://127.0.0.1:11434`. Along with the address it SHALL say where it came from: `setting`, `env` or `default`. An address written the way Ollama reads `OLLAMA_HOST` (scheme and port optional, `0.0.0.0` meaning loopback) SHALL be normalized to a base URL.

#### Scenario: a saved setting wins
- **WHEN** `OLLAMA_HOST` is `10.0.0.2:11434` and the saved setting is `http://192.168.1.50:11434`
- **THEN** the resolver returns `http://192.168.1.50:11434` with source `setting`

#### Scenario: the environment is next
- **WHEN** there is no saved setting and `OLLAMA_HOST` is `10.0.0.2`
- **THEN** the resolver returns `http://10.0.0.2:11434` with source `env`

#### Scenario: the default
- **WHEN** there is neither
- **THEN** the resolver returns `http://127.0.0.1:11434` with source `default`

#### Scenario: no restart needed
- **WHEN** the setting is saved while the server is running
- **THEN** the next assistant, describe or check request uses the new address

### Requirement: The setting is stored privately and validated

The saved address SHALL live in `settings.json` next to the server's token (`~/.config/veritmo/`, honouring `XDG_CONFIG_HOME`), in a directory of mode 0700 and a file of mode 0600, written atomically. An address SHALL be accepted only when its scheme is `http` or `https` (or absent, meaning `http`), it has a host, no user or password, and no path (other than `/`), query or fragment, and it is at most 200 characters; anything else SHALL be rejected with a message that says which rule failed, and nothing SHALL be saved. A saved value SHALL be stored normalized (with scheme). Removing the setting SHALL delete the key so the resolver falls back to `OLLAMA_HOST`.

#### Scenario: a valid address is saved
- **WHEN** the address `192.168.1.50:11434` is saved
- **THEN** `settings.json` holds `http://192.168.1.50:11434`, its mode is 0600, and the resolver uses it

#### Scenario: credentials are refused
- **WHEN** the address is `http://user:pass@host:11434`
- **THEN** it is rejected, nothing is saved, and the message says credentials are not allowed

#### Scenario: a path is refused
- **WHEN** the address is `http://host:11434/api`
- **THEN** it is rejected with a message that says the address must have no path

#### Scenario: another scheme is refused
- **WHEN** the address is `ftp://host`
- **THEN** it is rejected with a message that says only http and https are accepted

#### Scenario: removing the setting
- **WHEN** the saved setting is removed while `OLLAMA_HOST` is set
- **THEN** the resolver returns the environment's address with source `env`

### Requirement: The settings endpoints report, change and test the address

The server SHALL expose, behind the same token rules as the rest of the API and documented in `openapi.yaml`: `GET /api/settings` returning `{ollama: {host, source, remote, saved}}` (the address in use, where it comes from, whether it is not this machine, and the saved value or an empty one); `PUT /api/settings` taking `{ollama_host}` (an address to save, or an empty value to remove the setting) and answering like the GET, or `400` for an invalid address; and `POST /api/settings/ollama/test` taking an optional `{host}` (default: the address in use) and answering `{ok, host, remote, version?, models?: [{name, vision}], error?}` after asking that Ollama for its version and models. The test SHALL time out in 5 seconds, SHALL NOT follow redirects, SHALL read at most 4 MiB, SHALL list at most 200 models, SHALL send no credentials and no image or text of the user, and an unreachable address SHALL be a normal answer with `ok: false` and a message naming the address, not an error status. The test SHALL apply the same address validation as saving.

#### Scenario: reading the settings
- **WHEN** a client calls `GET /api/settings` with no saved setting and `OLLAMA_HOST` unset
- **THEN** it gets `host` `http://127.0.0.1:11434`, `source` `default`, `remote` false and an empty `saved`

#### Scenario: saving and removing
- **WHEN** a client saves `192.168.1.50` and then saves an empty value
- **THEN** the first answer has source `setting` and the second source `env` or `default`

#### Scenario: an invalid address
- **WHEN** a client saves `http://host/path`
- **THEN** the answer is 400 with the reason and the setting is unchanged

#### Scenario: a working Ollama
- **WHEN** the test reaches an Ollama with models `gemma4:26b` (vision) and `qwen3:8b`
- **THEN** the answer has `ok` true, the version, and both models with `vision` true and false

#### Scenario: nothing answers
- **WHEN** the test targets an address where nothing listens
- **THEN** the answer has `ok` false and an error that names the address, with status 200

#### Scenario: testing an address before saving it
- **WHEN** the test is called with a `host` that differs from the saved one
- **THEN** that host is tested and nothing is saved

#### Scenario: the token is required
- **WHEN** the server has a token and the request has none
- **THEN** the answer is 401

### Requirement: A remote address is flagged as such

The address SHALL be `remote` when its host is not this machine: anything other than `localhost`, `127.0.0.0/8` or `::1`. Every answer that carries an address SHALL carry `remote`. The Settings screen SHALL show, whenever the address in use, or the one typed and tested, is remote, a warning that images, clips and assistant texts sent by the assistant, "describe a reference" and the quality check will leave this computer for that address.

#### Scenario: loopback is local
- **WHEN** the address is `http://127.0.0.1:11434` or `http://localhost:11434`
- **THEN** `remote` is false

#### Scenario: another machine is remote
- **WHEN** the address is `http://192.168.1.50:11434`
- **THEN** `remote` is true and the screen shows the warning

### Requirement: The Settings screen edits and tests the address

The web app SHALL offer a Settings screen at `/settings`, reachable from the side menu, with an Ollama card that shows the address in use and its source (saved setting, environment or default), an input for a new address, a **Test connection** button, a **Save** button and, when a setting is saved, a button to go back to the environment's address. The test result SHALL show whether Ollama answered, its version and its models, marking those that read pictures. An invalid address SHALL show the server's reason next to the input and keep the previous setting. Saving SHALL not require a restart, and the screen SHALL show the new address in use right after.

#### Scenario: testing then saving
- **WHEN** the user types an address, presses Test connection and sees it answers, then presses Save
- **THEN** the card shows that address as in use with source "setting"

#### Scenario: a failed test
- **WHEN** the user tests an address where nothing answers
- **THEN** the screen says it did not answer and names the address, and Save stays available

#### Scenario: going back to the environment
- **WHEN** the user presses the button to use the environment's address
- **THEN** the setting is removed and the card shows the environment's (or the default) address

#### Scenario: an invalid address
- **WHEN** the user saves `http://host/path`
- **THEN** the reason is shown next to the input and the address in use does not change
