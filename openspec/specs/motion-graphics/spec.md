# motion-graphics Specification

## Purpose
Lets a video project use animated HTML pages, written by the language model and rendered to video with Chrome, as full scenes of motion graphics and as transparent text overlays (titles, lower thirds, credits) on any scene.

## Requirements

### Requirement: A scene can be a motion graphic

A scene of a video project SHALL accept `motion_graphic`, a text describing what the graphic shows. The scene SHALL then be made from an animated page rendered to video instead of a generated picture and animation. The validation SHALL reject `motion_graphic` combined with `image`, `init_image`, `clip`, `animate`, `motion_prompt`, `animation` or `render`, and in a project that is not video, and SHALL warn that `prompt`, `negative_prompt`, `lora` and `variants` are ignored. No diffusion model SHALL be loaded for such a scene.

#### Scenario: A valid motion scene
- **WHEN** a scene has `motion_graphic: "three bullet points appearing one by one over a dark blue background"`
- **THEN** the configuration is valid and no diffusion model is needed for that scene

#### Scenario: Exclusive with other sources
- **WHEN** the scene also has `clip`, `image` or `animate: true`
- **THEN** the validation rejects it and names the fields

#### Scenario: Not a video project
- **WHEN** a music project has a scene with `motion_graphic`
- **THEN** the validation rejects it

### Requirement: The page lasts as long as the scene and is rendered frame by frame

The scene's video SHALL last as long as its narration, or its `silence_seconds` for a silent scene, at the project's resolution and frame rate. The page SHALL be rendered by setting its time explicitly for each frame, not by recording it in real time, so that the same page, length, frame rate and resolution SHALL always produce the same frames, however loaded the machine is. The page SHALL be told its duration, so it can pace its animation to the scene.

#### Scenario: Duration follows the narration
- **WHEN** the narration of a motion scene lasts 6.4 s at 30 fps
- **THEN** the scene's video has 192 frames and lasts 6.4 s

#### Scenario: Reproducible
- **WHEN** the same scene is rendered twice, or the machine is busy the second time
- **THEN** both files contain the same frames

#### Scenario: Silent scene
- **WHEN** a motion scene has `silence_seconds: 3`
- **THEN** its video lasts 3 s

### Requirement: The page is written by the model, stored in the project and editable

When a motion scene or an overlay has no stored page, the run SHALL ask the language model of the routed `motion` task for one self-contained HTML page from the description, the scene's narration text, the duration, the resolution and the project's style, and SHALL store it in the project under `motion/` with the scene's ID. The stored page SHALL be reused by later runs without calling the model; the user SHALL be able to read it, replace it or ask for a new one, and a new one SHALL replace the stored page only on request. The page SHALL be checked before it is stored: it SHALL be a complete document, SHALL not load any external resource, SHALL render without script errors at the first, middle and last frame, SHALL, when it declares a timeline, declare a valid one (see "A page may declare its animation as a timeline"), and SHALL keep its text inside the frame and apart: a text element partly outside the frame on its left or right side at the middle or the last frame, or two text boxes that overlap by more than 20% of the smaller one at the last frame, or a page that has text of which none is visible at the last frame, is a problem (a cut at the top or the bottom is not, so rolling credits can leave); a page that fails SHALL be requested again up to 3 times with the error, and when still failing the scene SHALL fail with an error that includes the last problem.

#### Scenario: First run asks for a page
- **WHEN** a motion scene has no stored page and the model answers with a valid page
- **THEN** the page is stored under `motion/` and rendered

#### Scenario: Second run does not call the model
- **WHEN** the run is repeated with the stored page unchanged
- **THEN** the model is not called and the cached video is used

#### Scenario: Edited by hand
- **WHEN** the user edits the stored page and runs again
- **THEN** the scene is rendered again from the edited page and the model is not called

#### Scenario: Page with a script error
- **WHEN** the model's page throws on the middle frame
- **THEN** the model is asked again with that error, and after 3 failed attempts the scene fails with an error event naming the problem

