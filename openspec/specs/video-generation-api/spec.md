# video-generation-api Specification

## Purpose
TBD - created by archiving change add-generation-api. Update Purpose after archive.

## Requirements

### Requirement: The server lists the ways a video can be made

`GET /api/generation/options` SHALL list the options a video can be made with, in a stable order, each described well enough for a client to draw its form without knowing the engines. The first available option SHALL be marked as the default.

#### Scenario: listing the options

- **WHEN** the options are asked for
- **THEN** each option has a stable identifier, a name, a short description, the engine it uses, `available`, the length in seconds of the clip its engine makes by itself (`clip_seconds`), the range of durations it accepts, whether it can write audio and an estimate of the seconds a generation takes, and all identifiers are different

#### Scenario: an option that cannot run

- **WHEN** the weights an option needs are not installed
- **THEN** the option is listed with `available: false` and a `reason` that says what is missing, as the model availability explains a missing model

#### Scenario: the default

- **WHEN** at least one option is available
- **THEN** exactly one option is marked as the default, the first available one

### Requirement: A video is requested in one call

`POST /api/generations` SHALL take a prompt, an optional start picture, an option, a duration in seconds, an aspect ratio, a quality, an audio flag and an optional `client`, plan the generation from them, create it as a project of its own, launch its run on the existing queue and answer `202` with its identifier and the identifier of the run. `client` is the name of the dashboard that asks, written by that dashboard itself (for example `video-studio`): one to 32 characters among lowercase letters, digits and hyphens. Without it the client is `unknown`. A request the option does not accept, or a `client` that does not fit that form, SHALL be refused with `422` and the reason, and nothing SHALL be created.

#### Scenario: a valid request

- **WHEN** a generation is requested with a prompt, an available option, 8 seconds, `16:9`, a quality and no audio
- **THEN** the server answers `202` with the identifier of the generation and of its run, and the run is queued or running

#### Scenario: a start picture

- **WHEN** the request carries a picture
- **THEN** the picture is the start of the clip and a prompt is optional

#### Scenario: no prompt and no picture

- **WHEN** the request has neither a prompt nor a picture
- **THEN** it is refused saying a prompt or a picture is needed, and no generation exists

#### Scenario: outside what the option accepts

- **WHEN** the duration is outside the option's range, the aspect ratio or the quality is not one of those offered, or the audio is asked for an option that cannot write it
- **THEN** the request is refused with `422` saying which setting and what is accepted, and no generation exists

#### Scenario: an option that cannot run or does not exist

- **WHEN** the option is unavailable or unknown
- **THEN** the request is refused with the reason, and no generation exists

#### Scenario: a client

- **WHEN** a generation is requested with `client` `video-studio`
- **THEN** the server answers `202` and the generation, its project and its run carry `video-studio`

#### Scenario: no client

- **WHEN** a generation is requested without `client`
- **THEN** its client is `unknown`

#### Scenario: a client that does not fit

- **WHEN** `client` has capitals, spaces or more than 32 characters, or is empty
- **THEN** the request is refused with `422` saying what a client may be, and no generation exists

### Requirement: The duration is the length of the scene and the motion loops

The duration of a request SHALL be the length of the scene (`silence_seconds`), not the length of the engine's clip. The clip an engine makes is short and fixed by its settings; the server SHALL stretch it as the clip phase already does (slowed by at most 2x, then looped, or cut), and SHALL NOT chain clips. The answer to the request and the options SHALL say the length of the clip (`clip_seconds`) so a client can tell that a longer video repeats the same motion.

#### Scenario: a duration longer than the clip

- **WHEN** a video of 20 seconds is requested with an option whose clip is about 4 seconds
- **THEN** the scene lasts 20 seconds, the clip is looped to fill it, and the answer reports `clip_seconds`

#### Scenario: the cost does not follow the duration

- **WHEN** the same option is estimated for 5 and for 20 seconds
- **THEN** the time to generate the clip is the same and only the stretching changes

### Requirement: A generation can be followed and listed

`GET /api/generations/{id}` SHALL give the state (`queued`, `running`, `done` or `failed`), the progress from 0 to 100, the request it was made from, its `client` and the identifier of its run, plus the address of the video when done or the message when failed. `GET /api/generations` SHALL list them newest first. A generation made before clients existed SHALL show the client `unknown`. The events of the run SHALL be followed through the existing run event stream and the video SHALL be served read-only as an artifact.

