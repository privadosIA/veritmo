# client-labels Specification

## Purpose
TBD - created by archiving change add-client-labels. Update Purpose after archive.

## Requirements

### Requirement: The labels in use are listed with their counts and colours

`GET /api/clients` SHALL answer the `client` labels that at least one item the server shows has, each with `name`, `color` (`#rrggbb`), `counts` (an object with the number of `projects`, `animations`, `generations`, `image_edits`, `video_edits` and `model_generations` that have it, zero when none), `total` and `last_used` (the most recent creation or update among them, RFC 3339). The labels `embedded` and `unknown` SHALL be listed like any other when an item has them. The list SHALL be sorted by `last_used`, newest first, and SHALL count only the items the content mode lets that server show (a project, animation or generation for adults on a censored server is not counted, and a label that only such items have is not listed). The route SHALL need the same authentication as the other `/api/` routes and SHALL change nothing.

#### Scenario: Two dashboards and some animations
- **WHEN** `dashboard-full` made three projects and one animation, and `claude-test` made a generation
- **THEN** the answer lists `dashboard-full` with `projects: 3` and `animations: 1`, and `claude-test` with `generations: 1`, each with a colour and a `last_used`

#### Scenario: Items for adults on a censored server
- **WHEN** the only item a label has is for adults and the server is censored
- **THEN** the label is not listed

#### Scenario: No items
- **WHEN** the server has none
- **THEN** the answer is an empty list

### Requirement: A label has a colour the server keeps

A label SHALL have the colour set with `PUT /api/clients/{name}` and `{color: "#rrggbb"}`, kept by the server with its other settings (a `client_colors` setting) so that every dashboard shows the same one, and `{color: null}` SHALL remove it. A label with no colour set SHALL have the default colour worked out from its name: the hue is the first four bytes of the SHA-256 of the name taken as a number, modulo 360, with a saturation of 55 % and a lightness of 50 %, written as `#rrggbb`; the same rule is in the documentation so a client may compute it. A name that is not `embedded`, `unknown` or of the form `^[a-z0-9-]{1,32}$` SHALL be refused with `400`, a colour that is not `#rrggbb` with `400`, and a label with no items MAY have a colour. The colour SHALL be a visual aid only: it SHALL NOT change what a label may do.

#### Scenario: A colour is set
- **WHEN** `PUT /api/clients/dashboard-full` is sent with `{"color": "#3366cc"}`
- **THEN** `GET /api/clients` lists that label with that colour, and so after a restart of the server

#### Scenario: The default colour
- **WHEN** a label has none set
- **THEN** its colour is the one the documented rule gives for its name, and it is the same on every call

#### Scenario: Back to the default
- **WHEN** `PUT /api/clients/dashboard-full` is sent with `{"color": null}`
- **THEN** the label has the default colour again

#### Scenario: A bad name or colour
- **WHEN** the name is `Dashboard Full` or the colour is `red`
- **THEN** the answer is `400` naming what is accepted and nothing is kept

### Requirement: A generation, an edit or a model generation can be relabelled

`PATCH /api/generations/{id}`, `/api/image-edits/{id}`, `/api/video-edits/{id}` and `/api/models3d/generations/{id}` SHALL accept `{client}` (a client name, or `null` or an empty text to remove it, which leaves the one the server gives by default) and SHALL write it wherever the item keeps its label (its record and its project) so that the listings and `GET /api/clients` show it at once. The change SHALL be refused with `409` while the item has an active or queued run, `400` for a field other than `client` or a name that is not valid, and `404` for an unknown item or one for adults on a censored server. The relabel SHALL change nothing else about the item, and the runs already made SHALL keep the label they had.

#### Scenario: Relabel a generation
- **WHEN** `PATCH /api/generations/{id}` is sent with `{"client": "video-studio"}`
- **THEN** the generation, its listing entries and `GET /api/clients` show `video-studio`, and its earlier runs still show the old label

#### Scenario: While it runs
- **WHEN** the generation has a run in progress
- **THEN** the answer is `409` and the label does not change

### Requirement: The label of an animation follows to its project

