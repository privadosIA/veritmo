# project-management Specification

## Purpose
Defines how the management API discovers, creates, duplicates, deletes and edits `scenes.yaml` projects, applying the same validation the pipeline itself runs, and how it reports per-scene cache status.

## Requirements

### Requirement: A project is a directory holding a scenes.yaml and its work directories

The server SHALL keep each project as a row of the database, identified by its id, holding its definition (the text of its `scenes.yaml`) and what the listings say about it, and a directory of the same name under the configurable projects root holding its sources and its work directories. Project listing SHALL return each project's identifier, its declared `project` name, its `output.mode`, and its scene count, read from the database without opening any directory. Pipeline runs for a project SHALL execute with that project's directory as working directory, so its `tmp/` and `out/` stay inside it; before a run starts the server SHALL write the definition to `scenes.yaml` in that directory, and when the run ends it SHALL take a definition the run changed into the database as a new version. A `scenes.yaml` changed in the directory by hand SHALL have no effect and SHALL be overwritten by the next run. The directories that hold a `scenes.yaml` when the server first starts with the database SHALL be imported as projects (see `data-store`), an invalid one included, marked as invalid with its error.

#### Scenario: listing projects

- **WHEN** the database holds the projects `demo` and `trailer`
- **THEN** the project list returns two entries identified as `demo` and `trailer` with their mode and scene count, and no directory is read

#### Scenario: non-project directories are ignored

- **WHEN** a subdirectory of the projects root has no row
- **THEN** it does not appear in the project list

#### Scenario: A run gets the saved definition

- **WHEN** a run of a project starts
- **THEN** the `scenes.yaml` of its directory is the definition saved in the database, whatever the file held before

#### Scenario: A run that fixes the definition

- **WHEN** a run applies a fix to `scenes.yaml`
- **THEN** after the run the definition of the project is the fixed one and the one before is a version

#### Scenario: Existing projects

- **WHEN** the server first starts with the database and the projects root has 12 directories with a `scenes.yaml`
- **THEN** the list answers the same 12 projects, with the same fields, as before

### Requirement: Projects can be created, duplicated and deleted

The server SHALL create a new project from a name (creating its directory and a valid starter `scenes.yaml`), SHALL duplicate an existing project (copying `scenes.yaml` but not `tmp/` or `out/`), and SHALL delete a project including its `tmp/` and `out/`. Creation and duplication SHALL reject an identifier that already exists; identifiers SHALL be restricted to filesystem- and URL-safe names. Deletion SHALL be rejected while the project has an active or queued run.

#### Scenario: create

- **WHEN** a project is created with a fresh valid name
- **THEN** its directory exists with a `scenes.yaml` that passes the pipeline's validation

#### Scenario: duplicate skips work directories

- **WHEN** an existing project with populated `tmp/` and `out/` is duplicated
- **THEN** the copy has the same `scenes.yaml` but empty/absent `tmp/` and `out/`

#### Scenario: delete is blocked while in use

- **WHEN** deletion is requested for a project with a queued or active run
- **THEN** the request is rejected with an error naming the run

### Requirement: Reading and saving scenes.yaml goes through pipeline validation

The server SHALL return a project's configuration both as the raw YAML text and as a structured representation produced by the same loader the pipeline uses. Saving SHALL validate with that same loader first and SHALL NOT write the file when validation fails, returning the loader's error message; a successful save SHALL be atomic (no partially written file observable).

#### Scenario: invalid save is rejected without writing

- **WHEN** a save is submitted whose content violates validation (e.g. duplicate scene ids, empty prompt, or `tts_text` together with `silence_seconds`)
- **THEN** the file on disk is unchanged and the response carries the validation error message

#### Scenario: valid save round-trips

- **WHEN** a valid configuration is saved and then read back
- **THEN** the returned YAML matches what was saved

### Requirement: Per-scene cache status reports what a run would re-execute

For a project, the server SHALL report, per scene and per phase, whether the next run would be a cache hit or would re-execute, computed with the pipeline's own cache decision logic against the current `scenes.yaml` and cache manifest. When no cache manifest is present (e.g. the last run did not keep `tmp/`), the report SHALL state that the next run starts cold.

#### Scenario: edited scene invalidates only its affected phases

