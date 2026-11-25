# blender-scene-writer Specification

## Purpose
TBD - created by archiving change add-blender-scene-writer. Update Purpose after archive.

## Requirements

### Requirement: A model writes the description of a 3D scene from an idea

`POST /api/projects/{id}/blender/descriptions` with `{name, idea, seconds, revise?, provider?}` SHALL ask the language model of the routed `motion_graphics` task for a description (contract version 1 of `blender-engine`) of a scene `seconds` long, and SHALL store it as `sources/<name>.scene.json` only when it passes the same checks as one written by hand. `name` SHALL be a plain file name ending in `.scene.json`, `idea` a text of up to 2000 characters and `seconds` a number from 0.5 to 600. The model SHALL be told, from the project and from the contract and not from a fixed text: the length of the scene and the frame's aspect, the colours behind `bg`, `fg` and `accent` of the project's design, the fonts `display` and `body` only when the design has a font in the server's pack for them, the tempo and the number of beats the scene lasts or, when the project has no tempo, that keys are in seconds, and the object types, light types, easings, properties and limits the contract accepts. The idea SHALL reach the model as data to illustrate and never as an instruction that can change what is accepted: the answer is stored only if it passes the checks, whatever the idea says. The answer of the call SHALL carry the stored description as written, the same resolved (every key in seconds, every colour `#rrggbb`), the provider and model that wrote it and the number of attempts. An existing file of that name SHALL be replaced only when the new description is accepted.

#### Scenario: An idea becomes a scene
- **WHEN** the call is made with the idea "the word Hola in big letters rising from the floor, a red ball that hits on every beat, a slow dolly in" and `seconds: 5` in a project with a design and a tempo of 120 bpm
- **THEN** `sources/<name>.scene.json` exists, passes the checks of `blender-engine`, and the answer says which model wrote it and in how many attempts

#### Scenario: The writer uses only what the project has
- **WHEN** the project has no `defaults.motion.bpm`, or its design has no font of the server's pack for `display`
- **THEN** the model is told to key in seconds, or not to set a font, and a description that uses `beat` or `font: "display"` anyway is asked for again with the problem

#### Scenario: The idea tries to change the rules
- **WHEN** the idea says to ignore the contract and answer with something else
- **THEN** nothing but a description that passes the checks is stored, and the call fails with `422` if the model obeyed

#### Scenario: The name is not a plain file name
- **WHEN** `name` is `../x.scene.json`, an absolute path, or does not end in `.scene.json`
- **THEN** the call is refused with `400` and the model is not called

### Requirement: A description that does not pass is asked for again and then refused

When the model's answer is not valid JSON, breaks a rule of the contract, or is a scene with no object or in which nothing moves, the server SHALL ask again with the problems, each naming its place in the JSON, and the description it sent, up to 3 attempts in all. When no attempt passes it SHALL answer `422` with the last problems and SHALL leave the stored file as it was. A scene SHALL count as having something that moves when the camera or an object has at least two keys that differ in a channel they both give.

#### Scenario: Repaired at the second attempt
- **WHEN** the first answer has a key beyond the length of the scene and the second does not
- **THEN** the second is stored and the answer says 2 attempts

#### Scenario: Nothing moves
- **WHEN** the answer is a valid description with one key on the camera and one on each object, all alike
- **THEN** it is asked for again, naming that nothing moves

#### Scenario: Three failures
- **WHEN** no attempt passes
- **THEN** the answer is `422` with the problem of the last attempt, and a file that was stored before is as it was

### Requirement: A stored description can be revised in words

With `revise: true` the model SHALL be shown the stored description and the request in `idea`, and SHALL answer with the whole new description, which goes through the same checks, attempts and storing as a new one. A call with `revise: true` for a name with no stored file SHALL be refused with `404`.

#### Scenario: Slower camera
- **WHEN** a description is stored and the call asks with `revise: true` for a slower camera, and the model answers with a valid description
- **THEN** the stored file is the new one and only the model's answer decides what changed

#### Scenario: Nothing to revise
- **WHEN** `revise` is true and `sources/<name>.scene.json` does not exist
- **THEN** the call is refused with `404` and the model is not called

### Requirement: A description can be read and saved by hand

`GET /api/projects/{id}/blender/descriptions/{name}` SHALL return the stored file as written and resolved, and `PUT` with `{description, seconds}` SHALL store a description written by hand after the same checks as the writer's (against a scene `seconds` long, as the writer's call does; the length is not stored, and the render checks the description again against the real length of the scene), answering `400` with the problems (each naming its place in the JSON) when it does not pass and leaving the stored file as it was. `GET /api/projects/{id}/sources` SHALL list these files with `kind: scene`. A project whose scene says `blender.build: <name>` SHALL validate once the file is stored.

#### Scenario: Edited by hand
- **WHEN** a description written by the model is read, changed and saved with `PUT`
- **THEN** it is stored if it passes, and a scene of the project that names it renders as usual

