> **STATUS (2026-10-09): COMPLETED.** This plan was executed in two OpenSpec changes:
> `add-orchestrator-http-api` (Go bridge, archived) and `add-angular-management-frontend`
> (Angular frontend in `web/`). The decisions in item 4 were settled as follows: two changes;
> `web/` monorepo; `embed.FS` behind the `webui` build tag; run history in SQLite via GORM;
> Spanish (Castilian) with `@angular/localize` from v1. It is kept as a design reference.

# TASK: Plan an Angular management frontend for Veritmo with OpenSpec

You are a senior Go + Angular engineer. Your job in this session is ONLY planning
(OpenSpec `propose` flow): do not implement anything until I run `/opsx:apply`.

## 0. Mandatory reading before proposing anything
Read and summarise in 10 lines what you understand of:
- `openspec/config.yaml` (project context) and the current specs: if
  `add-script-to-video-pipeline` is still in `openspec/changes/`, tell me whether I should archive it
  (`/opsx:archive`) before creating the new change so that its specs move to
  `openspec/specs/` as the source of truth.
- `README.md` — CLI flags, `scenes.yaml`, hash cache, `--only`, `--keep-tmp`, `--gpu`.
- `docs/events.md` — the JSON-lines contract of `--json`. IT IS THE INTEGRATION CONTRACT
  of the frontend: do not change it, consume it.
- `cmd/veritmo` and `internal/` — only to understand how a run is launched and which
  packages exist (one capability per package).
- The specs of `add-image-output-mode`, `add-scene-importer` and `add-camera-motion`
  (archived in `openspec/specs/` or still in `openspec/changes/`): the `scenes.yaml`
  schema that the editor must support is the one resulting from those three changes, not
  the one in the original README. If any of them has not been applied yet, tell me: this plan
  depends on them.

## 1. Goal
An Angular web frontend that lets you manage Veritmo without touching the terminal:
edit `scenes.yaml` projects, launch runs with their flags, watch progress live,
review intermediate artifacts and the final video, and check the state of the environment
(binaries, models, voices, GPUs).

## 2. Architecture constraints (non-negotiable)
- The browser cannot launch processes: a bridge is needed. Propose it in Go inside
  this same repo (e.g. an `veritmo serve` subcommand or `cmd/server`) that
  exposes REST + SSE and launches the existing CLI/pipeline. No Python or Node in the backend.
- The bridge does NOT reimplement the pipeline: it reuses the `internal/` packages or invokes
  the binary with `--json` and re-emits the `docs/events.md` events over SSE unaltered.
- Run execution: one run at a time per GPU (same constraint as the CLI); a
  simple FIFO queue if several arrive.
- Frontend: latest stable Angular, standalone components, signals, OnPush, no
  NgModules. Component library: **Angular Material** (same major as Angular)
  with **Angular CDK**; dark theme by default. Do not add other UI libraries.
- **Angular conventions (mandatory, apply to `tasks.md`):**
  - One component per folder, with its three separate files:
    `nombre.component.ts`, `nombre.component.html`, `nombre.component.scss`.
    Never `template:` or `styles:` embedded in the `.ts`, not even for
    one-line components.
  - A `shared/` folder for everything reusable across features (components,
    directives, pipes), and one folder per feature (`projects/`, `runs/`, `editor/`,
    `gallery/`, `environment/`...). No shared components inside a
    feature.
  - Every table comes with a paginator (`mat-paginator`) with `pageSizeOptions` fixed to
    `[4, 10, 20, 50, 100]`, and the paginated table is a reused `shared/` component,
    not a `mat-table` + `mat-paginator` copied into each view. The chosen page
    size is remembered per table for the duration of the session.
  - Per-component SCSS consuming the theme tokens (`--mat-sys-*` and our own);
    no hardcoded colours or sizes in the component `.scss` files.
- Respect the repo conventions (Apache-2.0 licence "Copyright (c) 2026 Jesús Suero",
  `CLAUDE.md`, `go test ./...`, `openspec validate --strict`).