#### Scenario: External resource
- **WHEN** the page references a script or image by URL
- **THEN** the check rejects it and the model is asked again

#### Scenario: Text cut by the side of the frame
- **WHEN** the page's title is partly outside the left edge at the middle frame
- **THEN** the check reports it and the model is asked again with that problem

#### Scenario: Overlapping text
- **WHEN** two text boxes overlap over 20% of the smaller one at the last frame
- **THEN** the check reports it and the model is asked again

#### Scenario: Text that never appears
- **WHEN** the page's text is still at opacity 0 at the last frame because its animation never takes effect
- **THEN** the check reports that none of the text is visible and the model is asked again

#### Scenario: Rolling credits leave
- **WHEN** the credits are partly out of the frame at the top or bottom at the last frame
- **THEN** the check does not report it

#### Scenario: Invalid timeline
- **WHEN** the page's timeline block is not valid JSON, or a track names a property or a curve that does not exist, or its selector matches no element
- **THEN** the check reports the track and the problem and the model is asked again

### Requirement: A scene can carry an overlay

Any scene of a video project SHALL accept `overlay` with `text` (a description or the literal text to show), optional `start` and `end` in seconds within the scene (default the whole scene), and optional `position` (`top`, `center`, `bottom`; default `bottom`). The overlay page SHALL be generated, stored, checked and rendered as a motion graphic page is, on a transparent background, and SHALL be composited over the scene's video in the clip phase, keeping the transparency, so the picture or clip below is visible around the text. The overlay SHALL apply to generated, animated, own-picture, own-clip and motion scenes alike. The validation SHALL reject `start` or `end` outside the scene's length when it is known, `end` not after `start`, and an unknown `position`.

The overlay page SHALL be asked to draw only the overlay's own text, with neither the project's style nor the scene's narration given to the model, and a page whose frames cover more than half of the picture SHALL be rejected as one whose background would hide the scene, so that it is written again.

#### Scenario: An overlay with a background
- **WHEN** the model's overlay page has an opaque background
- **THEN** the check rejects it, saying how much of the frame it covers, and the model is asked again

#### Scenario: Title over a generated scene
- **WHEN** a generated scene has `overlay: {text: "Chapter 1"}`
- **THEN** the scene's clip shows the picture with the text animated above it, and the picture is visible around the text

#### Scenario: Only part of the scene
- **WHEN** the overlay has `start: 1` and `end: 3`
- **THEN** the text is visible only between seconds 1 and 3 of the scene

#### Scenario: Invalid range
- **WHEN** `end` is not after `start`
- **THEN** the validation names the scene and the fields

#### Scenario: Own clip with overlay
- **WHEN** a scene with `clip` also has an `overlay`
- **THEN** the overlay is composited over the clip

### Requirement: The rendered video is cached by what it depends on

The cache key of a motion scene or an overlay SHALL depend on the content of the stored page, the duration, the frame rate, the resolution and the overlay's range and position, not on the file's name or date. A change to any of them SHALL render it again; a change to a scene's description alone SHALL NOT invalidate the stored page's render until the page is regenerated.

#### Scenario: Unchanged page
- **WHEN** a run is repeated without changes
- **THEN** the motion render is skipped and reported as skipped

#### Scenario: Longer narration
- **WHEN** the narration of the scene becomes 1 s longer
- **THEN** the scene is rendered again at the new length

### Requirement: Chrome is detected, reported and required only when needed

The system SHALL look for Chrome or Chromium in this order: the path in the settings, `google-chrome-stable`, `google-chrome`, `chromium`, `chromium-browser` in the `PATH`. The environment status SHALL show the browser found, its version, and its path, or that none was found. When no browser is found, the validation of a project with a motion scene or an overlay SHALL fail with a message that names the scenes and tells how to install one or set its path; a project without them SHALL not be affected.

#### Scenario: Chrome present
- **WHEN** `google-chrome-stable` is in the `PATH`
- **THEN** the environment status shows it with its version and a motion scene validates