- **WHEN** a cached project's scene changes only its `motion` preset
- **THEN** the report marks that scene's clip phase as re-execute and its tts/render phases as cache hits

#### Scenario: missing manifest means cold run

- **WHEN** the project has no cache manifest
- **THEN** the report states the next run will re-execute all phases

### Requirement: A project keeps source audio files

The server SHALL keep the audio files a project's tracks transform in its `sources/` folder: list them (name, size), accept an upload (`PUT`, raw body, `.mp3` or `.wav` by name, at most 256 MB; an existing name answers 409), copy an already exported file from the project's `out/` folder under a given or derived name, and delete one. Deleting a source named by a track of the project SHALL answer 409. Names SHALL be plain file names; anything else SHALL answer 400. Duplicating a project SHALL copy its `sources/` folder.

#### Scenario: Upload
- **WHEN** a valid mp3 is uploaded as `base.mp3`
- **THEN** it is listed and playable through the artifact endpoint

#### Scenario: Name already taken
- **WHEN** a file is uploaded under a name that exists
- **THEN** the answer is 409 and the file is unchanged

#### Scenario: Not audio
- **WHEN** the name ends in anything but `.mp3` or `.wav`
- **THEN** the answer is 400

#### Scenario: Copy an exported song
- **WHEN** `out/songs/ballad.mp3` is copied as a source
- **THEN** `sources/ballad.mp3` exists and the original is untouched

#### Scenario: Copy from outside out/
- **WHEN** the origin is not a file under `out/`
- **THEN** the answer is 400

#### Scenario: Delete in use
- **WHEN** a track names the source
- **THEN** deleting answers 409

#### Scenario: Duplicate
- **WHEN** a project with sources is duplicated
- **THEN** the copy has the same sources

### Requirement: A video upload becomes an audio source

Uploading a file with a video extension (`.mp4`, `.mov`, `.mkv`, `.webm`, `.avi`) as a source SHALL extract its audio to `sources/<name>.mp3` with ffmpeg and answer that name; the video SHALL NOT be kept. An existing target SHALL answer 409; a file without audio or a failed extraction SHALL answer 422 carrying ffmpeg's last lines and leave no file; a missing ffmpeg SHALL answer 503 saying so.

#### Scenario: Video with audio
- **WHEN** `clip.mp4` is uploaded
- **THEN** `sources/clip.mp3` exists, the answer names it and no `clip.mp4` is stored

#### Scenario: No audio track
- **WHEN** the video has no audio
- **THEN** the answer is 422 and nothing is stored

#### Scenario: Target exists
- **WHEN** `sources/clip.mp3` already exists
- **THEN** the answer is 409 and it is unchanged

#### Scenario: ffmpeg missing
- **WHEN** ffmpeg is not installed
- **THEN** the answer is 503

### Requirement: Sources include pictures

The sources of a project SHALL also accept `.png`, `.jpg`, `.jpeg` and `.webp` files (upload, list, play through the artifact endpoint), and deleting one named by a scene's `image` SHALL answer 409.

#### Scenario: Upload a picture
- **WHEN** `photo.jpg` is uploaded
- **THEN** it is listed with the other sources

#### Scenario: Delete in use
- **WHEN** a scene names the picture
- **THEN** deleting answers 409

#### Scenario: Audio fields refuse it
- **WHEN** a track's `source` names a picture
- **THEN** validation rejects it

### Requirement: A starting picture in use cannot be deleted

Deleting a source named by a scene's `init_image` SHALL answer 409 naming the scene.

#### Scenario: Delete in use
- **WHEN** a scene uses the picture as `init_image`
- **THEN** deleting answers 409

### Requirement: A YouTube URL becomes an audio source