#### Scenario: following

- **WHEN** a running generation is read
- **THEN** it reports `running`, a progress and its run identifier, and the same progress appears in the run's events

#### Scenario: a finished generation

- **WHEN** a generation is done
- **THEN** it reports `done` with the address of the video, which can be fetched with the access the rest of the API asks for

#### Scenario: a failed generation

- **WHEN** the run fails
- **THEN** the generation reports `failed` with the message of the failure

#### Scenario: the list

- **WHEN** two generations were made one after the other and the generations are listed
- **THEN** the second comes before the first

#### Scenario: the client in the reads

- **WHEN** a generation made by `video-studio` is read, alone or in the list
- **THEN** it reports the client `video-studio`

#### Scenario: an old generation

- **WHEN** a generation that was made before clients existed is read
- **THEN** it reports the client `unknown`

### Requirement: A generation can be removed

`DELETE /api/generations/{id}` SHALL cancel the generation when it is queued or running and remove it with its files and its run history.

#### Scenario: removing a running generation

- **WHEN** a running generation is deleted
- **THEN** its run is cancelled, it is no longer listed and its files and run history are gone

#### Scenario: removing a finished generation

- **WHEN** a finished generation is deleted
- **THEN** its video is no longer served and it is no longer readable

### Requirement: Generations are not listed as projects

A generation SHALL exist as a project the server creates and looks after, and SHALL NOT appear in `GET /api/projects`, the project statistics or the storage report as a user's project. Its disk use SHALL be reported with the generations.

#### Scenario: the project list

- **WHEN** a generation exists and the projects are listed
- **THEN** the generation is not among them

### Requirement: The access rules of the API apply

The generation endpoints SHALL ask for the same token as the rest of `/api`, SHALL respect the content mode (a request SHALL NOT create anything for adults while the server is censored), and SHALL refuse with `503` and the reason when the machine cannot run the option now.

#### Scenario: no token

- **WHEN** a generation is requested without the token the server asks for
- **THEN** it is refused as any other call is

### Requirement: The options say which inputs each one accepts

Each option of `GET /api/generation/options` SHALL carry an optional `inputs` object that says what the option takes besides the prompt, with its limits: `start_image` (boolean), `end_image` (boolean), `reference_images` (`{min, max}`), `reference_videos` (`{max, max_seconds}`) and `reference_audios` (`{max, max_seconds}`). An input that is missing, `false` or `0` is not accepted by the option. The limits SHALL be those measured for the memory of the card the server would use, and the options that take none of the new inputs SHALL describe only `start_image: true` and otherwise behave as before.

#### Scenario: an option with a last picture

- **WHEN** the options are asked for on a machine that has the first/last-frame bundle of MiniMax H3 and LTX 2.3
- **THEN** `h3-full`, `h3-draft` and `ltx` say `start_image: true` and `end_image: true`, and the Wan and AnimateDiff options say `start_image: true` and nothing else

#### Scenario: an option with references

- **WHEN** the Ref2VA bundle of MiniMax H3 is installed
- **THEN** `h3-ref` and `h3-ref-draft` are listed with `start_image` and `end_image` false and with limits for the three kinds of reference, and no other option announces references

#### Scenario: the limits follow the card

- **WHEN** the card the server would use has 12 GB, and then 16 GB
- **THEN** the references of `h3-ref` are 2 pictures, 1 video of at most 2 seconds and 1 audio of at most 3 seconds with 12 GB, and 6 pictures, 2 videos of at most 3 seconds and 3 audios of at most 4 seconds with 16 GB

#### Scenario: a client that does not read it

- **WHEN** a client ignores `inputs` and sends what it sent before
- **THEN** its requests are accepted and made exactly as before

### Requirement: A video can end on a picture

`POST /api/generations` SHALL accept a part `end_image` (png, jpg, jpeg or webp) with an option whose `inputs` has `end_image: true`, and it SHALL need the start picture too. The clip SHALL start on the start picture and end on the last one.

#### Scenario: a first and a last picture

- **WHEN** a request to `h3-full` carries `image` and `end_image`
- **THEN** the server answers `202` and the video starts on the first picture and ends on the last