#### Scenario: No browser
- **WHEN** no browser is found and a scene has an overlay
- **THEN** the validation names the scene and says how to install or configure a browser

#### Scenario: Not needed
- **WHEN** no browser is found and no scene uses motion
- **THEN** the project validates and runs as before

### Requirement: The page runs in an offline sandbox

The page SHALL be loaded in a headless browser with a new empty profile for each render, deleted afterwards, with every network request blocked, no access to the user's files except the stored page itself, and a time limit for loading and for each frame. A render that exceeds the limit SHALL fail the scene with an error event; the browser process SHALL be stopped whether the render succeeds or fails.

#### Scenario: Network is blocked
- **WHEN** a page tries to fetch a remote URL while rendering
- **THEN** the request fails, and a page that depends on it fails the check

#### Scenario: Infinite loop in the page
- **WHEN** a page never returns for a frame
- **THEN** the scene fails with a timeout error and no browser process is left running

### Requirement: The scene editor previews and regenerates the page

The scene editor SHALL let the user set the description of a motion scene and the overlay of any scene, SHALL show the stored page playing with a time slider and play/pause without rendering a video, SHALL offer to regenerate the page and to open it for editing, and, when the page declares a timeline, SHALL show it as an editable timeline (see "The editor edits the timeline of a page"). When the browser or the page is unavailable it SHALL say which and SHALL disable the control rather than failing silently; when the model that writes the pages is not available (see model-availability) the controls that ask for a page SHALL be hidden, not disabled, while the preview and the manual editing of a stored page SHALL stay.

#### Scenario: Preview before the run
- **WHEN** the user generates the page of a scene and moves the slider
- **THEN** the page shows its state at that second

#### Scenario: No browser
- **WHEN** no browser is found
- **THEN** the controls are disabled with the reason shown

#### Scenario: No model
- **WHEN** the model that writes the pages is not available and the scene has a stored page
- **THEN** the page plays and can be edited by hand, and there is no generate button

### Requirement: The video and its provenance are shown like any clip

A motion scene SHALL appear in the gallery and the review like any other scene, SHALL accept transitions, voice and music as the others, and the metadata of its clip SHALL state that it was a motion graphic, the browser version, the model that wrote the page and whether the page was generated or edited.

#### Scenario: Transition into a motion scene
- **WHEN** a motion scene has `transition_seconds: 0.5`
- **THEN** the join into it is a 0.5 s crossfade

#### Scenario: Metadata
- **WHEN** the user opens the details of a motion scene's clip
- **THEN** the kind, browser version and model that wrote the page are shown

### Requirement: The model that writes the pages can be chosen on its own

