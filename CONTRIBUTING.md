# Contributing to Veritmo

## Maintenance

This repository is maintained by one person in their free time. Issues and pull requests are welcome and are read, but there is no promised response time, and some will not be answered. A repository that has not changed for a long time is not abandoned: it is finished or waiting.

## Issues

- Search the existing issues first, and open one issue per problem.
- Say the version (the commit), the operating system, the graphics card and its memory when it matters, the exact command or request, and the log.
- A problem that cannot be repeated is closed after a reasonable wait.

## Pull requests

- Keep them small and about one thing. For anything big (a new engine, a new endpoint, a rewrite) open an issue first, so the work is not lost.
- The tests must pass: `go test ./...`.
- No formatting-only changes in files you are not touching.
- By sending a pull request you agree that your work is under the licence of this repository (Apache-2.0). There is no CLA.

## What is probably not accepted

- Dependencies on paid services.
- Models whose licence forbids sharing them.
- Anything that sends the user's data out without being asked for (the software is local on purpose).

## What is not here

This repository is the server: the HTTP API, its contract (`internal/server/openapi.yaml`) and the pipelines. The dashboards that use the API are a separate project and are not published here.

## Adult content

The project has an adult label and a censored mode for it. Issues and pull requests must not include explicit images.

## Conduct

Everyone taking part is expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security

Do not report a vulnerability in a public issue: see [SECURITY.md](SECURITY.md).