#### Scenario: a last picture alone

- **WHEN** a request carries `end_image` and no `image`
- **THEN** it is refused with `422` saying that a last picture needs a first one, and no generation exists

#### Scenario: an option that does not take it

- **WHEN** a request to a Wan option carries `end_image`
- **THEN** it is refused with `422` saying that the option cannot end on a picture and which options can, and no generation exists

### Requirement: A video can be guided by references

`POST /api/generations` SHALL accept the repeated parts `reference_images`, `reference_videos` and `reference_audios` with an option whose `inputs` announces them, in the order they come. The request SHALL carry at least one reference picture, which anchors the scene and is the first reference; it SHALL NOT carry a start picture or a last picture; the number of each kind SHALL NOT pass the maximum the option announces; each video and audio SHALL be readable and last no more than the option announces. The prompt SHALL be given the reference pictures by number (`<Picture 1>`, `<Picture 2>`, …) in the order of the parts, as the engine reads them.

#### Scenario: pictures, a video and an audio

- **WHEN** a request to `h3-ref` carries two reference pictures, a video of 2 seconds and an audio of 3 seconds
- **THEN** the server answers `202` and the video is made with all four as references

#### Scenario: more than the option accepts

- **WHEN** a request carries more pictures, videos or audios than the option announces, or a video or an audio longer than it announces
- **THEN** it is refused with `422` saying which input and what is accepted, and no generation exists

#### Scenario: a file that is not what it says

- **WHEN** a reference video is not a video, a reference audio has no sound, or a picture cannot be read
- **THEN** it is refused with `422` saying which part, and no generation exists

#### Scenario: no reference picture

- **WHEN** a request carries references but no reference picture
- **THEN** it is refused with `422` saying that at least one reference picture is needed, and no generation exists

#### Scenario: references with a start or a last picture

- **WHEN** a request carries references and `image` or `end_image`
- **THEN** it is refused with `422` saying that they cannot be combined, and no generation exists

#### Scenario: an option that does not take references

- **WHEN** a request to `ltx` or `h3-full` carries a reference part
- **THEN** it is refused with `422` saying that the option takes no references and which options do, and no generation exists

#### Scenario: a card with too little memory

- **WHEN** the card has not the memory the references need
- **THEN** `h3-ref` is listed unavailable with the memory it needs and the memory there is, and a request to it is refused with that reason

### Requirement: The inputs a generation used are reported and can be used again

`GET /api/generations/{id}` and the list SHALL give `inputs`: one entry for each file the generation used, with its kind (`start_image`, `end_image`, `reference_image`, `reference_video` or `reference_audio`), its name and the read-only address it can be fetched from, in the order they were sent. `POST /api/generations` SHALL accept `reuse_inputs_of` with the identifier of another generation, and then use the same inputs without receiving them again.

#### Scenario: reading the inputs

- **WHEN** a generation made with a last picture and another made with references are read
- **THEN** each reports its inputs with their kinds and addresses, and a generation made without any extra input reports its start picture alone, or none

#### Scenario: repeating a generation

- **WHEN** a request carries `reuse_inputs_of` with the identifier of a generation that used a start and a last picture, and an option that takes them
- **THEN** the new generation uses the same two pictures and reports them as its inputs

#### Scenario: reusing and sending

- **WHEN** a request carries `reuse_inputs_of` and also sends an input part
- **THEN** it is refused with `422` saying to send the inputs or to reuse them, and no generation exists

#### Scenario: a generation that does not exist or has none

- **WHEN** `reuse_inputs_of` names an unknown generation, or one that kept no inputs
- **THEN** it is refused with `422` saying so, and no generation exists

#### Scenario: an old generation

- **WHEN** a generation made before this change is read
- **THEN** it reports the inputs it can be known to have used (its start picture, if it had one) and nothing is invented

### Requirement: The records of the calls are kept in the database

The record of a generation, a picture edit or a video edit made in one call (its id, its kind, its run, when it was asked, its request, its client, its estimate and its inputs) SHALL be kept in the database; the ones that exist as files SHALL be imported when the server starts (see `data-store`) and answer as before. `GET /api/generations` and `GET /api/video-edits` SHALL be lists that can be paged (see `api-listing`): sort by `created_at` (newest first by default, as today), filter by `client`. `GET /api/image-edits` SHALL list the picture edits the same way. Removing one SHALL delete its record, its project and its directory.