- The current CLI does not change its behaviour or its flags. If the bridge needs something
  new from the core, propose it as a separate capability and justify it.

## 3. Candidate functional scope (validate it, cut whatever adds nothing in v1)
- **Projects**: list/create/duplicate/delete projects (`scenes.yaml` + their `tmp/`,
  `out/` folders). Editor for `defaults` (including the global `negative_prompt` and `subject_prompt`)
  and for scenes with validation identical to the Go one: unique ids, non-empty `prompt`,
  `tts_text` required unless the scene declares `silence_seconds`,
  `resolution` WIDTHxHEIGHT, optional `seed`, `speed`, `lora`. Controls for `motion`
  (type + intensity) and `look` (grain, white balance) with the presets defined by
  the specs, without exposing raw ffmpeg expressions. Dropdowns for `model`, `voice` and
  LoRAs filled from `models/`, `voices/` and the `lora-model-dir`.
- **Output mode**: `video` / `images` selector per project (= `output.mode`).
  In images mode, the audio/montage fields are hidden or dimmed and `variants`
  appears per scene.
- **Gallery** (images mode): view of the PNGs in `out/<project>/` reading the
  `manifest.json` left by `image-export` (scene, variant, effective seed, final
  prompt), with the actions "fijar esta seed en la escena" (pin this seed on the scene) and "re-renderizar esta escena" (re-render this scene).
  The gallery is built by the UI; the backend only serves files and the manifest.
- **Import**: upload a JSON/HTML with the format accepted by `veritmo import` and
  create a new project from it, showing the report of what was mapped and what was
  discarded before saving. The UI does not reimplement the parsing: it calls the subcommand.
- **Runs**: launch with `--gpu`, `--keep-tmp`, `--force`, `--only <id>`, `--vram-warn-mb`;
  always with `--json` underneath. Live progress per scene and per phase
  (tts → probe → render → clip → concat). Cancellation. Run history with its logs.
- **Cache**: show which scenes are cached and which phases would be re-run
  (remember that `motion`/`look` and the global fields go into the hash: changing a
  preset invalidates the clip, not the render nor the audio); a "re-render only this
  scene" action (= `--only`). Warn if `--keep-tmp` is disabled and therefore the next
  run will start cold.
- **Artifacts**: per scene, play the `.wav`, view the `.png`, play the intermediate `.mp4`;
  player for the final video and download.
- **Environment**: presence/version of `piper`, `sd`, `ffmpeg`, `ffprobe`, `nvidia-smi`;
  list of GPUs (`nvidia-smi -L`) with free VRAM; explicit warning about the Ollama caveat
  (`OLLAMA_KEEP_ALIVE`) if Ollama is detected on the chosen GPU. Launching the `scripts/`
  scripts from the UI is OUT of v1 (they are long and one-off) — only show
  whether anything is missing and which script fixes it.

## 3b. Visual design (mandatory for the frontend change)
If the `frontend-design` skill exists (`.claude/skills/frontend-design/SKILL.md` or
`/mnt/skills/public/frontend-design/SKILL.md`), read it in full before designing and
apply it. If it does not exist, tell me and apply the following, which is its core:

- **Two passes before touching code.** First a compact design plan that will go
  in the change's `design.md`: a base palette of 4–6 named colours with hex values, the
  typefaces and their role, a layout concept with ASCII wireframes (scene editor,
  run monitor, gallery, environment) and 3–4 principles for what makes
  this interface unique. Then review that plan and discard everything you would do the same for
  "any dashboard"; say what you change and why. Only then are the implementation
  tasks planned.
- **Start from the subject, not from the template.** This is a render control desk:
  phase timings, VRAM, seeds, long prompts, thumbnails and video. The aesthetic comes
  from there (telemetry, console, darkroom, moviola), not from a SaaS kit.
- **"AI-generated" tells that are forbidden** unless I ask for them: cream
  background with serif and terracotta accent; black with a single acid/neon-green accent;
  everything chopped into identical cards with the same radius and the same grey shadow;
  UPPERCASE eyebrows above every title; a single word of the headline in colour or
  italics; metadata joined with middle dots; monospace only to "look technical";
  "→" arrow on buttons and links; entrance animations on every section and hover on
  every card. Boldness is spent on ONE memorable element (probably the live run monitor
  or the gallery) and the rest stays quiet and disciplined.