`POST /api/projects/{id}/sources/from-url` with `{"url": "<url>", "name": "<optional>"}` SHALL download the audio of one YouTube video with `yt-dlp` and store it as `sources/<name>.mp3`, answering `201 {name}`. The URL SHALL be `https` on `youtube.com`, `www.youtube.com`, `m.youtube.com`, `music.youtube.com` or `youtu.be`; anything else, a playlist-only URL, or a URL that does not parse SHALL be refused with `400` naming what is accepted. Only the video's own audio SHALL be taken: no playlist, no video stream. The call SHALL NOT use a login, cookies, or any option taken from the request, and the URL SHALL reach the program as one argument after `--`, never through a shell. The name SHALL default to the video's title reduced to letters, digits, `.`, `_` and `-` (at most 60 characters), made unique with a numeric suffix; a given `name` SHALL follow the source-name rules and an existing source SHALL answer `409`. A video longer than 20 minutes, a result larger than the source size limit, or a download that takes more than 5 minutes SHALL be refused or stopped, with nothing left in `sources/`. When the request is cancelled the download SHALL stop. A video that needs a login or is unavailable SHALL answer `422` with the reason, and a missing `yt-dlp` or `ffmpeg` SHALL answer `503` saying how to install it.

#### Scenario: A video is added
- **WHEN** a valid YouTube URL is posted
- **THEN** `sources/<title>.mp3` exists, the answer is `201` with its name and it is listed like any source

#### Scenario: Not YouTube
- **WHEN** the URL is `https://example.com/a.mp3` or `http://youtube.com/watch?v=x`
- **THEN** the answer is `400` and nothing is downloaded

#### Scenario: Too long
- **WHEN** the video lasts more than 20 minutes
- **THEN** the answer is `422` and `sources/` has no new file or leftover

#### Scenario: Needs a sign-in
- **WHEN** `yt-dlp` reports the video is private or asks to sign in
- **THEN** the answer is `422` with that reason and no cookies or account are used

#### Scenario: The program is missing
- **WHEN** `yt-dlp` is not installed
- **THEN** the answer is `503` and names `scripts/get-ytdlp.sh`

#### Scenario: Name already used
- **WHEN** the given `name` exists in `sources/`
- **THEN** the answer is `409`

#### Scenario: Cancelling
- **WHEN** the client closes the request during the download
- **THEN** the program is stopped and no partial file remains

### Requirement: Waveform peaks of a project audio

`GET /api/projects/{id}/peaks?file=<path>&buckets=<n>` SHALL answer, for an audio file of the project (`sources/<name>` or an exported `out/<project>/<name>` that is an mp3 or wav), `{duration, buckets, peaks, active}`: the length in seconds, the number of buckets, `peaks` an array of that many values from 0 to 1 (the peak level of each equal slice of the file) and `active` the first and last instants (`{start, end}`, seconds) where the level is above a fixed silence threshold, or `null` when the whole file is silent. `buckets` SHALL default to 800 and be at most 4000. The path SHALL be confined to the project (no `..`, no other folders, no symlink out); an unknown project or file answers `404`, a path outside `sources/` and `out/` or a name that is not an audio file answers `400`, an unreadable file `422` and a missing ffmpeg `503`. The answer SHALL not depend on the browser.

#### Scenario: Peaks of a source
- **WHEN** the peaks of `sources/base.mp3` are asked with `buckets=400`
- **THEN** the answer has 400 values in 0..1, the file's duration and its active range

#### Scenario: Silent edges
- **WHEN** a file has silence at the start and the end
- **THEN** `active.start` and `active.end` skip them

#### Scenario: Outside the allowed folders
- **WHEN** the file is `tmp/x.mp3`, `../x.mp3` or an absolute path
- **THEN** the answer is `400` and nothing is read

#### Scenario: Too many buckets
- **WHEN** `buckets` is 100000
- **THEN** the answer is `400`

### Requirement: A picture upload can ask for its generation metadata and accept a video or GIF

`PUT /api/projects/{id}/sources/{name}` with the query `as=picture` SHALL accept a picture (`.png`, `.jpg`, `.jpeg`, `.webp`), an animated GIF (`.gif`) or a video (`.mp4`, `.mov`, `.mkv`, `.webm`, `.avi`) and answer `201` with `{name, metadata?}`. A picture SHALL be stored as it is. A GIF or video SHALL be reduced with ffmpeg to its first frame, stored as `sources/<stem>.png`, and the original SHALL NOT be kept. `metadata` SHALL be present only when generation settings were found in the original file, and SHALL carry the fields of the generation-metadata capability. An existing target SHALL answer 409 and leave it unchanged; a GIF or video that cannot be decoded SHALL answer 422 with ffmpeg's last lines and store nothing; a missing ffmpeg SHALL answer 503 when a GIF or video needs it. Without `as=picture` the endpoint SHALL behave exactly as before, including a video becoming an audio source.

