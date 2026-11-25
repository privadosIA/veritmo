# scene-import Specification

## Purpose
Converts external scene datasets — a JSON array of `[id, title, lora_info, full_prompt]` tuples, or an HTML page embedding that array in a `const data = [...]` script — into a `scenes.yaml` that passes the orchestrator's validation unmodified, reporting what was mapped and what was discarded.

## Requirements

### Requirement: The import subcommand converts a dataset without running the pipeline

The CLI SHALL accept `veritmo import <input> -o <output>` as a subcommand. It SHALL read the dataset at `<input>`, write a `scenes.yaml` to `<output>`, print a mapping report to stdout, and exit. It SHALL NOT invoke `piper`, `sd`, `ffmpeg`, or `ffprobe`, and SHALL NOT read or write `tmp/`, `out/`, or any cache manifest.

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `-o <path>` | (required) | Output `scenes.yaml` path |
| `--silence-seconds <f>` | 2.0 | `silence_seconds` value for entries without a dialogue line (must be > 0) |
| `--force-overwrite` | false | Allow overwriting an existing output file |

Invoking `orchestrator` without the `import` subcommand SHALL behave exactly as today: the subcommand dispatch MUST NOT change any existing flag, positional argument, or behaviour of the pipeline CLI.

#### Scenario: import never launches pipeline binaries

- **WHEN** `veritmo import testdata/demo_prompts.html -o /tmp/scenes.yaml` runs
- **THEN** no `piper`, `sd`, `ffmpeg`, or `ffprobe` subprocess is spawned
- **AND** the process exits 0 with the YAML written and the report on stdout

#### Scenario: existing output is not clobbered

- **WHEN** the `-o` path already exists and `--force-overwrite` is not set
- **THEN** the import fails with an error naming the path
- **AND** the existing file is unchanged

#### Scenario: pipeline invocation is unaffected by the subcommand's existence

- **WHEN** `veritmo --json scenes.yaml` runs (no `import` argument)
- **THEN** the pipeline behaves exactly as before this change

### Requirement: JSON and HTML inputs yield identical results for equivalent data

The importer SHALL accept two input forms:

- **JSON**: a file whose top-level value is an array of `[id, title, lora_info, full_prompt]` arrays of four strings.
- **HTML**: a file containing a `<script>` element that declares `const data = [...]` with that same tuple shape. The importer SHALL locate the array literal textually, resolve `${NAME}` interpolations **only** for `NAME`s bound to plain string constants (`const NAME = "..."`) declared in the same script, and then treat the result as data. It SHALL NOT evaluate any other JavaScript: function calls, computed expressions, or template placeholders not bound to a string constant cause the affected entry to be rejected with a report line naming the unresolved placeholder.

Given an HTML file and a JSON file representing the same entries, the importer SHALL produce byte-identical `scenes.yaml` output.

#### Scenario: HTML fixture and JSON fixture converge

- **WHEN** `testdata/demo_prompts.html` and `testdata/demo_prompts.json` (its JSON equivalent) are each imported with the same flags
- **THEN** the two generated `scenes.yaml` files are byte-identical

#### Scenario: NEG interpolation resolves from the same script

- **WHEN** the HTML declares `const NEG = "Negative: …"` and entries embed `${NEG}`
- **THEN** each entry's `full_prompt` is processed with `${NEG}` replaced by that string before mapping

#### Scenario: unresolvable placeholder rejects only the affected entry

- **WHEN** an entry's `full_prompt` contains `${UNKNOWN}` with no matching string constant
- **THEN** that entry is rejected and the report names `UNKNOWN` and the entry id
- **AND** the remaining entries are still imported

### Requirement: Entries map onto scenes deterministically

For each `[id, title, lora_info, full_prompt]` entry, the importer SHALL produce one scene:

