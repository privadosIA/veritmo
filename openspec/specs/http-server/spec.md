# http-server Specification

## Purpose
Defines the `veritmo serve` subcommand: a local HTTP server that exposes the management API (and optionally the embedded web UI) while leaving the existing CLI surface and pipeline behavior untouched.

## Requirements

### Requirement: The serve subcommand starts a local HTTP server without touching the pipeline CLI

The CLI SHALL accept `veritmo serve` as a subcommand, dispatched before pipeline flag parsing (like `import`). It SHALL start an HTTP server and keep running until stopped. Invocations without the `serve` subcommand SHALL behave exactly as before this change: same flags, same output, same exit codes.

#### Scenario: serve starts a server

- **WHEN** `veritmo serve` is invoked
- **THEN** an HTTP server starts and responds to API requests, and no pipeline run begins

#### Scenario: pipeline CLI is untouched

- **WHEN** `orchestrator` is invoked without the `serve` subcommand
- **THEN** flags, output, and exit behavior are identical to the pre-change CLI

### Requirement: The server binds to loopback by default with a configurable address

The server SHALL listen on a loopback address by default so it is not reachable from other machines unless explicitly requested. The subcommand SHALL accept a flag to change the listen address/port, and SHALL fail with a descriptive error (non-zero exit) when the address cannot be bound. When the requested address is not loopback and no access token is configured, the subcommand SHALL refuse to start with an error explaining how to set a token, unless an explicit opt-out flag is given, in which case it SHALL start and print a prominent warning that the API is exposed without authentication.

#### Scenario: default binding is loopback

- **WHEN** `veritmo serve` is invoked with no address flag
- **THEN** the server listens only on a loopback interface

#### Scenario: bind failure is reported

- **WHEN** the requested address is already in use
- **THEN** the subcommand exits non-zero with an error naming the address

#### Scenario: non-loopback without token is refused

- **WHEN** `veritmo serve --addr 0.0.0.0:4666` is invoked with no token configured and no opt-out flag
- **THEN** the subcommand exits non-zero before binding, with a message explaining how to set a token

#### Scenario: non-loopback with token

- **WHEN** `veritmo serve --addr 0.0.0.0:4666` is invoked with a token configured
- **THEN** the server starts and requires the token on API requests

#### Scenario: explicit opt-out

- **WHEN** a non-loopback address is given with the opt-out flag and no token
- **THEN** the server starts and prints a prominent warning that the API is unauthenticated

### Requirement: Graceful shutdown finishes cleanly and releases the active run

On receiving an interrupt/termination signal, the server SHALL stop accepting new requests, cancel any active pipeline run (terminating its subprocesses), record that run as interrupted in the history, and exit cleanly.

#### Scenario: interrupt during an active run

- **WHEN** the server receives an interrupt signal while a run is active
- **THEN** the run's subprocesses are terminated, the run is recorded as interrupted, and the process exits cleanly

### Requirement: API errors are structured and consistent

Every API error response SHALL be a JSON object carrying at least a machine-readable code and a human-readable message, with an HTTP status consistent with the error class (client validation errors in the 4xx range, server failures in the 5xx range).

#### Scenario: validation error shape

- **WHEN** any endpoint rejects a request due to invalid input
- **THEN** the response has a 4xx status and a JSON body with an error code and a message describing what was invalid

### Requirement: A front end is served from a directory the operator gives

When `veritmo serve` is given a web directory (`--web-dir`, or `ORCHESTRATOR_WEB_DIR` when the flag is absent), the server SHALL serve the static files in it at the HTTP root, whatever front end they belong to, and SHALL answer `index.html` for any other path outside `/api/`. The directory SHALL be read on each request.

#### Scenario: serving a build

- **WHEN** the server starts with `--web-dir` pointing at a directory that holds an `index.html` and its assets, and the root is requested
- **THEN** the response is that `index.html`, and an asset of the directory is returned with its own content

#### Scenario: a route of the front end