The settings SHALL accept a model name for the pages of motion graphics and overlays, trimmed, without spaces or control characters and of at most 120 characters (anything else is refused with the reason), and an empty value SHALL remove it. That model SHALL be used, on whichever provider the `motion_graphics` task is routed to, in preference to the model the provider declares for the task and to the assistant's; with none saved, an Ollama SHALL use the first of `gemma4:26b` and `gemma4:31b` that it has installed, and otherwise the assistant's model (another provider keeps the model it declares for the task, then the assistant's). The details of each page SHALL say which model wrote it, and the model SHALL be dropped from the machine's GPUs as soon as the page is written, so the pictures of the same run find their memory free. The style of the project SHALL be given to the model only as guidance for colour and mood and never as text to show.

#### Scenario: A model saved for this task
- **WHEN** `qwen3-coder:30b` is saved and the assistant's model is `qwen3:8b`
- **THEN** the pages are written by `qwen3-coder:30b` and the assistant keeps `qwen3:8b`

#### Scenario: A preferred model is installed
- **WHEN** no model is saved for the task and Ollama has `gemma4:26b`
- **THEN** `gemma4:26b` writes the pages

#### Scenario: Nothing saved
- **WHEN** no model is saved and Ollama has neither `gemma4:26b` nor `gemma4:31b`
- **THEN** the assistant's model writes the pages

#### Scenario: A name that is not valid
- **WHEN** the name contains a space
- **THEN** it is refused with the reason and nothing is saved

### Requirement: The instructions given to the model ask for a designed, faithful page

The instructions that ask the model for a page SHALL give design rules (a type scale with one font stack, a base, text and accent colour with a gradient background and slow decorative layers, an alignment grid, staggered entrances with easing and at least three kinds of motion), the technical rules (offline, exact canvas size, time driven by the renderer, absolute positions only inside a positioned ancestor) and a short example of structure with neutral content. They SHALL ask the model to declare the entrances and the main movement of the content in the page's timeline block (with the property and curve names it accepts and an example) rather than in `@keyframes`, leaving CSS only for continuous decoration such as a drifting background. They SHALL require the page to write exactly the words of the description and no others, to give hierarchy by their order of importance, to make items that appear one by one do so and credits that rise scroll upward at constant speed, and SHALL tell the model not to copy the example's words or choices.

#### Scenario: Exact words
- **WHEN** the description quotes three texts
- **THEN** the instructions require those three and no other label, caption or tagline

#### Scenario: Neutral example
- **WHEN** the instructions include an example page
- **THEN** its content is neutral and the instructions say not to copy its words

#### Scenario: Entrances in the timeline
- **WHEN** the instructions are built
- **THEN** they name the timeline block, its properties, its curves and ask for the entrances to be declared there

### Requirement: A page may declare its animation as a timeline that the renderer and the preview run

A page MAY contain one `<script type="application/json" id="motion-timeline">` block with `{"version": 1, "tracks": [...]}`. Each track SHALL have `id`, an optional `label`, `target` (a CSS selector), `start` (seconds, 0 or more), `duration` (seconds, more than 0), an optional `ease`, an optional `stagger` (seconds between the elements the selector matches, in document order), and `from` and `to` objects. The properties are `opacity` (0 to 1), `x` and `y` (pixels), `scale`, `scaleX`, `rotate` (degrees), `blur` (pixels) and `reveal` (0 to 1: the share of the element shown, from the left, by clipping). `ease` SHALL be `linear`, `in`, `out` (cubic-bezier .22,1,.36,1), `in-out` (cubic-bezier .65,0,.35,1), `out-back`, or four numbers of a cubic-bezier; it defaults to `out`. A property missing from `from` or `to` is left alone. The script that applies the tracks SHALL be part of the virtual clock the renderer and the preview inject, not of the page: at each time `t` it SHALL set, for each matched element, the properties of every track as the interpolation between `from` and `to` (before `start` the `from` values, after `start + duration` the `to` values, and the element `index * stagger` later), before the page's own `window.__seek` runs, so the preview and the video show the same. A page without the block SHALL behave exactly as before.

#### Scenario: Values at the ends and in the middle
- **WHEN** a track moves `opacity` from 0 to 1 and `y` from 40 to 0 starting at 1 s for 1 s, with the `out` curve
- **THEN** at 0.5 s the element has opacity 0 and y 40, at 2 s opacity 1 and y 0, and at 1.5 s a value between with the curve's shape

#### Scenario: Stagger
- **WHEN** a track targets three list items with a stagger of 0.1 s
- **THEN** each item starts 0.1 s after the previous one

#### Scenario: Preview and video agree
- **WHEN** the same page is previewed and rendered at 1.5 s
- **THEN** both draw the same values

#### Scenario: A page without the block
- **WHEN** a page has no timeline block
- **THEN** its CSS animations and its `__seek` run as before and nothing else is applied

### Requirement: The editor edits the timeline of a page

When the stored page has a timeline block, the page editor SHALL show a timeline panel with one row per track: its label, a bar over the time axis of the scene's duration with a playhead (a red bar, also on the ruler) that follows the preview and that the user can drag to move the preview to another time, and fields for start, duration, stagger, curve and the `from` and `to` values. A clip of more than 4 seconds SHALL edit its timeline in a window instead of inline: the editor offers an "Open the timeline" button that opens the preview, its player and the panel in a window that moves by its title bar, resizes from any side or corner and maximizes, like the mixer's, and whose edits are the editor's own (closing it loses nothing). The editor's buttons (edit the page, open the timeline, save the changes, write another page) SHALL share one row with room between them. The motion scene and its overlay SHALL behave the same. Dragging a bar SHALL move its start and dragging its right edge SHALL change its duration, in steps of 0.05 s and within the scene's duration; the fields SHALL do the same by number. Every change SHALL rewrite the block inside the page's HTML in the browser, refresh the preview at the same time of the clip at once, and enter an undo and redo history of the panel; saving SHALL store the page like any manual edit. The panel SHALL NOT need the model. A page without the block SHALL show that it animates with CSS and offer to write another page (where the model is available).

#### Scenario: Moving a bar
- **WHEN** the user drags the bar of a track 0.5 s later
- **THEN** its `start` grows by 0.5, the preview shows the change and the page can be saved

#### Scenario: Editing without a model
- **WHEN** no model is available and the page has a timeline
- **THEN** the panel works and nothing asks for the model

#### Scenario: Dragging the playhead
- **WHEN** the user drags the red bar to the middle of the axis
- **THEN** the preview shows that time and nothing is edited

#### Scenario: A long clip
- **WHEN** the clip lasts more than 4 seconds
- **THEN** the timeline is not inline: a button opens it in a window that can be moved, resized and maximized, with the preview and the panel

#### Scenario: Undo
- **WHEN** the user changes a curve and presses undo
- **THEN** the previous curve returns and the preview follows

#### Scenario: Page that animates with CSS
- **WHEN** the stored page has no timeline block
- **THEN** the panel says so and does not offer to edit tracks

### Requirement: A motion scene can be made from a part of the catalog

A scene with `motion_graphic` or `motion_part` SHALL accept `motion_part`, the name of a part of the catalog of animated parts (or `auto`), `motion_params` (the parameters of the part), `motion_text` (a text of at most 120 characters shown in the verse layer for the whole scene), and `motion_bpm` (the tempo of the scene, between 40 and 240). A scene with a named part SHALL NOT need `motion_graphic`; with `auto` it SHALL need it, as the description the model works from. The validation SHALL refuse a part that is not in the catalog, a parameter the part does not have or one outside its limits, a `motion_bpm` outside the range, `motion_params` or `motion_text` without `motion_part`, and `motion_part` combined with `image`, `init_image`, `clip`, `animate`, `motion_prompt`, `animation` or `render`, naming the field, and it SHALL refuse it in a project that is not video. No diffusion model SHALL be loaded for such a scene.

#### Scenario: A valid scene
- **WHEN** a scene has `motion_part: terminal` and `motion_params: {lines: "$ run|ready"}`
- **THEN** the configuration is valid and no model is needed for it

#### Scenario: A parameter outside its limits
- **WHEN** a scene gives `motion_params: {count: 99}` to a part whose `count` goes up to 5
- **THEN** the validation refuses it naming the scene, the part and the parameter

#### Scenario: Auto needs a description
- **WHEN** a scene has `motion_part: auto` and no `motion_graphic`
- **THEN** the validation refuses it

### Requirement: The page of such a scene is built from the part, with no model

A scene with a named part SHALL get its page built from the part, the project's motion design and a clock of a fixed tempo (the scene's `motion_bpm`, else `defaults.motion.bpm`, else 120) with no sections and no words, without calling the model, and the page SHALL be stored in the project as `motion/<scene>.html` like any other and be reused by every later run. The text of `motion_text`, when there is one, SHALL appear in the verse layer for the whole scene. The video SHALL last as long as the scene does (its narration or its `silence_seconds`), the page SHALL be told its duration, and the same scene SHALL always give the same frames. The page SHALL pass the checks of a motion page. A page edited by the user SHALL be used as edited, and the scene SHALL be drawn again when the part, its parameters, the design, the tempo or the text change.