#### Scenario: After the import

- **WHEN** the server starts with generations kept as files
- **THEN** `GET /api/generations` and `GET /api/generations/{id}` answer the same bodies as before

#### Scenario: The picture edits

- **WHEN** `GET /api/image-edits` is asked
- **THEN** it lists the picture edits newest first, each as `GET /api/image-edits/{id}` answers it

#### Scenario: A page of generations

- **WHEN** the list is asked with `limit=20&offset=20`
- **THEN** the generations 21 to 40 come back with the total in `X-Total-Count`

### Requirement: Blender is one more way of making a video

`GET /api/generation/options` SHALL list an option `blender` among the video options, with its `engine` and a `duration` range of its own. It SHALL be `available: false` with a `reason` (the one Blender's report gives, and how to fix it) when Blender is missing or too old, and `available: true` otherwise, and it SHALL never be the default option. Its `inputs` SHALL say `scene: true`, `start_image: false`, `end_image: false` and no references; the other options SHALL describe `scene` as `false` or leave it out. Choosing `blender` with an unavailable Blender SHALL be `503` as any unavailable option is.

#### Scenario: Blender is usable
- **WHEN** the options are read on a machine where Blender runs
- **THEN** the option `blender` is there with `available: true`, `default: false` and `inputs.scene: true`

#### Scenario: Blender is not installed
- **WHEN** the options are read on a machine without Blender
- **THEN** the option `blender` is there with `available: false` and a `reason` that says how to install it, and the other options are as they were

### Requirement: A video can be a 3D scene

`POST /api/generations` with `option: "blender"` SHALL take `blender: {description}` in the request (the JSON body, or the `request` part when the request is multipart), where `description` is the JSON of a 3D scene. The description SHALL be checked as a hand-written one is, for a scene as long as `duration` and with no design and no tempo (keys in seconds), and a failure SHALL be `422` with each problem naming its place in the JSON and nothing created. When it passes it SHALL be stored in the hidden project's `sources/` and the project SHALL have one scene `blender: {build}` whose length is `duration`, whose frame follows `aspect_ratio` and whose resolution and samples follow `quality`. A prompt SHALL NOT be needed. `blender` SHALL be refused (`422`) with another option, and the option `blender` SHALL be refused without a description unless `reuse_inputs_of` brings one; `start_image`, `end_image` and references SHALL be refused with it. The `duration` SHALL be inside the range the option announces. The description SHALL NOT be copied into the `request` the generation reports: it is an input.

#### Scenario: A scene becomes a video
- **WHEN** the request is `{option: "blender", duration: 5, aspect_ratio: "16:9", blender: {description: {...}}}` with a description that passes
- **THEN** the answer is `202` with a generation whose project has `sources/<id>.scene.json` and one scene rendered by Blender for 5 seconds

#### Scenario: The scene has a fault
- **WHEN** a key of the description is beyond the 5 seconds of the request
- **THEN** the call is `422` naming the key, and no generation, project or run exists

#### Scenario: A description for another option
- **WHEN** the request sends `blender` with an option that is not `blender`
- **THEN** it is refused with `422` saying that only the option `blender` takes a scene

### Requirement: The scene is an input that is reported and reused

A generation made with the option `blender` SHALL report its description in `inputs` with `kind: "scene"`, its `name` (`<id>.scene.json`) and an `address` that answers the JSON of the description with the same access as the video. `reuse_inputs_of` SHALL copy the description of a generation into the new project when the option is `blender`, and SHALL be refused with `422` saying what is wrong when the option does not take a scene. When the request carries `blender.description` and also `reuse_inputs_of`, the description sent SHALL win: the reused one is not copied and the generation reports the new one.

#### Scenario: Repeating a generation as it was
- **WHEN** a request with `option: "blender"` and `reuse_inputs_of` naming a generation that used a description is sent without `blender`
- **THEN** the new generation uses a copy of that description and reports it as its input

#### Scenario: Repeating with a changed scene
- **WHEN** the same request also carries `blender.description`
- **THEN** the description sent is the one stored and reported, and the first generation is untouched