`PATCH /api/music-motion/pieces/{id}` with a new `client` SHALL also write it into the project of the animation when it has one (it has one from its first render), so that `managed=all` with `client=` finds the animation under the new label in the project-based listings, as `GET /api/clients` already counts it from its own row. The change SHALL be refused with `409` while the animation has an active or queued run, and nothing SHALL change then. An animation with no project yet SHALL change its own row only, and the project SHALL be written with the new label at its first render.

#### Scenario: A rendered animation is relabelled
- **WHEN** an animation that has been rendered is relabelled from `first-dash` to `second-dash`
- **THEN** `GET /api/projects?managed=all&client=second-dash` lists it and `client=first-dash` does not, and `GET /api/clients` counts it under `second-dash`

#### Scenario: While it renders
- **WHEN** the animation has a render in progress
- **THEN** a change of its `client` answers `409` and the label does not change

### Requirement: The animations can be included in the shared listings

`GET /api/projects`, `GET /api/clips`, `GET /api/library` and `GET /api/storage` SHALL accept `managed=all`, which lists, besides what `managed=include` lists, the animations of the music mode: each with `kind: music_motion` (`project_kind` in the clips, where `kind` already says `clip` or `animation`), its `client` and its real size, and following the `client` filter and the content mode as the other items do. `managed=include` SHALL answer exactly what it did before and SHALL NOT list an animation, and any value other than `include` and `all` SHALL be `400` naming both. The totals of the disk in `GET /api/storage` SHALL remain those of the disk. The runs and the statistics SHALL keep reporting the animations as they do (mode `animation`).

#### Scenario: Include does not change
- **WHEN** `GET /api/projects?managed=include` is requested on a server with animations
- **THEN** the answer is the one it gave before, with no animation

#### Scenario: All lists the animations
- **WHEN** `GET /api/projects?managed=all&client=dashboard-full` is requested
- **THEN** the animations that label made are listed with `kind: music_motion`, among the other items it made

#### Scenario: A bad value
- **WHEN** `managed=everything` is sent
- **THEN** the answer is `400` and names `include` and `all`

### Requirement: An animation is deleted by its own route

`DELETE /api/projects/{id}` of an animation SHALL be refused with `400` naming `DELETE /api/music-motion/pieces/{id}`, as for the other managed projects, so the history and the drafts of the animation are removed with it.

#### Scenario: Deleted as a project
- **WHEN** `DELETE /api/projects/{id}` is sent with the id of an animation
- **THEN** the answer is `400` that names the route to use and the animation is untouched

### Requirement: A managed project is relabelled by its own route

`PATCH /api/projects/{id}` for a project of the generation API or of the music mode (`api-gen-`, `api-edit-`, `api-vid-`, `api-m3d-`, `api-mm-`) whose body has a `client` SHALL be refused with `400` naming the route that changes the label of its kind (`PATCH /api/generations/{id}`, `PATCH /api/image-edits/{id}`, `PATCH /api/video-edits/{id}`, `PATCH /api/models3d/generations/{id}` or `PATCH /api/music-motion/pieces/{id}`), and nothing SHALL change, so the label of the project and the one of its record cannot disagree. The fields `name` and `adult` of those projects SHALL still be accepted by the project route, and the project route SHALL still relabel an ordinary project.

#### Scenario: Relabelling a generation through the project route
- **WHEN** `PATCH /api/projects/api-gen-…` is sent with `{"client": "x"}`
- **THEN** the answer is `400`, it names `PATCH /api/generations/{id}` and the label is unchanged

#### Scenario: Relabelling an animation through the project route
- **WHEN** `PATCH /api/projects/api-mm-…` is sent with `{"client": "x"}`
- **THEN** the answer is `400` and names `PATCH /api/music-motion/pieces/{id}`

#### Scenario: Another field
- **WHEN** `PATCH /api/projects/api-gen-…` is sent with `{"adult": true}`
- **THEN** it is accepted as before

#### Scenario: An ordinary project
- **WHEN** `PATCH /api/projects/mine` is sent with `{"client": "x"}`
- **THEN** it is relabelled as always