#### Scenario: a picture with metadata
- **WHEN** `scene.png` with generation settings is uploaded with `as=picture`
- **THEN** the answer is 201 with its name and the found settings, and the picture is stored

#### Scenario: a picture without metadata
- **WHEN** `photo.jpg` without settings is uploaded with `as=picture`
- **THEN** the answer is 201 with only the name

#### Scenario: a video becomes its first frame
- **WHEN** `clip.mp4` whose `comment` carries settings is uploaded with `as=picture`
- **THEN** `sources/clip.png` exists, no `clip.mp4` is stored, and the answer names `clip.png` with the settings

#### Scenario: an animated GIF
- **WHEN** `anim.gif` is uploaded with `as=picture`
- **THEN** `sources/anim.png` holds its first frame

#### Scenario: the audio path is untouched
- **WHEN** `clip.mp4` is uploaded without `as=picture`
- **THEN** `sources/clip.mp3` is created as before

#### Scenario: target exists
- **WHEN** `sources/clip.png` already exists
- **THEN** the answer is 409 and it is unchanged

### Requirement: The peaks answer can say where the pulse falls

`GET /api/projects/{id}/peaks` SHALL accept `bpm`, a number from 20 to 400, and then answer `beat_offset`: the second, under one beat, of the first beat of the audio, found in its first 30 seconds with tempos up to 3 % either side of `bpm`. It SHALL be `null` without `bpm` or when no pulse is clearly stronger than chance, and a `bpm` outside the range SHALL be refused with 400. Answers are cached per file and tempo.

#### Scenario: A click train
- **WHEN** an audio with a click every 0.5 s starting at 0.23 s is asked with `bpm=120`
- **THEN** `beat_offset` is 0.23 within 20 ms

#### Scenario: No tempo or no pulse
- **WHEN** the request has no `bpm`, or the audio is steady noise
- **THEN** `beat_offset` is null

#### Scenario: Bad tempo
- **WHEN** the request has `bpm=500`
- **THEN** the answer is 400

### Requirement: The loudness of a project audio can be measured

`GET /api/projects/{id}/loudness` SHALL accept `file`, an `.mp3` or `.wav` in `sources/` or in a folder of `out/` (the same rule as the peaks), and answer `{lufs}`, the integrated loudness (EBU R128) of the whole file, or `null` when it is silent. Another path SHALL be refused with 400, a missing file with 404 and an unreadable one with 422. Answers are cached per file content.

#### Scenario: A song
- **WHEN** a song is asked
- **THEN** the answer carries its integrated loudness in LUFS, and asking again does not read the file again

#### Scenario: Silence
- **WHEN** the file is silent
- **THEN** `lufs` is null

#### Scenario: Another place
- **WHEN** the file is outside `sources/` and `out/`
- **THEN** the answer is 400

### Requirement: A project can be downloaded as a dataset or a package

`GET /api/projects/{id}/export` SHALL answer with a download. With `format=dataset` (the default) it SHALL be a JSON array of `[id, title, lora_info, full_prompt]` tuples that the import reads back into the same scenes (video, images) or songs (music): each `full_prompt` carries the text and the lines `Dialogue:`, `Negative:`, `Motion:`, `Seed:`, `Speed:`, `Silence:` and `Variants:` for scenes, or the style, `Lyrics:`, `Language:`, `BPM:`, `Duration:`, `Key:`, `Time signature:`, `Seed:` and `Variants:` for songs. The project's subject prompt SHALL be put in front of each scene, its negative prompt follow the scene's own in `Negative:`, and the style prefix and the voice be folded into each song's style. What the format cannot carry SHALL be listed in the `X-Export-Notes` header (URL-encoded JSON) and the number of entries in `X-Export-Count`. With `format=zip` it SHALL be the full project: `scenes.yaml`, the files of `sources/`, a README and the recipe (`RECIPE.md`, in the language of `lang` (`es` or `en`, default `en`), and `recipe.json`), without models or `out/` and `tmp/`. Another format SHALL be refused with 400, an unknown project with 404 and a project with nothing to put in a dataset with 422.