- `scene.id` = `id`. Duplicate ids across entries SHALL fail the whole import with an error naming both positions (ids are cache keys and `--only` targets; silently renaming would corrupt expectations).
- A line matching `Dialogue:` or `Dialogue (<who>):` SHALL set `scene.tts_text` to the text inside its double quotes and be removed from the prompt. At most one dialogue line per entry; a second one fails the entry with a report line.
- An entry with no dialogue line SHALL become a silent scene: empty `tts_text` and `silence_seconds` set to the `--silence-seconds` value.
- A line starting with `Negative:` SHALL be removed from the prompt. If every entry carries the **same** negative text, it SHALL be emitted once as `defaults.negative_prompt` and no scene repeats it. Otherwise `defaults.negative_prompt` SHALL be left empty and each entry's own negative text SHALL go to its `scene.negative_prompt` — never both, because the pipeline concatenates `defaults.negative_prompt` after every scene's own negative at render time, so mixed placement would double-apply the shared text onto divergent scenes.
- A line starting with `Camera/style:` SHALL be removed from the prompt and NOT parsed: `motion` and `look` are left unset, and the discarded text appears in the report for that entry.
- `scene.prompt` = the remaining text, whitespace-normalized (surrounding blank lines trimmed); an empty resulting prompt fails the entry.
- A non-empty `lora_info` SHALL be discarded with a warning line in the report (LoRA is reserved, not active).

#### Scenario: fixture maps per the rules

- **WHEN** `testdata/demo_prompts.html` is imported with defaults
- **THEN** the output declares scenes `intro`, `bronze_a`, `bronze_b`, `silver_a`, `silver_b`, `outro` in dataset order
- **AND** `intro.tts_text` is `Nobody chooses the night shift at Saltmarsh Point. The night shift chooses you.`
- **AND** `bronze_b` and `outro` have empty `tts_text` and `silence_seconds: 2`
- **AND** `defaults.negative_prompt` carries the shared negative text exactly once and no scene repeats it
- **AND** no scene's `prompt` contains a `Dialogue`, `Negative:`, or `Camera/style:` line

#### Scenario: divergent negatives stay scene-local

- **WHEN** one entry's `Negative:` line differs from the text shared by the others
- **THEN** `defaults.negative_prompt` is left empty
- **AND** every entry keeps its own negative text in `scene.negative_prompt`
- **AND** no scene's effective render-time negative gains text its source entry did not carry

#### Scenario: duplicate ids abort the import

- **WHEN** two entries share the same `id`
- **THEN** the import exits non-zero naming the id and both entry positions
- **AND** no output file is written

### Requirement: The output passes the orchestrator's own validation

Before writing, the importer SHALL run the generated config through the same `Load`-equivalent structures and `Validate` logic the pipeline uses, in video mode. A generated config that fails validation SHALL abort the import, print the validation error, and write nothing. Fields the dataset cannot supply (`defaults.model`, `defaults.voice`, `defaults.resolution`, `defaults.sampler`, `defaults.steps`, `defaults.fps`, `output.file`) SHALL be emitted as commented placeholder lines, and the validation run SHALL account for their absence (structural validation of scenes: ids, prompts, dialogue/silence exclusivity), so the only edits the user ever makes by hand are filling those placeholders.

#### Scenario: generated YAML round-trips through config.Load

- **WHEN** an import succeeds and the placeholders are filled with valid paths
- **THEN** `config.Load` + `config.Validate` accept the file with no further edits

#### Scenario: invalid mapping writes nothing

- **WHEN** the mapping produces a structurally invalid config (e.g. an entry yields an empty prompt)
- **THEN** the import exits non-zero with the validation error on stderr
- **AND** the `-o` path is not created

### Requirement: The report says what happened to every entry

The importer SHALL print a per-entry report to stdout: the entry id, which fields were produced (`tts_text` or `silence_seconds`, `prompt`, negative placement), and every discarded item (camera/style text, non-empty `lora_info`, unresolved placeholders, rejected lines) with its content or reason. A summary line SHALL state totals: entries imported, silent scenes, entries rejected.

#### Scenario: report covers mapped and discarded items

- **WHEN** the fixture is imported
- **THEN** the report lists, for each entry, the mapped fields
- **AND** lists each entry's discarded `Camera/style` text
- **AND** ends with a summary of totals (6 imported, 2 silent, 0 rejected)

#### Scenario: lora_info is warned, not silently dropped

- **WHEN** an entry carries a non-empty `lora_info`
- **THEN** the report contains a warning naming the entry and stating that LoRA is not active in v1

### Requirement: Music mode turns a dataset into tracks

In music mode the importer SHALL read the same dataset shape (tuples of id, title, lora_info and full_prompt, as JSON or HTML) and SHALL produce a project with `output.mode: music` and one track per accepted entry. In an entry's `full_prompt`, the lines before the first marker SHALL form the track's style (`prompt`); a `Lyrics:` marker SHALL start the lyrics, taken from the same line and the following lines up to the next marker with their line breaks kept; `Language:`, `BPM:`, `Duration:` (seconds), `Key:`, `Time signature:`, `Seed:` and `Variants:` SHALL each set the matching track field; a line `Instrumental` or `Lyrics: [Instrumental]` SHALL make the track instrumental. Markers SHALL be matched without regard to case. The title SHALL be ignored and `lora_info` SHALL be discarded with a note.

