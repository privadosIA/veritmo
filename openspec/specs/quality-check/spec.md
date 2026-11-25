# quality-check Specification

## Purpose
TBD - created by archiving change add-clip-quality-check. Update Purpose after archive.

## Requirements

### Requirement: Animated clips are checked frame by frame

The check SHALL sample the raw animation of every animated scene that has one, at 4 frames per second (configurable), looking only at the part the scene uses (up to `silence_seconds` for a silent scene, the whole clip otherwise). For each frame it SHALL measure the shift of the mean colour from the first frame and the ratio of its sharpness to the first frame's, and flag the frame as `color` when the shift is above 20 or as `blur` when sharpness has fallen by more than 30 %. A first frame with no detail (a flat colour) SHALL NOT be flagged for blur.

#### Scenario: Colour cast
- **WHEN** a clip's colour shifts strongly at 1.5 s
- **THEN** its frames from 1.5 s are flagged `color`

#### Scenario: Steady clip
- **WHEN** nothing changes in a clip
- **THEN** no frame is flagged and the scene is `ok`

### Requirement: A vision model can judge the people in each frame

When a vision model is named (`--model`, `--check-model` or `$VERITMO_VISION_MODEL`) the check SHALL send every sampled frame to it through Ollama and ask for a JSON verdict (`defect`, `severity` 0 to 10, `issue`); a frame with a defect of severity 7 or more (configurable) SHALL be flagged `vision`. Without a model the check SHALL measure only and SHALL list the vision-capable models Ollama has. A model SHALL never be chosen automatically. When the check ends it SHALL ask Ollama to unload the model.

#### Scenario: Deformed face
- **WHEN** the model answers `{"defect": true, "severity": 8}` for a frame
- **THEN** the frame is flagged `vision` with the model's issue

#### Scenario: No model named
- **WHEN** no model is named
- **THEN** only colour and sharpness are checked and the note lists the vision models available

#### Scenario: Memory freed
- **WHEN** the check ends
- **THEN** Ollama is asked to drop the model from memory

### Requirement: The verdict says how much of a clip is clean

From the flagged frames the check SHALL give each scene a verdict. A frame flagged by a measurement SHALL count on its own; a frame flagged only by the vision model SHALL count when at least two frames are flagged or its severity is 9 or more, and a single doubtful frame SHALL be ignored and noted. With no counted frame the verdict is `ok`. Otherwise the clean part is the time before the first counted frame minus one sampling step, rounded down to a tenth: when it is at least 0.8 s (configurable) the verdict is `trim` with that time, and otherwise `retry`. The reports SHALL be written to `out/<project>/quality.json`.

#### Scenario: Cut
- **WHEN** a severe frame is found at 1.5 s
- **THEN** the verdict is `trim` to 1.2 s

#### Scenario: Bad from the start
- **WHEN** every frame is flagged
- **THEN** the verdict is `retry`

#### Scenario: A doubtful frame
- **WHEN** only one frame is flagged by the vision model, with severity 7
- **THEN** the scene is `ok` and the report notes one doubtful frame

### Requirement: Fixes are edits of scenes.yaml

With `--fix` the check SHALL edit `scenes.yaml` through its YAML nodes, keeping its comments and the order of its keys and writing `scenes.yaml.bak` with the original the first time: a `trim` verdict on a silent scene SHALL set its `silence_seconds` to the clean time, and any other scene with a verdict other than `ok` (a `retry`, or a narrated scene whose clip cannot be cut) SHALL get its `seed` increased by 1000 (1000 when it had none). A fix for a scene not in the file SHALL be refused without writing anything.

#### Scenario: Cut a silent scene
- **WHEN** a silent scene of 2 s is `trim` to 1.2 s and the check runs with `--fix`
- **THEN** its `silence_seconds` is 1.2, its comments are still there and a backup exists

#### Scenario: Converges
- **WHEN** the check runs again after the cut
- **THEN** only the first 1.2 s of the clip are looked at and the scene is `ok`

#### Scenario: Narrated scene
- **WHEN** a narrated scene is `trim`
- **THEN** it gets a new seed instead, since its clip is fitted to the narration

### Requirement: Faces are judged on their own

When the vision model can locate faces, the check SHALL ask it for the faces of each frame, crop each one with a margin, enlarge it and judge the crop apart from the whole frame. The frame SHALL count as the worse of the two verdicts, and an issue found in a face SHALL say it is about a face. A failure to locate or judge faces SHALL be reported as an issue of the clip and SHALL NOT stop the check.

#### Scenario: Small deformed face
- **WHEN** the whole-frame judge sees no defect but the judge of a face crop reports a severe one
- **THEN** the frame is flagged as a vision defect whose issue starts with `face:`

#### Scenario: Answer shapes
- **WHEN** the model answers a list of faces, a bare list or a single box
- **THEN** the boxes are read in all three cases

#### Scenario: Locating fails
- **WHEN** the model cannot be asked for the faces of a frame
- **THEN** the clip's report lists the failure and the frame is still judged as a whole

### Requirement: The vision judgement uses the provider routed to the check

The vision judgement of frames and faces SHALL use the provider routed to `quality` with that provider's vision model, unless the command names another `--provider`. The check SHALL send frames of the user's clips only to a provider that is local or whose use the user acknowledged. The unloading of the model after the check SHALL be done only for a provider of type `ollama`. With the built-in Ollama routed, behaviour is as before.

#### Scenario: a routed provider judges the frames
- **WHEN** `quality` is routed to an acknowledged remote provider with a vision model
- **THEN** the frames are judged by that provider and no unload request is made

#### Scenario: no acknowledgement
- **WHEN** `quality` names a remote provider that was never acknowledged
- **THEN** the check does not start and says to accept it in Settings
