# music-motion Specification

## Purpose
Makes motion-graphics pieces that follow a song: the server reads the song into a map of beats, sections and words, proposes a design and a script from it, builds the scenes from a catalog of animated parts and renders them so that the cuts and the words land on the music. It also states the HTTP contract a dashboard uses to create, edit and follow such a piece.

## Requirements

### Requirement: A song is read into a song map

For a song of a music project (or an audio kept in a project's sources) the server SHALL build a song map with: the tempo in beats per minute, the time of each beat, the sections (the tag of the lyrics such as `[Verse]` or `[Chorus]`, with start and end in seconds), each sung word with its start and end in seconds, and the loudness of the song every 0.25 s. It SHALL reuse the lyrics, the tempo estimate, the Whisper transcription and the loudness envelope the server already has, and SHALL NOT use the network. A song with no lyrics SHALL give a map with beats and loudness and no words. The same song SHALL always give the same map. The map SHALL be stored in the project and rebuilt only on request or when the song changes.

#### Scenario: A song with lyrics
- **WHEN** a map is requested for a song with tagged lyrics
- **THEN** it has the bpm, the beats, one section per tag with its times, the sung words with their times and the loudness

#### Scenario: Instrumental
- **WHEN** the song has no lyrics and is not transcribed
- **THEN** the map has beats, bpm and loudness and an empty list of words, and no error

#### Scenario: Same song, same map
- **WHEN** the map is requested twice for the same file
- **THEN** both are equal and the second does not run the analysis again

### Requirement: A design belongs to a song and can be edited

A piece SHALL have a design: two tones (a dark and a light colour, which are the background and the text of a mode, swapped in the other), one accent (colours), a typography pair chosen from a fixed list of fonts that need nothing from the network, a `mode` (`light` or `dark`) for each section tag, and a `concept` (a text of at most 200 characters: the visual idea that every scene returns to). The assistant SHALL propose the design from the song's lyrics and style, and a change of the song SHALL NOT overwrite a design the user edited. The validation SHALL reject a colour that is not a hex colour, a mode that is neither, and an accent whose contrast with the background of any mode is below 3:1 (the text is large and bold), naming the field. Without an assistant the design SHALL be made from fixed palettes chosen by the style of the song, with no error.

#### Scenario: Proposed from the lyrics
- **WHEN** a design is requested for a song about permission dialogs
- **THEN** the answer has three colours, two typefaces, a mode per section and a concept that names the theme

#### Scenario: Low contrast accent
- **WHEN** the user sets an accent that has less than 3:1 contrast with the background of a mode
- **THEN** the validation refuses it and names the colour and the mode

#### Scenario: No assistant
- **WHEN** no model is available
- **THEN** the design comes from a fixed palette and the piece is still made

### Requirement: The parts come from a catalog

The server SHALL keep a catalog of animated parts. Each part SHALL have a name, a description, the complexity levels it supports, its parameters (name, type, default and limits) and a page fragment that animates from the song map. A script SHALL name a part and its parameters, and the page SHALL be made from the fragment and the design, not written by the model. A part named by a script that is not in the catalog, or a parameter outside its limits, SHALL be refused naming it. A new part SHALL be added by putting a folder with its manifest and its fragment in the catalog folder, without changing the server, and SHALL appear in the list after the next request.

#### Scenario: A script uses a part
- **WHEN** a script asks for the part `toggle` with its label parameter
- **THEN** the scene's page is made from that part and the design, and the model is not called to write it

#### Scenario: Unknown part
- **WHEN** a script names a part that is not in the catalog
- **THEN** it is refused and the answer names the part

#### Scenario: A part added by the user
- **WHEN** the user adds a folder with a valid manifest to the catalog folder
- **THEN** the next list of parts includes it and a script can use it

#### Scenario: A broken part
- **WHEN** a folder in the catalog has an invalid manifest
- **THEN** the part is left out of the list, the others work and the problem is reported once in the log

### Requirement: The text has two layers

A scene made for a piece SHALL show the verse that is being sung in the lower layer, with the words appearing at the time they are sung and the keyword of the line in the accent colour, and MAY show a word hit in the upper layer: one word at full screen, with the time it enters and its length. A line of more than six words SHALL be shown in groups, one group at a time: two to three words in a section of the top third of energy, three to five in the middle third, four to six in the bottom third, breaking at punctuation or at a pause of 150 ms or more between words, the keyword always inside a group; a group opens 0.2 s before its first word (never before the line starts) and is removed 0.12 s after its last word ends or when the next group opens; the group that holds the keyword carries the accent. A design MAY turn the grouping off (`verse_groups: false`). The keyword of a line and the word hit SHALL be chosen by the script and SHALL be a word of that line. The words SHALL stay inside the frame and apart from each other, as for every page: the word hit SHALL be sized to the smaller of 60 % of the frame's height and the size at which its width is 90 % of the frame's width, measured on the page with its display font loaded (the page requests the font before the first measure and measures again on every seek until the fonts report loaded, and once after), in every look of the hit and on the house page and the scene page alike, and a page whose hit leaves the frame SHALL fail the page checks as the server's fault, never the scene's. A line with no timed words SHALL appear whole when it starts.

#### Scenario: Words at their time
- **WHEN** a line is sung from 12.0 s to 14.5 s with its words timed
- **THEN** each word is shown from its own start time and none before

#### Scenario: A long line in a loud section
- **WHEN** a nine-word line is sung in the section of energy rank 1
- **THEN** it is shown in groups of two or three words, one at a time, each opening 0.2 s before its first word

#### Scenario: A short line
- **WHEN** a line has five words
- **THEN** it is shown as one line, as before

#### Scenario: A long word hit
- **WHEN** the hit is `FLIGHT` in a wide serif at 1280×720
- **THEN** it is drawn smaller than 60 % of the height so that its whole width fits inside 90 % of the frame, and no letter is cut

#### Scenario: The font arrives after the first measure
- **WHEN** the display font is not loaded when the page builds and `NIGHT` is measured in the fallback face
- **THEN** the hit is measured again once the font is in and fits inside 90 % of the frame at 0.4 s on the scene page

#### Scenario: A short word hit
- **WHEN** the hit is `go`
- **THEN** it is drawn at 60 % of the height

### Requirement: Complexity and frame rate are chosen

A piece SHALL have a `level` (`simple`, `medium` or `advanced`, default `simple`) and a frame rate of 24, 30 or 60 (default 30). A part SHALL be used only at a level it supports; at a lower level the script SHALL use the nearest part that supports it, and say so. `simple` SHALL use flat parts and cuts and entrances placed on the beat; `medium` MAY add depth, parallax and soft shadows; `advanced` MAY add 3D wireframe, glitch, particles and motion blur. Before rendering, the piece SHALL give an estimate of the render time from its length, level and frame rate and the times measured in earlier renders on this machine, and the estimate SHALL be marked as such.

#### Scenario: Default
- **WHEN** a piece is created with no level or rate
- **THEN** it is `simple` at 30 fps

#### Scenario: A part above the level
- **WHEN** a piece at `simple` has a script that uses a part that needs `advanced`
- **THEN** the nearest simpler part is used and the answer lists the replacement

#### Scenario: Estimate
- **WHEN** a 4-minute piece at `advanced` and 60 fps is planned
- **THEN** the answer includes an estimated render time, larger than the one for `simple` at 30 fps

### Requirement: Cuts and entrances land on the beat

In a piece, the cut between two scenes SHALL be placed on the nearest beat to the section or line boundary it comes from, within half a beat, and the entrances of a scene SHALL be placed on beats or on subdivisions of a beat. The renderer SHALL be given the song map and SHALL set, for each frame, the beat number, the position inside the beat (0 to 1), the section, and the written word and line being sung at that frame with the position inside each (or `null` when none is), so that a page can animate on them. The same piece SHALL always give the same frames.

#### Scenario: Cut on the beat
- **WHEN** a chorus starts at 31.4 s and the nearest beat is at 31.5 s
- **THEN** the scene of the chorus starts at 31.5 s

#### Scenario: Far from any beat
- **WHEN** a boundary has no beat within half a beat
- **THEN** it keeps its own time

#### Scenario: The word of the frame
- **WHEN** a frame falls inside the span of a sung word
- **THEN** the page receives that word, its line and the position inside each with the beat

#### Scenario: Same frames
- **WHEN** a piece is rendered twice
- **THEN** both videos contain the same frames

### Requirement: A script is proposed from the song and can be edited

From the song map, the design, the level and the catalog, the server SHALL propose a script: a list of pieces, each with its start and end, the section, the part and its parameters, the lines it covers with their keywords and, optionally, a word hit and a mode (the proposer SHALL NOT write a mode on its own: the mode comes from the design unless the assistant has a reason to set one, and the fixed rule writes none). The proposal's input SHALL carry, per section, its mean loudness and its energy rank from the map, and the device and hook of the plan when the piece has them, and its instruction SHALL ask that the cut density follow the energy (pieces of 8–15 s in the quiet sections, one piece per two to four bars in the loud ones, one held piece on a breakdown), that the chorus return to one home part named in the answer and escalate on each return, that the ending rhyme with the opening, that a piece longer than 8 s carry a change inside it (an animated parameter, an extra that enters mid-piece or a layer item) and that at most a third of the seams be hard cuts (the rest with `transition_seconds` of 0.3–0.6 s). The proposal SHALL be made by the assistant and checked against the catalog and the limits; a proposal that fails SHALL be asked again up to 3 times with the error, and without an assistant the script SHALL be made by a fixed rule: pieces whose length follows the section's energy, the same part for every chorus piece, parts chosen in turn from those the level supports for the rest. The script SHALL be stored in the project as a file the user can read and edit, a later request SHALL NOT replace it unless asked, and an edited script SHALL be validated with the same checks before it is used.

#### Scenario: Proposal
- **WHEN** a script is requested for a song with three sections
- **THEN** the answer has pieces that cover the song with no gap and no overlap and every part exists in the catalog

#### Scenario: Energy in the input
- **WHEN** the map's loudness is highest in the chorus
- **THEN** the proposal's input lists the chorus with energy rank 1 and the instruction asks for the shortest pieces there

#### Scenario: The fixed rule follows the energy
- **WHEN** the fixed rule makes a script for a song whose verse is quiet and whose chorus is loud
- **THEN** the verse pieces are longer than the chorus pieces and both chorus pieces share one part

#### Scenario: Edited by hand
- **WHEN** the user changes the part of a piece and renders
- **THEN** that piece is rendered again and the others use their cache

#### Scenario: Invalid edit
- **WHEN** the edited script has a piece that ends before it starts
- **THEN** the validation refuses it and names the piece

#### Scenario: No mode written
- **WHEN** the fixed rule makes a script for a design with `default_mode: dark`
- **THEN** no piece carries a `mode` and every page is drawn dark

### Requirement: A piece is rendered as scenes of the project

Rendering a piece SHALL build one `motion_graphic` scene per piece of the script in a project of its own, run them on the existing run queue and join them, with the song as the sound, into one video of the length of the song. Scenes whose script piece, design, level, rate and song map are unchanged SHALL come from the cache. The video and the scenes SHALL appear like any other result, and the run SHALL report progress per scene.

#### Scenario: Render
- **WHEN** a piece is rendered
- **THEN** a video as long as the song, with the song as its sound, is produced and shown in the gallery

#### Scenario: Only one piece changed
- **WHEN** one piece of the script is edited and the piece is rendered again
- **THEN** only the scene of that piece is rendered again

### Requirement: The server offers the contract to a dashboard

The server SHALL offer, behind the same authentication as the other `/api/` routes:
`GET /api/music-motion/catalog` (parts, with parameters and levels, the font stacks, and the characters of the library), `POST /api/music-motion/pieces` (create a piece from a song of a project, with an optional design, level and rate; answers `201` with its identifier), `GET /api/music-motion/pieces` and `GET /api/music-motion/pieces/{id}` (state, design, level, rate, script, song map and, when done, the address of the video), `PUT /api/music-motion/pieces/{id}/design` and `PUT /api/music-motion/pieces/{id}/script` (replace them after validating), `POST /api/music-motion/pieces/{id}/preview` and `GET /api/music-motion/pieces/{id}/frame` (see their requirements), `GET /api/music-motion/library/characters/{name}` and `GET /api/music-motion/library/backgrounds/{name}`, `POST /api/music-motion/pieces/{id}/propose` (ask for a design or a script from the assistant, with `what` equal to `design` or `script`), `POST /api/music-motion/pieces/{id}/render` (launch the render on the queue; answers `202` with the run identifier and the estimate) and `DELETE /api/music-motion/pieces/{id}`. A request the contract does not accept SHALL be refused with `422` and the reason, and nothing SHALL be created or changed. A piece SHALL NOT appear in `GET /api/projects` as a user's project. The routes SHALL be in the OpenAPI description and walked by the test that compares it with the handlers.

#### Scenario: Create and render
- **WHEN** a dashboard creates a piece from a song and then calls render
- **THEN** it gets an identifier and then a run identifier whose events it can follow, and the piece ends `done` with the address of the video

#### Scenario: Refused
- **WHEN** a piece is created with a level that does not exist
- **THEN** the answer is `422` with the reason and no piece exists

#### Scenario: Not a user's project
- **WHEN** a piece exists
- **THEN** `GET /api/projects` does not list it

#### Scenario: Documented
- **WHEN** the OpenAPI description is read
- **THEN** every route of this requirement is in it with its answers

### Requirement: A piece can carry more parts

A piece of a script MAY have `with`, a list of up to three extra parts, each with its name and parameters, that are drawn together with the piece's main part, in the order given and over it, so that a part that stays (a strip of years) can be put on the pieces whose main part changes. Each extra part SHALL be checked as the main part is (in the catalog, its parameters inside their limits) and SHALL be refused naming the piece and the part otherwise. The extra parts SHALL be given the same beat clock as the main part.

#### Scenario: A strip over a screen
- **WHEN** a piece has `part: phosphor-terminal` and `with: [{part: timeline-strip}]`
- **THEN** the page shows the terminal with the strip drawn together, both moving on the beat

#### Scenario: A part that is not there
- **WHEN** an extra part is not in the catalog
- **THEN** the script is refused naming the piece and the part

### Requirement: A piece knows its place in the song

The page of a piece SHALL be told where the piece starts in the song and how long the song is, so that a part can draw the position of the song and not only of its own scene. A scene made outside a piece of music SHALL be told them too when the project has a song (`defaults.motion.song`), and none otherwise.

#### Scenario: The position
- **WHEN** a piece that starts at 40 s of a 200 s song is drawn
- **THEN** a part can read that the piece starts at 40 s and the song lasts 200 s

### Requirement: A piece can have a design of its own

A piece of a script MAY have `design`, an object with any of the fields `dark`, `light`, `accent`, `display`, `body`, `verse_style` and `hit_style`, whose values replace those of the song's design for that piece only. The result SHALL be validated as a whole design (colours, fonts, the contrast of the accent with the background of the piece's mode), and a piece whose result is not valid SHALL be refused naming the piece and the field. A piece with no `design` SHALL be drawn with the song's design. The fixed rule SHALL NOT write a `design` of its own, and a design stored with the piece SHALL NOT change the design of the song.