#### Scenario: Style and lyrics
- **WHEN** an entry's prompt is a style line followed by `Lyrics:` and a few lines
- **THEN** the track has that style and those lyrics with their line breaks

#### Scenario: Metadata markers
- **WHEN** an entry has `Language: es`, `BPM: 90` and `Duration: 120`
- **THEN** the track has `vocal_language: es`, `bpm: 90` and `duration_seconds: 120`

#### Scenario: Instrumental
- **WHEN** an entry has the line `Instrumental`
- **THEN** the track is instrumental and any lyrics in the entry are discarded with a note

#### Scenario: lora_info is discarded
- **WHEN** an entry has a non-empty `lora_info`
- **THEN** it is listed as discarded, saying adapters are chosen in the studio

### Requirement: Music entries are checked one by one

An entry SHALL be rejected, with its reason, and the others imported, when its style is empty, when a numeric marker has a value that is not a number (or not positive where it must be), or when it has a second `Lyrics:` block. Duplicate entry ids SHALL abort the whole import. The generated project SHALL be validated with the rules for tracks, without requiring the music models to be installed, and nothing SHALL be written when it is invalid.

#### Scenario: Bad number rejects only its entry
- **WHEN** one entry has `BPM: fast`
- **THEN** it is reported as rejected and the other entries are imported

#### Scenario: Empty style
- **WHEN** an entry has only a `Lyrics:` block
- **THEN** it is rejected for having no style

#### Scenario: Duplicate ids
- **WHEN** two entries share an id
- **THEN** the import aborts with an error naming the id

### Requirement: The command line imports music

`veritmo import --mode music <input> -o <scenes.yaml>` SHALL run the music mode and print the same per-entry report, ending with a summary of tracks imported, instrumental and rejected. Without `--mode` the importer SHALL behave exactly as before.

#### Scenario: Music import from the command line
- **WHEN** the user runs the command with `--mode music`
- **THEN** the file written has `output.mode: music` and a track per accepted entry

#### Scenario: Default stays video
- **WHEN** no `--mode` is given
- **THEN** the video import runs unchanged

### Requirement: Scene entries can carry their settings

An entry's `full_prompt` MAY contain, one per line and case-insensitive: `Motion: <text>`, which SHALL set the scene's `motion_prompt` and `animate: true`; `Seed: <n>` (an integer >= 0) and `Variants: <n>` (an integer >= 1), which SHALL set the scene's seed and variants; `Speed: <x>` (> 0), which SHALL set the narration speed of a scene with dialogue; and `Silence: <seconds>` (> 0), which SHALL set the length of a scene without dialogue instead of the default silence. An entry with an invalid value SHALL be rejected with its reason and the others SHALL import. A `Silence:` on a scene with dialogue and a `Speed:` on a silent scene SHALL be reported as discarded. When any scene animates, the generated file SHALL carry a commented `defaults.animate` block.

#### Scenario: A moving scene
- **WHEN** an entry has the line `Motion: leaves move in the wind`
- **THEN** its scene has `animate: true` and that `motion_prompt`, and the file has the commented animate block

#### Scenario: Seed, speed and silence
- **WHEN** an entry says `Seed: 7` and `Speed: 1.2` and has dialogue, and another says `Silence: 3` and has none
- **THEN** the first scene has seed 7 and speed 1.2 and the second is silent for 3 seconds

#### Scenario: Bad value
- **WHEN** an entry says `Seed: abc`
- **THEN** it is rejected naming the value and the rest import

### Requirement: A song dataset may carry the tags of a song

In the music format of the dataset a line `Tags:` SHALL give the tags of the song (words separated by commas), and the import SHALL put them in the track's `tags` normalised. Datasets without the line SHALL import as before.

#### Scenario: Tags
- **WHEN** a song entry has `Tags: piano,happy`
- **THEN** the track has `tags: piano,happy`

#### Scenario: No tags
- **WHEN** an entry has no `Tags:` line
- **THEN** the track has no tags

### Requirement: Motion mode turns a dataset into animated graphics

