# music-motion-parts Specification

## Purpose
Gives the music motion catalog the interface parts, the text finishes and the mascot that the studied videos use, so the design of a song can choose a visual vocabulary as well as a palette.

## Requirements

### Requirement: Interface parts come with the catalog

The catalog SHALL include the parts `search-box`, `click-button`, `roll-counter`, `sunburst`, `achievement-toast`, `data-table`, `dependency-tree` and `password-field`. Each SHALL read the beat clock and draw from it, SHALL fit in the frame at 1280x720 and at any other size the piece has, SHALL load nothing from outside, SHALL take its colours and fonts from the design, SHALL be listed with its parameters, limits, levels and the cost of a frame measured on this machine, and SHALL give the same frames when drawn twice. A part SHALL move on the beat: the frame on a beat and the frame on the next one SHALL differ.

#### Scenario: Listed
- **WHEN** the catalog is requested
- **THEN** the eight parts are in it with their parameters, levels and cost

#### Scenario: On the beat
- **WHEN** any of the parts is drawn at 30 fps over two beats
- **THEN** the frames inside the beat and between the beats differ, and two renders are equal

#### Scenario: Passes the page checks
- **WHEN** a page is made from any of the parts
- **THEN** it passes the checks of a motion page: complete, nothing loaded from outside, no script error and its text inside the frame and apart

### Requirement: The search box types and suggests

`search-box` SHALL show a field with a magnifier in which a phrase (the `query` parameter) is typed letter by letter, and under it a list of suggestions (the `suggestions` parameter, separated by `|`) of which one more appears on every beat, the first one marked. Typing SHALL take about as long as the `type_beats` parameter says. It SHALL show the whole phrase and every suggestion by its end.

#### Scenario: Typing
- **WHEN** the part is drawn at the start and in the middle of its typing
- **THEN** the field holds the beginning of the phrase and then more of it, and no suggestion is listed before the first beat

#### Scenario: Suggestions on the beat
- **WHEN** four beats have passed
- **THEN** four suggestions are listed

### Requirement: The click button is pressed by a cursor

`click-button` SHALL show a button with a label (`label`) and a cursor that comes from the edge of the frame to the button and presses it on a beat (the `press_beat` parameter), when the button sinks for a moment and shows its second label (`pressed_label`). Before the press the cursor SHALL be seen moving; after it, the button SHALL keep the second label.

#### Scenario: Before and after the press
- **WHEN** the part is drawn before and after its press beat
- **THEN** the button shows the first label and then the second, and the cursor is on the button at the press

### Requirement: The roll counter rolls from one number to another

`roll-counter` SHALL show a counter of digits that roll vertically from a number (`from`) to another (`to`) in the beats it is given (`beats`), each digit rolling when its turn comes, with a label (`label`) beside it. With `glitch` on, when the counter reaches 0 it tears and shows a negative value for a beat. A number with fewer digits than the counter SHALL be shown with leading zeros hidden.

#### Scenario: Rolling
- **WHEN** the part is drawn at the start and at the end of its beats
- **THEN** it shows `from` and then `to`, and in between a digit that is in the middle of its roll

#### Scenario: Glitch at zero
- **WHEN** the counter reaches 0 with `glitch` on
- **THEN** the digits are torn on that beat and the value shown is negative

### Requirement: The sunburst, the toast and the panels

`sunburst` SHALL draw rays from the centre of the frame in the design's colours that turn slowly and pulse on every beat, behind the other layers. `achievement-toast` SHALL show a notice with a title (`title`) and a line (`text`) that enters from the top on a beat, shows a progress bar that fills to the `progress` percentage and leaves after a number of beats (`beats`). `data-table` SHALL show a table with the columns and rows it is given (`columns`, `rows`, separated by `|`) of which one more row enters on each beat and a row (`highlight`) is marked in the accent colour. `dependency-tree` SHALL show a list of names (`items`, with their depth given by the number of leading dashes) with a bar for each that grows to its value on the beats. `password-field` SHALL show a field in which dots appear one per beat for a password (`password`), and a bar of five steps (`strength`: 0 to 5) that fills to its value with the colour of the accent for the weak ones.

#### Scenario: A table that fills
- **WHEN** the table is drawn after one beat and after four
- **THEN** one row is shown and then four, and the highlighted row is in the accent colour