#### Scenario: Another palette in one piece
- **WHEN** a piece has `design: {accent: "#ffb000", dark: "#1a1000"}`
- **THEN** that piece is drawn with that accent and that dark tone, and the next piece with the song's design

#### Scenario: A design that is not valid
- **WHEN** a piece's design gives an accent that is not a colour or has too little contrast
- **THEN** the script is refused naming the piece and `design.accent`

#### Scenario: Nothing to override
- **WHEN** a piece has no `design`
- **THEN** it is drawn exactly as before

### Requirement: A prompt steers the assistant

Making a piece and asking for a proposal SHALL accept `prompt`, free text of at most 2000 characters (`422` above that, nothing created or changed). The assistant SHALL read it as the direction for the design and for the script, in addition to the song. A prompt sent when the piece is made SHALL be kept with the piece, shown in its view, and used by later proposals that send none; a prompt sent with a proposal SHALL replace the kept one. Without a prompt, proposals SHALL be made as before.

#### Scenario: The prompt reaches the assistant
- **WHEN** a proposal is asked with a prompt
- **THEN** the text the assistant is given for the design and for the script contains the prompt

#### Scenario: Too long
- **WHEN** the prompt has 2001 characters
- **THEN** the answer is `422` and nothing is created

#### Scenario: Kept with the piece
- **WHEN** a piece is made with a prompt and a script is proposed with none
- **THEN** the assistant is given the piece's prompt