#### Scenario: A faulty description saved by hand
- **WHEN** `PUT` sends a description with a key of `t: 9` for a scene of 5 seconds
- **THEN** the answer is `400` naming `objects[0].keys[2].t` and the stored file is as it was

### Requirement: A frame of a description can be previewed cheaply

`POST /api/projects/{id}/blender/descriptions/{name}/preview` with `{t, seconds, width?}` SHALL render the single frame at second `t` of the stored description (checked against a scene `seconds` long, like the writer's call) at a width of up to 960 pixels (480 by default, the project's aspect) with few samples, on the CPU so that it never takes a card from a run, and SHALL answer with the PNG. It SHALL refuse with `503`, saying how to install it, when Blender is not available, and with `400` when `t` is outside `seconds` or the description is not valid, and with `404` when there is no such description.

#### Scenario: A frame to look at
- **WHEN** the preview of a stored description is asked at `t: 1.5`
- **THEN** the answer is a PNG of the scene as it looks 1.5 seconds in, and no card of a run is used

#### Scenario: No Blender
- **WHEN** Blender is not installed
- **THEN** the writer and the revisions still work, and the preview is refused with `503` and the way to install it

### Requirement: The writer follows the routing, the consent and the runs of the model it uses

The call SHALL use the provider of the `motion_graphics` task (or the `provider` of the body), SHALL refuse with the routing errors the other model calls give (a remote provider without consent, a cap of calls reached), and SHALL answer `409` while a run is queued or active when the model loads into this machine's memory. It SHALL answer `503` when there is no model.

#### Scenario: A remote model without consent
- **WHEN** the provider of the task is remote and the user has not consented
- **THEN** the call is refused as every other model call is and nothing is sent

#### Scenario: A run is active
- **WHEN** a run is active and the model is a local one that needs the machine's memory
- **THEN** the call answers `409` naming the run

### Requirement: A scene can be written and previewed without a project

`POST /api/blender/descriptions` with `{idea, seconds, aspect_ratio?, description?, provider?}` SHALL ask the model of the `motion_graphics` task, with the writer and the checks of the descriptions of a project, for the description of a scene `seconds` long (from 0.5 to 600) in a frame of `aspect_ratio` (16:9 when it is not given), and answer `{description, resolved, provider, model, attempts}`; with `description` it SHALL change that one as the idea says. It SHALL use no design and no tempo (keys in seconds), SHALL store nothing, and SHALL fail as the writer of a project does (`422` after three attempts, the guards of consent and of the wait for a run). `POST /api/blender/preview` with `{description, t, seconds, aspect_ratio?, width?}` SHALL check the description as one of a project and answer a PNG of the frame at second `t` drawn on the CPU like the preview of a project, storing nothing; a description that does not pass SHALL be `400` naming each problem. Neither SHALL need or touch a project.

#### Scenario: A scene written for a vertical frame
- **WHEN** the writer is asked for an idea, `seconds: 5` and `aspect_ratio: "9:16"`
- **THEN** the answer has a description that passes the checks for a 9:16 frame and nothing was written to any project

#### Scenario: A preview of what is being edited
- **WHEN** the preview is asked for a description in the body at `t: 1.5`
- **THEN** it answers a PNG, and a description with a key beyond `seconds` is `400` naming that key

#### Scenario: Blender is not usable
- **WHEN** the preview is asked on a machine without Blender
- **THEN** it answers `503` with the reason

### Requirement: A model writes the 3D scene of a piece of the script

`POST /api/music-motion/pieces/{id}/script/{piece}/blender` with `{idea, revise?, description?, provider?, save?}` SHALL ask the model of the `motion_graphics` task, with the writer and the checks of `blender/descriptions`, for the description of a scene as long as the piece `{piece}` of the script, for the piece's frame, told the song's tempo and the number of beats the piece lasts and the design's colours and fonts (the fonts only when the design has one in the server's pack), and SHALL put it in the script as the piece's `blender` (the piece stops being made from a part when it was). With `revise` the piece's own description SHALL be shown to the model and `idea` says how to change it (`404` if the piece is not 3D); with `description` the one given SHALL be the one that is changed, so an editor that has not saved can change what it holds, and with `save: false` the script SHALL be left as it was and the description only answered. The answer SHALL carry the piece, the resolved description, the provider, the model and the attempts. It SHALL fail as the writer of a project does (`422` after three attempts with the script untouched, the guards of consent and of the wait for a run, `404` for an unknown piece or script, `409` while the piece renders). The idea SHALL be data to illustrate, never an instruction that changes what is accepted.

#### Scenario: A chorus becomes a 3D scene
- **WHEN** the call is made for the piece `c1`, a chorus of 12 s in a 120 bpm song, with the idea "a red ball that hits the floor on every beat"
- **THEN** the piece `c1` of the script has `blender` with a description that passes the checks, no `part`, and the answer says which model wrote it