#### Scenario: No model
- **WHEN** a scene with `motion_part: counter` is run
- **THEN** its page is stored under `motion/` and rendered and the model is not called

#### Scenario: Reused
- **WHEN** the run is repeated with nothing changed
- **THEN** the stored page is used and the cached video is kept

#### Scenario: A parameter changes
- **WHEN** a `motion_params` value changes
- **THEN** the page is built again and the scene is drawn again

#### Scenario: Moves on the tempo
- **WHEN** a scene with a part is drawn at 120 bpm
- **THEN** the part moves on a beat every half second

### Requirement: The model can choose the part

A scene with `motion_part: auto` SHALL get, when it has no stored page, a choice of part and parameters from the model of the motion-graphics task, given the description, the narration, the catalog (names, descriptions, parameters and limits), the duration and the design's concept, as one JSON object with the part and its `params`. The answer SHALL be checked as a scene with a named part is (part in the catalog, parameters inside their limits); an answer that fails SHALL be asked again with the reason up to three times. When no answer is valid, or no model is available, the scene SHALL go the way a scene with only `motion_graphic` goes (the whole page written by the model) and the run SHALL say that the choice failed and why. The part and the parameters chosen SHALL be stored in the project with the page, so a later run does not ask again.

#### Scenario: A valid choice
- **WHEN** the model answers with an existing part and valid parameters
- **THEN** the page is built from them, stored, and the model is not asked again on the next run

