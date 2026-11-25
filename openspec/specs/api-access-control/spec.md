# api-access-control Specification

## Purpose
Defines how the management API is protected when it is reachable beyond the local machine: optional Bearer-token authentication, cross-origin access from an explicit allow-list, and short-lived scoped tickets for requests that cannot carry headers.

## Requirements

### Requirement: An optional Bearer token protects the API

When a token is configured (by flag or by the `VERITMO_TOKEN` environment variable; the flag wins), every `/api/*` request SHALL carry `Authorization: Bearer <token>` or be rejected with HTTP 401 and the standard structured error body. The token SHALL be compared in constant time. `GET /api/health` and CORS preflight requests SHALL NOT require the token. When no token is configured, the API SHALL behave exactly as before this change.

#### Scenario: valid token

- **WHEN** a token is configured and a request carries the matching Bearer token
- **THEN** the request is handled normally

#### Scenario: missing or wrong token

- **WHEN** a token is configured and a request has no Authorization header or a different token
- **THEN** the response is 401 with a JSON error body (code and message) and the request has no side effects

#### Scenario: no token configured

- **WHEN** no token is configured and a request carries no Authorization header
- **THEN** the request is handled normally

#### Scenario: health stays open

- **WHEN** a token is configured and `GET /api/health` is requested without credentials
- **THEN** it returns 200

### Requirement: The token never appears in logs or responses

The server SHALL NOT print the token, nor echo it in any response or error body.

#### Scenario: startup output

- **WHEN** the server starts with a token configured
- **THEN** stdout and stderr do not contain the token value

### Requirement: Cross-origin access is limited to an explicit allow-list

Cross-origin access SHALL be disabled unless one or more origins are allowed, by the repeatable `--cors-origin` flag or by the settings `cors_origins` and `cors_networks` (see the `cors-settings` capability). For a request whose `Origin` is allowed, the response SHALL carry `Access-Control-Allow-Origin` with that origin (never `*`) and `Vary: Origin`. Preflight `OPTIONS` requests from an allowed origin SHALL be answered successfully, allowing the methods the API uses and the `Authorization` and `Content-Type` request headers. Credentials (cookies) SHALL NOT be allowed. Requests from other origins SHALL receive no CORS headers.

#### Scenario: allowed origin

- **WHEN** an origin listed in the allow-list sends a request
- **THEN** the response carries `Access-Control-Allow-Origin` equal to that origin

#### Scenario: preflight

- **WHEN** an allowed origin sends an `OPTIONS` preflight for `PUT` with `Authorization`
- **THEN** the response succeeds without requiring the token and allows that method and header

#### Scenario: unlisted origin

- **WHEN** an origin not in the allow-list sends a request
- **THEN** the response carries no `Access-Control-Allow-*` headers

#### Scenario: default is same-origin only

- **WHEN** no origin is configured and a request carries an `Origin` header
- **THEN** the response carries no CORS headers

#### Scenario: origin allowed by a range

- **WHEN** the origin's host is an IP inside a configured network
- **THEN** it is treated as listed

### Requirement: Short-lived scoped tickets serve header-less requests

`POST /api/auth/ticket`, authenticated with the Bearer token, SHALL return a signed ticket and its expiry. A ticket SHALL be accepted as a `ticket` query parameter, in place of the Authorization header, only on `GET /api/runs/{id}/events`, `GET /api/downloads/{id}/events`, `GET /api/projects/{id}/artifacts/...`, `GET /api/reviews/{id}/findings/{fid}/frame`, `GET /api/motions/{id}/preview`, `GET /api/music-motion/references/{id}/frames/{name}` and `GET /api/music-motion/part-drafts/{id}/preview/{name}`; it SHALL be rejected on every other route and method. Tickets SHALL expire after a short fixed lifetime and SHALL become invalid when the server restarts. When no token is configured, the endpoint SHALL still respond and tickets SHALL be unnecessary.

#### Scenario: issuing a ticket

- **WHEN** an authenticated client posts to `/api/auth/ticket`
- **THEN** it receives a ticket and an expiry time

#### Scenario: ticket on an allowed route

- **WHEN** a request to an artifact URL carries a valid unexpired `ticket` and no Authorization header
- **THEN** the artifact is served

#### Scenario: ticket on the download event stream

- **WHEN** a request to `GET /api/downloads/{id}/events` carries a valid unexpired `ticket` and no Authorization header
- **THEN** the event stream is served

#### Scenario: ticket on a mutating route

- **WHEN** a request to `POST /api/projects/{id}/runs` or `POST /api/downloads` carries a valid `ticket` and no Authorization header
- **THEN** the response is 401

#### Scenario: expired or forged ticket

- **WHEN** a request carries an expired or tampered ticket
- **THEN** the response is 401

#### Scenario: ticket endpoint requires the token

- **WHEN** `/api/auth/ticket` is called with a token configured and no valid Bearer token
- **THEN** the response is 401

### Requirement: Failed authentications are rate-limited per address

After a fixed number of `401` answers from one client address within a window (at least 10 per minute), the server SHALL answer further unauthenticated requests from that address with `429` and the standard error body, until the window passes. Authenticated requests are not affected. The limiter SHALL use the connection's address, not a forwarded header, unless the operator configures a trusted proxy.

#### Scenario: guessing the token
- **WHEN** an address sends 50 requests with wrong tokens in one minute
- **THEN** the answers after the limit are `429`, and the right token from another address is accepted

#### Scenario: no token configured
- **WHEN** the server runs without a token
- **THEN** no request is counted and nothing is limited
