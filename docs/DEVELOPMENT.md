# Development

[← Back to the README](../README.md)


```bash
go build ./...
go vet ./...
go test ./...
openspec list                                              # active changes, if any
openspec validate <change> --strict --no-interactive       # when working on a change
```

The project is spec-driven with [OpenSpec](https://github.com/Fission-AI/OpenSpec): current behavior lives in `openspec/specs/` (one capability per `internal/` package, plus the management-server and web-frontend capabilities), completed changes in `openspec/changes/archive/`. As of 2026-10-09 the full planned stack is implemented and archived: the four core changes, the HTTP management server (`veritmo serve`, `docs/http-api.md`) and, until it moved to `dashboards-lab`, the Angular management frontend (`web/`, from `docs/PLAN_FRONTEND_PROMPT.md`). Remote use (token, CORS, tickets) landed in `add-remote-api-access`. Model management (list, catalog, background downloads, delete) landed in `add-model-manager`. Catalog tabs (Imagen/Voz), Hugging Face access guidance and run result links landed in `improve-model-catalog`. Narration with the real Piper (voice, stdout WAV, project-dir paths) landed in `fix-tts-real-piper`. Music mode (`tracks:`, `ace-lm`/`ace-synth`, `docs/MUSIC_MODE.md`) landed in `add-music-output-mode`, and its web screens (the music studio, the Music tab of Models, the music phases in the run monitor) in `add-music-mode-ui`. There are no active changes. When a change is archived, its line goes into `## [Unreleased]` of [CHANGELOG.md](../CHANGELOG.md), in the same commit (see [RELEASING.md](RELEASING.md)); a change with nothing a user could notice gets no line.

Later changes added: **describing a reference** (`internal/assistant/describe`, `docs/ASSISTANT.md`), one scene per shot of a clip, the **Settings** screen with the editable Ollama address (`internal/platform/settings`), **model providers** with a common interface and an OpenAI-compatible client (`internal/assistant/llm`, keys in their own private file) and **routing per task** with consent, a badge, a call cap and no automatic fallback (`internal/assistant/llm` resolver, `internal/server/core_routing.go`, `internal/server/api/routing.go`). Their specs are `describe-reference`, `ollama-settings`, `model-providers` and `provider-routing`.

**Editions.** The program has a private and a public edition, fixed when it is built (`GET /api/environment` and `veritmo --version` say which). The public edition, built without any tag, accepts only models that have their own safety training (a deny list of known models without it, applied to every model name before anything is sent to it), gives no suggestions to get around a model's refusal, uses only the built-in Ollama and a short list of cloud providers, and has no Civitai downloads. What a model writes or declines stays the model's behaviour: the application adds no content filter in either edition. The age check of **Describe a reference** is the same code in both and has no switch.


**The Create flow and the music tags (server side).** The screens that use them are in the dashboards of `dashboards-lab`. «Mejorar con IA» is the assistant task `improve` (`internal/assistant/assist/improve.go`). The vocabulary of the tags HeartMuLa is asked for, its synonyms and the ten popular combinations are plain tables in `internal/assistant/assist/tagvocab.go` (edit them there; the tests check that every combination is made of vocabulary tags and `GET /api/assist/music-tags` serves them to the page). The structure of the lyrics by length is `songShape` in `internal/assistant/assist/songs.go`.

The installer and the documentation checks have their own tests (they use stubbed tools and never install anything):

```bash
bash scripts/test/run-all.sh            # installer tests, documentation link tests and the link check
bash scripts/test/docker-distros.sh     # the installer's detection and package steps in Arch, Debian, Ubuntu and Fedora containers (needs Docker)
bash scripts/check-docs.sh              # every relative link and anchor in README.md and docs/*.md
```

## Logging

The server logs with the standard `log/slog`. `veritmo serve --log-level debug|info|warn|error` (default `info`) sets what reaches stderr, as `time=... level=WARN msg=... key=value` lines, and the code asks for the logger with `slog.Warn`, `slog.Info` and the like: a package never prints its own warning with `fmt.Fprintf(os.Stderr, ...)`. A test captures what a package logs by setting `slog.SetDefault` to a handler of its own for the length of the test. What a command prints for the person who ran it (a usage error, a report) is output, not log, and stays on stdout or stderr as before.

## Wiring and test seams

Nothing is wired by an `init()` and no test replaces a package variable. What a package needs from outside is given to it:

- The programs and services the video-edit tools use (ffprobe, the networks, `sd`, the vision service, the clip maker and the library of motions) are in a `videoedit.Catalog`. `NewCatalog()` has the code of every tool; `NewBareCatalog()` has none and the tests register stubs in it. The server makes one and hands it to the generation API and to the runs (`pipeline.Options.VideoEdit`); a test makes its own.
- The catalog of motion parts is `config.PartCatalog` (what validates a scene) and `pipeline.MotionParts` (what builds its page and chooses a part); `musicmotion.Parts` implements both, because `musicmotion` imports `config` and `pipeline` and they cannot import it. The command (`loadConfig`, `Options.MotionParts`) and the server (`workspace.Host.Motion`) hand it over, and `cmd/veritmo/parts_test.go` and `internal/server/core_parts_test.go` say they do. A `Config` is given its catalog (`Config.Parts`) and its machine checks (`Config.Checks`: the browser and Blender) by whoever validates it; with none, parts are not checked and the machine is asked.
- The location of the services of the music generators is `pipeline.Options.MusicServices`; the way the resources of a run are read is `workspace.Config.Sampler`.
- A test seam is a field of the struct that uses it (`Handler.probeAudio`, `Server.motionModelTimeout`) or a parameter, set before the value is used; a test that needs the real thing leaves it alone. `t.Chdir` and `t.Setenv` replace `os.Chdir` and `os.Setenv` in tests (`TestMain` may still set `HOME`, once).
- The state of the server is in its database ([DATA_STORE.md](DATA_STORE.md)). Two handles are the exception to "nothing is process-wide", because the pipeline and the commands read them through package-level functions: the settings (`settings.Load`, `settings.Save` and everything built on them use the `settings.Source` the process chose with `settings.UseSource`: the database for the program, `settings.json` when none was chosen) and the catalog of HyperFrames blocks the music motion layers read (`musicmotion.BlockStore`). `server.Config.DatabaseState` installs both on the server's database after the import of the state kept in files and gives them back at shutdown; **only `veritmo serve` sets it**. A test leaves it off, so the settings stay in the file it isolates (`t.Setenv("XDG_CONFIG_HOME", t.TempDir())`) and a test cannot leave its database behind for the next; a test that needs the database settings calls `settings.UseSource(settingsdb.New(st))` and gives it back with `t.Cleanup(func() { settings.UseSource(nil) })`. The commands install a lazy source in `main()` (`cmd/veritmo/settingssource.go`) that opens the database the first time a setting is needed. A code that reads a setting to change it and save it must read it with `settings.ReadFile`, which says when the settings cannot be read, never `Load`, which answers no settings: saving that would erase them.
- A store that keeps its record in a file and in the database (`hyperframes.Store`, `motionlib.Store`) has a constructor for each (`Store{Dir}` and `NewStoreDB(dir, st)`) and its tests run on both through an `eachStore` helper, so the two behave the same.

The tests of `internal/server` are not run in parallel: they work in a temporary copy of the repository root, which they enter with `t.Chdir`, and isolate the settings with `t.Setenv`, and neither can be done in a parallel test.

## Documenting the code

Go has no Javadoc. A *doc comment* is the comment that sits right above a declaration, and `go doc`, the editors and `pkgsite` read it. The rules here are the ones of the Go project ([Go Doc Comments](https://go.dev/doc/comment)) plus four of ours.

- **One package comment per package**, above the `package` clause of one file: `// Package cache keeps ...` (for a `main` package, `// Command orchestrator ...`). It says what the package is for, what it owns (files, processes, state) and where it sits among the others.
- **A comment on every exported name** (function, method, type, variable, constant). It is made of whole sentences and starts with the name: `// WriteError writes the error body of a response.` A type may start with `A`, `An` or `The`. A group of constants has one comment above the group. A method that implements a well-known interface (`Error`, `String`, `Write`, `ServeHTTP`, `MarshalJSON`...) needs none.
- **The syntax is Go's, not Markdown**: a blank `//` line between paragraphs, `# Heading` for a heading, lines indented by one more space for code, `-` for a list, `[Name]` or `[pkg.Name]` to link, and a paragraph that starts with `Deprecated:` for a deprecated name. No backticks around names, no `**bold**`, no `##`.
- **What a comment says**: what the caller can rely on (arguments, result, units), the errors it may test with `errors.Is` or `errors.As`, whether it is safe for concurrent use, and which files or processes it creates, owns or deletes. For an HTTP handler, the route (`GET /api/x/{id}`) and a pointer to `internal/server/openapi.yaml`, never a copy of its fields.
- **What it does not say**: the history of the code (what it did before, which change made it; that lives in `openspec/changes/archive` and `git log`) and how the body works line by line.
- **English**, like the rest of the code.

```go
// Good: starts with the name, says what the caller can rely on.
//
// Median is the middle value of v (the mean of the two middle ones when there is
// an even number); 0 for no values. v is not changed.
func Median(v []float64) float64

// Bad: starts with another word, tells the story, uses Markdown.
// This **used to** be called median2 before the refactor of March.
func Median(v []float64) float64
```

A package whose use is easy to get wrong (an input format, an ordering, a hashing rule, an error body) also gets an `Example` function in a `_test.go` file with an `// Output:` comment: `go test` runs it and `go doc -all` shows it next to the name. Add one when a reviewer asks "what does the input look like?", or when a bug came from misreading a comment.

`internal/architecture/doc_test.go` checks the first two rules on every test run, in both editions (it reads the source of the files of both twins), and the private CI runs the same checks of `staticcheck`. To read the documentation:

```bash
go doc ./internal/platform/cache                      # one package
go doc ./internal/server/api.WriteError                # one name
go run golang.org/x/pkgsite/cmd/pkgsite@latest -open .  # every package as a web site, on this machine
```

The module is called `Veritmo`, with no host in its name, so no public site (pkg.go.dev) indexes it: `pkgsite` on your machine is how to browse it.

## Code layout

`internal/` groups the packages by feature, two levels deep (`internal/<folder>/<package>`); the package names are the same as ever.

| Folder | What is in it |
|---|---|
| `platform/` | The base: `runner` (external programs), `events`, `cache`, `sched`, `edition`, `settings`, `media` (where projects live), `probe` (ffprobe) and `musicsvc` (starts and stops the .NET services) |
| `project/` | The scene file and what runs it: `config` (the model and its validation), `pipeline` (the run), `importer`, `export`, `quality`, `genmeta` |
| `video/` | Images and clips: `render`, `sdlog`, `animate`, `assemble`, `blender`, `motion` (animated HTML pages), `motionlib`, `videoedit`, `videovision` |
| `music/` | Songs: `music`, `songmap`, `beatgrid`, `audiomeasure`, `musicmotion`, `motionref`, `videoclip`, `transcribe` and the engines `heartmula`, `diffrhythm2` and `clap` |
| `speech/` | `tts` (Piper) |
| `assistant/` | The language-model helpers: `assist`, `llm`, `describe` |
| `prompts/`, `promptfetch/` | The prompt presets: the preset and its files (shared by the server and the command), and the command that fills them from public datasets |
| `models/` | `convrot` (converting model files) |
| `server/` | The HTTP layer and the contract (`openapi.yaml`); the top of the stack, only `cmd/` imports it. It is a composition root (`core_*.go`: routes, authentication, CORS, settings, content mode) over one package per family: `api/` (the kernel), `modelcat/`, `assistant/`, `workspace/` (projects and runs), `models/`, `generation/` and `music/`; see its `README.md` |

The rules that hold, and that `internal/architecture` checks on every test run: `platform` imports nothing from the other folders, `assistant`, `speech` and `models` import only `platform`, and nothing but `cmd/` imports `server`. Between `project`, `video` and `music` the packages do use each other (`config` validates what the engines offer, `pipeline` drives them), but there are no import cycles. A new package goes in the folder of the feature it serves; if it is used by all of them, it goes in `platform`.

## Working on a dashboard against the contract

The server is one and any number of dashboards can talk to it; the contract between them is `internal/server/openapi.yaml` (also `GET /api/openapi.json` on a running server). A dashboard in another repository is built against that description: its types and its tests come from it, and the Go tests of `internal/server` (`core_contract_required_test.go`, `core_contract_answers_test.go`) keep it true against what the handlers answer. A field a dashboard needs and the description lacks is fixed in `openapi.yaml`, so every dashboard is protected.



## Benchmark (comparing machines)

`python3 scripts/benchmark.py` runs the same real jobs on any machine (an SDXL image set, Krea 2 and IntoRealism renders, a Wan 5B clip, an H3 first-frame clip, an LTX 2.3 clip, a 30 s ACE-Step song and a song from each of the two .NET generators, HeartMuLa and DiffRhythm2) with fixed seeds and no cache, and writes `benchmarks/<host>-<date>.json`: seconds per phase, peak VRAM and lowest free RAM per test. A test whose models are not installed is skipped; one that fails (out of memory on a modest card) is recorded as failed with the reason, which is a result too.

```bash
python3 scripts/benchmark.py --list                      # the tests and whether their models are here
python3 scripts/benchmark.py --only sdxl,wan5b,h3        # some of them
python3 scripts/benchmark.py --gpu 0                     # on one GPU (index as nvidia-smi shows it)
python3 scripts/benchmark.py --keep                       # keep the pictures, clips and songs to look at them
python3 scripts/benchmark.py --compare benchmarks/desktop.json benchmarks/laptop.json
```

The two generators that run as .NET services (`heartmula`, `diffrhythm2`) need the server running (`./serve.sh --local`): the test starts the service through its API, so the model load lands in the measured VRAM, and stops it afterwards; the seconds until it was ready are the phase `service_start`, and a service that does not start is a failed test. Stop other GPU users (Ollama, a running render) first, or the figures are not comparable. The tests write `out/bench-*` and clean it afterwards; they do not touch your projects. With `--keep` the results are moved to `out/bench-results/<host>-<date>/<test>/` (git ignores `out/`) instead of being deleted.

**An `ok` only says the run ended.** The script does not look at what was drawn. Open the pictures and clips, or check a clip with ffmpeg (`ffmpeg -i clip.mp4 -vf signalstats,metadata=print:key=lavfi.signalstats.YAVG -f null -`: a mean luma of 255, or a file of a few KB for 2 s of video, is a blank clip). That is how the LTX problem below was found: with `sd` master-1010 the LTX test reported `ok` in 101 s and produced a white clip of 5 KB. The result files record the `sd` version (`machine.sd`); compare machines only when it is the same, which `scripts/build-sd.sh` now ensures by pinning it (see [INSTALL.md](INSTALL.md#stable-diffusioncpp-sd)).

**Results of the laptop** (`benchmarks/laptop-rtx4080-2026-10-25.json`: i7-13650HX, 30 GB of RAM, RTX 4080 Laptop with 12 GB, `sd` master-949; it had no Ollama and no internet access to external services, and the nine tests ran without any model failing). Seconds, peak VRAM over what was already in use, lowest free RAM:

| Test | Time | VRAM | Free RAM |
|---|---|---|---|
| SDXL, 4 images | 41 s | 7.1 GB | 25.5 GB |
| Krea 2, 4 images | 72 s | 8.2 GB | 24.2 GB |
| IntoRealism, 4 images | 98 s | 10.7 GB | 12.4 GB |
| Wan 5B clip | 77 s | 6.8 GB | 19.7 GB |
| MiniMax H3 clip | 122 s | 10.6 GB | 25.2 GB |
| LTX 2.3 clip | 127 s | 10.7 GB | 23.9 GB |
| 30 s song (ACE-Step XL) | 10 s | 6.0 GB | 28.0 GB |
| 40 s song, DiffRhythm2 (service) | 25 s | 2.5 GB | 22.1 GB |
| 60 s song, HeartMuLa (service) | 90 s | 10.9 GB | 19.6 GB |

Against the RTX 4070 machine the times are the same (x1.0 to x1.1); against the RTX 5060 Ti the laptop takes 70 to 80 % of the time in the video and Krea 2 tests (`--compare` prints it). **HeartMuLa leaves almost no margin on a 12 GB card**: the card showed 11.7 GB used of its 11.9 GB during the song (the service loads its model onto the GPU when it starts, 26 s here), so anything else on the GPU would make it run out of memory; DiffRhythm2 keeps its model in RAM and only borrows the GPU while it makes a song (21 s of synthesis for 40 s of music, 2 s to be ready). With 12 GB of VRAM even MiniMax H3 and LTX run (the CPU offload and `--mmap` of their engines), and free RAM never fell under 12 GB (IntoRealism, the tightest), so these tests do not call for more than 30 GB.

**LTX 2.3 and `sd` master-1010.** The first run of this laptop used the latest `sd` (master-1010, `012c7f8`) and gave a white LTX clip with `ok`. The raw AVI that `sd` writes was already white (luma 255 in every frame), so the fault is in `sd`, not in the assembly. Repeating the run by hand with `sd -v` ended without a warning (`exit 0`, sampling 83 s, decode 2.8 s), and three runs gave the same white clip. Building master-949 (`7867f6d`) with the same compiler and CUDA, and running the same command, gave a normal clip (luma about 100), and the full benchmark on it passed. The files of the first run are kept as `*-sd-master1010` for reference. Nothing was reported upstream yet.