#### Scenario: Round trip
- **WHEN** a project with a narrated scene and an animated silent one is exported as a dataset and imported
- **THEN** the same scenes come back, with the subject prompt in each prompt

#### Scenario: Shared negative
- **WHEN** every scene ends up with the same negative prompt
- **THEN** the import puts it back in `defaults.negative_prompt`

#### Scenario: The package
- **WHEN** a project with a picture in `sources/` and results in `out/` is exported as a zip
- **THEN** it holds `scenes.yaml`, the README, `RECIPE.md`, `recipe.json` and `sources/<picture>`, and nothing else

#### Scenario: Nothing to export
- **WHEN** a project without scenes is exported as a dataset
- **THEN** the answer is 422

### Requirement: Sources hold videos and kept clips go with the run's files

The project's `sources/` SHALL accept video files (`.mp4`, `.mov`, `.webm`, `.mkv`) next to audio and pictures: listing them with `kind: video`, uploading, deleting and playing them as for the others, and refusing to delete one a scene uses. Deleting runs **with their files** SHALL also remove the kept clips (`out/clips/`) of the projects concerned, as it removes their `tmp/`, except for a project with a queued or running run. Deleting a project SHALL remove them with the rest of its directory.

#### Scenario: Delete a run with its files
- **WHEN** the user deletes a run with the option to delete its files
- **THEN** the project's `out/clips/` is removed with its output and `tmp/`

#### Scenario: Delete the project
- **WHEN** the user deletes a project
- **THEN** its kept clips go with it

#### Scenario: A video in use
- **WHEN** the user deletes a video from `sources/` that a scene uses
- **THEN** the server refuses and names the scene

### Requirement: The package carries the recipe of what the project uses

The recipe SHALL list the settings the project generates with (resolution, sampler, steps, cfg scale, fps, the animation engine with its resolved resolution, frames, fps, steps and cfg, and whether it uses a LoRA; for music the synthesis steps and format), the catalog's recommended settings of the chosen image model and animation model with whether the project uses them or what it changed, and every file the project uses with its place in `scenes.yaml`, a label in the chosen language, its path, its size when it is on the machine, whether it is installed, and its catalog entry and download URL when the catalog ships a file with that name. When a file is in several catalog entries, the entry that also ships the most of the project's other files SHALL be chosen. The recipe SHALL be built from `scenes.yaml`.

#### Scenario: Preset changed
- **WHEN** a project uses 6 steps with a model whose preset is 8
- **THEN** the recipe says the project changed the preset and lists `steps 8 → 6`

#### Scenario: Shared file
- **WHEN** a VAE is shipped by two catalog entries and the project's text encoder is only in the first
- **THEN** the VAE points at the first

### Requirement: A project package can be imported with a report of what is missing

`POST /api/import/package/preview` (body: the `.zip`) SHALL create nothing and answer with the project's name, the suggested id and whether it exists, the counts, the recipe with the installed state of each file on this machine, the missing files grouped by catalog entry (`install`, with the number of missing and total files), the missing files the catalog does not know (`unknown`), what the project's own validation says with its sources in place apart from missing models (`problems`), and `ready` true only when all three are empty. `POST /api/import/package/confirm?id=` SHALL create the project from `scenes.yaml` and the files directly under `sources/` and nothing else of the zip, refuse an existing id with 409 and an invalid one with 400, and not be stopped by missing models. A zip with an unsafe path, without `scenes.yaml` or not a zip SHALL be refused with 422.

#### Scenario: Missing bundle
- **WHEN** a package uses a catalog bundle that is not installed
- **THEN** the report lists it in `install` with the files missing and `ready` is false

#### Scenario: Complete package
- **WHEN** every model is installed and every source is in the package
- **THEN** `ready` is true and confirming creates a project that validates

#### Scenario: Source not carried
- **WHEN** a scene uses a picture that the package does not carry
- **THEN** `problems` names it and `ready` is false

#### Scenario: Unsafe paths
- **WHEN** the zip has an entry outside the project or a nested folder in `sources/`
- **THEN** a path with `..` is refused and nothing outside `scenes.yaml`, `recipe.json` and `sources/<file>` is written

### Requirement: A new project can be created in Easy mode

