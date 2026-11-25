# describe-reference Specification

## Purpose
Reads a picture or clip the user uploaded with a local vision model (Ollama) and writes the prompt that would draw it, so a scene can be recreated from a reference. The file is only read, never handed to a generator; every person is age-checked first, and a model's refusal is reported, never replaced by an invented prompt.

## Requirements

### Requirement: An uploaded image or video is described as a prompt

The describe step SHALL accept one image (PNG, JPEG or WebP) or one video, send it to the chosen local vision model through Ollama, and return one English prompt of about 70 to 120 words covering the subjects (apparent age range, body, hair, clothing or its absence), pose, expression, setting, light, camera framing and style. In the private edition the text SHALL be described literally and the instructions given to the model SHALL NOT ask it to omit, soften or censor visible content; in the public edition the instructions SHALL describe only the format and SHALL NOT say to describe literally or to omit nothing (see `editions`). The returned text SHALL be cleaned like the text assistant's (no reasoning blocks, no wrapping quotes, single paragraph). An empty result SHALL be an error, never an empty prompt.

#### Scenario: image to prompt
- **WHEN** the user uploads a PNG of two people on a bench and runs describe
- **THEN** the response contains one English prompt naming the subjects, clothing, setting, light and framing

#### Scenario: nothing is softened
- **WHEN** the image shows adults in lingerie (private edition)
- **THEN** the request's instructions do not tell the model to leave out or soften clothing or body

#### Scenario: the public edition does not ask for literalness
- **WHEN** the build is public
- **THEN** the request's instructions carry no sentence about describing literally or about not censoring

#### Scenario: empty answer
- **WHEN** the cleaned answer is empty
- **THEN** describe fails with an error saying the model returned nothing

### Requirement: A video is described from key frames

For a video the step SHALL extract between 4 and 8 evenly spaced frames with ffmpeg, describe each one, and merge the results into one image prompt for the scene plus one motion prompt of at most about 25 words for the animation. The video's length SHALL be limited (default 5 minutes) and a longer file SHALL be refused with a message that names the limit. Extracted frames SHALL be deleted when the step ends.

#### Scenario: key frames
- **WHEN** a 20 s video is described
- **THEN** between 4 and 8 frames are described and the response holds one image prompt and one motion prompt

#### Scenario: too long
- **WHEN** a 40 minute video is uploaded
- **THEN** describe is refused with an error naming the length limit and no frame is extracted

#### Scenario: temporary frames removed
- **WHEN** describe ends, whether it succeeded or failed
- **THEN** no extracted frame remains on disk

### Requirement: The descriptor model is chosen automatically and recorded

The default descriptor SHALL be `gemma4:26b`. The options SHALL list the vision-capable models installed in Ollama, and the user MAY pick any of them. A model that is not installed SHALL give an error naming it and `ollama pull <model>`; the application SHALL NOT download models. When the default is not installed, the first installed vision model SHALL be used and shown. The response SHALL carry the model used, and the scene's generation metadata SHALL record it as `described_by` together with whether a reference was used (never the file's content).

#### Scenario: default descriptor
- **WHEN** describe runs without a model choice and `gemma4:26b` is installed
- **THEN** that model is used and the response names it

#### Scenario: default missing
- **WHEN** `gemma4:26b` is not installed but `qwen3.8:27b` is a vision model
- **THEN** `qwen3.8:27b` is used and the response names it

#### Scenario: model not installed
- **WHEN** the user picks a model that is not in Ollama's list
- **THEN** describe fails with an error naming the model and `ollama pull`, and sends no request

#### Scenario: metadata
- **WHEN** a scene is generated from a described prompt
- **THEN** its generation metadata holds `described_by` with the model name

### Requirement: A refusal is reported and never replaced by an invented prompt

When the model's answer is a refusal (as `IsRefusal` decides) or does not describe the image, describe SHALL return a `describe_refused` error that names the model. In the private edition the error also lists the other installed vision models, so the user can retry with one of them; in the public edition it lists nothing and suggests nothing (see `editions`). It SHALL NOT fall back to a generic or made-up prompt, and the application SHALL NOT add any content filter of its own.

