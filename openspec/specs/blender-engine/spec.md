# blender-engine Specification

## Purpose
Lets a video project use scenes rendered by Blender (headless, Cycles) from a .blend file of the project, or built from a JSON scene description (kept as a .blend): configuration, injection of texts and colours, one reserved GPU or the CPU, caching and availability. Blender is optional.

## Requirements

### Requirement: A scene can be rendered by Blender

A scene of a video project SHALL accept `blender`, with exactly one of `file` (a `.blend` file inside the project's sources) or `build` (a scene description, a `.scene.json` file inside the project's sources, given as a relative path), optional `inject` (a map of names to text, numbers or colours; only with `file`) and optional `samples`, `device` (`auto`, `gpu` or `cpu`) and `frames` (first and last frame of the animation to use; only with `file`). The scene SHALL then be made from the render instead of a generated picture. The validation SHALL reject `blender` combined with `prompt`-generated sources (`image`, `init_image`, `clip`, `animate`, `motion_graphic`, `motion_part`), in a project that is not video, with both or neither of `file` and `build`, with `inject` or `frames` alongside `build`, with a file that is missing or is not a `.blend` (for `file`) or not a `.scene.json` (for `build`), with a `file` or `build` that is an absolute path, contains `..` or resolves outside the project's sources, or with an unknown `device`. No diffusion model SHALL be loaded for such a scene.

#### Scenario: A valid scene
- **WHEN** a scene has `blender: {file: turntable.blend, inject: {title: "Hello"}}` and the file exists
- **THEN** the configuration is valid and no diffusion model is needed for that scene

#### Scenario: A built scene
- **WHEN** a scene has `blender: {build: titulo.scene.json, samples: 32}` and the description is valid
- **THEN** the configuration is valid and no diffusion model is needed for that scene

#### Scenario: Exclusive with other sources
- **WHEN** the scene also has `animate: true` or `motion_graphic`
- **THEN** the validation rejects it and names the fields

#### Scenario: Both file and build
- **WHEN** a scene has `file` and `build`, or `build` with `inject`
- **THEN** the validation rejects it and names the fields

#### Scenario: Missing file
- **WHEN** `file` or `build` does not exist in the project
- **THEN** the validation rejects it and names the file

### Requirement: The render is headless, scriptless on disk and safe

The render SHALL run Blender in the background with auto-execution of scripts of the file disabled. The values to inject SHALL travel in an environment variable as JSON and SHALL never be interpolated into the script text; a scene description SHALL be passed as the path of the file, read by the script as JSON data, and none of its values SHALL be evaluated, formatted into code or used to look up a Python name except through fixed tables of allowed values. No Python file SHALL be written or shipped: the script SHALL be passed inline. Rendering SHALL produce a PNG sequence, and ffmpeg SHALL join it into the scene's video at the project's resolution and frame rate.

#### Scenario: Awkward text is injected unchanged
- **WHEN** an injected text contains quotes, backslashes and a newline
- **THEN** the rendered text shows exactly that text and the script is unchanged

#### Scenario: Awkward text in a description
- **WHEN** a text object of a description has quotes, backslashes, a newline and `__import__('os')`
- **THEN** the rendered text shows exactly those characters and nothing is executed

#### Scenario: A file with an auto-run script
- **WHEN** the `.blend` has a script set to run on load
- **THEN** it does not run

### Requirement: The video lasts as long as the scene

The scene's video SHALL last as long as its narration, or its `silence_seconds` for a silent scene, at the project's frame rate. When the animation of the file is shorter than the scene, its last frame SHALL be held; when it is longer, the video SHALL be cut at the scene's length.

#### Scenario: Short animation
- **WHEN** a 2 s animation is used in a 5 s scene
- **THEN** the video lasts 5 s and shows the last frame for the final 3 s

### Requirement: The render uses Cycles on one reserved GPU

The render SHALL use the Cycles engine only. With `device: auto` it SHALL use OptiX on one NVIDIA GPU taken from the run's GPU list and pinned with `CUDA_VISIBLE_DEVICES`, so that every other GPU is not touched; if the run has no GPU it SHALL use the CPU. With `device: cpu` it SHALL never use a GPU, and with `device: gpu` it SHALL fail, naming the reason, when no GPU is available. The GPU SHALL be held by the render the way `sd` holds it, so two processes never share it. The Intel iGPU SHALL NOT be used.