### Requirement: One proposal can write the design and the script

`propose` SHALL accept `what: "all"`, which writes the design and then the script, the script being written from the design just written, with the same prompt and model. If the design is written by the fixed rule the script SHALL still be asked of the assistant. The result SHALL say, for each part, its `source` and, when it is `fallback`, its `fallback_reason`.

#### Scenario: Design then script
- **WHEN** `what: "all"` is proposed and the assistant answers both
- **THEN** the piece holds the new design and a script made with that design, both with `source` `assistant`

### Requirement: A proposal can run in the background

`what: "all"`, and any proposal that sends `async: true`, SHALL answer `202` and work in the background, never holding the request. `GET /pieces/{id}` SHALL carry a `proposal` object with `state` (`waiting` while a run is queued or active and the assistant needs the memory, `writing`, `done` or `failed`), `what`, `model`, `design_source`, `design_fallback_reason`, `script_source`, `script_fallback_reason` and, when it fails, `error`. A proposal that waits SHALL go on when no run is in progress, for up to 15 minutes, and then fall back with the reason that the wait timed out. Asking for a second proposal of a piece while one is `waiting` or `writing` SHALL be `409`, as SHALL editing its design or script. A proposal that was running when the server stopped SHALL read `failed` with the reason. A proposal without `async` and with `what` `design` or `script` SHALL stay synchronous and SHALL fall back at once, with its reason, when a run is in progress.

#### Scenario: Polling
- **WHEN** `what: "all"` is proposed
- **THEN** the answer is `202`, and `GET /pieces/{id}` reads `writing` and then `done` with both sources