#### Scenario: An invalid choice is asked again
- **WHEN** the model answers with a part that does not exist
- **THEN** it is asked again with the reason, and a valid second answer is used

#### Scenario: It cannot choose
- **WHEN** the model fails three times
- **THEN** the scene's page is written by the model as it was before the catalog, and the run says why the choice failed

### Requirement: The project has a motion design

A video project SHALL accept `defaults.motion` with `bpm` (the default tempo, 40 to 240) and `design` (the fields of the design of a music piece: two tones, an accent, two fonts, a default mode and modes by tag, a concept, `verse_style` and `hit_style`). The design SHALL be validated as a piece's is, naming the field. Without `defaults.motion.design` a fixed palette SHALL be chosen from the words of `subject_prompt`, the same palette for the same words, so every scene of a project looks alike.

#### Scenario: A project design
- **WHEN** `defaults.motion.design` gives colours and fonts
- **THEN** every scene with a part uses them

#### Scenario: A design that is not valid
- **WHEN** the accent is not a colour or has too little contrast with the background
- **THEN** the validation refuses it naming `defaults.motion.design.accent`

#### Scenario: No design
- **WHEN** a project has no design and its `subject_prompt` says `electronic, code`
- **THEN** the palette is the same on every run

### Requirement: A song can be added to a motion video

A video project SHALL accept `defaults.motion.song`, the name of an `.mp3` or `.wav` in the project's sources. With it the tempo and the beats of every scene made from a part SHALL come from the song, followed in its audio, and not from a fixed tempo: the scene's clock SHALL start at the position of the song where the scene starts (the scenes follow one another, so a scene starts where the ones before it end), and the page SHALL be given the song map cut to the scene's own time. The song SHALL be the soundtrack of the video at full volume unless the project's `bgm` says otherwise. A `motion_bpm` on a scene SHALL be ignored with a warning when there is a song. A song that is not in the sources, or that is not an `.mp3` or `.wav`, SHALL be refused naming the field. The scenes SHALL be drawn again when the song changes, and not when it does not. A project with no song SHALL behave as the requirement "The page of such a scene is built from the part" says. The words of the song (sections and the time of each word) are not part of this requirement: they belong to the pieces of music motion.

#### Scenario: Scenes follow the song
- **WHEN** a project with a song has two scenes of 5 s made from parts
- **THEN** the first scene's beats are those of the first 5 s of the song and the second scene's, those of the following 5 s, each in the scene's own time

#### Scenario: The soundtrack
- **WHEN** a project has `defaults.motion.song`
- **THEN** the video has that song as its sound

#### Scenario: A tempo that the song replaces
- **WHEN** a scene has `motion_bpm: 90` and the project has a song
- **THEN** the scene uses the beats of the song and the run warns that `motion_bpm` is ignored