#### Scenario: Two cards
- **WHEN** a run lists GPUs 0 and 1 and a Blender scene renders on 1
- **THEN** only GPU 1 is visible to the render and GPU 0 stays free for another scene

#### Scenario: No GPU
- **WHEN** `device: auto` and the run lists no GPU
- **THEN** the scene renders on the CPU and the log says so

#### Scenario: GPU demanded and absent
- **WHEN** `device: gpu` and no GPU is available
- **THEN** the scene fails with a message saying no GPU is available

### Requirement: The result is cached

The scene's clip SHALL be reused when the content of the `.blend` file (for `file`) or of the description resolved with the project's tempo and design colours, and the font file it uses (for `build`), the injected values, `samples`, `frames`, the resolution, the frame rate, the frame count and Blender's version are all unchanged, and SHALL be rendered again when any of them changes or, for a built scene, when its kept `.blend` is missing. The device (GPU or CPU) SHALL NOT be part of the key.

#### Scenario: Unchanged
- **WHEN** a run is repeated with nothing changed
- **THEN** Blender is not started

#### Scenario: A value changes
- **WHEN** an injected text changes
- **THEN** the scene is rendered again

#### Scenario: The design's accent changes
- **WHEN** a built scene uses `accent` and the project's design changes its accent colour
- **THEN** the scene is rendered again

#### Scenario: The tempo changes
- **WHEN** a built scene uses `beat` and the project's tempo changes
- **THEN** the scene is rendered again

#### Scenario: The design's font changes
- **WHEN** a built scene uses `font: "display"` and the design's display font changes
- **THEN** the scene is rendered again

### Requirement: Blender is optional

When Blender is not installed or is older than 4.2, the engine SHALL be reported as unavailable with the reason, and a project with a Blender scene SHALL be refused before anything runs, naming the reason and how to install Blender. Every other feature SHALL work without it.

#### Scenario: Not installed
- **WHEN** Blender is missing and a project has a `blender` scene
- **THEN** the run is refused with "Blender is not installed" and no scene is processed

#### Scenario: Not needed
- **WHEN** Blender is missing and no scene uses it
- **THEN** everything works as before

### Requirement: A scene can be built from a description

