# prompt-assistant Specification

## Purpose
Turns a short scene description in any language into a prompt for the image generator or for the animation, using a local Ollama model that always runs on the CPU, so the GPUs stay free for rendering.

## Requirements

### Requirement: The image task returns one English prompt line

The `image` task SHALL send the user's description to the local model with instructions to write one line in English, about 70 to 100 words, describing a photorealistic photograph (subject, setting, light, camera and lens, mood). The returned text SHALL be a single line: reasoning blocks (`<think>…</think>`), line breaks, wrapping quotes and surrounding whitespace SHALL be removed. When the model stopped because it reached its token limit, the text SHALL be cut at its last complete sentence. An empty result SHALL be an error, never an empty prompt.

#### Scenario: Spanish description to an English prompt
- **WHEN** the task `image` runs with "Una mujer lee un libro en una cafetería soleada"
- **THEN** the request asks for English output and the returned text is one line

#### Scenario: reasoning is removed
- **WHEN** the model answers `<think>…</think> A woman reads…`
- **THEN** the returned text is `A woman reads…`

#### Scenario: truncated generation
- **WHEN** the model stops at its token limit in the middle of a sentence
- **THEN** the returned text ends at the last complete sentence

#### Scenario: empty answer
- **WHEN** the cleaned answer is empty
- **THEN** the task fails with an error saying the model returned nothing

### Requirement: The motion task returns a short English motion prompt

The `motion` task SHALL ask for a prompt in English of at most about 25 words describing only subtle movement of the scene and the camera, and SHALL return it with the same cleaning as the image task. Its generation limit SHALL be smaller than the image task's.

#### Scenario: motion prompt
- **WHEN** the task `motion` runs with "una mujer lee un libro en una cafetería"
- **THEN** the request asks for at most 25 words in English and the returned text is one line

### Requirement: The assistant never uses the GPUs

Every request to Ollama SHALL carry `num_gpu: 0` and disable the model's reasoning mode. After the answer arrives, while the model is still loaded, the assistant SHALL ask Ollama which models are loaded and SHALL verify that this model occupies 0 bytes of VRAM; if it does not, the assistant SHALL unload it and fail with an error saying the model was loaded on a GPU. The model SHALL be unloaded when the task ends, whether it succeeded or failed, so neither VRAM nor RAM stays held.

#### Scenario: request options
- **WHEN** any task runs
- **THEN** the request to Ollama sets `num_gpu` to 0 and turns thinking off

#### Scenario: a model found in VRAM
- **WHEN** after the answer Ollama reports the model with a non-zero VRAM size
- **THEN** the model is unloaded and the task fails with an error about the GPU

#### Scenario: unloaded afterwards
- **WHEN** a task succeeds or fails
- **THEN** an unload request for the model has been sent to Ollama

### Requirement: Memory and model checks run before calling the model

Before generating, the assistant SHALL check that the configured model is installed in Ollama and SHALL refuse to run when the memory available on the machine is smaller than the model's size times 1.3 plus 4 GiB. A missing model SHALL produce an error that names it and says to install it with `ollama pull <model>`; the assistant SHALL NOT download models by itself. An unreachable Ollama SHALL produce an error that says so and names the address used.

#### Scenario: model not installed
- **WHEN** the configured model is not in Ollama's list
- **THEN** the task fails with an error naming the model and `ollama pull`
- **AND** no generation request is sent

#### Scenario: not enough memory
- **WHEN** the available memory is below the model size times 1.3 plus 4 GiB
- **THEN** the task fails with an error about memory and no generation request is sent

#### Scenario: Ollama is down
- **WHEN** Ollama does not answer
- **THEN** the task fails with an error naming the address that was tried

### Requirement: Model and server are configurable

The model SHALL default to `qwen3:8b`, be overridden by the environment variable `VERITMO_ASSIST_MODEL` and then by the command's `--model` flag. The Ollama address SHALL come from the resolver of `ollama-settings` (the address saved in the settings, then `OLLAMA_HOST` as Ollama itself reads it, then `127.0.0.1:11434`) and SHALL be resolved at each request.

#### Scenario: precedence of the model
- **WHEN** `VERITMO_ASSIST_MODEL` is set and `--model` is also given
- **THEN** the flag's value is used

#### Scenario: the saved address is used
- **WHEN** an address is saved in the settings and `OLLAMA_HOST` names another
- **THEN** the assistant contacts the saved address

#### Scenario: the environment when nothing is saved
- **WHEN** no address is saved and `OLLAMA_HOST` is set
- **THEN** the assistant contacts `OLLAMA_HOST`