The new-project dialog SHALL offer an Easy mode option, off by default. When it is on, the style and quality choices SHALL be disabled and, after the project is created, the app SHALL write into its defaults the image model, level and (for video projects) animation that the Easy planner chooses for the machine with no time limit, leaving the scenes untouched. If no image model fits, or the project is a music project, nothing SHALL be written.

#### Scenario: Easy mode on a video project
- **WHEN** a video project is created with Easy mode on and an image model and Wan are installed
- **THEN** the saved scenes.yaml has that image model and an animate block, and no scenes

#### Scenario: Nothing fits
- **WHEN** no installed image model fits the machine
- **THEN** the project is created as without Easy mode

### Requirement: The dataset export of a music project writes the tags

The dataset export of a music project SHALL write a `Tags:` line for each song that has tags, so that the import reads them back. The engine itself is a setting and SHALL not be in the dataset; it SHALL be in the full-project package, whose `scenes.yaml` carries it, and the list of what the dataset cannot carry SHALL say so.

#### Scenario: Round trip
- **WHEN** a music project with tags is exported as a dataset and imported
- **THEN** the songs have the same tags

#### Scenario: The engine is not carried
- **WHEN** a project with `generator: heartmula` is exported as a dataset
- **THEN** the notes of the export say that the engine is not carried

### Requirement: The dataset export writes animated graphics and titles

The dataset export of a video project SHALL write each `motion_graphic` scene as an entry whose `full_prompt` has the line `Graphic: <description>` (and `Dialogue:`, `Speed:` or `Silence:` as for any scene) instead of skipping it, and SHALL write the `overlay` of any scene with the lines `Overlay:`, `Overlay position:`, `Overlay start:` and `Overlay end:` for the values that are set. The scene mode and the motion mode SHALL both read these entries back, so a project of only graphics and a mixed one travel the same way. The pages already written for the graphics and their overlays SHALL NOT be exported: the notes that follow the export SHALL say that they are written again by the model when the dataset is imported and that the full project keeps them.

#### Scenario: Round trip of a graphic
- **WHEN** a project with a graphic scene and a titled scene is exported as a dataset and imported
- **THEN** the scenes come back with the same descriptions, lengths and overlays

#### Scenario: The pages stay behind
- **WHEN** a project with a stored graphic page is exported as a dataset
- **THEN** the notes say the page is not carried and point to the full project

### Requirement: The projects are told apart by where they were made

A project whose name starts with `mobile-` (the ones the Create page makes) SHALL be a *mobile* project, whichever device made it, and every other a *desktop* one. The projects list, the Gallery, the run history, the song library and the statistics SHALL offer one shared choice, **Hechos en**, with both origins chosen by default, remembered in the browser and the same on every screen. A screen SHALL show only the projects (and their runs and songs) of the origins chosen, together with the modes chosen; with no origin chosen it SHALL show none. A new mobile project SHALL add its origin to the choice, so that it is not hidden.

#### Scenario: Only mobile

- **WHEN** only *Móvil* is chosen
- **THEN** the lists show only the `mobile-…` projects, their runs and their songs

#### Scenario: Shared

- **WHEN** the choice is changed in the Gallery
- **THEN** the projects list has the same choice

#### Scenario: A new request

- **WHEN** only *Escritorio* is chosen and the Create page makes a request
- **THEN** *Móvil* is chosen again

### Requirement: Every listed project says who made it

Each item of `GET /api/projects` SHALL carry `client`: the `client` stored in the project's `scenes.yaml`, else the client stored in the request of a managed project, else `embedded` for an ordinary project and `unknown` for a managed one. A managed project SHALL also carry `kind`: `generation`, `image_edit` or `video_edit`; an ordinary project SHALL NOT.

#### Scenario: A project of the server's own dashboard
- **WHEN** an ordinary project has no `client` in its `scenes.yaml`
- **THEN** its item says `client: "embedded"` and has no `kind`

#### Scenario: A project with a stored client
- **WHEN** a project's `scenes.yaml` says `client: video-studio`
- **THEN** its item says `client: "video-studio"`

### Requirement: Managed projects can be listed on request

`GET /api/projects` SHALL leave out the managed projects (`api-gen-`, `api-edit-`, `api-vid-`) unless the request has `managed=include`, in which case they SHALL appear as one more project with their `client` and `kind`. Any other value of `managed` SHALL be a `400` naming the accepted one. The animations of the music motion engine SHALL NOT appear in either case.