A `.scene.json` description (contract version 1) SHALL describe a world colour and strength, a camera (lens and keyframed `location` with `target` or `rotation`), up to 8 lights (`sun`, `area`, `point`, with `power`, `color`, `size` for area, and `location` with `target` or `rotation`), up to 32 named materials (`color`, `roughness`, `metallic`, `emission`), and up to 64 objects of type `text`, `cube`, `sphere`, `cylinder`, `cone`, `torus`, `plane` or `backdrop` (at most one; a floor curving into a back wall with `size` and `curve`, `curve` within [0.1, size/2]), each with an `id`, a `size`, a `material` and keyframes. A key SHALL carry exactly one of `t` (seconds from the start of the scene) or `beat` (a number ≥ 0, fractions allowed, converted to seconds with the project's tempo, `defaults.motion.bpm`), and any of `location`, `rotation` (degrees), `scale` (a number or three) and `emission`, and an optional `ease` (`linear`, `inout`, `in`, `out`, `back`, `bounce`, `elastic`, `step`; `inout` when absent) for the interval that ends at it (the ease of a channel's first key is ignored), where `step` holds the previous value until the key's frame and jumps to the key's value on it; only the channels a key gives SHALL be keyed, and a key at the scene's length (`t` equal to it, or the `beat` that falls on it) SHALL fall on the scene's last frame. A text object SHALL take `text` (up to 120 characters), `extrude`, `bevel`, `align` and an optional `font`: `display` or `body` (the design's display font at weight 700 when the pack has it, else 400, or the design's body font at 400, taken from the server's font pack and converted to a format Blender reads), or a `.ttf` or `.otf` in the sources (a relative path under the same rule as `build`); Blender's built-in font when absent. A colour SHALL be `#rrggbb` or one of `bg`, `fg`, `accent`, taken from the project's design. The server SHALL check the description before starting Blender and SHALL refuse it, naming the JSON path of the fault, when its version is not 1, it is larger than 256 KiB, a limit above is exceeded, an object has more than 400 keys, a key has both or neither of `t` and `beat`, a `beat` is used in a project without a tempo, a `t` or a converted `beat` is outside the scene's length (the message giving both the beat and its second), a coordinate is outside ±1000, an `id` is not `^[a-z][a-z0-9_-]{0,31}$` or is repeated, or a `material` or `font` named does not exist. Blender SHALL start from an empty scene, build what the description says, set the scene's frame rate to the project's, and render as a `file` scene renders. When a built scene does not give `samples`, it SHALL use the default for built scenes recorded in the docs from the measure of this change.

#### Scenario: The example
- **WHEN** the project `testdata/blender-build` is rendered at 1280×720 and 30 fps
- **THEN** the clip lasts 5 s, the title in the design's display font rises from below with an overshoot by beat 1.2, the camera moves toward it, there is no horizon line behind it, and the accent sphere is brightest on the frame of each beat from 1 to 9 and not on the frame before

#### Scenario: A hit does not anticipate
- **WHEN** an object has a key `{beat: 4, emission: 4, ease: "step"}` after a key with emission 0, at 120 bpm and 30 fps
- **THEN** its emission is 0 on frame 60 and 4 on frame 61

#### Scenario: Beats without a tempo
- **WHEN** a description uses `beat` in a project with no `defaults.motion.bpm`
- **THEN** the scene is refused before Blender starts and the message names the first key that uses it

#### Scenario: A beat beyond the scene
- **WHEN** a key has `beat: 12` at 120 bpm in a 5 s scene
- **THEN** the scene is refused and the message names the key, beat 12 and 6 s

#### Scenario: A key at the end
- **WHEN** a key has `t: 5` (or `beat: 10` at 120 bpm) in a 5 s scene at 30 fps (150 frames)
- **THEN** the scene is accepted and that key lands on frame 150

#### Scenario: A path outside the sources
- **WHEN** `build` or a text object's `font` is `../x.scene.json`, an absolute path, or a symbolic link to a file outside the sources
- **THEN** the scene is refused before Blender starts and the message names the field

#### Scenario: A key out of the scene
- **WHEN** a key has `t: 7` in a 5 s scene
- **THEN** the scene is refused before Blender starts and the message names `objects[0].keys[3].t`

#### Scenario: An unknown material
- **WHEN** an object names a material the description does not define
- **THEN** the scene is refused naming the object and the material

#### Scenario: The design's palette
- **WHEN** the project's design has accent `#ff3b30` and a material's colour is `accent`
- **THEN** that material renders in that colour

#### Scenario: The design's font
- **WHEN** the design's display font is Playfair Display and a text has `font: "display"`
- **THEN** the text renders in Playfair Display 700 and the kept `.blend` holds that font packed

### Requirement: The built scene is kept as a .blend

Before rendering, a built scene SHALL be saved, with its fonts packed, as `<scene-id>.blend` next to the scene's clip in the project's output, and SHALL be downloadable through the project's files. The file SHALL open in Blender 5.3 or newer (the version that saves it) on another machine with the same objects, materials, camera, lights and keyframes, and SHALL carry no device preference of this machine. A render that fails after the scene was built SHALL still leave the `.blend`.

#### Scenario: Open it elsewhere
- **WHEN** the `.blend` of the example is downloaded and opened on a Mac
- **THEN** it shows the same scene and plays the same animation as the clip

#### Scenario: A failed render
- **WHEN** the render of a built scene fails after the build
- **THEN** the `.blend` is there and the error says the render failed

### Requirement: A description can place a model from a file

An object of a description MAY have `type: "model"`, which places a 3D model of the project's `sources/` in the scene. It SHALL carry `file` (a plain name of a `.glb` of `sources/`), `size` (the height of the model in metres: its bounding box along Z in the pose it is saved in, after the file's own units), and MAY carry `material` (a material of the description that replaces every material of the model, so that a colour or an `emission` key applies to it) and `animation`. `animation` SHALL be `{clip, speed?, loop?, offset?}`: `clip` the name of one of the clips of the file, `speed` a number from 0.1 to 4 (1 by default) that multiplies how fast the clip plays, `loop` a boolean (true by default) that repeats the clip for the whole scene, and `offset` a number of seconds (0 by default) that delays the clip, so that a looped clip is out of phase with another and a clip that does not loop holds its first pose until then. A clip SHALL keep its own duration in seconds. A model without `animation` SHALL stay in the pose it is saved in. The `keys` of a model (`location`, `rotation`, `scale`, `emission`, in seconds or in beats, with the same eases) SHALL move the model as a whole while its clip plays; the model's origin (usually its feet) is the point `location` places, and at `rotation` `[0, 0, 0]` it faces the negative Y axis. `file` and `animation` SHALL only be for objects of type `model`, and `text`, `extrude`, `bevel`, `align`, `font` and `curve` SHALL NOT be given to one. The contract version stays 1.