- **WHEN** a path that is not a file in the directory and does not start with `/api/` is requested (for example `/runs/12`)
- **THEN** the response is the directory's `index.html` with status 200

#### Scenario: an unknown API path is not the front end

- **WHEN** an unknown path under `/api/` is requested
- **THEN** the response is the API's structured 404, never the `index.html`

#### Scenario: a new build without a restart

- **WHEN** the files in the directory are replaced while the server runs
- **THEN** the next request returns the new files

#### Scenario: a path that tries to leave the directory

- **WHEN** a request path contains `..` segments or an encoded form of them
- **THEN** no file outside the web directory is ever returned

### Requirement: Front-end files are cached by how they are named

Files whose name carries a content hash SHALL be served as cacheable for good; every other file SHALL be revalidated on each load, so an installed app never keeps showing a previous build. `manifest.webmanifest` SHALL be served as `application/manifest+json`.

#### Scenario: caching by name

- **WHEN** a file named after its content (such as `chunk-0i58r8Jb.js`) is requested
- **THEN** the response carries `Cache-Control: public, max-age=31536000, immutable`
- **AND** `index.html`, `ngsw.json`, `ngsw-worker.js` and `manifest.webmanifest` carry `Cache-Control: no-cache`

#### Scenario: the manifest type

- **WHEN** `manifest.webmanifest` is requested
- **THEN** its `Content-Type` is `application/manifest+json`

### Requirement: The server needs no front end to be built or run

The server SHALL build, pass its tests and serve the full API with no front-end toolchain installed and no front-end file in the repository or in the binary. Without a web directory, the root SHALL answer a short plain-text note, and a web directory that is not a directory SHALL stop the server from starting.

#### Scenario: no web directory

- **WHEN** the server starts without `--web-dir` and the root is requested
- **THEN** the response is a short plain-text note that this server has no front end and that the API lives under `/api/`, and the API works as usual

#### Scenario: a directory that does not exist

- **WHEN** `--web-dir` names a path that is not a directory
- **THEN** the server refuses to start and says which path is wrong

#### Scenario: build without a front-end toolchain

- **WHEN** the project is built and tested on a machine with no npm/Node installed
- **THEN** the build and tests succeed and the server serves the API

### Requirement: The listener has connection timeouts

The server SHALL close a connection that has not finished sending its request headers within a short fixed time (at most 30 s), SHALL close an idle keep-alive connection after a fixed time (at most 5 min), and SHALL reject request headers over a fixed size (at most 1 MiB). It SHALL NOT impose a global write timeout, so event streams and downloads keep working.

#### Scenario: slow headers
- **WHEN** a client opens a connection and sends one header byte per second
- **THEN** the server closes it once the header timeout passes and no handler runs

#### Scenario: an event stream outlives the timeouts
- **WHEN** a client reads `GET /api/runs/{id}/events` for longer than the idle timeout while events keep arriving
- **THEN** the stream is not closed by the server

### Requirement: Every API request body is limited

The server SHALL limit the body of every `/api/*` request to a default size (1 MiB) before any handler reads it, and an endpoint that accepts uploads SHALL declare its own larger limit explicitly. A body over the limit SHALL be answered `413` with the standard error body naming the limit, and nothing SHALL be written to disk or memory beyond the limit. Multipart requests SHALL be limited before the form is parsed. The import of a project package SHALL be spooled to a temporary file, never held whole in memory, and the export of a project SHALL be streamed to the client.

#### Scenario: an oversized JSON body
- **WHEN** `POST /api/projects` carries a 2 MiB body
- **THEN** the answer is `413` and no project is created

#### Scenario: an extra multipart part
- **WHEN** a multipart request to `POST /api/generations` carries a 200 MiB part the handler does not read
- **THEN** the answer is `413` and no temporary file larger than the declared limit is left behind

#### Scenario: a package import
- **WHEN** a 900 MiB project package is imported
- **THEN** the process's memory does not grow by the size of the package and the import succeeds or fails with a structured error