### Requirement: The command line prints the result

`veritmo assist <image|motion> "<text>"` SHALL print the generated prompt on standard output followed by a newline and nothing else, and SHALL print errors on standard error with a non-zero exit status. An unknown task or an empty text SHALL be rejected before contacting Ollama.

#### Scenario: success
- **WHEN** the command runs with a valid task and text
- **THEN** standard output holds exactly the generated line and the exit status is 0

#### Scenario: invalid task
- **WHEN** the task is `foo`
- **THEN** the command exits with an error and Ollama is not contacted

### Requirement: The server exposes the assistant and protects running jobs

`POST /api/assist` with `{"task", "text"}` SHALL return `200 {"text"}` with the generated prompt, behind the same authentication as the other `/api/` routes. It SHALL answer `400` for an unknown task, an empty text or a text longer than 2000 characters, and `409` while any run is queued or active, without contacting Ollama. Failures of the checks above SHALL be returned with the standard error body and a message that says what to do.

#### Scenario: a run is active
- **WHEN** a pipeline run is active and a request arrives at `/api/assist`
- **THEN** the response is `409` and Ollama is not contacted

#### Scenario: invalid input
- **WHEN** the body has an empty text or an unknown task
- **THEN** the response is `400`

#### Scenario: authentication
- **WHEN** the server requires a token and the request has none
- **THEN** the response is `401`, as for every other `/api/` route

### Requirement: The music task returns an English style and lyrics

The `music` task SHALL send the user's description to the local model with instructions to reply with a line `STYLE:` holding an English style description (genre, instruments, mood, tempo, vocals) and, after a line `LYRICS:`, lyrics with section tags such as `[Verse]` and `[Chorus]`, written in the language of the description. The reply SHALL be returned as a `caption` and `lyrics`. Reasoning blocks and wrapping quotes SHALL be removed and line breaks in the lyrics SHALL be kept. When the model stopped at its token limit, the lyrics SHALL end at their last complete line. A reply with no `STYLE:` marker SHALL use its first line as the style. An empty style SHALL be an error, never an empty prompt. With `instrumental` the task SHALL ask for the style only and return empty lyrics.

#### Scenario: Spanish description gives Spanish lyrics and an English style
- **WHEN** the task `music` runs with "Una balada sobre volver a casa bajo la lluvia"
- **THEN** the caption is in English and the lyrics are in Spanish with section tags

#### Scenario: Instrumental asks for no lyrics
- **WHEN** the task runs with `instrumental` true
- **THEN** the request asks for the style only and the lyrics are empty

#### Scenario: Unmarked reply
- **WHEN** the model answers with text and no `STYLE:` marker
- **THEN** its first line is the caption

#### Scenario: Empty style
- **WHEN** the cleaned reply has no style
- **THEN** the task fails saying the model returned nothing

#### Scenario: Truncated lyrics
- **WHEN** generation stops at the token limit in the middle of a line
- **THEN** the lyrics end at the last complete line

### Requirement: The server and the command line offer the music task

`POST /api/assist` SHALL accept `task: "music"` and an optional `instrumental` flag and SHALL answer `caption` and `lyrics` (and `text` with the reply as written). `veritmo assist music "<text>"` SHALL print the style and the lyrics, with `--instrumental` for the style only. The same protections as the other tasks SHALL apply: CPU only, memory check, unload when finished, and refusal while a run is queued or active.

#### Scenario: Request over the API
- **WHEN** a client posts `{"task":"music","text":"..."}`
- **THEN** the answer has `caption` and `lyrics`

#### Scenario: Refused while a run is active
- **WHEN** a run is queued or active
- **THEN** the request is answered with a conflict, as for the other tasks

#### Scenario: Command line
- **WHEN** the user runs `veritmo assist music "..."`
- **THEN** it prints the style and the lyrics and nothing else

### Requirement: A request can name the model, and the installed models can be listed

`POST /api/assist` SHALL accept an optional `model`, naming a model installed in Ollama, and SHALL use it for that request in the image, motion and music tasks; an empty or absent `model` SHALL mean the server's default (`VERITMO_ASSIST_MODEL`, else `qwen3:8b`). A model that is not installed SHALL be refused with the error that names it and says to install it with `ollama pull <model>`; nothing SHALL be downloaded. The memory check and the CPU-only checks SHALL apply to the chosen model. `GET /api/assist/models` SHALL answer `{default, models: [{name, size_bytes, fits}]}`, behind the same authentication as the other `/api/` routes: the models installed in Ollama that can chat (embedding and reranker models are left out), `fits` telling whether the model passes the memory rule (its size times 1.3 plus 4 GiB against the memory available now, true when that cannot be read), and `default` the model used when a request names none. An unreachable Ollama SHALL answer the same error as the other assistant routes.