#### Scenario: A character runs on the rink
- **WHEN** a description has the object `{id: "ana", type: "model", file: "character-female-a.glb", size: 1.7, animation: {clip: "sprint"}, keys: [{t: 0, location: [-4, 0, 0]}, {t: 5, location: [4, 0, 0]}]}`
- **THEN** the clip shows a character 1.7 m tall whose legs and arms move for the 5 s while she crosses the scene, and the sprint cycle lasts as long in seconds as it does in the file at any frame rate

#### Scenario: Two characters out of phase
- **WHEN** two models use the same file and clip, one with `offset: 0.2`
- **THEN** on every frame their poses differ, and both loop for the whole scene

#### Scenario: A clip that does not loop
- **WHEN** a model has `animation: {clip: "jump", loop: false, offset: 2}`
- **THEN** it keeps the first pose of the clip until second 2, plays the clip once, and keeps its last pose until the end

#### Scenario: A flat colour
- **WHEN** a model names `material: "m"` and a key gives `emission: 4`
- **THEN** every part of the model renders with that material and the emission follows the key

#### Scenario: A field that is not for a model
- **WHEN** an object of type `model` has `text` or `font`, or an object of another type has `file` or `animation`
- **THEN** the description is refused naming the object and the field

### Requirement: A model is checked before Blender starts

The checks of a description SHALL read each model file as data and refuse, naming the object and the field by their place in the JSON (`objects[2].file`), without starting Blender: a `file` that is not a plain name of a `.glb` of `sources/` (a path, `..`, a link that leads out of the folder, another extension), a file that does not exist, a file that is not a binary glTF (magic, version 2, a readable JSON chunk), a file whose buffers are not inside it, a file whose image is neither embedded nor a relative path (no scheme, not absolute, no `..` segment) whose last name is a picture of `sources/` (`Textures/colormap.png` needs `colormap.png`), a picture that a file needs and the sources do not have, a file larger than 32 MiB or with more than 500,000 triangles, an `animation` on a file with no clips, a `clip` the file does not have (the message SHALL list the clips it has), a `speed` or an `offset` out of range (an `offset` beyond the length of the scene included), and more than 16 `model` objects in a scene. A scene described with no project (the one-call generation, the writer and the preview with no project, a piece of a music motion script) has no `sources/`, so a `model` there SHALL be refused saying that models are files of a project's sources.

#### Scenario: A clip the file does not have
- **WHEN** a model asks for `clip: "run"` and the file has `idle`, `walk` and `sprint`
- **THEN** the scene is refused before Blender starts with `objects[0].animation.clip: "run" is not in character-female-a.glb (clips: idle, sprint, walk)`

#### Scenario: A file that is not there
- **WHEN** `file` names a `.glb` that is not in `sources/`
- **THEN** the scene is refused naming `objects[0].file`

#### Scenario: A file that is not a glb
- **WHEN** the file named is a renamed image, or a `.glb` cut in half
- **THEN** the scene is refused naming the file and saying it is not a valid binary glTF

#### Scenario: A file that points elsewhere
- **WHEN** the `.glb` has an image whose `uri` is `../../secret.png`, `/etc/passwd` or `http://x/y.png`, or a buffer that is another file
- **THEN** the scene is refused and nothing outside the sources is read