- **Typography with intent:** one or two clearly distinct families, a defined type
  scale, text lines under 80 characters. Nothing from the framework's default
  family.
- **Structure = information.** Borders, dividers, numbering and groupings only
  when they encode something (the phases of a run ARE a sequence; so are the scenes of a
  project; the environment panels are not).
- **Motion only as a response to an action** (open, expand, confirm, a phase
  finishing). At most one orchestrated moment in the whole app.
- **Interface text:** active voice, the button says what it does ("Launch run", not
  "Submit"), the same name throughout the flow, errors that say what happened and how to
  fix it, empty states that invite action. No marketing copy.
- **Quality floor, unannounced:** responsive down to mobile (even if the main use
  is desktop), visible keyboard focus, `prefers-reduced-motion` respected,
  accessible contrast, dark theme by default with an optional light one.
- **Angular Material, but themed from the design plan, not the other way round.** Use
  Material 3 theming (`mat.theme()` / `mat.define-theme` in SCSS, `--mat-sys-*`
  CSS variables) so that the palette, typography and density of `design.md` are
  the theme; the default theme (indigo/pink, Roboto, standard radius and density) is
  forbidden as a final result. Without a custom theme applied there is no Material component
  on screen.
- Use Material components for what they provide (accessibility, keyboard, states,
  overlays, forms), not to chop the screen into `mat-card`s: the structure
  of each view is decided by the design plan from the previous point, and a `mat-card` only
  appears where a card encodes something. List explicitly in `design.md` which
  Material components each view uses and why.
- Angular CDK for what Material does not cover: virtual scroll in long lists
  of scenes/events, drag & drop to reorder scenes, custom overlays if
  needed, `BreakpointObserver` for responsiveness.
- Typography: Material's is overridden with the family chosen in the plan via the
  theme's typography tokens; no default Roboto.

## 4. Decisions you must close WITH ME before writing specs
Ask me one at a time, with your recommendation and why:
1. One change or two? My preference: `add-orchestrator-http-api` (Go: serve, REST, SSE,
   environment, artifacts) and `add-angular-management-frontend` (dependent on the first).
   Justify it if you think it should be a single one.
2. Where does the frontend live: `web/` in this repo (monorepo) or a separate repo? Bear in mind
   that it might later be packaged with Tauri as a desktop app.
3. How is it served in production: the Go binary embeds Angular's `dist/`
   (`embed.FS`) or they ship separately?
4. Persistence of run history: files on disk (`.orchestrator/history/`,
   one JSON per run) vs SQLite. My bias: files, no DB.
5. UI language (Spanish by default) and whether i18n is needed in v1.

## 5. Expected OpenSpec process
1. Resolve the questions in item 4.
2. `/opsx:propose` for each agreed change. In each one: `proposal.md`, `design.md`
   (decisions + risks: SSE vs WebSocket, run concurrency, artifact paths
   outside the root, duplicated Go/Angular validation), `specs/<capability>/spec.md` with
   SHALL requirements and technology-neutral WHEN/THEN scenarios, and `tasks.md` with
   small, verifiable tasks (each one says how it is checked).
3. Minimum new capabilities I expect to see, name them better if appropriate:
   `project-management`, `project-import`, `run-control`, `run-event-streaming`,
   `artifact-access`, `environment-status`, `scene-editor`, `run-monitor`,
   `image-gallery`.
4. `openspec validate <change> --strict --no-interactive` green in each change.
5. For the frontend, the design plan from item 3b (palette, typefaces, layout,
   principles) goes inside `design.md`, and `tasks.md` includes as the first UI task
   creating the token system from it, before any component.

## 6. Output of this session
Summary of the changes created, their location, questions that genuinely remain
open (not decisions you skipped) and the `/opsx:apply` command to start with.
Do NOT implement anything.