In motion mode the importer SHALL read the same dataset shape (tuples of id, title, lora_info and full_prompt, as JSON or HTML) and SHALL produce a video-mode project with one `motion_graphic` scene per accepted entry. The lines of an entry's `full_prompt` before the first marker SHALL form the scene's `motion_graphic` (what the graphic shows), joined with a space. A line `Graphic: <text>` SHALL be accepted as the description too (it is what the export writes), and wins over the lines before the first marker, which are then discarded with a note. A `Dialogue:` / `Dialogue (<who>):` line SHALL set the narration text as in a scene dataset, with `Speed:`; `Duration:` (seconds, more than 0) SHALL set the `silence_seconds` of a graphic without dialogue and, with the project default (`--silence-seconds`), is what a graphic without it lasts; a `Duration:` on an entry with dialogue SHALL be reported as discarded, because the narration sets the length. Markers SHALL be matched without regard to case. The title SHALL be ignored and `lora_info` SHALL be discarded with a note. The importer SHALL NOT require Chrome or a language model, which are needed when the project runs.

#### Scenario: A graphic with a length
- **WHEN** an entry is the line `A bar chart growing from 2 to 9` and `Duration: 4`
- **THEN** its scene has that `motion_graphic` and `silence_seconds: 4`

#### Scenario: A narrated graphic
- **WHEN** an entry has a description and a `Dialogue:` line
- **THEN** the scene has the narration text and no `silence_seconds`, and a `Duration:` on it is reported as discarded

#### Scenario: Nothing needed to import
- **WHEN** the machine has neither Chrome nor a language model
- **THEN** the import still succeeds

### Requirement: Motion entries are checked one by one

In motion mode an entry SHALL be rejected, with its reason, and the others imported, when its description is empty or when a numeric marker has a value that is not a number (or not positive where it must be). Duplicate entry ids SHALL abort the whole import. The generated project SHALL be validated with the rules for scenes, and nothing SHALL be written when it is invalid.

#### Scenario: Empty description
- **WHEN** an entry has only a `Duration:` line
- **THEN** it is rejected for having no description and the others import

#### Scenario: Duplicate ids
- **WHEN** two entries share an id
- **THEN** the import aborts with an error naming the id

### Requirement: An entry may carry a title over its scene

In the scene and motion datasets, `Overlay: <text>` SHALL add an `overlay` with that text to the scene, and `Overlay position:` (`top`, `center` or `bottom`), `Overlay start:` and `Overlay end:` (seconds, 0 or more, the end after the start) SHALL set its position and range. An `Overlay position:`, `Overlay start:` or `Overlay end:` without `Overlay:` SHALL be reported as discarded, and an invalid value SHALL reject only its entry.

#### Scenario: A lower third
- **WHEN** an entry has `Overlay: Ana Ruiz, director` and `Overlay position: bottom`
- **THEN** the scene has an overlay with that text at the bottom

#### Scenario: Position without text
- **WHEN** an entry has `Overlay position: top` and no `Overlay:`
- **THEN** it is reported as discarded and the scene has no overlay

### Requirement: A scene dataset may mark a scene as an animated graphic

In the scene dataset (video and images), a line `Graphic: <text>` SHALL make the scene a `motion_graphic` described by that text, so that a project that mixes pictures and graphics travels in one dataset. The scene SHALL have no picture prompt: lines before the first marker SHALL be discarded with a note.

#### Scenario: A graphic among scenes
- **WHEN** a scene dataset has an entry with `Graphic: Opening title over blue waves`
- **THEN** that scene is a motion graphic and the others are ordinary scenes

### Requirement: The command line imports animated graphics

`veritmo import --mode motion <input> -o <scenes.yaml>` SHALL run the motion mode and print the same per-entry report, ending with a summary of graphics imported and rejected. `--mode` SHALL accept `video`, `music` and `motion`; any other value SHALL be an error naming them.

#### Scenario: Motion import from the command line
- **WHEN** the user runs the command with `--mode motion`
- **THEN** the file written has a `motion_graphic` scene per accepted entry

#### Scenario: Unknown mode
- **WHEN** the user passes `--mode audio`
- **THEN** the command fails saying the accepted values

### Requirement: The animated-graphics dataset carries the catalog parts

In an animated-graphics dataset (`MapMotion`) an entry SHALL accept the markers `Part: <name>` (a part of the catalog, or `auto`), `Param <name>: <value>` (repeatable: one parameter of the part, its value read as the type of the parameter), `Text: <text>` (the verse text), `BPM: <number>` and `Design: <words>` (words of style, from which the palette of the project is chosen; the first entry that gives them sets it for the project). The markers SHALL map to `motion_part`, `motion_params`, `motion_text`, `motion_bpm` and `defaults.subject_prompt` (the words from which the palette of the project is chosen when it has no `defaults.motion.design`), and the report SHALL say how each one was mapped. An entry with `Part:` and no `Graphic:` SHALL be valid with a named part, and one with `Part: auto` SHALL use its text as the description. A part that is not in the catalog, a parameter the part does not have or one outside its limits, or a `BPM:` outside the range SHALL be rejected for that entry with the reason, and the rest SHALL be imported.