#### Scenario: A picture the model needs
- **WHEN** the `.glb` has an image `Textures/colormap.png` and `sources/` has `colormap.png`
- **THEN** the scene is accepted and the model renders with that picture as its texture

#### Scenario: A picture that is missing
- **WHEN** the same file is used and `sources/` has no `colormap.png`
- **THEN** the scene is refused with `objects[0].file: character-female-a.glb needs the picture colormap.png, which is not in the sources`

#### Scenario: Too many models
- **WHEN** a description has 17 objects of type `model`
- **THEN** it is refused saying at most 16

#### Scenario: A model without a project
- **WHEN** a description with a `model` is checked in the one-call generation or in a music motion piece
- **THEN** it is refused saying that models are files of a project's sources

### Requirement: A model is imported without running anything and scaled to its size

The build SHALL import each `model` with Blender's glTF importer, from a copy of the file placed with the pictures it needs where the file expects them (so a picture of `sources/` is found whatever folder the file names), with the scene's frame rate already set so that a clip keeps its duration in seconds, with the auto-execution of scripts still disabled and no file written, put it under an object named by its `id` that carries its `keys`, and scale it so that its height is `size`. A clip SHALL be assigned to the model's armature and retimed by `speed` and `offset`, and SHALL repeat for the whole scene when `loop` is true. A model that Blender fails to import SHALL fail the run naming the object and the file. The kept `.blend` of the scene SHALL hold the model's meshes, rig, textures and clips packed, so that it opens on another machine with no access to the sources.

#### Scenario: The size
- **WHEN** a model of 2.0 m in its file has `size: 1.0`
- **THEN** it renders half as tall, and a `scale` key multiplies that size

#### Scenario: The kept scene
- **WHEN** a scene with a model has rendered and its `.blend` is opened in a clean Blender with the sources absent
- **THEN** the model is there with its textures and plays its clip

#### Scenario: A model that cannot be imported
- **WHEN** the file passes the checks but Blender refuses it
- **THEN** the run fails with a message that names the object and the file, and the `.blend` of the scene is not left half built

### Requirement: The content of a model is part of the scene's identity

The cache key of a built scene SHALL include the content of every model file it uses and of every picture those files need (not their names or dates), so that a scene is rendered again when a model file is replaced by another with the same name, and SHALL NOT change when a file of `sources/` that no object names changes. A description that cannot be read or resolved SHALL still get a key.

#### Scenario: The same name, another file
- **WHEN** `character-female-a.glb` is replaced by another file with that name and the scene is run again
- **THEN** the scene is rendered again

#### Scenario: Its texture changes
- **WHEN** `colormap.png` is replaced by another picture and the scene is run again
- **THEN** the scene is rendered again

#### Scenario: A file the scene does not use
- **WHEN** another `.glb` is added to `sources/` and the scene is run again
- **THEN** the scene's clip is reused

### Requirement: A model can be sized by its largest dimension

An object of type `model` MAY carry `fit`, which says which dimension of the model its `size` is: `"height"` (its bounding box along Z in the pose it is saved in, the default and what `size` has always meant) or `"largest"` (the longest of the three dimensions of that bounding box). The model SHALL be scaled uniformly so that the chosen dimension equals `size`, and the helper objects the importer adds SHALL NOT count in the measure, as they do not for the height. A description with no `fit` SHALL mean `"height"`. `fit` on an object that is not a model, or any value other than `height` and `largest`, SHALL be refused before Blender starts, naming the object and the field. A `scale` key SHALL still multiply the result. The contract stays at version 1.

#### Scenario: A pizza
- **WHEN** a pizza that is 3 cm tall and 30 cm wide is placed with `size: 0.3` and `fit: "largest"`
- **THEN** it is 30 cm wide, and with no `fit` the same `size` makes it 10 times larger

#### Scenario: A character is unchanged
- **WHEN** a character is placed with `size: 1.7` and no `fit`
- **THEN** it is 1.7 m tall, as before

#### Scenario: A bad fit
- **WHEN** a model has `fit: "width"`, or a cube has `fit: "largest"`
- **THEN** the description is refused naming the object and `fit`