#### Scenario: Waiting for a run
- **WHEN** a run is active and a proposal is made in the background
- **THEN** its state is `waiting` until the run ends, and then it writes with the assistant

#### Scenario: Synchronous as before
- **WHEN** `what: "script"` is proposed with no `async`
- **THEN** the answer is `200` with the script, as before

### Requirement: The model is chosen and named

`propose` SHALL accept `model`, the name of an installed model of the assistant's provider; one that is not installed SHALL be `422` naming it, before anything is done. The answer, the `proposal` object and a failure SHALL name the model that was used, or the one that was asked for when it failed. Without `model` the usual routing applies.

#### Scenario: A model that is not there
- **WHEN** a proposal asks for a model that is not installed
- **THEN** the answer is `422` naming the model

#### Scenario: Named
- **WHEN** a proposal is made with an installed model
- **THEN** the answer and the `proposal` name it

### Requirement: A fallback says why

Every proposal whose `source` is `fallback` SHALL carry `fallback_reason`, one short sentence: there is no assistant or model, a run is in progress, the assistant failed with its error, the last answer was refused with the reason of the refusal, or the wait timed out. It SHALL say that the fixed rule does not read the prompt when a prompt was sent. A proposal whose `source` is `assistant` SHALL carry none.

#### Scenario: Refused three times
- **WHEN** the assistant's three answers are refused
- **THEN** `source` is `fallback` and `fallback_reason` holds the last refusal

#### Scenario: No assistant
- **WHEN** no model is available
- **THEN** `source` is `fallback` and `fallback_reason` says there is no assistant

### Requirement: The assistant is told the contrast its design is checked against

The text the assistant is given to write a design SHALL state the contrast the design needs: at least 4.5:1 between `dark` and `light`, and at least 3:1 between the accent and the background of every mode the design uses, and SHALL say that with two modes in use the accent has to pass against both tones. These statements SHALL be in the system text, before the keys of the answer. The text it is given to write a script SHALL NOT carry a block of rules about the limits of the parameters or of the keywords: stating them there lowered the acceptance of a real model, so the script learns them only from the message of a retry.

#### Scenario: Design limits in the text
- **WHEN** the assistant is asked for a design
- **THEN** its system text names 4.5:1, 3:1 and the backgrounds of every mode in use

#### Scenario: Nothing new in the script text
- **WHEN** the assistant is asked for a script
- **THEN** its system text is the one it was before this change

### Requirement: A refused answer is asked again with how to fix it

When an answer of the assistant is refused and asked again, the message SHALL say how to fix the refusal: for an accent without contrast it SHALL name both backgrounds and the contrast of the accent with each; for a parameter outside its limits it SHALL add the limits of that parameter of that part; for a keyword or hit word that is not in the line it SHALL repeat that the word must be copied from the line. When the second answer of a design is refused only for the accent's contrast, the server SHALL NOT ask a third time: it SHALL adjust the accent's lightness, keeping its hue and chroma, until it has at least 3:1 against the background of every mode the design uses, keep the design as the assistant's with a note naming the original and the adjusted accent, and fall back only when no lightness reaches the contrast. The refusals the API returns for a design or a script that a person sends SHALL not change.

#### Scenario: Contrast refusal
- **WHEN** an accent has 1.8:1 against the light background and the assistant is asked again
- **THEN** the message names the light and the dark backgrounds and the contrast of the accent with each

#### Scenario: Contrast fixed on the second refusal
- **WHEN** the assistant's second answer has `#d48806` on a light background of `#f4e4bc` (2.3:1) and nothing else wrong
- **THEN** the design is kept with a darker accent of the same hue that reaches 3:1, `design_source` is `assistant` and the note names both colours

#### Scenario: Parameter refusal
- **WHEN** a `label` of 42 characters is refused for a part that allows 20
- **THEN** the message gives the limit of that parameter of that part

#### Scenario: The API says what it said
- **WHEN** a person sends a design with a low-contrast accent
- **THEN** the `422` has the same message as before

### Requirement: The pages of a piece can be previewed without saving

`POST /api/music-motion/pieces/{id}/preview` SHALL answer the pages the renderer would draw for the piece: `width`, `height`, `fps` and a `scenes` list in script order, each with `id`, `start`, `end`, `html` (the page of the piece wrapped as the preview of a piece, with the same virtual clock and beat clock the renderer injects) and `song` (the song map cut to the piece, as the renderer gives it to the page). The body MAY carry a `design` and a `script`; those replace the saved ones for this call only, and the saved ones are used for whichever is missing. The query `?scenes=p1,p4` SHALL build only those pieces and a name that is not a piece of the script SHALL be `422`. A design or a script that is not valid SHALL be `422` with the same wording as `PUT /design` and `PUT /script`, and nothing SHALL be saved or changed in any case. The call SHALL need no Chrome, SHALL be allowed while the piece is being rendered, and the `replacements` it returns SHALL list the pieces whose part was swapped for the level, which is none when the script is drawn as saved.

#### Scenario: A saved piece
- **WHEN** the preview of a piece with a script of three pieces is requested with no body
- **THEN** the answer has three scenes whose pages are those the render writes for the piece, and the song of each is the sliced map

#### Scenario: An edit that is not saved
- **WHEN** the preview is requested with a script that changes the part of one piece
- **THEN** that scene's page draws the new part, and `GET /api/music-motion/pieces/{id}` still returns the old script

#### Scenario: An invalid edit
- **WHEN** the preview is requested with a script whose piece uses a part that does not exist
- **THEN** the answer is `422` with the same reason `PUT /script` gives, and nothing changed

#### Scenario: Some pages only
- **WHEN** `?scenes=p2` is given
- **THEN** only the page of `p2` is built and returned

### Requirement: A frame of the saved piece can be drawn

`GET /api/music-motion/pieces/{id}/frame?t=12.4` SHALL answer an `image/png` of the saved piece at that second, drawn with Chrome as the render draws it, so a dashboard can compare its own preview with the server's. A `t` outside the piece or not a number SHALL be `422`; a server with no Chrome SHALL answer `503` with the reason.

#### Scenario: A frame
- **WHEN** `t=12.4` is requested for a piece of 200 seconds
- **THEN** the answer is a PNG of the size of the piece

