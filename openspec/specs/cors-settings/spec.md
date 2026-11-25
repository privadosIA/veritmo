# cors-settings Specification

## Purpose
Lets the owner of the machine decide, in one local file, which browser pages and networks may call the API cross-origin, without any way to change it through the API.

## Requirements

### Requirement: Allowed origins and networks live in the server settings

The server SHALL read `cors_origins` (a list of exact origins, scheme + host + optional port, no path) and `cors_networks` (a list of CIDR ranges) from the settings file. An origin SHALL be allowed when it equals one of the `cors_origins` (a trailing `/` ignored), or when its host is an IP address inside one of the `cors_networks`, whatever its port or scheme. Host names that are not IP addresses SHALL be allowed only by `cors_origins`. An entry that is not valid SHALL be ignored with a warning naming it, and SHALL NOT stop the server or the other entries.

#### Scenario: Exact origin
- **WHEN** `cors_origins` has `http://192.168.1.50:4200` and a page of that origin calls the API
- **THEN** the response carries `Access-Control-Allow-Origin: http://192.168.1.50:4200`

#### Scenario: Network range
- **WHEN** `cors_networks` has `192.168.1.0/24` and a page at `http://192.168.1.77:4300` calls the API
- **THEN** it is allowed, and a page at `http://192.168.2.5:4300` is not

#### Scenario: Name is not an IP
- **WHEN** `cors_networks` has `192.168.1.0/24` and a page at `http://dashboard.example.lan:4200` calls the API
- **THEN** it is allowed only if `cors_origins` lists that exact origin

#### Scenario: Bad entry
- **WHEN** an entry is `not-a-range`
- **THEN** it is ignored with a warning and the valid entries still apply

### Requirement: The lists follow the file without a restart

The server SHALL apply a change of `cors_origins` or `cors_networks` to the requests that come after the file is saved, without restarting. A file that cannot be read or parsed SHALL keep the last valid lists (and the flag origins), never open access wider.

#### Scenario: Edit while running
- **WHEN** an origin is added to the file while the server runs
- **THEN** the next request from that origin is allowed

#### Scenario: Broken file
- **WHEN** the file is saved with invalid JSON
- **THEN** the previous lists stay in force

### Requirement: The lists can only be changed on the machine

The lists SHALL be changed only by editing the settings file or with `veritmo cors add|remove|list` run on the machine. No API endpoint SHALL return or change them, and saving the other settings through the API SHALL leave them as they were.

#### Scenario: No endpoint
- **WHEN** a client sends `PUT /api/settings` with `cors_origins`
- **THEN** the key is not accepted and the lists in the file are unchanged

#### Scenario: Command
- **WHEN** `veritmo cors add http://192.168.1.50:4200` is run and then `veritmo cors list`
- **THEN** the origin is in the file and in the list

#### Scenario: Other settings saved
- **WHEN** the dashboard saves the Ollama address
- **THEN** `cors_origins` and `cors_networks` in the file are as before

### Requirement: A rejected origin is reported once in the log

When a browser request from a different origin is not allowed, the server SHALL write one line to its log the first time it sees that origin (not on every request), naming the origin and the command that would allow it, and SHALL NOT write it for requests whose origin is the server's own address. The response SHALL stay as it is (normal status, no CORS headers). The number of origins remembered SHALL be bounded.

#### Scenario: First rejection
- **WHEN** a page at `http://192.168.1.50:4200`, not allowed, calls the API twice
- **THEN** the log has one line naming that origin and `veritmo cors add`

#### Scenario: Own origin
- **WHEN** a page served by the server itself sends a `POST` carrying its own origin
- **THEN** nothing is logged

#### Scenario: Allowed later
- **WHEN** the origin is added to the settings and calls again
- **THEN** it is allowed and no line is written