#### Scenario: Default
- **WHEN** a client lists projects with no `managed`
- **THEN** the answer is what it was before, with the new `client` field added

#### Scenario: Included
- **WHEN** a client lists with `managed=include` and a generation `api-gen-…` made by `video-studio` exists
- **THEN** it is listed with `client: "video-studio"` and `kind: "generation"`

#### Scenario: Another value
- **WHEN** the request has `managed=all`
- **THEN** the answer is a `400`

### Requirement: The listing can be narrowed to some clients

`GET /api/projects` SHALL accept `client=a,b` and keep only the projects whose client is one of those listed; each name SHALL be a client name or `embedded` or `unknown`, and a name that is none is a `400`. Without it nothing is filtered.

#### Scenario: Two clients
- **WHEN** a client lists with `managed=include&client=video-studio,embedded`
- **THEN** only projects of those two clients are listed

### Requirement: A project can be created with a client

`POST /api/projects` SHALL accept an optional `client` (a client name) and store it as `client:` in the new project's `scenes.yaml`; without it nothing is stored. A value that is not a client name SHALL be a `422` naming the rule.

#### Scenario: Created with a client
- **WHEN** `POST /api/projects {"id": "demo", "client": "video-studio"}`
- **THEN** the project's `scenes.yaml` has `client: video-studio` and it is listed with that client

### Requirement: A managed project is deleted through its own endpoint

`DELETE /api/projects/{id}` of a managed project SHALL be refused with a `400` that names the endpoint that deletes it: `DELETE /api/generations/{id}`, `DELETE /api/image-edits/{id}` or `DELETE /api/video-edits/{id}`. Nothing SHALL be deleted.

#### Scenario: Delete a generation as a project
- **WHEN** `DELETE /api/projects/api-gen-…` is called
- **THEN** the answer is a `400` naming `DELETE /api/generations/{id}` and the project is still there

### Requirement: The definition of a project keeps its versions

Every save of a definition that changes it (through `PUT /api/projects/{id}/config`, a fix applied by a review or a run, an extension of a clip, a restore) SHALL keep the text it replaces as a version with its date and the reason of the change. The server SHALL keep the 20 newest versions of a project. `GET /api/projects/{id}/versions` SHALL list them newest first (`version`, `created_at`, `reason`, `bytes`), `GET /api/projects/{id}/versions/{version}` SHALL answer its text, and `POST /api/projects/{id}/versions/{version}/restore` SHALL make it the definition after the validation any save passes, keeping the replaced one as a version. An unknown version SHALL be `404`; a restore SHALL be `409` while the project has an active run. The `scenes.yaml.bak` files that exist SHALL be imported as versions.

#### Scenario: Two saves

- **WHEN** a definition is saved twice with different text
- **THEN** the list of versions has the two texts it replaced, newest first

#### Scenario: A save that changes nothing

- **WHEN** the same text is saved again
- **THEN** no version is added

#### Scenario: Restore

- **WHEN** a version is restored
- **THEN** `GET /api/projects/{id}/config` answers that text and the text it replaced is the newest version

### Requirement: One project can be read and its details edited

`GET /api/projects/{id}` SHALL answer the project as the list shows it. `PATCH /api/projects/{id}` SHALL change the fields sent among `name` (the declared `project` name), `client` and `adult`, writing them in the definition as a save does (a version is kept), and answer the project; it SHALL be `400` naming a field that cannot be changed this way, `404` for an unknown project and `409` while the project has an active run. `GET /api/projects` SHALL be a list that can be paged (see `api-listing`): sort by `id` (the order of today by default), `name` or `updated_at`, search `q` in the id and the name, combined with the filters it has today.

#### Scenario: A new client

- **WHEN** `{client: "lab"}` is patched
- **THEN** the project and its definition say `client: lab`, and the previous definition is a version

#### Scenario: A page of the list

- **WHEN** the list is asked with `limit=10&sort=-updated_at`
- **THEN** the 10 projects changed last come back with the total in `X-Total-Count`

### Requirement: Sources accept 3D models