#### Scenario: model refuses
- **WHEN** the model answers "I'm sorry, I can't help with that"
- **THEN** describe returns `describe_refused` with the model's name and the alternatives
- **AND** the scene prompt is left unchanged

#### Scenario: model refuses in the public edition
- **WHEN** the same answer arrives in a public build
- **THEN** describe returns `describe_refused` with the model's name and no alternatives

#### Scenario: retry with another model
- **WHEN** the user retries after a refusal with another installed vision model
- **THEN** that model's description is returned if it is not a refusal

### Requirement: Minors are never described

Before returning a prompt the step SHALL ask the model whether any person in the image or frame is, or looks like, a minor (under 18), whether the image is a photograph, an illustration or anime. If the answer is yes, or the model cannot rule it out for a person whose age is unclear, describe SHALL return a `describe_minor` error, SHALL NOT return any description, and SHALL NOT keep the uploaded file. For a video, one such frame SHALL be enough to refuse the whole video. The generated prompts SHALL state an adult age (18 or older) for every person described.

#### Scenario: apparent minor
- **WHEN** the check finds a person who looks under 18
- **THEN** describe returns `describe_minor`, no prompt, and the upload is deleted

#### Scenario: one frame of a video
- **WHEN** one of six extracted frames shows an apparent minor
- **THEN** the whole video is refused with `describe_minor`

#### Scenario: drawn characters
- **WHEN** the image is an anime illustration of a character who looks under 18
- **THEN** describe returns `describe_minor`

#### Scenario: adults only in the prompt
- **WHEN** a prompt is returned
- **THEN** every person in it has an adult age range (18 or more)

### Requirement: The reference is read only to write text

The uploaded file SHALL be used only as input to the vision model. It SHALL NOT be passed to any generator as an init image, control image, adapter or face reference, and the scene generated from the described prompt SHALL be a plain text-to-image or text-to-video request. The file SHALL be stored in the project's `sources/` folder (uploaded with the existing sources endpoints), never sent outside the machine, and SHALL be deleted by the editor as soon as the user uses or discards the proposal (it is kept only while a refusal can be retried with another model); deleting the project deletes it.

#### Scenario: text only
- **WHEN** a scene is generated from a described prompt
- **THEN** the generator request carries no init image, control image or reference image

#### Scenario: stays local
- **WHEN** describe runs
- **THEN** the only network destination is the configured local Ollama address

#### Scenario: the reference does not stay
- **WHEN** the user uses or discards the proposal
- **THEN** the file is removed from the project's sources

### Requirement: The describe step needs free memory and says so

Before loading the model the step SHALL check that Ollama is reachable and that the machine has enough free memory: 4 GiB when the model runs on a GPU (its weights go to VRAM), and the model's size times 1.3 plus 4 GiB when it is kept on the CPU (`VERITMO_DESCRIBE_CPU=1`). The server SHALL refuse the step while a pipeline run is queued or active, with a message saying to wait for it. After the answer, the model SHALL be unloaded (`keep_alive: 0`) whether it succeeded or failed.

#### Scenario: render in progress
- **WHEN** a run is queued or active and the user runs describe
- **THEN** describe is refused with a message saying to wait for the run or stop it

#### Scenario: memory rule by device
- **WHEN** 8 GiB are free and the model is 10 GiB
- **THEN** a GPU run is allowed and a CPU run is refused for lack of memory

#### Scenario: unloaded afterwards
- **WHEN** describe succeeds or fails
- **THEN** an unload request for the model has been sent to Ollama

### Requirement: Describe is available from the API, the CLI and the editor