#### Scenario: a request names an installed model
- **WHEN** a request carries `"model": "example/qwen3-tuned:8b"` and that model is installed
- **THEN** the generation is sent to that model and the answer is the usual `200 {"text"}`

#### Scenario: no model named
- **WHEN** the request has no `model`
- **THEN** the server's default model is used, as before

#### Scenario: a model that is not installed
- **WHEN** the request names a model Ollama does not have
- **THEN** the answer is the not-installed error naming it and `ollama pull`, and no generation request is sent

#### Scenario: listing
- **WHEN** Ollama has a chat model of 5 GB, a 19 GB one and an embedding model
- **THEN** the list has the two chat models with their sizes and `fits`, not the embedding one, and `default` names the default model

### Requirement: A refusal by the model is reported as such

When the model answers a task with a refusal instead of the requested text, the assistant SHALL NOT return it as a suggestion: the server SHALL answer `422` with the code `assist_refused` and a message that says the named model refused to write this text and that the refusal comes from the model. In the private edition the message also says that another model can be chosen (the selector, or `VERITMO_ASSIST_MODEL`); in the public edition it says nothing about choosing another one. A reply is a refusal when, after cleaning, it opens (case-insensitively, ignoring wrapping quotes) with a refusal phrase from a fixed list in English or Spanish ("I can't", "I cannot", "I'm sorry", "I am sorry", "Sorry", "I'm unable", "I am unable", "I won't", "I apologize", "As an AI", "Lo siento", "No puedo", "Lamento", "No me es posible") and has at most 80 words. For the music task the check SHALL run on the reply before it is parsed, and only when the reply has no `STYLE:` line. The command line SHALL print the same message on standard error with a non-zero exit status.

#### Scenario: an image task refused
- **WHEN** the model answers "I'm sorry, but I can't help with that request."
- **THEN** the response is 422 `assist_refused` naming the model, and no suggestion is returned

#### Scenario: the public message
- **WHEN** the same happens in a public build
- **THEN** the message names the model and says the refusal is the model's, without suggesting another model

#### Scenario: a Spanish refusal
- **WHEN** the model answers "Lo siento, no puedo ayudar con eso."
- **THEN** the response is the same refusal error

#### Scenario: a prompt that starts like a refusal word
- **WHEN** the reply is a 90-word description whose first words are "Sorry-looking dog sitting…"
- **THEN** it is returned as the prompt, because it is not short and does not open with a refusal phrase

#### Scenario: the music task refused
- **WHEN** the reply to a music request has no `STYLE:` line and opens with "I cannot"
- **THEN** the response is the refusal error, not a format error

### Requirement: The application adds no content filter

The assistant's instructions to the model SHALL describe only the format and length of the answer, and the application SHALL NOT screen, rewrite or refuse the user's text or the model's answer for its content beyond the refusal report above. Whether a model writes a given text is the model's own behaviour, which the user changes by choosing another installed model.

#### Scenario: instructions carry no content rule
- **WHEN** the system instructions of the image, motion and music tasks are read
- **THEN** none of them states what subjects are allowed or forbidden

### Requirement: The assistant uses the provider routed to it

The assistant (picture, movement, song and videoclip-storyboard prompts) SHALL use the provider routed to `assistant`, with that provider's text model, unless the call names another `provider`. The requirements of this capability that describe Ollama (staying on the CPU with `num_gpu` 0, verifying that the model holds no VRAM, the memory and installed-model checks, and unloading the model) SHALL apply only when that provider is of type `ollama`; for any other provider none of those options or checks SHALL be sent or made. The cleaning of the answer, the detection of refusals and the error kinds SHALL be the same for every provider. With the built-in Ollama routed, behaviour is as before.

#### Scenario: routed to an OpenAI-compatible provider
- **WHEN** `assistant` is routed to an `openai` provider and the image task runs
- **THEN** the request goes to that provider with its text model, carries no `num_gpu`, and the answer is cleaned to one line

#### Scenario: a refusal from a cloud provider
- **WHEN** the provider answers "I'm sorry, I can't help with that"
- **THEN** the task fails as a refusal naming the provider's model, as it does for Ollama

#### Scenario: still on the CPU with Ollama
- **WHEN** `assistant` is routed to the built-in Ollama
- **THEN** the request carries `num_gpu` 0 and the VRAM check and unload run

### Requirement: The public edition's assistant does not use models without safety training