#### Scenario: The model fails
- **WHEN** no attempt passes the checks
- **THEN** the answer is `422` with the last problems and the script is as it was

### Requirement: A description of a piece can be looked at without saving it

`POST /api/music-motion/pieces/{id}/script/{piece}/blender/frame` with `{description, t, width?}` SHALL check the description in the body as a hand-written one for a scene as long as the piece `{piece}`, with the song's tempo and design, and answer a PNG of the frame at second `t` of the piece drawn by Blender on the CPU like the preview of a description, storing nothing. A description that does not pass SHALL be `400` naming each fault by its place in the JSON; `503` without Blender; `404` for an unknown piece or script.

#### Scenario: Looking at what is being edited
- **WHEN** the frame is asked with a description that the person has not saved, at second 3 of a piece of 10 seconds
- **THEN** a PNG is answered and the script is as it was

### Requirement: The writer is told which models the project has

When the project's `sources/` holds `.glb` files, the writer of `POST /api/projects/{id}/blender/descriptions` (and the revision of a stored description) SHALL be told, from the files and not from a fixed text, the name of each one and the clips it has, that it can place one with `type: "model"`, `file`, `size` and `animation`, and what `size`, `offset` and `loop` mean. When the project has none, the model SHALL NOT be told about the `model` type, so that a project with no models gets the prompt it had. A description the model writes that names a file or a clip the project does not have SHALL be asked for again with the problem, as any other fault. The preview of a stored description SHALL draw its models like the render does, on the CPU with few samples.

#### Scenario: A project with a character
- **WHEN** `sources/` holds `character-female-a.glb` with the clips `idle`, `sprint` and `walk`, and the idea is "a girl running across a rink"
- **THEN** the model's prompt lists that file and its clips, and a description that uses `file: "character-female-a.glb"` with `clip: "sprint"` passes the checks

#### Scenario: The model invents a file
- **WHEN** the model writes `file: "skater.glb"` and the project has no such file
- **THEN** the description is asked for again with `objects[1].file: ... was not found in the sources`, and it is stored only if the next one passes

#### Scenario: A project without models
- **WHEN** the project has no `.glb`
- **THEN** the prompt is the same as before this change and mentions no `model` type

#### Scenario: A preview with a model
- **WHEN** a stored description with a model is previewed at second 2
- **THEN** the PNG shows the model in the pose its clip has at second 2, drawn on the CPU

### Requirement: The writer of a 3D piece is told the models of the piece

When the piece holds models, `POST /api/music-motion/pieces/{id}/script/{piece}/blender` SHALL tell the model, as the writer of a project does, the name and the clips of each model of the piece that a description could use now (a valid file with every picture it needs present) and how to place one (`type: "model"`, `file`, `size`, `animation`, and what `size`, `offset` and `loop` mean), and a description that names a file or a clip the piece does not have SHALL be asked for again with the problem. A piece without models SHALL get the prompt it had before. The frame of a description that is not saved (`POST …/script/{piece}/blender/frame`) SHALL draw its models.

#### Scenario: A piece with a character
- **WHEN** the piece holds `character-female-a.glb` with `sprint` and the idea is "a girl running across the stage"
- **THEN** the prompt lists the file and its clips and a description that places it with `clip: "sprint"` passes the checks and is saved as the piece's `blender`

#### Scenario: A piece without models
- **WHEN** the piece holds no model
- **THEN** the prompt is the one the writer had before and does not mention the `model` type

#### Scenario: A frame with a model
- **WHEN** a description that places a model of the piece is sent to `…/blender/frame` at a second inside the piece
- **THEN** the PNG shows the model in the pose its clip has at that second

### Requirement: The end of the scene is never told rounded up

The writer SHALL tell the model the end of the scene in beats (and the keys of its worked example) cut to three decimals, never rounded up, so that a key the model puts at the end falls on the last frame and not after it. A scene of 3.68 s at 115.38 bpm lasts 7.0765 beats and SHALL be told as ending at beat 7.076.

#### Scenario: A length that is not a round number of beats
- **WHEN** the writer is asked for a scene of 3.68 s at 115.38 bpm
- **THEN** the prompt says the scene ends at beat 7.076 and the worked example it shows passes the checks of that scene

### Requirement: The writer is told how to size a model

When the project or piece holds models, the writer SHALL be told that `size` is the model's height unless the object has `fit: "largest"`, which makes it the model's longest dimension, and SHALL be told to use `"largest"` for flat or wide things (a plate, a rug, food, a tile, a board) and the default for people and things that stand. A project or piece without models SHALL get the prompt it had before.

#### Scenario: A project with a pizza and a character
- **WHEN** the writer is asked for a scene with both
- **THEN** its prompt explains `fit` and a description that gives the pizza `fit: "largest"` passes the checks