The server SHALL expose `POST /api/describe` (body: `project`, `source` — the name of a picture or clip in the project's `sources/` — optional `model`, optional `detail`) and `GET /api/describe/models` (the installed vision models, the default and the detailed one), both behind the usual token rules and documented in `openapi.yaml`. A scene SHALL be able to record the model that wrote its prompt in an optional `described_by` field. The CLI SHALL offer `veritmo describe [--model name] [--detail] <file>` printing the prompt on stdout (a second line `MOTION: …` for a clip) and the model used on stderr. The scene editor SHALL offer "Describe a reference" under the prompt of a generated scene: one file picker, then a proposal with the descriptor shown beside it, and on "Use" the prompt field is filled and stays editable (and `described_by` is recorded).

#### Scenario: API flow
- **WHEN** a client uploads a file to the project's sources and calls describe with its name
- **THEN** the response has `prompt`, `motion` (video only) and `model`

#### Scenario: invalid request
- **WHEN** the source is not a plain picture or clip name of that project
- **THEN** describe answers 400 or 404 and no model is called

#### Scenario: CLI
- **WHEN** the user runs `veritmo describe photo.png`
- **THEN** the prompt is printed on stdout and nothing else

#### Scenario: recorded model
- **WHEN** a scene saved with `described_by: gemma4:26b` is read back
- **THEN** the field is kept

#### Scenario: editor
- **WHEN** the user picks a file in "Describe a reference"
- **THEN** a proposal with the prompt and the descriptor model appears, and "Use" fills the editable prompt field

### Requirement: A clip can be split into one scene per shot

When asked to split (`split: true`), describe SHALL find the cuts of a clip with ffmpeg's scene-change filter (threshold 0.3), merge every shot shorter than 1.5 seconds into the previous one, and keep at most 12 shots: when there are more, it SHALL retry with the thresholds 0.4 and 0.5 and, if still above the limit, merge the shortest shots into their neighbours until 12 remain. For each shot it SHALL take the frame at the middle of the shot, run the age check on it and describe it with the same instructions as a single picture, and write a movement prompt of at most about 25 words from that description. The answer SHALL carry `shots`, a list of `{start, end, prompt, motion?}` in seconds and playing order, and the model used. A clip with no cut SHALL give one shot covering the whole clip. A picture ignores the option and is described as one scene.

#### Scenario: a clip with three shots
- **WHEN** a 12 s clip with cuts at 4 s and 8 s is described with `split: true`
- **THEN** the answer has three shots (0-4, 4-8, 8-12), each with its own prompt

#### Scenario: a very short shot is merged
- **WHEN** a cut leaves a shot of 0.8 s
- **THEN** that shot is merged into the previous one and no scene is made for it

#### Scenario: too many shots
- **WHEN** the clip has 30 cuts even at threshold 0.5
- **THEN** the answer has 12 shots, made by merging the shortest ones

#### Scenario: no cut
- **WHEN** the clip has no detectable cut
- **THEN** the answer has one shot covering the whole clip

#### Scenario: a picture
- **WHEN** a picture is described with `split: true`
- **THEN** it is described as one scene

### Requirement: Splitting keeps every safeguard of describing

In split mode the age check SHALL run on the representative frame of every shot before any description is returned, and one frame showing, or looking like, a minor, or whose age cannot be confirmed, SHALL refuse the whole clip with `describe_minor` and delete the file. A refusal by the model SHALL be reported as `describe_refused` with the alternatives and no shots. The model SHALL be unloaded and the extracted frames removed when the work ends, and the prompts SHALL give adult ages only.

#### Scenario: one minor shot refuses everything
- **WHEN** the age check finds a minor in the frame of the third shot
- **THEN** describe answers `describe_minor`, returns no shots and the clip is deleted

#### Scenario: refusal
- **WHEN** the model refuses to describe the second shot
- **THEN** describe answers `describe_refused` with the alternatives and no shots

### Requirement: The editor offers one scene or several, and creates them only when used

The "Describe a reference" control SHALL offer **One scene** (the default) and **Several scenes (one per shot)**. With several, the proposal SHALL list one entry per shot with its time range, an editable prompt, its movement prompt and a checkbox (checked) to drop it, and the descriptor model SHALL be shown. Nothing SHALL be added to the project until the user presses **Use**, which SHALL insert the checked shots as new scenes right after the current scene, in order, without changing or replacing any existing scene. Each new scene SHALL have a unique id, the shot's prompt, `described_by` set to the model, and, in a video project, `silence_seconds` equal to the shot's length rounded to 0.1 s (at least 1 s); a scene SHALL animate, with the shot's movement prompt as `motion_prompt`, when the project is set to animate. In an images project it SHALL have no `silence_seconds`. No new scene SHALL use the clip as its source. **Discard** SHALL add nothing, and the clip SHALL be deleted when the proposal is used or discarded.

#### Scenario: using three shots
- **WHEN** the user presses Use with three checked shots while scene 2 of 4 is selected
- **THEN** three new scenes appear after scene 2, in order, with unique ids and `described_by`, and the other scenes are unchanged

#### Scenario: dropping a shot
- **WHEN** the user unchecks the second shot and presses Use
- **THEN** only the other shots become scenes

#### Scenario: the new scenes are valid
- **WHEN** the project is a video project
- **THEN** every new scene has `silence_seconds` of at least 1 s and the project validates and saves

#### Scenario: animated project
- **WHEN** the project has animation configured
- **THEN** each new scene animates and its `motion_prompt` is the shot's movement prompt

#### Scenario: discarding
- **WHEN** the user discards the proposal
- **THEN** no scene is added and the clip is deleted from the project's sources

#### Scenario: one scene stays the default
- **WHEN** the user does not choose "Several scenes"
- **THEN** a clip is described as one scene, as before

### Requirement: Splitting is reachable from the API and the CLI

`POST /api/describe` SHALL accept `split` (boolean, default false) and, when true and the file is a clip, answer with `shots` (and `prompt` set to the first shot's, for clients that ignore `shots`). `veritmo describe --split <clip>` SHALL print one line per shot, `[start-end] prompt`, and the movement prompt on a following `MOTION:` line when there is one.

#### Scenario: API
- **WHEN** a client calls describe with `split: true` for a clip with two shots
- **THEN** the answer has `shots` with two entries and the model used

#### Scenario: CLI
- **WHEN** the user runs `veritmo describe --split clip.mp4`
- **THEN** each shot is printed as `[0.0-4.0] prompt…` and the model used goes to stderr

### Requirement: Describing uses the provider routed to it

Describing a picture SHALL use the provider routed to `describe_image` and describing a clip the one routed to `describe_video`, with that provider's vision model, unless the call names another `provider`. The answer SHALL carry the `provider` used besides the `model`, and a scene created from it SHALL record `described_by` as the model alone for the built-in provider and as `<provider id>:<model>` for any other. With a remote provider the job SHALL respect the call cap of `provider-routing`. The model and memory rules of describe (installed model, free memory, unloading) SHALL apply only when the provider is of type `ollama`.

#### Scenario: a routed provider
- **WHEN** `describe_image` is routed to provider `openrouter` with vision model `gpt-4o`
- **THEN** a described picture answers `provider` `openrouter`, `model` `gpt-4o`, and a scene made from it records `described_by` `openrouter:gpt-4o`

#### Scenario: the built-in provider is recorded as before
- **WHEN** the built-in Ollama describes with `gemma4:26b`
- **THEN** `described_by` is `gemma4:26b`

### Requirement: Refusal alternatives include other providers

When describing is refused (`describe_refused`) in the private edition the alternatives SHALL list the other vision models of the provider that refused and every other provider that has a vision model, each as a choice the user can retry with by naming the provider. The retry SHALL be a new call made only when the user asks. The public edition has no alternatives.

#### Scenario: a cloud refusal offers local
- **WHEN** a cloud provider refuses a picture, a local Ollama has a vision model and the build is private
- **THEN** the alternatives include the local Ollama and its vision model

#### Scenario: a cloud refusal in the public edition
- **WHEN** the same happens in a public build
- **THEN** the answer carries no alternatives