In the public edition the assistant SHALL apply the deny list of `editions` to the model named by the request, the routing, `VERITMO_ASSIST_MODEL` or the first installed one, and SHALL answer `400 model_not_allowed` for a denied model without calling it.

#### Scenario: a denied model through the environment
- **WHEN** `VERITMO_ASSIST_MODEL` names a denied model in a public build
- **THEN** every assistant call answers `400 model_not_allowed` and nothing is sent to Ollama

### Requirement: The music task can also return the tags and writes the sections of the track's generator

The `music` task SHALL accept `generator` (`ace-step`, `heartmula` or `diffrhythm2`; default `ace-step`). For `heartmula` and `diffrhythm2` the model SHALL be asked for a third line `TAGS:` with 4 to 8 short English words separated by commas, and the answer SHALL carry them normalised (lower case, no repeats) as `tags`. For `ace-step` the answer SHALL NOT carry `tags`. The section tags of the lyrics SHALL be the ones that generator reads: `[Verse]`, `[Chorus]`, `[Bridge]`, `[Outro]` for ACE-Step; and, for HeartMuLa and DiffRhythm2, which read the same structure tags, lower case `[intro]`, `[verse]`, `[chorus]`, `[bridge]`, `[outro]`, `[inst]`, `[solo]`, `[hook]`, `[break]` and `[end]`. Sections the generator does not know SHALL be removed from the reply. A reply with no `TAGS:` for a generator that needs them SHALL not fail: the answer has no `tags` and the studio keeps its own button to derive them.

#### Scenario: HeartMuLa
- **WHEN** the task runs with `generator: "heartmula"` and a description of a wedding song
- **THEN** the answer has `caption`, `lyrics` with lower case sections such as `[intro]` and `[verse]`, and `tags` such as `piano,acoustic,wedding,romantic`

#### Scenario: ACE-Step is unchanged
- **WHEN** the task runs without `generator`
- **THEN** the answer has no `tags` and the lyrics use `[Verse]` and `[Chorus]` as before

#### Scenario: A section the generator does not read
- **WHEN** the reply for DiffRhythm2 contains `[Pre-Chorus]` (a tag the engine does not know)
- **THEN** that line is removed and the rest of the lyrics are kept

#### Scenario: The model gives no tags
- **WHEN** the reply for HeartMuLa has no `TAGS:` line
- **THEN** the answer is returned without `tags` and the rest is kept

### Requirement: The lyrics-from-transcript task orders a transcription without inventing text