#### Scenario: Outside the piece
- **WHEN** `t=999` is requested for a piece of 200 seconds
- **THEN** the answer is `422` and no Chrome is started

### Requirement: The catalog gives the font stacks

`GET /api/music-motion/catalog` SHALL keep `fonts` (the names) and add `font_stacks`, a map from each of those names to the CSS stack the page sets for it, so that a dashboard can draw a page with the same fonts.

#### Scenario: The stacks
- **WHEN** the catalog is requested
- **THEN** `font_stacks` has one entry for each name in `fonts`, equal to the stack the page of a piece uses

### Requirement: The drawings of the library can be read

`GET /api/music-motion/library/characters/{name}` SHALL answer a character as the server reads it: `name`, `width`, `height`, `anchor`, `default_pose`, `default_expression`, `expressions`, `source` and `poses`, a map from each pose name to its SVG. `GET /api/music-motion/library/backgrounds/{name}` SHALL answer `name`, `source` and `svg`. A name that is not in the library or not a valid name SHALL be `404` and nothing outside the library SHALL be read. The answers SHALL hold only what is in the files, with no path of this machine.

#### Scenario: A character
- **WHEN** a character of the catalog is requested
- **THEN** every pose of the catalog is a key of `poses` with its SVG

#### Scenario: A background
- **WHEN** a background of the catalog is requested
- **THEN** the answer holds its SVG

#### Scenario: Not a name
- **WHEN** `..%2F..%2Fsettings` is requested
- **THEN** the answer is `404`

### Requirement: A parameter can change over a piece

A script piece and each of its `with` entries MAY carry `animate`, a map from a parameter name to a list of keyframes `{t, value, ease}`. `t` SHALL be in seconds from the start of the piece, `ease` one of `linear`, `in`, `out`, `in-out`, `step` (default `linear`). Between two keyframes a numeric value SHALL be interpolated with the ease of the earlier one; a value that is not a number, and any value under `step`, SHALL be held until the next keyframe. Before the first keyframe and after the last the value SHALL be the nearest keyframe's. The preview pages and the render SHALL draw the same value at the same second. A script without `animate` SHALL be drawn exactly as before.

#### Scenario: A counter climbs
- **WHEN** a piece of 8 s has `animate: {from: [{t: 0, value: 0}, {t: 4, value: 100, ease: "out"}]}` on a part whose `from` is animatable
- **THEN** the frame at 4 s and at 7 s shows 100, and the frame at 2 s shows a value between 0 and 100

#### Scenario: A held value
- **WHEN** an enum or string parameter has keyframes at 0 s and 4 s
- **THEN** it keeps the first value until 4 s and then the second, never a mixture

#### Scenario: Nothing animated
- **WHEN** no piece of the script has `animate`
- **THEN** the pages are identical to those of a server without this feature

### Requirement: Animated parameters are validated like static ones

`PUT /pieces/{id}/script` and the preview SHALL refuse with `422`, naming the piece, the part and the parameter, an `animate` on a parameter that is not `animatable`, on a parameter the part does not have, with no keyframe, with `t` outside the piece or not strictly increasing, with an unknown `ease`, or with a value the parameter would not accept as a static value. Nothing SHALL be saved on a refusal.

#### Scenario: A parameter that cannot move
- **WHEN** a script animates `unit` of `counter`, which is not animatable
- **THEN** the answer is `422` with `piece "p2" (#2): the parameter "unit" of "counter" cannot be animated` (the wording of `PUT` for any piece)

#### Scenario: A value out of range
- **WHEN** a keyframe gives `from` a value above its `max`
- **THEN** the answer is `422` naming the piece, the parameter and the keyframe, and the saved script is unchanged

### Requirement: The catalog says what the server supports

The catalog answer SHALL carry `features`, a list of names, and SHALL include `animate` while that requirement is met, `reference-breakdown` while references carry shots and a sheet and a piece can name a reference, and `word-timing` while the words of the map carry `heard`, `confidence` and `words_source`, the words can be moved by hand and the frame data names the word being sung, so a dashboard can tell before it saves a script that carries `animate`, offers a reference to a piece or offers the words editor.

#### Scenario: A dashboard asks first
- **WHEN** `GET /api/music-motion/catalog` is read
- **THEN** `features` contains `"animate"`, `"reference-breakdown"` and `"word-timing"`

### Requirement: A script can have layers over its pieces

A script MAY carry `layers`, a list of up to 4 layers `{id, items}`, each with up to 32 items `{part, params, start, end}` (`start` and `end` in song seconds). An item SHALL be drawn, in the page of every piece whose range it touches, only between its `start` and `end`, over the parts of the piece, in the order of the layers (the last on top) and under the verse and the word hit. The part of an item SHALL be told its time as seconds from the item's `start`. The preview pages and the render SHALL draw the same. A script without `layers` SHALL be drawn exactly as before.

#### Scenario: A strip that stays
- **WHEN** a layer holds `timeline-strip` from 0 s to 212 s over a script of nine pieces
- **THEN** the page of each piece draws the strip, and the frame at the first second of the second piece continues where the first piece's last frame left it

#### Scenario: Outside its range
- **WHEN** an item runs from 30 s to 60 s and a piece covers 20 s to 40 s
- **THEN** the page of that piece shows the item from 30 s on and not before

#### Scenario: No layers
- **WHEN** a script has no `layers`
- **THEN** its pages are identical to those of a server without this feature

### Requirement: Layers are validated

`PUT /pieces/{id}/script` and the preview SHALL refuse with `422`, naming the layer and the item, a part that is not in the catalog, parameters the part would not accept, a range that is empty or outside the song, items of one layer that overlap, a repeated layer id, more layers or items than the limits, and a piece whose page would draw the same part twice (as its main part, an extra or another item) or more than 8 parts. Nothing SHALL be saved on a refusal.

#### Scenario: The same part twice on a page
- **WHEN** a layer item uses `terminal` over a piece whose main part is `terminal`
- **THEN** the answer is `422` naming the piece, the part and the layer, and the saved script is unchanged