The project's `sources/` SHALL accept binary glTF files (`.glb`) next to the other kinds: uploaded, listed, downloaded and deleted like the others, with `kind: "model"`. Each model of the listing SHALL carry `clips` (the names of the animations the file has, possibly none), `triangles` (its number of triangles) and `needs` (the names of the pictures of `sources/` the file refers to, possibly none), read from the file. A file that is not a valid binary glTF SHALL be refused on upload with a `400` that says so, and the other 3D formats (`.gltf`, `.fbx`, `.obj`) SHALL be refused as any name that is not accepted. Deleting a model that a stored description names is allowed, and the scene that names it is then refused at its next run, naming the file.

#### Scenario: Upload a model
- **WHEN** `character-female-a.glb` is uploaded to `PUT /api/projects/{id}/sources/character-female-a.glb`
- **THEN** it answers `201` and `GET /sources` lists it with `kind: "model"`, its clips (`idle`, `sprint`, `walk`, …), its triangles and `needs: ["colormap.png"]`

#### Scenario: Not a glb
- **WHEN** a text file named `x.glb` is uploaded
- **THEN** the upload is refused with `400` saying it is not a valid binary glTF and nothing is stored

#### Scenario: Another 3D format
- **WHEN** `x.fbx` is uploaded
- **THEN** it is refused as a name that is not accepted

### Requirement: A library model can be added to a project

`POST /api/projects/{id}/sources/model` with `{from, name?}` SHALL copy the model `from` (`<pack>/<name>` of the library) into the project's `sources/`: the `.glb` as `<name>.glb` (or `name` when given, a plain `.glb` name) and every picture it needs, each stored as `<pack>--<picture name>` with the addresses inside the copy of the `.glb` rewritten to that name, so that models of two packs that call their texture the same never collide. A picture that is already there with the same content SHALL be reused and a `.glb` that is already there with the same content SHALL answer `200` without copying again. A file of the same name with other content SHALL be `409` naming it; an unknown model `404`; a project with a run queued or active `409`. The answer SHALL be `201 {name, pictures: [...]}` and the copy SHALL be an ordinary source, listed with `kind: model`, usable by `file` in a description, carried by the export of the project, and independent of the library from then on.

#### Scenario: Add a character
- **WHEN** `kenney-mini-characters/character-female-a` is added to a project
- **THEN** `sources/` has `character-female-a.glb` and `kenney-mini-characters--colormap.png`, the list says `kind: model` with its clips and `needs: ["kenney-mini-characters--colormap.png"]`, and a scene that places it renders with its texture

#### Scenario: Two characters of a pack
- **WHEN** a second character of the same pack is added
- **THEN** the picture is reused, not copied twice

#### Scenario: Two packs with the same atlas name
- **WHEN** models of two packs whose atlas is called `colormap.png` are added
- **THEN** each has its own picture (`<pack>--colormap.png`) and both render with their own texture

#### Scenario: A name taken by another file
- **WHEN** the project has another `character-female-a.glb` with other content
- **THEN** the call is `409` naming it, and `name` can give another

### Requirement: The listings and the history know the mode models3d

The projects listing SHALL report the mode `models3d` and SHALL accept it in the filter by mode where the listing has one; `GET /api/runs` and `GET /api/stats` SHALL accept `mode=models3d` (with the other modes, comma-separated) and report the runs of these projects and of the managed projects `api-m3d-…` under it; the check of whether a succeeded run still has its result SHALL look for a `.glb` in the run's output folder; the storage of a project SHALL count `out/<project>/*.glb` with its output; the project package SHALL carry its `models:` entries and the pictures of the entries that have their own, and its recipe SHALL say the engines it uses and the weights each one reads (the models themselves are outputs, as the songs and pictures of the other modes are, and a project makes them again); and `mode` values that are not known SHALL still be `400` naming the accepted ones, which now include `models3d`.

#### Scenario: The history by mode
- **WHEN** the run history is asked with `mode=models3d`
- **THEN** only the runs of projects of that mode and of generations are listed

#### Scenario: A result that was deleted
- **WHEN** the `.glb` of a run is removed from `out/`
- **THEN** the run no longer reports a result

#### Scenario: The package
- **WHEN** a project of this mode is exported as a package and imported
- **THEN** it comes back with its entries and the pictures of the entries that have their own, and its recipe names the engines and the weights they read