The task `lyrics-from-transcript` SHALL take `text` (a transcription, one segment per line, empty for a song without words), `language`, an optional `hint` (what the user says about the song), the optional measures `bpm` and `keyscale`, an optional `picture` (the description of a video's picture) and `generator`, and answer `{caption, lyrics, tags?}`. The model SHALL be told to keep the words of the transcription as they are, to put the section tags of the generator by the repetition of lines (a block that comes back is the chorus), to join lines of one block and to **never add, complete, translate or correct** a line. Whatever the model returns SHALL be checked against the transcription: when the lyrics have fewer than 70% of the words of the input, or words that are not in it beyond the section tags, the answer SHALL fall back to the transcription as it is under a single first section tag, and the answer SHALL say so (`unchanged_words: false`). The transcription may be up to 8000 characters. The `caption` is English and describes the genre, mood and vocals that the lyrics and the hint allow; it SHALL NOT name instruments the user did not mention, and it SHALL use the measures and the picture only to say tempo, mood and setting. The server SHALL enforce this: an item of the style or of the tags that names an instrument that the user's hint and the picture's description do not mention is removed from the answer. The refusal and provider rules of the other tasks apply.

#### Scenario: A song with a chorus
- **WHEN** a transcription has one block of four lines three times and verses in between
- **THEN** the repeated block is tagged as the chorus and the verses as verses, with the original words

#### Scenario: The model rewrites the lyrics
- **WHEN** the model's lyrics add lines that the transcription does not have
- **THEN** the answer keeps the transcription's words and says `unchanged_words: false`

#### Scenario: Instruments are not guessed
- **WHEN** the hint is empty
- **THEN** the caption describes mood and vocals from the lyrics and names no instrument

#### Scenario: The model names an instrument anyway
- **WHEN** the model's style says "acoustic guitar" and nothing the user gave mentions a guitar
- **THEN** that item is removed from the style and from the tags

#### Scenario: A song with no words
- **WHEN** the text is empty and the measures and a hint are given
- **THEN** the answer has a caption, no lyrics and the tags, and the studio proposes the track as instrumental

### Requirement: A free-form request is turned into what the generator needs

The assistant SHALL offer one operation that takes a request written or spoken in any language and answers, in a single response, the kind of thing asked for (`video`, `image` or `music`), a short title, the language of the request and the texts that kind needs: for an image an image prompt; for a video an image prompt and a movement prompt; for music a style, lyrics (empty when the request asks for an instrumental) and, when the request names one, the length. The texts SHALL be produced by the same tasks, with the same safeguards, as when each is asked on its own. The response SHALL say where the kind came from: the caller (`user`), the model (`model`), an attachment (`attachment`) or the default (`default`).

#### Scenario: a song

- **WHEN** the request is "una canción triste de piano en español, instrumental"
- **THEN** the kind is `music`, the style describes a sad piano piece, the lyrics are empty and the title is short

#### Scenario: a video

- **WHEN** the request is "un gato en una azotea al atardecer"
- **THEN** the kind is `video` and the answer carries an English image prompt and a movement prompt of at most 25 words

#### Scenario: the caller fixes the kind

- **WHEN** the request names `kind: image`
- **THEN** the kind is `image` with source `user` and the model is not asked to decide it

#### Scenario: attachments

- **WHEN** the request has an audio attachment and no kind
- **THEN** the kind falls back to `music` with source `attachment` if the model's answer is not one of the kinds

### Requirement: A request that cannot be fully planned is not lost

When the kind is decided but writing the texts fails, the operation SHALL answer the kind, the title and a warning saying what failed, with the texts empty, so the interface can let the user write them. When the kind cannot be decided by the model it SHALL fall back as above instead of failing. The operation SHALL keep the existing refusals: a run in progress is a conflict, an unreachable model or short memory is unavailable, and a refusal by the model is reported as such.

#### Scenario: the texts fail

- **WHEN** the kind is `music` and the lyrics step fails
- **THEN** the answer has `music`, the title, a warning, and empty style and lyrics

#### Scenario: a run is in progress

- **WHEN** a run is active
- **THEN** the operation answers a conflict saying the assistant waits until no run is in progress, as the other assistant operations do

### Requirement: The plan of a video request carries the speech it names

For a video request the operation that turns a free-form request into what the generator needs SHALL also answer `speech`: the words the video has to say, taken from what the request quotes or asks to be said ("que diga…", "says…", a narration), in the language of the request, without the quotes or the words that introduce them. It SHALL be empty when the request names no words. The speech SHALL be found without an additional model call, SHALL NOT be translated, and SHALL NOT appear in the image prompt or the movement prompt.

#### Scenario: a quoted line

- **WHEN** the request is "una mujer en la playa que dice «hola, bienvenidos»"
- **THEN** the kind is `video`, `speech` is "hola, bienvenidos" and the image prompt does not contain the quote

#### Scenario: no speech

- **WHEN** the request is "un gato en una azotea al atardecer"
- **THEN** `speech` is empty

#### Scenario: other kinds

- **WHEN** the request is a song
- **THEN** `speech` is empty and the lyrics are unaffected

### Requirement: The tags written for HeartMuLa come from a vocabulary and are checked

The music task for HeartMuLa SHALL tell the model the vocabulary of tags by category (genre, voice, timbre, mood, instrument, scene and tempo) and ask for one genre first and at most two tags per other category. The server SHALL check the answer against that vocabulary: lower case, synonyms mapped to their tag, tags outside the vocabulary and duplicates dropped, ordered by category and limited to eight. When no genre is left the suggestion SHALL be returned without a genre and say so. `GET /api/assist/music-tags` SHALL return the vocabulary by category so that a client offers the same tags. The tags of ACE-Step (which has none) and the style of DiffRhythm2 (a short English sentence) SHALL not be checked against it.

#### Scenario: an invented tag

- **WHEN** the model answers `TAGS: Techno, rap, super-fast, chickenpluckercore`
- **THEN** the suggestion carries `techno,rap,fast` and the tag outside the vocabulary is gone

#### Scenario: no genre

- **WHEN** every tag of the answer is outside the vocabulary
- **THEN** the suggestion has no tags and the response says the genre is missing

#### Scenario: the client lists the vocabulary

- **WHEN** a client requests `GET /api/assist/music-tags`
- **THEN** it receives the categories with their tags, the same ones the server validates against

### Requirement: The lyrics follow the structure of the length of the song

The music task SHALL ask for lyrics in stanzas of four lines whose chorus is repeated word for word, with a structure that depends on the length of the song: short (75 seconds or less) verse, chorus, verse, chorus and outro (about 16 lines); normal verse, chorus, verse, chorus, bridge, chorus and outro (about 28 lines); long (over 150 seconds) three verses (about 36 lines). A request without a length SHALL be normal. `POST /api/assist` and `POST /api/assist/request` SHALL accept the length in `seconds` for a song, and the section tags SHALL be written in the case the generator reads.

#### Scenario: a long song

- **WHEN** the music task is asked for a song of 180 seconds
- **THEN** the instruction asks for three verses with their choruses, a bridge and an outro, about 36 lines

#### Scenario: no length

- **WHEN** a request names no length
- **THEN** the lyrics have the normal structure with a bridge and the chorus three times

### Requirement: The vocabulary and the popular combinations are served together

`GET /api/assist/music-tags` SHALL return the categories of the vocabulary in the order genre, mood, instrument, tempo, vocal, era and production, and the ten popular combinations of tags, each with an id, a name and its tags. The tags of HeartMuLa SHALL be written with the spelling of that vocabulary and the task SHALL show the combinations to the model as examples.

#### Scenario: the combinations

- **WHEN** a client requests the vocabulary
- **THEN** it also receives ten combinations such as `lo-fi, chill, piano, slow`, every tag of which is in the vocabulary

### Requirement: A request to change a picture becomes an English instruction

When a picture is attached to an image request, `POST /api/assist/request` SHALL return in `image_prompt` a short imperative English instruction written from the user's words in any language: it names who or what changes by position or feature, describes only the change and does not repeat colours that are not the one to apply, and ends with "Keep everything else exactly the same." (added by the server) unless it is a transformation of the whole picture ("Transform the picture into: …"). The picture SHALL NOT be described as a scene. When the instruction cannot be written, the answer SHALL carry a warning and no `image_prompt`, so that the caller keeps the user's words.

#### Scenario: Spanish request with a picture

- **WHEN** the request is "ponle el pelo rubio al chico de la camiseta roja" with a picture attached
- **THEN** `image_prompt` is an English instruction that ends with "Keep everything else exactly the same."

#### Scenario: the instruction cannot be written

- **WHEN** the assistant fails on that task
- **THEN** the answer has the kind, a warning that names the picture instruction and no `image_prompt`

### Requirement: The assistant writes an SVG from a description

`POST /api/assist/svg` SHALL accept `text` (a description in any language, up to 2000 characters), and optionally `model` and `provider`, and SHALL answer `{svg}` with one SVG document written by the assistant. It SHALL apply the guards of every assistant call: the routing of the task and the consent for a remote provider, `409` while a run is queued or active and the model loads on this machine, the memory rule, CPU only and unloading for a local model, `503` when Ollama, the model or the memory is missing, and `422 assist_refused` when the model declines. An empty text or one over 2000 characters SHALL be `400` and nothing SHALL run. The instructions given to the model SHALL ask for a single `<svg>` with a `viewBox`, an `id` on every element worth selecting, fills and strokes as attributes, no `<style>`, gradients, filters or `<text>`, no script or outside reference, and at most 40 elements.

#### Scenario: A drawing
- **WHEN** a description is sent and the model answers an SVG, even inside a code fence or with words around it
- **THEN** the answer is `200` with the document from `<svg` to `</svg>` as the model wrote it

#### Scenario: Invalid input
- **WHEN** the text is empty or has 2001 characters
- **THEN** the answer is `400` and the assistant is not contacted

#### Scenario: A run is active
- **WHEN** a pipeline run is queued or active
- **THEN** the answer is `409` naming the run and the assistant is not contacted

### Requirement: The SVG that comes back is checked

The server SHALL accept the model's answer only if it is well-formed XML, its root is `<svg>`, it has a non-empty `viewBox`, it is at most 200 KB, generation was not cut at the token limit, and it holds no `<script>`, no `<foreignObject>`, no event attribute (`on*`) and no `href` that points outside the document (anything but a `#fragment`). Otherwise the answer SHALL be `422` with the code `validation_failed` and a sentence saying which check failed. The server SHALL NOT judge the quality of the drawing.

#### Scenario: Prose instead of a drawing
- **WHEN** the model answers words with no `<svg>` element
- **THEN** the answer is `422 validation_failed` saying the reply has no `<svg>` element

#### Scenario: A script
- **WHEN** the drawing holds a `<script>` or an `onload` attribute
- **THEN** the answer is `422 validation_failed` naming it

#### Scenario: Cut off
- **WHEN** generation stopped at the token limit
- **THEN** the answer is `422 validation_failed` saying the drawing was cut off

### Requirement: The catalog says the operation exists

`GET /api/music-motion/catalog` SHALL list `svg` in `features` on a server that has `POST /api/assist/svg`, so that a client offers the operation only where it exists.

#### Scenario: Advertised
- **WHEN** the catalog is read
- **THEN** `features` contains `svg`

### Requirement: The assistant writes a whole dataset from one sentence
`POST /api/assist/dataset` SHALL accept `{mode, text, language?, engine?, negative?, look?, images?: {variants}, generator?, examples?, model?, provider?}` with `mode` one of `video`, `motion` or `music`, and SHALL answer `{entries, mode, warnings}` where each entry is `[id, title, lora_info, text]` in the grammar `import/preview` reads. It SHALL have the guards of `POST /api/assist/plan`: routing and consent for a remote provider, `409` while a run is in progress, the memory rule, `422 assist_refused` when the model refuses and nothing was written, `503` when no model is reachable. An unknown `mode`, an empty or over-long `text` and a bad `language` SHALL be `400` without calling the model.

#### Scenario: A storyboard from a sentence
- **WHEN** a client posts `mode: "video"` and a sentence asking for five scenes
- **THEN** the answer has up to the entries the model wrote, each with a unique `id`, and the text of each carries its `Dialogue:`, `Motion:` and prompt lines

#### Scenario: Shared lines come from the request
- **WHEN** the request has `negative` and `images.variants: 2`
- **THEN** every video entry carries the `Negative:` line and `Variants: 2`

#### Scenario: Motion entries name catalog parts
- **WHEN** `mode` is `motion` and the model names a part
- **THEN** an entry with `Part:` is kept only when the part exists in the catalog; otherwise it is left out with a warning

#### Scenario: Music entries are tracks
- **WHEN** `mode` is `music` with `generator: "heartmula"`
- **THEN** each entry has a style, `Lyrics:` unless instrumental, and `Tags:` in that generator's vocabulary

#### Scenario: Refused
- **WHEN** the model refuses and no entry was written
- **THEN** the answer is `422 assist_refused`

### Requirement: What the model could not do is reported, not hidden
The answer of `assist/dataset` SHALL list in `warnings` every entry the model wrote unusably or that was dropped (duplicate id, unknown part, text over the limits) and every missing piece that was filled by a default. Entries SHALL be checked with the import's own parser before they are returned, so what comes back previews without errors.

#### Scenario: A malformed entry is dropped
- **WHEN** one of the entries the model wrote has no text
- **THEN** it is not returned and `warnings` names its position

### Requirement: Style examples can be sent with an assistant call
`POST /api/assist`, `POST /api/assist/plan` and `POST /api/assist/dataset` SHALL accept an optional `examples: [{prompt, negative?, tags?}]` of at most 5 items, each field at most 2000 characters. The examples SHALL be put in the model's context as examples of the style to follow, with the instruction not to copy them. More than 5, an item without `prompt` or a field over the limit SHALL be `400`.

#### Scenario: Examples reach the model
- **WHEN** a call has two examples
- **THEN** the model's prompt contains both, marked as style examples

#### Scenario: Too many
- **WHEN** a call has six examples
- **THEN** it is `400` and the model is not called

### Requirement: The catalog says the dataset and the examples exist
`GET /api/music-motion/catalog` SHALL list `dataset` and `examples` in `features`.

#### Scenario: A client checks first
- **WHEN** a client reads the catalog
- **THEN** `features` contains `dataset` and `examples`

### Requirement: The plan accepts the length of the song
`POST /api/assist/plan` SHALL accept an optional `seconds` (1 to 600). When given it SHALL be used as the song's length and SHALL NOT produce the warning that the length is missing; the model is not asked for it.

#### Scenario: The length is given
- **WHEN** a plan request has `seconds: 90`
- **THEN** `song.seconds` is 90 and there is no warning about the length

### Requirement: The plan of an animated music video carries a storyline

The plan written from one sentence SHALL also carry `world` (where the video happens and what it looks like, one line), `device` (one of: `type carries the argument`, `a character does the work`, `one object across the song`, `life before and after`, `a number lands`, `the result first`, `a question the song answers`) and `hook` (what the first two seconds show, one line), in English, next to the concept. A missing or unknown device SHALL become `type carries the argument` with a note. The three SHALL be kept with a piece made from the plan and given to the design proposal (world and device), the script proposal (device and hook) and the scene writer (world and device, the hook for the first piece), so that the scenes of a video belong to one film.

#### Scenario: A plan with a device
- **WHEN** the request is "a night drive through a city that forgets you"
- **THEN** the plan has a world (the city at night, one line), a device from the list and a hook, and the piece made from it keeps the three

#### Scenario: An unknown device
- **WHEN** the model answers a device that is not in the list
- **THEN** the plan says `type carries the argument` and its note says the model's device was not one offered

### Requirement: The dataset assistant can write 3D scenes

`POST /api/assist/dataset` in mode `video` or `motion` with `"picture": "blender"` SHALL write, for every entry, the description of a 3D scene with the same writer and checks as `POST /api/projects/{id}/blender/descriptions` (the model of the `motion_graphics` task, up to 3 attempts), from the entry's own text, and SHALL give the entry a `Blender: <id>.scene.json` line. The answer SHALL carry the descriptions in `descriptions` (`{<id>.scene.json: <description>}`) beside `entries`, `mode` and `warnings`. An entry whose description does not pass SHALL be left out with a warning naming the problems; the call SHALL fail with `422` only when no entry is usable. At most 6 entries SHALL be written in this way per call, the rest left out with a warning. An optional `project` (an existing project) SHALL give the design, the tempo and the frame; without it the writer SHALL use no design, keys in seconds and a 16:9 frame. The guards of the dataset assistant apply (consent for a remote provider, `409` with a local model while a run is active, the memory rule, `assist_refused`). In mode `motion` the entries SHALL be 3D graphics: each gets a `Blender: <id>.scene.json` line and a `Duration:` line (the length its description is written for) and no `Part:`, `Graphic:` or `Param` line. `picture` with mode `music`, or a value other than `blender`, SHALL be `400`.

#### Scenario: A storyboard of 3D scenes
- **WHEN** the call asks for 4 scenes with `picture: "blender"`
- **THEN** the answer has 4 entries each with a `Blender:` line and 4 descriptions, and the entries preview with the import's own mapping

#### Scenario: One scene does not pass
- **WHEN** the description of the third entry fails its 3 attempts
- **THEN** the answer has the other entries and a warning for the third, with status `200`

#### Scenario: More than the limit
- **WHEN** the model writes 8 entries
- **THEN** 6 are kept, and a warning says that 2 were left out

#### Scenario: A local model while a run is active
- **WHEN** the model is local and a run is active
- **THEN** the call answers `409`, as every call of the dataset assistant does

#### Scenario: Four 3D titles
- **WHEN** the call asks for four animated titles with `picture: "blender"` in mode `motion`
- **THEN** the answer has up to four entries with a `Blender:` line and a `Duration:` line and their descriptions, and the entries preview with the motion mode of the import

### Requirement: The catalog says the 3D datasets exist

`GET /api/music-motion/catalog` SHALL list `blender` in `features` when `POST /api/assist/dataset` accepts `picture: "blender"` and the import reads the `Blender:` line.  A server without that code SHALL NOT list it. The animations' side SHALL be announced by its own entry when it exists, never by this one.

#### Scenario: A server with it
- **WHEN** the catalog is read
- **THEN** `features` has `dataset` and `blender`

### Requirement: The assistant writes a dataset of 3D models

`POST /api/assist/dataset` SHALL accept `mode: "models3d"` and write up to 12 entries from the sentence, each the prompt of **one object alone** (a concrete shape, material and colour; no scene, no people, no text to read), in the tuple shape the import reads, with the guards of the other modes (the routing and the consent of a remote provider, the wait for a run, the memory rule, the model's own refusal) and its answer checked by the import of this mode before it is returned, so that what comes back imports. The `examples` of the call SHALL be the presets of the kind `model3d` when it names them, and `picture` and `parts` SHALL be refused for this mode. An unknown `mode` SHALL be `400` naming the accepted ones, which now include `models3d`.

#### Scenario: A sentence becomes prompts
- **WHEN** the dataset is asked for "ten objects for a roller derby team room"
- **THEN** up to ten entries come back, each the description of a single object, and the answer imports as a project of the mode

#### Scenario: Not an object
- **WHEN** the model writes an entry that describes a scene with people
- **THEN** the entry is rewritten asking for one object, or dropped with a warning, and what is returned still imports

### Requirement: The assistant writes the prompt of one 3D model

`POST /api/assist/request` SHALL accept `kind: "model3d"` and answer `{kind, kind_source, title, model_prompt, warnings}`, where `model_prompt` is an English prompt for one object alone written from the request in any language, with the guards of the other kinds; when it cannot be written the answer carries the kind and the title and a warning with the prompt empty. The kind SHALL be decided by the caller only (the model does not choose it between the kinds): a request with an attachment of a picture SHALL be a `model3d` only when the caller says so.

#### Scenario: A request in Spanish
- **WHEN** the text is "un patín de ruedas retro, rosa y brillante" and the kind is `model3d`
- **THEN** `model_prompt` is an English description of one roller skate