#### Scenario: Overlapping items in a lane
- **WHEN** two items of one layer overlap by one second
- **THEN** the answer is `422` naming the layer and the second item

### Requirement: The catalog and the estimate know about layers

The catalog `features` SHALL include `layers`, and the render estimate SHALL count the parts of the layers a page draws.

#### Scenario: A dashboard asks first
- **WHEN** `GET /api/music-motion/catalog` is read
- **THEN** `features` contains `"layers"`

### Requirement: The page leaves the verse to a part that owns it

When the main part of a script piece declares `verse: own`, the page of that piece SHALL carry the lines in `window.__scene.lines` and SHALL NOT draw the
verse layer; when it declares `hit: own`, the page SHALL NOT draw the hit layers. The renderer and the preview page (`PreviewHTMLSong`) SHALL behave the same.

#### Scenario: Preview and render agree
- **WHEN** a piece's page is built for the render and for the preview
- **THEN** neither has `#verse` for a script piece whose main part owns the verse

### Requirement: The page embeds the theme and the fonts

`BuildPage` and the preview page SHALL inject the design's theme CSS after the page stylesheet, expose `window.__theme`, fill `{{asset:name}}` slots and
embed the `@font-face` of the packed fonts the design names; a design without a theme and with stack fonts SHALL produce the page of today.

#### Scenario: No theme, no change
- **WHEN** a piece's design has no theme and names `grotesk` and `mono`
- **THEN** the page equals today's page but for the embedded fonts of those two names

### Requirement: A reference steers the proposal and the script

Making a piece and asking for a proposal SHALL accept `reference`, the id of a reference of the library; `422` unless it exists and is `ready`, nothing created or changed. A reference sent when the piece is made SHALL be kept with the piece, shown in its view, used by later proposals that send none, replaced by one sent with a proposal and cleared by an empty string. When the piece has a reference with a rhythm sheet, the text the assistant is given for the design and for the script SHALL contain the sheet's paragraph (and, for the script, the median shot length of each third), and the fixed rule of the script SHALL aim the length of the pieces of each third of the song at the reference's median shot length of that third, between two beats and eight beats, never splitting a line and still cutting on beats. The frames, the words and the colours of the reference SHALL NOT be given to the assistant by this requirement (the colours already reach a piece through a style). A reference without a sheet SHALL be accepted and the proposal SHALL say in its notes that the sheet is missing. Without a reference, proposals SHALL be made as before.

#### Scenario: The sheet reaches the assistant
- **WHEN** a proposal is asked for a piece whose reference has a sheet
- **THEN** the text given for the design and for the script contains the sheet's paragraph

#### Scenario: The fixed rule follows the reference
- **WHEN** the script falls back to the fixed rule for a piece whose reference cuts every 2.1 s in its middle third
- **THEN** the pieces of the song's middle third are about 2.1 s long (on beats, between two and eight beats, whole lines)

#### Scenario: Not ready
- **WHEN** a piece is made with a reference that is `analysing`
- **THEN** the answer is `422` and nothing is created

#### Scenario: Kept with the piece
- **WHEN** a piece is made with a reference and a script is proposed with none
- **THEN** the assistant is given that reference's sheet

### Requirement: The mode of a piece follows the design unless it is set

A script piece MAY carry a `mode`; when it does not, its page SHALL use the mode the design gives its section (`modes[tag]`, else `default_mode`), and every reader of a piece's mode (the page, the scene page, the scene writer's input, the estimate, the export) SHALL resolve it the same way. Saving a design whose `default_mode` or `modes` changed SHALL answer a notice counting the pieces of the script that carry a mode of their own differing from the design's, and SHALL change none of them. `PUT /api/music-motion/pieces/{id}/script` SHALL accept pieces without a mode.

#### Scenario: A dark design and a piece with no mode
- **WHEN** the design says `default_mode: dark` and a piece of the verse has no `mode`
- **THEN** its page is drawn in the dark mode

#### Scenario: The design changes under explicit modes
- **WHEN** a design is saved with `default_mode: dark` while twelve pieces carry `mode: light`
- **THEN** the answer's notice says twelve pieces carry a mode of their own that differs, and the script is unchanged

#### Scenario: Clearing the modes
- **WHEN** the script is saved again with no `mode` on any piece
- **THEN** every page follows the design

### Requirement: The fixed rule only chooses parts that show the lyric