#### Scenario: A song that is not there
- **WHEN** `defaults.motion.song` names a file that is not in the project's sources
- **THEN** the validation refuses it naming the field

#### Scenario: Same scenes, no song
- **WHEN** the same scenes are run in a project with no song
- **THEN** they are drawn at the fixed tempo, with no other change

### Requirement: A page may be given the song map and a beat clock

When a scene belongs to a music motion piece, the renderer and the preview SHALL give its page the song map and SHALL set, for each frame and before the page's own `window.__seek` runs, the current beat number, the position inside the beat (0 to 1) and the section, so that a page can animate on them and the preview and the video show the same. The beat clock SHALL be part of the virtual clock they inject, not of the page. A page of a scene that is not part of a piece SHALL not receive any of it and SHALL behave exactly as before. A page made from catalog parts SHALL be checked like any other page: complete, with no external resource, with no script error at the first, middle and last frame, and with its text inside the frame and apart.

#### Scenario: Animating on the beat
- **WHEN** a page of a piece at 120 bpm is rendered
- **THEN** at 0.5 s the beat number goes up by one and at 0.25 s the position inside the beat is 0.5

#### Scenario: Preview and video agree
- **WHEN** the same page of a piece is previewed and rendered at 3.2 s
- **THEN** both draw the same beat number, position and section

#### Scenario: A page outside a piece
- **WHEN** a motion scene of an ordinary video project is rendered
- **THEN** its page receives no song map and no beat clock

### Requirement: The pages of a project are kept in the database

The page of a motion scene or of an overlay, and its provenance (who wrote it, with which model, when, the hash of what was generated), SHALL be kept in the database with the project. `GET` and `PUT /api/projects/{id}/scenes/{scene}/motion` SHALL read and replace it there. Before a run the server SHALL write the pages of the project under `motion/` in its directory; when the run ends, a page the run wrote or replaced SHALL be taken into the database. A page changed in the directory by hand SHALL have no effect: the user edits a page through the API. The pages that exist as files SHALL be imported with their project (see `data-store`). Deleting or duplicating a project SHALL delete or copy its pages.

#### Scenario: The first run writes a page

- **WHEN** a run asks the model for the page of a scene that has none
- **THEN** after the run `GET .../scenes/{scene}/motion` answers that page and its provenance

#### Scenario: The second run does not call the model

- **WHEN** the run is repeated
- **THEN** the page written to `motion/` is the stored one, the model is not called and the cached video is used

#### Scenario: A page replaced through the API

- **WHEN** the page is replaced with `PUT` and the project runs again
- **THEN** the scene is rendered from the replaced page

### Requirement: The motion of a stored page can be revised without writing the page again

When a stored motion page declares a timeline, the program SHALL be able to change its motion from a request in words (for example slower entrances or a bounce on the title) without asking the model for a new page. It SHALL ask the language model of the routed `motion_graphics` task for the new content of the timeline block only: the request SHALL carry the page, or a reduced view of it that keeps the timeline block, the size and duration of the clip and the request; the instructions SHALL state the properties with their ranges and the curves that are accepted, SHALL require every selector to match an element of the page shown and SHALL ask for the JSON of the block and nothing else. The answer SHALL be checked as a timeline (version 1; tracks with a unique id, a target, a start of 0 or more, a duration above 0, a stagger of 0 or more when given, a known curve or four numbers, and `from` and `to` objects whose properties exist and are inside their ranges) and then written into the page, which SHALL keep every other byte as it was; the resulting page SHALL pass the same check as a new page. An answer that fails either check SHALL be requested again up to 3 times with the problems and the previous timeline, and when still failing the revision SHALL fail with an error that includes the last problem. A page without a timeline block SHALL be refused with an error that says so, without calling the model. A compression service (ctxpress) is optional: when `CTXPRESS_URL` is set the page MAY be shown to the model through it, and when it is not set, does not answer, does not compress or drops the timeline block, the model SHALL be shown the page itself.