#### Scenario: The bar of a password
- **WHEN** the field is drawn at the end of its beats
- **THEN** as many dots as letters are shown and the bar has as many steps filled as `strength`

#### Scenario: A toast that leaves
- **WHEN** the toast is drawn before its first beat, in the middle and after its last
- **THEN** it is out of the frame, then in it with a bar that has filled part of the way, and then out again

### Requirement: The design chooses how the verse is marked

A design SHALL accept `verse_style`: `plain` (the default: as before), `highlight` (the word that is being sung has a background in the accent colour, with the text in the colour of the mode's background, and keeps it once sung, so the verse is marked up to the word of the moment), `karaoke` (the words already sung change to the accent colour) or `glow` (each word in the text colour with a soft glow and a halo of the same colour, with no background, appearing as it is sung). A value that is not one of them SHALL be refused, naming the field and the allowed values. A design stored without the field SHALL behave as `plain`. In `highlight`, `karaoke` and `glow` the keyword of a line SHALL still be shown in the accent colour while the line is sung.

#### Scenario: Highlight
- **WHEN** a verse of three words is drawn while the second is being sung with `verse_style: highlight`
- **THEN** the first two words are marked and the third is not

#### Scenario: Karaoke
- **WHEN** the same verse is drawn with `karaoke`
- **THEN** the first two words are in the accent colour and the third is in the text colour

#### Scenario: Glow
- **WHEN** a verse is drawn with `verse_style: glow`
- **THEN** its words have a text shadow of the text colour and no background

#### Scenario: Unknown style
- **WHEN** a design says `verse_style: wiggle`
- **THEN** it is refused naming the field and the four allowed values

### Requirement: The design chooses how the word hit looks

A design SHALL accept `hit_style`: `plain` (the default: as before), `metal` (the letters filled with a metallic gradient and a shine that crosses them while the word is up) or `glow` (the letters in the accent colour with a soft light around them). In `metal` and `glow` the letters SHALL enter one after another over the first part of the hit, not all at once. A value that is not one of them SHALL be refused, naming it, and a design stored without the field SHALL behave as `plain`. The hit SHALL stay inside the frame and be gone after its length, as before.

#### Scenario: Letters one after another
- **WHEN** a hit of five letters is drawn shortly after it starts with `hit_style: metal`
- **THEN** only some of the letters are shown, and all of them a little later

#### Scenario: Gone after its length
- **WHEN** the hit is drawn after its length with any style
- **THEN** it is not shown

#### Scenario: Unknown style
- **WHEN** a design says `hit_style: neon`
- **THEN** it is refused naming the field and the allowed values

### Requirement: The library has a mascot and a pastel background

The illustrated library SHALL include the character `mascot`, a chibi with the poses `idle`, `wave`, `point` and `cheer` and the expressions `happy`, `sad` and `surprised`, drawn with the design's colours, and the background `pastel-dots`. They SHALL be listed in the catalog like any other item and SHALL be usable in a stage as the others are.

#### Scenario: Listed
- **WHEN** the catalog is requested
- **THEN** `mascot` is among the characters with those poses and expressions and `pastel-dots` among the backgrounds

#### Scenario: On a stage
- **WHEN** a stage places the mascot with a pose and an expression on the background
- **THEN** the stage is valid and the mascot is drawn in the design's colours

### Requirement: The era parts come with the catalog

The catalog SHALL include the parts `timeline-strip`, `phosphor-terminal`, `oscilloscope`, `synth-sun`, `warp-grid`, `nebula`, `word-cloud` and `network-diagram`. Each SHALL read the beat clock and draw from it, SHALL fit in the frame at any size the piece has, SHALL load nothing from outside, SHALL take its colours and fonts from the design, SHALL give the same frames when drawn twice (what looks random comes from a seeded generator, never from the clock or `Math.random()`), SHALL be listed with its parameters, limits, levels and the cost of a frame measured on this machine, and SHALL move on the beat. A page made from any of them SHALL pass the checks of a motion page.

#### Scenario: Listed
- **WHEN** the catalog is requested
- **THEN** the eight parts are in it with their parameters, levels and cost

#### Scenario: Deterministic
- **WHEN** a part with random-looking elements (`nebula`, `word-cloud`) is drawn twice at the same time with the same seed
- **THEN** the two frames are equal, and another seed gives another frame

#### Scenario: Passes the page checks
- **WHEN** a page is made from any of the parts
- **THEN** it passes the checks of a motion page

### Requirement: The timeline strip follows the whole song

`timeline-strip` SHALL draw a bar along the top of the frame with marks and labels (`labels`, separated by `|`, spread evenly) and a cursor that moves from the first mark to the last as the song goes (the piece's start and the song's length are known to it), so that in every piece of the song the cursor is where the song is. With `progress` set (0 to 100) the cursor SHALL stay there instead. The label the cursor has passed SHALL be drawn in the accent colour. Its text SHALL stay inside the frame and apart.

#### Scenario: The cursor follows the song
- **WHEN** the strip is drawn at the middle of a song in a piece that starts at 40 % of it
- **THEN** the cursor is a little beyond 40 % of the bar

#### Scenario: A fixed progress
- **WHEN** `progress` is 25
- **THEN** the cursor is at a quarter of the bar whatever the time

### Requirement: The phosphor terminal, the oscilloscope and the sun

`phosphor-terminal` SHALL show a terminal with scan lines and a glow, in green or amber (`tone`), whose lines (`lines`, separated by `|`) are typed on the beat with a cursor. `oscilloscope` SHALL draw a Lissajous figure of `lobes` lobes that turns and pulses on every beat. `synth-sun` SHALL draw a sun with `stripes` stripes over a perspective grid whose lines move towards the viewer, at `speed`, with a pulse on every beat.

#### Scenario: Typed on the beat
- **WHEN** the terminal is drawn after one beat and after four
- **THEN** it shows more of its lines the second time

#### Scenario: Green and amber
- **WHEN** the terminal is drawn with `tone: green` and with `tone: amber`
- **THEN** the text colour is green in the first and amber in the second, whatever the design

### Requirement: The mesh, the nebula, the words and the network

`warp-grid` SHALL draw a mesh in perspective that is bent by a point of light which moves and leaves a trail, with `density` lines. `nebula` SHALL draw clouds of `amount` particles with a gradient from the accent colour, from a `seed`, that drift and pulse on the beat. `word-cloud` SHALL show the `words` (separated by `|`, up to `amount`) appearing one after another and floating, none outside the frame. `network-diagram` SHALL draw `layers` layers of `nodes` nodes joined by lines that light up from one layer to the next on the beats.

#### Scenario: Words appear
- **WHEN** the cloud is drawn after two beats and after eight
- **THEN** more words are shown the second time, and every word is inside the frame

#### Scenario: The network lights up
- **WHEN** the diagram is drawn after one beat and after `layers` beats
- **THEN** only the first layer is lit the first time and all of them the second

### Requirement: A part of the user's can be read back

`GET /api/music-motion/parts/{name}` SHALL answer, for any part of the catalog, its `name`, `source` (`user` or `builtin`), `origin` (if it has one), `manifest` (the content of `manifest.json`) and `fragment` (the content of `part.html`), so that the part can be carried to another server or drawn in a browser. A name that is not a part SHALL be `404`. A name that is not a valid part name SHALL be `404` and SHALL never read outside the catalog. The answer SHALL contain only what is in those two files, with no path of this machine.

#### Scenario: A part of the user's
- **WHEN** an approved part `my-ticker` is requested
- **THEN** the answer holds its manifest and its `part.html`, and posting them as a draft gives the same part

#### Scenario: A part that comes with the program
- **WHEN** `terminal` is requested
- **THEN** the answer is `200` with `source: builtin`, its manifest and its fragment

#### Scenario: A name that is not a name
- **WHEN** `..%2F..%2Fsettings` is requested
- **THEN** the answer is `404` and nothing outside the catalog is read

### Requirement: A part declares which parameters may animate

A manifest parameter MAY carry `animatable: true`, and the manifest of a part SHALL be read back with it. A part with an animatable parameter SHALL read the parameter from `window.__params` inside its `__partSeek`, not only while it loads. A parameter that the fragment fills in as text (`{{name}}`) SHALL NOT be `animatable`, and a manifest that says so SHALL be refused by the checks that validate manifests. Parts the assistant drafts SHALL NOT declare animatable parameters.

#### Scenario: A built-in part that follows a keyframe
- **WHEN** a built-in part with an animatable parameter is drawn at two seconds with different keyframed values
- **THEN** the two frames differ

#### Scenario: A text slot cannot move
- **WHEN** a manifest marks `animatable` a parameter its fragment uses as `{{unit}}`
- **THEN** the manifest is refused and the reason names the parameter

### Requirement: Scene parts are read back and listed on request

`GET /api/music-motion/parts/{name}` SHALL read a scene part like any part of the user's (manifest with its `scene` and `verse` fields, fragment), and
`GET /api/music-motion/catalog` SHALL list scene parts only with `include=scene` or `piece=<id>`, each with `source: "user"` and its `scene` field.

#### Scenario: Export reads it
- **WHEN** a dashboard exports a piece whose script uses `scene-p1-2`
- **THEN** `GET /parts/scene-p1-2` answers its texts and the dataset carries them

### Requirement: Fragments may use theme assets and the two frame parts exist

A fragment MAY read `window.__theme.assets[name]` and MAY carry `{{asset:name}}` slots; an unknown asset name SHALL leave the slot empty and be reported
by the part checker as a warning. `hud-chrome` and `grain` SHALL be built-in parts drawn from `t` and `beat` only.

#### Scenario: Unknown asset
- **WHEN** a fragment has `{{asset:logo}}` and the theme has no `logo`
- **THEN** the slot is empty and the draft's problems list "unknown asset logo" as a warning, not a refusal

### Requirement: A part may animate with GSAP timelines and clips

A fragment MAY register one or more paused GSAP timelines in `window.__timelines[<id>]` and MAY mark elements `class="clip"` with `data-start` and `data-duration` in seconds of the part's own time. When a fragment uses GSAP or `__timelines`, the page SHALL embed the installed GSAP core (`third_party/gsap/gsap.min.js`, fetched pinned by `scripts/get-gsap.sh`) inline before the fragment and SHALL NOT load it from the network; a page that needs it on a machine where it is not installed SHALL fail its check saying how to install it. On every frame, after `window.__partSeek(t, beat)`, the page SHALL seek every registered timeline to the part's local time without firing callbacks and SHALL show each clip only over `[start, start + duration)`. A part MAY use timelines, `__partSeek`, or both; one that uses neither SHALL be refused as today for not moving. The frames of such a part SHALL be the same on every render and in the preview the server builds. The catalog SHALL mark a part that registers timelines with `timeline: true`. The page's checks SHALL also refuse: a timeline with `repeat: -1`; a tween of `display`, `visibility` or `autoAlpha` on a `.clip`; a CSS `transform` on an element a timeline transforms; and SHALL warn on a clip that ends after the part's end.

#### Scenario: A timeline part
- **WHEN** a fragment builds `gsap.timeline({paused: true})` with a tween from 0 to 2 s and registers it, and the page is drawn at 1 s and again at 1 s
- **THEN** both frames are equal and show the tween half way

#### Scenario: A clip
- **WHEN** an element has `class="clip" data-start="1" data-duration="2"`
- **THEN** it is hidden at 0.9 s, visible at 1 s and at 2.9 s, and hidden at 3 s

#### Scenario: Both contracts
- **WHEN** a fragment registers a timeline and also defines `__partSeek` that pulses an element on `beat.position`
- **THEN** both run on every frame and the part passes the checks

#### Scenario: An endless repeat
- **WHEN** a fragment's timeline has `repeat: -1`
- **THEN** the part is refused naming the rule

#### Scenario: GSAP not installed
- **WHEN** a fragment uses `gsap` on a machine without `third_party/gsap/gsap.min.js`
- **THEN** the check fails with `bash scripts/get-gsap.sh` as the fix and the page is not drawn

### Requirement: HyperFrames blocks are imported as parts

`veritmo parts import-hyperframes <dir> [--only a,b] [--dry-run]` SHALL read the block files HyperFrames installed in `<dir>` (`compositions/<name>.html`, the composition inside `<template>`) and, for each, SHALL make a part named `hf-<name>`: the composition as the fragment, scaled from its canvas to the frame; its `data-variables` as manifest parameters (`string`, `number`, `boolean`, `select` kept; a colour variable as a string defaulting to the design's variable its name suggests; `image`, `video` and `url` variables refuse the block); its fonts kept when they are in the server's font pack and otherwise replaced by `var(--display)` or `var(--body)` by weight, the block's `@font-face` rules dropped before anything else is judged, so that a font file under `assets/` never refuses a block; the pictures the block reads from `assets/` embedded in the part as `data:` URLs: an SVG of at most 50 KB, sanitized like the theme's assets (no script, event handler, `<foreignObject>`, `@import` nor reference outside the file), or a PNG, JPEG or WebP of at most 200 KB, found relative to the block's folder and then to `<dir>`, written in place of the `src` of the `<img>` or the `url()` that named it, at most 600 KB of pictures per part and 1 MiB of `part.html`; video, audio, `<picture>` with sources, animated GIF, Lottie JSON, 3D models, any other file of `assets/`, a file missing from `<dir>` or over its limit, an `<img>` whose source is not an embedded file, `data-var-src`, `fetch`, imports, nested compositions and timing expressions refused with the line and the file. A block that embeds a raster SHALL get the warning that the picture keeps its own colours. Every candidate SHALL pass `CheckPart` like a user's part before it is written, and SHALL carry an `origin.json` naming HyperFrames, the block, its version, its licence (Apache-2.0), address and the embedded files (name, kind, bytes). The catalog SHALL list such a part with `pictures: <n>` when it embeds any. The command SHALL print, per block, written or refused and why, and SHALL write nothing with `--dry-run`. The server SHALL NOT run HyperFrames' CLI nor fetch blocks: the owner installs them with Node on their own machine. `THIRD_PARTY.md` SHALL credit HyperFrames for the imported parts.

#### Scenario: A kinetic type block
- **WHEN** a block with three string variables, a GSAP timeline and no media is imported
- **THEN** a part `hf-<name>` exists with three string parameters, passes the checks and is listed by the catalog with `timeline: true` and its origin

#### Scenario: A block with a video
- **WHEN** a block mounts a `<video>`
- **THEN** it is refused saying parts carry no media

#### Scenario: A title whose only file is its font
- **WHEN** a block's CSS has `@font-face { src: url('assets/fonts/ink.woff2') }` and reads nothing else from `assets/`
- **THEN** it is not refused for `assets/`, the `@font-face` is dropped, the family becomes `var(--display)` and the warning says so

#### Scenario: A title with an ink stroke
- **WHEN** a block has `<img src="assets/stroke.svg">` and the 12 KB file is in `<dir>/assets/`
- **THEN** the part's fragment has the `<img>` with a `data:image/svg+xml` source, the SVG is sanitized, `origin.json` lists `stroke.svg`, the catalog shows `pictures: 1`, and the page draws the same frame twice

#### Scenario: A raster background
- **WHEN** a block's CSS has `background-image: url(assets/paper.webp)` and the file weighs 140 KB
- **THEN** the `url()` is replaced by a `data:image/webp` URL and the line warns that the picture keeps its own colours

#### Scenario: Too heavy, or not there
- **WHEN** a block reads `assets/hero.png` of 900 KB, or a file that is not in `<dir>`
- **THEN** it is refused naming the file and the limit, or the file as missing

#### Scenario: Still media
- **WHEN** a block reads `assets/loop.gif`, `assets/anim.json` or `assets/model.glb`
- **THEN** it is refused saying parts carry no media nor runtimes

#### Scenario: Dry run
- **WHEN** the command runs with `--dry-run` over ten blocks
- **THEN** it prints what would pass and what would be refused and writes no part

### Requirement: The user's parts are kept in the database

The server SHALL keep each part of the user's (the scene parts written for a piece included) as a row of the database with its name, its manifest, its fragment and its origin; the parts that come with the program SHALL stay inside the program and never be rows. The catalog SHALL be the parts of the program plus the rows of the user, a row winning over a part of the program of the same name, with the same validation and the same answers as before. The folders of parts that exist SHALL be imported when the server starts (see `data-store`). A part SHALL be read by its name: reading one part, or the parts a script names, SHALL not read the other parts of the catalog.

#### Scenario: After the import

- **WHEN** the server starts with 118 folders of parts and no rows
- **THEN** `GET /api/music-motion/catalog` answers the same parts, with the same fields, as it did from the folders

#### Scenario: One part among many

- **WHEN** `GET /api/music-motion/parts/{name}` is asked on a catalog of 1000 parts of the user
- **THEN** it answers in the time it takes on a catalog of 10

#### Scenario: A part saved

- **WHEN** a part is approved, replaced or deleted
- **THEN** the next read of the catalog, by the server or by the command line, shows the change, and no file of parts is written

### Requirement: The parts can be listed in pages and created

`GET /api/music-motion/parts` SHALL answer the parts as a list that can be paged (see `api-listing`), each with `name`, `source`, `description`, `levels`, `fixed_copy` and, for a scene, `scene`; it SHALL accept the filters `source` (`user` or `builtin`), `level`, `scene` (`true`, `false`; by default scenes are left out, as in the catalog) and `piece` (the scenes of one piece), sort by `name` or `updated_at` and search `q` in the name and the description. `POST /api/music-motion/parts` with `{manifest, fragment}` SHALL create a part of the user's after the checks a draft passes before it is approved, and answer `201` with the part; it SHALL be `409` when a part of that name exists (of the user's or of the program) and `422` with the problems when it does not pass the checks, changing nothing.

#### Scenario: The scenes of a piece

- **WHEN** the list is asked with `piece=<id>`
- **THEN** only the scene parts written for that piece come back

#### Scenario: A name that exists

- **WHEN** a part is posted with the name of one of the program
- **THEN** the answer is `409` and the catalog is unchanged

#### Scenario: A part that fails the checks

- **WHEN** a part whose fragment loads an external resource is posted
- **THEN** the answer is `422` with that problem and nothing is saved

### Requirement: A part says whether it can be over a video

The manifest of a part SHALL accept `over_video` (a boolean, `false` when absent), true when the part leaves what is behind it visible: it draws no opaque background over the whole frame. The catalog SHALL list it for each part. The program's `blank` and its parts that draw only text, lines, shapes or characters on an otherwise empty frame SHALL be marked `true` once drawn on a transparent page and checked so that opaque pixels cover no more than half of the frame at the start, the middle and the end of the part; a part that is not marked SHALL NOT be the main part, an extra or a layer item of a piece that touches an item of the video track (a piece with a `background` does), and the refusal SHALL name the piece and the part. A part written by the assistant (a scene or a draft) SHALL be checked the same way and marked when it passes. The page of a piece that touches an item of the track SHALL NOT paint the design's `--bg` colour over the frame and SHALL NOT use it for the full-frame backdrop of a hit but a translucent one; the scrim belongs to the item of the track, not to the page.

#### Scenario: A part that fills the frame
- **WHEN** a piece that touches an item of the track uses a part whose manifest has no `over_video`
- **THEN** the script is refused naming the piece and the part

#### Scenario: Text on empty
- **WHEN** `blank` is drawn on a transparent page with a verse
- **THEN** the frame outside the words is transparent and `over_video` is `true`

#### Scenario: The hit over a clip
- **WHEN** a piece that touches an item of the track has a hit at full screen
- **THEN** the clip stays visible around the word, behind a translucent backdrop

### Requirement: A part says how much of each band of the frame it occupies

The manifest of a part SHALL accept `bands`, an object `{top, center, bottom}` of numbers from 0 to 1: the share of opaque pixels (alpha of 0.5 or more) in each third of the frame, counted at the first, the middle and the last frame of the part's standard check scene, drawn on a transparent page with no verse and no word hit, taking the largest share of the three frames for each band. The measure SHALL be the one that measures `over_video` (the same drawing, not a second one). The catalog SHALL list `bands` for each part that has it. The built-in parts SHALL carry it, and a draft SHALL get it, as `over_video`, when it is approved. A manifest that claims `bands` SHALL be checked against the measure and refused when a band differs from it by more than 0.1. A part with no `bands` is unknown, not empty: nothing SHALL be inferred from its absence.

#### Scenario: Flat bars at the top
- **WHEN** `flat-slabs` is drawn on a transparent page
- **THEN** its `bands.top` is at least 0.2

#### Scenario: A manifest that lies
- **WHEN** a manifest says `bands: {top: 0, center: 0, bottom: 0}` for a part that fills the top third
- **THEN** the check of the part fails naming the band

#### Scenario: A draft is measured
- **WHEN** a draft is approved
- **THEN** its manifest carries the `bands` that were measured

#### Scenario: A part with no measure
- **WHEN** a part has no `bands`
- **THEN** the catalog does not list them for it and no note is made about it