#### Scenario: A part in a dataset
- **WHEN** an entry says `Part: counter`, `Param from: 10`, `Param to: 0` and `Duration: 6`
- **THEN** its scene has `motion_part: counter`, `motion_params: {from: 10, to: 0}` and lasts 6 s

#### Scenario: A rejected entry
- **WHEN** an entry names a part that does not exist
- **THEN** that entry is rejected with the reason and the others are imported

#### Scenario: Auto
- **WHEN** an entry says `Part: auto` and describes the graphic
- **THEN** its scene has `motion_part: auto` and `motion_graphic` with the description

### Requirement: The export writes the new markers back

Exporting the scenes of a project in the animated-graphics format SHALL write `Part:`, `Param <name>:`, `Text:`, `BPM:` and, for the project, `Design:` for the scenes that have them, so that importing the export gives the same scenes.

#### Scenario: A round trip
- **WHEN** a project with scenes made from parts is exported and imported again
- **THEN** the scenes have the same part, parameters, text and tempo

### Requirement: A scene entry may name a 3D scene

In video mode and in the animated-graphics mode a line `Blender: <name>.scene.json` in the `full_prompt` of an entry SHALL make the scene a 3D scene: it gets `blender: {build: <name>}`, with `name` a plain file name ending in `.scene.json`. The line SHALL be one of the settings of an entry (at most one per entry). The text of the entry that is not a setting SHALL be kept as the scene's prompt when there is one, and an entry with a `Blender:` line and no other text SHALL be valid. The keys the configuration refuses beside `blender` (`Motion:` and, in the animated-graphics mode, `Part:` and `Graphic:`; own pictures and clips do not come through a dataset) SHALL not be applied and the report of the entry SHALL say which were discarded. A name that is not a plain `.scene.json` file name, a second `Blender:` line or an empty one SHALL reject the entry with the reason. In the animated-graphics mode the length of the scene is the entry's `Duration:` (or the default silence), `Part:`, `Graphic:` and the text before the markers are discarded with a report line, and an entry with only a `Blender:` line is valid. The music mode SHALL leave the line as it was.

#### Scenario: A 3D scene in a storyboard
- **WHEN** an entry has `Dialogue: "Hola"` and `Blender: hola.scene.json`
- **THEN** the scene has `tts_text: Hola` and `blender: {build: hola.scene.json}`, and the report says `blender.build ← hola.scene.json`

#### Scenario: A line that is not a file name
- **WHEN** the line is `Blender: ../x.scene.json` or `Blender: x.blend`
- **THEN** the entry is rejected with a reason naming the line, and the others are imported

#### Scenario: What cannot go beside it
- **WHEN** the entry also has `Motion: slow zoom`
- **THEN** `animate` is not applied and the report lists the discarded `Motion` line

#### Scenario: A 3D graphic in a graphics dataset
- **WHEN** an entry of the animated-graphics mode has `Duration: 6` and `Blender: logo.scene.json`
- **THEN** the scene is `blender: {build: logo.scene.json}` of 6 seconds and the report says what was discarded

### Requirement: The export writes the 3D scenes back

Exporting a project in the dataset format SHALL write `Blender: <name>` for every scene with `blender.build`, with the scene's dialogue or silence and the other settings it already writes, so that importing the export with its description files gives the same scenes. A scene with `blender.file` (a `.blend`) SHALL not be written as a scene and `X-Export-Notes` SHALL say that the scene was left out because the dataset cannot carry it. `X-Export-Notes` SHALL also say, once, that the dataset keeps the line and not the description file, naming the files, which travel in the zip of the project.

#### Scenario: A round trip with the package
- **WHEN** a project with two 3D scenes is exported as a dataset and as a zip, and the dataset is imported with the descriptions found in the zip's `sources/`
- **THEN** the new project has the same two scenes with the same `blender.build`

#### Scenario: A scene from a .blend file
- **WHEN** a scene has `blender.file: set.blend`
- **THEN** the dataset has no entry for it and `X-Export-Notes` says why