#### Scenario: Slower entrances
- **WHEN** a stored page has a timeline, the user asks for slower entrances and the model answers with a valid timeline
- **THEN** the page has the new timeline, its text, styles and scripts are byte for byte what they were, and it passes the check of a new page

#### Scenario: Invalid timeline
- **WHEN** the model names a property or a curve that does not exist, a value out of range, a repeated id or a selector that matches no element
- **THEN** it is asked again with the problem and its previous timeline, and after 3 failures the revision fails with an error that includes the last problem

#### Scenario: A page without a timeline
- **WHEN** the page has no timeline block
- **THEN** the revision is refused with an error that says so and the model is not called

#### Scenario: Compression service not available
- **WHEN** `CTXPRESS_URL` is set and the service does not answer, or the view it returns has no timeline block
- **THEN** the model is shown the page as it is and the revision goes on

#### Scenario: Through the API
- **WHEN** `POST /api/projects/{id}/scenes/{scene}/motion/revise` is called with `{"instruction": "slower entrances"}` for a scene whose stored page has a timeline
- **THEN** the stored page is replaced by the revised one, the answer is the page as the GET gives it plus the provider and model that retimed it, a page the model had written reads as `edited` afterwards, and a page saved by hand stays `supplied`

#### Scenario: The API refuses what cannot be revised
- **WHEN** the same call is made with an empty request, for a scene with no stored page, or for a page with no timeline block
- **THEN** the answer is `400`, `404` or `422` respectively, the model is not called and the stored page is as it was

### Requirement: An overlay can be given a song map and a page that was stored for it

The page of an overlay SHALL be given the song map and the beat clock of the scene (see "A page may be given the song map and a beat clock") when the project stored a song map for that scene, and an overlay without one SHALL be drawn exactly as before, with neither. A page stored for the overlay by the project that wrote it (a music piece over a clip) SHALL be used as it is: the model SHALL NOT be asked for it and the run SHALL NOT replace it, whether or not the overlay's `text` is a description, and `text` SHALL still be required and SHALL be kept as the scene's label. The clip hash of the scene SHALL change when the stored page or the song map of its overlay changes, so a scene whose overlay changed is composited again and no other scene is.

#### Scenario: An overlay that knows the song
- **WHEN** a scene with `clip` and `overlay` has a song map stored beside its page and the page reads `window.__scene.lines`
- **THEN** the overlay is drawn with the beat clock and the song of the scene, and the word being sung changes at its own second

#### Scenario: An ordinary overlay
- **WHEN** an overlay has no stored song map
- **THEN** its page receives neither song nor beat clock, as before

#### Scenario: A stored page is not written again
- **WHEN** `motion/<scene>.overlay.html` exists and the model is unreachable
- **THEN** the run uses the page, asks no model and does not fail

#### Scenario: Only the changed overlay is composited again
- **WHEN** the stored page of the overlay of one scene changes
- **THEN** that scene's clip is built again and the others come from the cache

### Requirement: A scene can be a transparent layer

A motion scene with `layer: true` SHALL have its page drawn on a transparent background for the whole length of the scene (VP9 with alpha, or ProRes 4444 when ffmpeg lacks it, as an overlay is), given the song map and the beat clock of the scene when the project stored a song map for it, and SHALL have no clip phase of its own: its transparent video is what the scene produces. A page stored by the project for the scene SHALL be used as it is and the model SHALL NOT be asked for it. The video of a layer SHALL be cached by the content of the page, the song map, the length, the frame rate and the size, and by nothing about the track it will be laid over.

#### Scenario: A layer that knows the song
- **WHEN** a layer scene has a stored page that reads `window.__scene.lines` and a song map
- **THEN** its video is transparent where the page draws nothing and the word being sung changes at its own second

#### Scenario: The track changes
- **WHEN** a clip of the project's track is replaced and the project runs again
- **THEN** no layer scene is drawn again

#### Scenario: A stored page and no model
- **WHEN** the model is unreachable and every layer scene has its stored page
- **THEN** the run does not fail