The fixed rule of the script SHALL choose, for each piece, only from the parts that support the level, are not `manual` and are not marked `fixed_copy`. A part whose manifest carries `fixed_copy: true` (its page draws example text of its own, such as a person's name, a button label or a title that is not a parameter) SHALL NOT be chosen by the fixed rule nor used by the level substitution, and SHALL still be usable when a script names it. The catalog SHALL list `fixed_copy` for every part. When no part is left the rule SHALL fail with the level in its message, as it does without parts today.

#### Scenario: A block with example text is not chosen
- **WHEN** the fixed rule makes a script for a catalog that holds `hf-lt-dark-card` marked `fixed_copy`
- **THEN** no piece uses it

#### Scenario: A script can still name it
- **WHEN** a script names a part marked `fixed_copy` and is saved
- **THEN** it is accepted

### Requirement: The fixed rule does not repeat a part

The fixed rule SHALL NOT give the same part to two consecutive pieces, except the home part of the chorus, and SHALL take the parts in turn over every part that fits, so that a script of N pieces uses min(N, the parts that fit) different parts apart from the chorus.

#### Scenario: Consecutive pieces differ
- **WHEN** the fixed rule makes a script of 34 pieces from a catalog of 12 parts that fit
- **THEN** no two consecutive pieces have the same part unless both are chorus pieces

### Requirement: A fallback script says which pieces are placeholders

A proposal whose `source` is `fallback`, and any proposal in which the rule chose some parts, SHALL carry `fallback_pieces`, the ids of the pieces whose part the rule chose. `GET /pieces/{id}` SHALL carry it with the script, and the piece review SHALL list, in one of its first notes (after the one that names the scenes older than the last batch), the pieces that still use a part chosen by the rule instead of a scene. A script written by the assistant SHALL carry none.

#### Scenario: Pieces named
- **WHEN** the script falls back to the fixed rule and makes 34 pieces
- **THEN** `fallback_pieces` holds the 34 ids

#### Scenario: A scene replaces one
- **WHEN** the scene of piece `p3` is approved
- **THEN** `p3` is no longer in `fallback_pieces`

### Requirement: A piece is kept in the database

The server SHALL keep each piece as rows of the database: its details and, apart, its design, its script, its song map, its proposal, its scene batch and its review. The song, the kept vocal stem, the frames and what a render writes SHALL stay in the folder of the piece. The pieces that exist as files SHALL be imported when the server starts (see `data-store`) and answer as before. Saving a design, a script, the words or the result of a proposal SHALL be complete or not happen (a failure never leaves half a document), and deleting a piece SHALL delete its rows, its scene parts and its folder.

#### Scenario: After the import

- **WHEN** the server starts with pieces kept as files
- **THEN** `GET /api/music-motion/pieces/{id}` answers the same body as before for each of them

#### Scenario: A piece deleted

- **WHEN** a piece with scene parts is deleted
- **THEN** its rows, its scene parts and its folder are gone, and the parts of other pieces are untouched

### Requirement: The pieces answer quickly

`GET /api/music-motion/pieces` SHALL not read any folder of parts or walk any folder of a piece: the size of a piece SHALL be a value kept with it, measured again when a render of the piece finishes and when its storage is cleaned. With the data of 2026-11-18 (7 pieces of up to 34 script pieces, 118 parts of the user) on the machine it was measured on, the list SHALL answer in under 1 second (it took 75 s) and `GET /api/music-motion/pieces/{id}` in under 300 ms (it took 8 s). The number of database reads the view of a piece makes SHALL not grow with the number of parts in the catalog.

#### Scenario: The list

- **WHEN** the list of the 7 pieces is asked
- **THEN** it answers in under 1 second with the same bodies as before

#### Scenario: A catalog ten times larger

- **WHEN** the catalog has ten times more parts
- **THEN** the view of a piece makes the same number of database reads

### Requirement: The pieces can be paged and their details edited

`GET /api/music-motion/pieces` SHALL be a list that can be paged (see `api-listing`): sort by `created_at` (newest first by default, as today) or `title`, search `q` in the title, and filter by `client`. `PATCH /api/music-motion/pieces/{id}` SHALL change the fields sent among `title`, `client`, `prompt`, `world`, `device` and `hook`, with the limits each has when a piece is created, and answer the piece; it SHALL be `400` naming a field that cannot be changed this way, `404` for an unknown piece and `409` while a proposal of the piece is being written.

#### Scenario: A new title

- **WHEN** `{title: "Neon Pulse v2"}` is patched
- **THEN** the piece has that title and nothing else changed

#### Scenario: A field that is not editable here

- **WHEN** `{fps: 60}` is patched
- **THEN** the answer is `400` naming `fps` and nothing changes

### Requirement: Work cut by a restart is marked when the server starts

A proposal or a scene batch of a piece that was being written when the server stopped SHALL be marked as interrupted when the server starts, before it answers any request.

#### Scenario: A restart during a batch

- **WHEN** the server is stopped while a scene batch runs and started again
- **THEN** the first read of the piece shows the batch as interrupted

### Requirement: A piece of the script can be a 3D scene

A piece of the script SHALL be either made from a part of the catalog (`part`) or a 3D scene (`blender: {description}`, where `description` is the JSON of a scene of the contract of `blender-engine`), never both and never neither. A 3D piece SHALL have no `params`, `animate`, `with`, `design`, `mode`, `lines`, `hit` or `stage`, and a script that gives any of them to a 3D piece SHALL be refused naming the piece and the field. The description SHALL be checked as a hand-written one against the piece's length, with the song's tempo (a key in beats is valid), the design of the song for its colours and fonts, and no font files; saving a script (`PUT .../script`) and rendering SHALL refuse a piece whose description does not pass with `422` naming the piece and each fault by its place in the JSON, and reading the script SHALL return the description of every 3D piece. The script SHALL still cover the whole song with no gap or overlap, and a 3D piece SHALL count in it like any other.

#### Scenario: A script with a 3D chorus
- **WHEN** the script has pieces made from parts and, for the chorus, `{id: "c1", start: 30, end: 42, blender: {description: {...}}}` with a description that passes
- **THEN** the script is saved and `GET` of the piece returns the 3D piece as it was sent

#### Scenario: A part and a scene at once
- **WHEN** a piece has `part` and `blender`, or has `blender` and `lines`
- **THEN** the script is refused with `422` naming the piece and what cannot go together, and the stored script is as it was

#### Scenario: A beat in the description
- **WHEN** a description has a key at `beat: 8` and the song is 120 bpm
- **THEN** it is valid for a piece of 4 seconds or more, and a key beyond the piece's length is refused naming its place

### Requirement: A 3D piece is drawn by Blender in the piece's project

Rendering a piece SHALL write each 3D piece as a scene `blender: {build: <id>.scene.json}` of the project of the piece, with the description stored in the project's `sources/`, a length of the piece's (plus the fade into the next piece, as for any scene), the song's tempo in `defaults.motion.bpm` and the design of the song in `defaults.motion.design`; the crossfade between pieces and the song as the soundtrack SHALL be as for the other pieces. A render that has 3D pieces SHALL be refused with `503` and the way to install Blender when Blender is not usable, and one with only 3D pieces SHALL NOT need the browser. The estimate of the render SHALL count a 3D piece by its length and the frame, not by the parts it has.

#### Scenario: A mixed piece renders
- **WHEN** a piece of 60 s has 4 pieces from parts and one 3D piece of 12 s and Blender is installed
- **THEN** the render starts and its project has five scenes, one of them `blender: {build}`, with the description in `sources/` and the tempo of the song in the project's defaults

#### Scenario: No Blender
- **WHEN** the render is asked and Blender is not installed
- **THEN** it answers `503` saying how to install it, and no run starts

### Requirement: A 3D piece can be seen without rendering

`POST /api/music-motion/pieces/{id}/preview` SHALL list a 3D piece with `blender: true` and no page, and `GET /api/music-motion/pieces/{id}/frame?t=` with `t` inside a 3D piece SHALL answer a PNG of that frame drawn by Blender on the CPU (the frame of the preview of a description), `503` without Blender. The review of a piece by the vision model SHALL leave a 3D piece out and say so in its answer.

#### Scenario: A frame of a 3D piece
- **WHEN** the frame is asked at a second inside a 3D piece
- **THEN** it answers a PNG at the piece's frame, drawn at the piece's own second, and stores nothing

### Requirement: The catalog announces the 3D pieces apart from the datasets

`GET /api/music-motion/catalog` SHALL list `blender-motion` in `features` when the script accepts 3D pieces. It SHALL be a different entry from `blender`, which announces the 3D scenes of the datasets.

#### Scenario: A server with both
- **WHEN** the catalog is read
- **THEN** `features` has `blender` and `blender-motion`

### Requirement: A piece keeps 3D models

A piece SHALL keep the 3D models its 3D pieces can place, in the `sources/` of the piece's project. `PUT /api/music-motion/pieces/{id}/models/{name}` with a raw body SHALL store a binary glTF (`name` a plain name ending in `.glb`, up to 32 MiB) after reading it as `blender-engine` reads a model, answering `400` with the reason and storing nothing for a file that is not a usable model, and SHALL store a picture (`.png`, `.jpg`, `.jpeg` or `.webp`, a plain name, up to 32 MiB) as it is: it is what a model's textures are, found by name. It SHALL never replace a file of that name (`409`), and a piece with a render queued or running SHALL refuse it with `409`. `GET /api/music-motion/pieces/{id}/models` SHALL list `{models: [{name, kind, size, clips?, triangles?, needs?}]}` with `kind` `model` or `picture`, and for a model its `clips`, `triangles` and `needs` as the sources list of a project does. `DELETE /api/music-motion/pieces/{id}/models/{name}` SHALL remove a file, `404` when it is not there, and `409` naming the piece when the saved script has a 3D piece whose description names it (as `file` of a model, or as a picture that a model of the script needs). An unknown piece SHALL be `404` and the routes SHALL follow the content gate of the piece.

#### Scenario: Upload a character and its texture
- **WHEN** `character-female-a.glb` and then `colormap.png` are uploaded to a piece
- **THEN** both answer `201` and `GET …/models` lists the character with `kind: "model"`, its clips (`idle`, `sprint`, `walk`, …), its triangles and `needs: ["colormap.png"]`, and the picture with `kind: "picture"`

#### Scenario: Not a model
- **WHEN** a text file named `x.glb` is uploaded
- **THEN** it is refused with `400` saying it is not a usable model and nothing is stored

#### Scenario: A model that the script uses
- **WHEN** a 3D piece of the saved script places `character-female-a.glb` and its deletion is asked
- **THEN** it is refused with `409` naming that piece and the file is still there

#### Scenario: A render in progress
- **WHEN** a run of the piece is queued or active and a model is uploaded
- **THEN** it is refused with `409`

### Requirement: A 3D piece can place the models of its piece

The description of a 3D piece SHALL be checked with the models of its piece: an object of type `model` SHALL be accepted when its `file` is a model of the piece, every picture that file needs is a picture of the piece and its `animation.clip` is one of the file's clips, and SHALL be refused, naming its place in the JSON (`pieces[2].blender.objects[0].file`), with the same reasons and limits as in a project (`blender-engine`: a file that is not there, a picture that is missing, a clip the file does not have, more than 16 models, a file that is too heavy). A font file SHALL still be refused: a piece has no fonts of its own, it takes the design's. Saving a script (`PUT …/script`), the writer, the preview and the render SHALL all check it this way, and the render SHALL import the models from the piece's project, so a model placed in a 3D piece plays its clip for the length of the piece, moves with its keys (in seconds, or in beats counted from the start of the piece) and is hashed in the cache by its content like any model. A script that places a model the piece does not have SHALL be refused with `422` naming the piece and the file.

#### Scenario: A character runs in the chorus
- **WHEN** the piece has `character-female-a.glb` and `colormap.png`, and the chorus is a 3D piece with a `model` object that plays `sprint` and crosses the scene with two keys
- **THEN** the script is saved, the preview of the chorus shows the character in the pose its clip has at that second, and the render plays it for the whole length of the piece, on the song's tempo and design, with the song as the soundtrack

#### Scenario: A model the piece does not have
- **WHEN** a 3D piece names `skater.glb` and the piece has no such model
- **THEN** the script is refused with `422` naming the piece, `objects[0].file` and the file

#### Scenario: A clip the file does not have
- **WHEN** a 3D piece plays `run` and the file has `idle`, `sprint` and `walk`
- **THEN** the script is refused with `422` naming the place and listing the clips the file has

#### Scenario: A font file
- **WHEN** a 3D piece's text names a `.ttf` file as its `font`
- **THEN** it is refused as before: a piece has no font files

### Requirement: The catalog announces the models of pieces

`GET /api/music-motion/catalog` SHALL list `blender-models` in `features` when a piece can keep models and its 3D pieces can place them. It SHALL be a different entry from `blender-motion` (a 3D piece at all) and from `blender` (the 3D scenes of the datasets), so that a dashboard can tell a server that places models in pieces from one that draws only primitives.

#### Scenario: A server with models in pieces
- **WHEN** the catalog is read
- **THEN** `features` has `blender-motion` and `blender-models`

### Requirement: A library model can be added to a piece

`POST /api/music-motion/pieces/{id}/models` with `{from, name?}` SHALL copy a model of the library into the piece's models exactly as `POST /api/projects/{id}/sources/model` does for a project (the `.glb`, its pictures renamed `<pack>--<name>` with the addresses inside rewritten, reuse of what is already there, `409` for another file of the same name, `404` for an unknown model, `409` while a render of the piece is queued or running) and answer `201 {name, pictures}`. The copy SHALL appear in `GET /api/music-motion/pieces/{id}/models` and SHALL be placeable by the 3D pieces of the script like any model of the piece.

#### Scenario: A character from the library in a chorus
- **WHEN** a library character is added to a piece and a 3D piece of the script places it
- **THEN** the script is saved, previewed and rendered as for a model that was uploaded
