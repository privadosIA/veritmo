# Management server and dashboard

[← Back to the README](../README.md)


`veritmo serve` starts a local REST + SSE server (default
`127.0.0.1:4666`) for managing projects, launching/monitoring runs,
browsing artifacts and checking the environment — the backend of the
Angular management frontend. It reuses the same pipeline packages and
re-emits the `docs/events.md` event stream verbatim over SSE; the CLI's
flags and behavior are unchanged.

```bash
bin/veritmo serve --addr 127.0.0.1:4666 --projects-dir projects
```

**One-command launcher.** `./serve.sh` (in the repo root) starts the server (after `bin/veritmo data migrate` has created or updated its database: the server never changes the schema and refuses to start while migrations are pending, see [DATA_STORE.md](DATA_STORE.md)), and only the server: it builds,
looks for and serves no front end. It listens on `0.0.0.0:4666` and is protected by a bearer token. The token is created
on first run and stored in `~/.config/veritmo/token`; it is not printed on the screen unless you start with `./serve.sh --show-token` (read it with `cat ~/.config/veritmo/token`). Paste it in the **Connection** of
the dashboard you use. It builds the binary (`bin/veritmo-api`) on every start. To serve a front end from the
same process, start the binary yourself with `--web-dir` (see below).

```bash
./serve.sh                # the server, reachable from the network
./serve.sh --local        # same, only on 127.0.0.1:4666 (use an SSH tunnel)
./serve.sh --stop         # stop it (clean shutdown; a run in progress is interrupted)
```

Environment overrides: `ADDR` (wins over `--local`), `PROJECTS_DIR`, `MEDIA_DIR` (see below),
`NO_BUILD=1` (start the binary as it is) — the server is otherwise rebuilt on every start, which takes a second or two when nothing changed and `MEM_MAX` (memory cap through systemd-run, **85% of the RAM by default** (about 26G on 32 GB, 52G on 64 GB) so a large model cannot hang the machine; use `MEM_MAX=0` for no cap or another size such as `MEM_MAX=24G`, the value for a 32 GB machine with a desktop: see [INSTALL.md](INSTALL.md#machines-with-32-gb-of-ram)).
`./serve.sh --help` lists them.

**Content for adults.** A project can be marked in its `scenes.yaml` with `adult: true` (for people of 18 or older), `adult: false` (looked at, fine for everyone) or nothing (not looked at); the editor and the new-project dialog set it. The server starts **censored**: a project marked `adult: true` does not exist for it. It is left out of the project list, the gallery, the runs and their history, the statistics, the storage report and the music library, and any request that names it answers `404`; nothing can be created, imported, duplicated or saved with `adult: true`. `veritmo serve --strict` (or `STRICT=1 ./serve.sh`) is stricter: it also hides every project that was not marked `adult: false`. The mode is chosen when the server starts and cannot be changed while it runs; `GET /api/environment` reports it as `content_mode` and the Environment screen shows it.

Projects are rows of the database (see [DATA_STORE.md](DATA_STORE.md#the-projects-in-the-database)) and live as subdirectories of `--projects-dir` (each holding its sources, its `tmp/` and `out/` and the working copy of its `scenes.yaml`, which the server writes before a run and takes back after it: **editing that file by hand has no effect**, change a definition with `PUT /api/projects/{id}/config` or `veritmo project push`; with a media directory those are links, see below), run history is kept in SQLite at
`.orchestrator/orchestrator.db`, and a front end is served at `/` from the folder given with
`--web-dir` (see below). See `docs/http-api.md` for every
endpoint, the SSE event types, and the error shape.

**Front end.** `--web-dir DIR` (or `WEB_DIR`; `./serve.sh` takes it from `WEB_DIR` or, if that is not set, from the path written in `~/.config/veritmo/web-dir`; `WEB_DIR=off` ignores the file) serves a built front end, for example `dashboards-lab/dist/dashboard-full/browser`, at the root of the same port. A Tailscale or tunnel address that points at the server then shows the dashboard with no CORS to set up; without it the root is a plain note and the server is only the API.

**Media directory.** `--media-dir DIR` (or `MEDIA_DIR`; `./serve.sh` takes it from `MEDIA_DIR` or, if that is not set, from the path written in `~/.config/veritmo/media-dir`; with neither, and with `MEDIA_DIR=off`, projects stay whole in `--projects-dir`) is where the projects really live, so the disk of the server holds only the server. Each project is a folder `DIR/<id>/` with `config/` (`scenes.yaml`, its `scenes.yaml.*` backups, `sources/`, `.reviews/`, `motion/`), `out/` and `tmp/`. In `projects/<id>/` the same names are symbolic links into it (plus the `models` and `voices` links of the install), so every path in a manifest, a run or the API (`out/<id>/manifest.json`) keeps working and nothing else needs to know. Anything that copies `projects/` must follow links (`rsync -L`), or copy `DIR` instead. The server creates and checks the directory at start and refuses to start if it cannot be written; if the disk goes away later, a run is refused (`503`) and nothing is written to the local disk. Projects made before are moved with the server stopped: `bin/veritmo-api media migrate --dry-run` lists what would move and the space it needs, and without `--dry-run` it copies, checks every file and size, and only then replaces the originals by links; it can be repeated. `GET /api/storage` gives `media.dir` and the free space of that disk, for a client that writes there too.

**The environment as the default.** A connection (the database, Ollama, the ctxpress service, the two music services, the monitoring tools, the browser origins) can be given by environment variables, as a default under what is saved in Settings; the variables, the rule and the `source` of each answer are in [DATA_STORE.md](DATA_STORE.md#the-environment-as-the-default-of-a-connection). The [Compose stack](COMPOSE.md) sets all of them for the pieces it starts (the database, Ollama, the music generators, Prometheus, Grafana, Loki and Redis), so nothing is typed in Settings.

**Which browser pages may call the API (CORS).** The token decides who may use the API; this decides which web pages a browser lets call it from another origin, and it is the server's decision, on this machine only. Two settings, kept in the database: `cors_origins` (exact origins such as `http://192.168.1.50:4200`) and `cors_networks` (CIDR ranges such as `192.168.1.0/24` or the tailnet `100.64.0.0/10`; a page whose host is an IP inside one is allowed on any port; a host *name* is allowed only by `cors_origins`). Change them with `veritmo cors add <origin>`, `veritmo cors add --network <range>`, `veritmo cors remove ...` and `veritmo cors list`, (they write the database the server uses): a running server follows the change within 5 seconds, no restart (settings that cannot be read keep the lists it had; a hand edit of `settings.json` has no effect, see [DATA_STORE.md](DATA_STORE.md)). `--cors-origin` still works and adds to them. No API endpoint reads or changes these lists, so a dashboard cannot widen its own access. Nothing configured means same-origin only; never `*`, never credentials. A range opens browser access to every machine in it (the token is still required). A page that is not allowed does not get an HTTP error: the browser blocks the answer and the page sees "Failed to fetch"; the server writes one line to its log the first time it sees each refused origin, with the command that would allow it.

**Models directory.** The model files (`models/`, hundreds of GB) can live on another disk too: `--models-dir DIR`, or `MODELS_DIR`, or the path written in `~/.config/veritmo/models-dir` (`MODELS_DIR=off` ignores the file; with none of them nothing changes). `models` in the repository root is then a symbolic link to `DIR`, so every `models/<file>` in a `scenes.yaml`, the engines, the installer and the music services keep working, and installing a model writes to `DIR`. Move what is there with the server stopped: `bin/veritmo-api models migrate --dry-run` says what it would copy and the space it needs; without `--dry-run` it copies (skipping what is already there with the same size, so a cut run resumes), checks every name and size, renames `models` to `models.migrated`, makes the link and only then deletes the old copy. `voices/` and weights kept outside the repository are not moved. At start the server warns if `models` is not yet that link or the directory is not there; while the disk is not mounted nothing is installed (it never writes a model to the local disk) and `GET /api/environment` and `GET /api/storage` give `models_dir` / `models` with `available: false`. Both give the path and the free space of that disk, read on every request, so a dashboard can show how much is left when the models and the projects are on separate disks.

**API reference.** The **API** screen (menu, after Environment) shows every endpoint with Swagger UI, bundled with the dashboard, and lets you try requests against the connected server using the token you set in «Connection». The description itself is `GET /api/openapi.json`, behind the same token as the rest of the API.

**Statistics.** The **Statistics** screen (menu, next to Runs) summarizes the run history for a project and period (7, 30, 90 days or all): runs per day split into correct, failed and others, the duration of each run with its median, time per pipeline phase (tts, render, animate, clip…), how much of the work was reused from the cache, and use per GPU, plus a table of the slowest runs that opens their monitor. Every chart can be switched to a table with the same numbers, follows the dark/light theme and fits a phone screen; they are drawn in plain SVG, with no charting library. The figures come from `GET /api/stats`, computed from the stored runs and events, so your existing runs are included.

**Resources of a run.** While a run is active the server records, every 2 seconds, the VRAM and utilization of each GPU the run occupies and the machine's RAM (`GET /api/runs/{id}/resources`), and the run's monitor shows them as curves with their peaks, with the peak VRAM per GPU also on the Statistics screen. Two things to keep in mind: the VRAM is that of the **whole GPU** during the run, so it includes anything else using the card (the desktop, Ollama, another run), not only the run's own processes; and only runs launched after this feature have samples (older runs just have durations, phases and cache). Sampling stops for a run after 20 000 stored rows, and deleting a run deletes its samples.

**Cleaning the history.** In the **Runs** screen you can delete finished runs from the history one at a time (trash icon), by selection (checkboxes, "select all finished in the list") or all at once ("delete all finished", which respects the project filter). Each deletion asks for confirmation, says how many runs go, that it cannot be undone and that, unless you tick **"Also delete the generated files"**, the files the runs generated (videos, images, `tmp/`) are kept: only the history rows and their stored events are removed. With the box ticked each deleted run also loses its output folder `out/<name>/` and the project's `tmp/` (the cache and the scene pictures the editor shows as thumbnails, so they disappear too and the next run starts from scratch; projects with a queued or running run keep their files; the result line says how many folders went). Runs of the same project that wrote to the same `out/<name>/` share it, so deleting the files of one removes what the others point to. Queued or running runs are never deleted (cancel them first); in a batch they are skipped and reported. The API equivalents are `DELETE /api/runs/{id}[?delete_files=true]` and `POST /api/runs/delete` (`"delete_files": true`) (by `ids` or by a `project` / `states` / `before` filter). SQLite reuses the freed space but the file does not shrink by itself.

## Models, voices and LoRAs

The UI's **Models** screen (and `GET/POST/DELETE /api/assets`,
`/api/downloads`) lists what is installed in `models/`, `voices/` and
`models/loras/`, installs from a built-in catalog (SD 1.5, SDXL, Piper
voices), a direct URL or a Hugging Face `repo` + `file`, shows live
progress and deletes. It is the in-app alternative to
`scripts/get-models.sh` / `get-piper.sh`. LoRA files are stored and listed
but the pipeline does not apply them yet. For gated Hugging Face repos,
export `HF_TOKEN` before starting the server (accept the model's license on huggingface.co with that account first; a refused download explains these steps).  The Models tab shows, in a strip above the tabs, whether the server has each key (Hugging Face) and lets you paste, replace or remove it right there (a masked field with Save and Remove; the key is written to a file with permissions `0600`, never shown again, and used by the next download without a restart). Each key is saved in a file: the Hugging Face key in the standard Hugging Face token file (`~/.cache/huggingface/token`, or `$HF_HOME/token`), so Hugging Face's own tools share it; an environment variable (`HF_TOKEN`) still wins over the file, and the screen says so; catalog entries that need a key say so (a warning while it is missing, but Install stays available because the server explains a refused download), and files the catalog declares as a text encoder or VAE are labelled "pieza de apoyo".  Four tabs: **Image**, **LoRA**, **Video** (Wan video-generation bundles of three files, e.g. Wan 2.2 TI2V 5B) and **Voz** (voices, video mode only; besides English ones it offers Spanish voices from Spain (davefx, carlfm), Mexico (claude, a woman; ald, a man) and Argentina (daniela, a woman); `sharvard` holds two speakers, a man and a woman, chosen with `speaker`). A scene can narrate with its own voice (`voice:` in `scenes.yaml`, picked in the editor) and `POST /api/tts/preview` speaks a text with any installed voice so it can be heard before a run. LoRA and video models can be installed but the pipeline does not use them yet. See `docs/http-api.md`.

## Settings: where Ollama is

The prompt assistant, "describe a reference" and the clip quality check talk to an
[Ollama](https://ollama.com). Its address comes, in this order, from the **Settings**
screen (`/settings`), from `OLLAMA_HOST` and from the default
`http://127.0.0.1:11434`. The screen shows the address in use and which of the three it
is, tests an address (version, models and which of them read pictures) before saving it,
and a saved one applies **without restarting** the server. It is stored in
the server's database (the settings were in `~/.config/veritmo/settings.json` before; [DATA_STORE.md](DATA_STORE.md) says what stays there); use "Use the environment's" to go back to `OLLAMA_HOST`. The command line (`assist`,
`describe`, `check`) reads the same settings.

When the address is not this computer (anything but `localhost`/`127.0.0.1`/`::1`) the
screen warns that the images, clips and texts those features send **leave this machine**
for it. Details in `docs/http-api.md`.

### Providers and API keys

Besides Ollama, the **Providers** card of the Settings screen registers other model providers: another Ollama (a machine of your network) or any server that speaks the OpenAI protocol (OpenAI, OpenRouter, Together, Groq, vLLM, LM Studio, llama.cpp). Each has a name, an address, a text model and a vision model, and a **Test** button that lists the models it offers. The providers are saved in `settings.json`; their **API keys** go to a separate file, `~/.config/veritmo/keys.json` (mode 600), are write-only (the screen never shows them again; an empty key field while editing leaves the saved one, and a button removes it) and are never sent over plain http to another machine. Which provider runs each task is chosen in the **Tasks** card, below. Details in `docs/http-api.md`.

### Tasks: which provider runs what

The **Tasks** card of Settings has one row per task (the prompt assistant, describing a picture, describing a clip, the clip quality check, and writing the page of a motion graphic or overlay) with a provider selector, the model that will be used and, when the provider is not this computer, a **☁ external · name** badge. Every task starts on the built-in Ollama and nothing changes until you choose. Choosing a **remote** provider opens a dialog that says what the task sends (texts, pictures and clips, or frames of your clips), to which provider and address, and the choice is saved only if you accept; the acceptance is tied to that address, so if you change the provider's address you accept again. The same badge appears next to the buttons that use the task: Improve with AI, Suggest motion, Describe a reference, writing a song with AI, the videoclip plan with AI and the quality check. A provider without the model a task needs is disabled in that row with the reason. A job on a remote provider makes at most **60 calls** (change it in the same card); the editor shows "up to N calls to X" before you start. **Controls follow availability**: when a task cannot run, its buttons are not shown (Improve with AI, Suggest motion, Write with AI for songs, the videoclip plan's AI, the review's "let the assistant propose it", Describe a reference). If the provider answers but has not the right model, a short notice takes their place and says what to install (for an Ollama, `ollama pull qwen3:8b` for text or `ollama pull gemma4:26b` for pictures) with a link to Settings; if nothing answers, they are simply hidden. The dashboard asks when it starts, after a change in Settings and when the window regains the focus, and the server keeps the answer 30 s. The quality check's *Analyze* stays (it measures without a model). **Nothing switches provider by itself**: after a refusal the editor offers the other providers (the local Ollama among them) with *Try with*, and only your click calls it. The terminal (`assist`, `describe`, `check`) follows the same routing and accepts `--provider`; a remote provider that was not accepted in Settings makes the command stop and say so.

## Settings: the database of the run history

By default the run history, the statistics, the reviews and the speed measurements are kept in a SQLite file (`.orchestrator/orchestrator.db`). The **Database** card in Settings lets it live instead in a **MySQL** or **Postgres** server, so that several servers share it and it can be reached from another machine:

1. In the database server create a database and a user of their own (for MySQL: `CREATE DATABASE veritmo CHARACTER SET utf8mb4; CREATE USER 'veritmo'@'%' IDENTIFIED BY '…'; GRANT ALL PRIVILEGES ON veritmo.* TO 'veritmo'@'%';`). The tables are created by the server; nothing else in that server is touched.
2. In Settings choose the engine, fill the server, port (3306 / 5432 by default), database, user and password, press **Test connection** (it opens the connection and asks the version; it creates and changes nothing) and **Save**. The password is kept in the settings file (mode 0600, like the other keys) and is never shown again; leaving it empty when saving again keeps the saved one.
3. **Copy the current history to that database** moves the SQLite history to the new one (it can be repeated: what is already there is left as it is, and it is all done in one transaction).
4. **Restart the server**: the engine is read only when it starts. Until then the card says what is in use and what is saved.

Each server keeps its **own projects and files**; only the history is shared. Every run records the **server** that made it (the name set in the same card, by default the computer's name), so Runs shows a *server* column, Statistics can be limited to one server (`GET /api/stats?server=`) or to one client of the generation API (`GET /api/stats?client=`, with `by_client` in the answer), and the speed measured for a card is each server's own. The runs of another server appear as runs with no result here (their files are not in this disk). A restart marks as interrupted only **its own** unfinished runs. Go back to SQLite by choosing it again and restarting. The command line (`--db`) keeps using the SQLite file at that path.

## Settings: monitoring tools (Grafana, Prometheus, Loki, Redis)

The **Monitoring** card keeps the address, user and key of the tools you run beside the server, so the addresses live in one place and can be tested. **The server does not depend on any of them** and does nothing with them on its own: they can be switched off, and a tool that does not answer is a normal answer of the test, not an error. Each one has an address (`host:port` or a URL; the usual port is filled in: Grafana 3000, Prometheus 9090, Loki 3100, Redis 6379), a user and a key. With a **user** the calls use Basic authentication; with **no user** the key is a token sent as a Bearer (for a Grafana service account, say). For Redis the key is the password. The key is kept in the settings file (mode 0600) and never shown again; saving again with it empty keeps the saved one while the address and the user are the same.

**Test** asks each tool its health and, with a key, something that needs it: Grafana `/api/health` and `/api/org`, Prometheus `/-/healthy` and its version, Loki `/ready` and its version, Redis `PING`. It writes nothing anywhere. **Open** links to the page of Grafana, Prometheus or Loki. A ready-made Grafana dashboard over the run history is in [grafana/](grafana/README.md). `GET /api/settings/monitoring`, `PUT /api/settings/monitoring/{tool}` and `POST /api/settings/monitoring/{tool}/test` are in `docs/http-api.md`.

## Generating a video or editing a picture from another client

A dashboard, a script or a phone app does not need to copy the planning of any dashboard or the `scenes.yaml` format: `GET /api/generation/options` lists what this machine can do, `POST /api/generations` makes a video and `POST /api/image-edits` retouches a picture, each in one call (each dashboard can name itself with `client`, for example `video-studio`, and the statistics and the metrics then group by it) (see [http-api.md](http-api.md#generation-a-video-or-a-picture-edit-in-one-call-video-generation-api-image-edit-api)). The server creates each one as a hidden project (`api-gen-…`, `api-edit-…`, kept under the projects folder but not listed with yours), so disk use shows in the generations, not in the storage per project; they are removed with `DELETE`, and the server never deletes them on its own. The same goes for a video you already have (`POST /api/video-edits`: scale, frame rate, reframe, remove background, depth, extend, motion control, with a library of motions at `/api/motions`); its contract is in `openapi.yaml`, it is explained in [VIDEO_EDIT.md](VIDEO_EDIT.md) and described in [http-api.md](http-api.md#video-editing-a-video-you-already-have-in-one-call-video-edit-api-video-edit-motion-library). The tools are listed by `GET /api/video-edits/options`, each with why it is not available when it is not.

A request can also end the video on a **last picture** (`end_image`) with the options that say so, or be guided by **references** (several pictures, videos and audios) with `h3-ref`; `GET /api/generation/options` gives each option its `inputs` with the limits for this card, a generation reports the files it used (`inputs`) and `reuse_inputs_of` repeats it without uploading them again. A clip that ends on a picture plays once and holds its last frame; the scene format has the same inputs (`end_image`, `references`, see [SCENES_YAML.md](SCENES_YAML.md)).

A longer video repeats the same motion: the engines make a clip of about 2 to 4 seconds and the scene is stretched and looped, never chained (chained clips lost contrast and colour). The client sees `clip_seconds` in the options and the answer.

## Metrics for Prometheus

`GET /api/metrics` gives the state of this server in the text format Prometheus reads: the runs by state, client and kind (`video`, `edit`, `models3d` or `project`) and how long the successful ones took (a histogram with the same labels), the database, the RAM, the free disk, each GPU (video memory, use, temperature, power; only when `nvidia-smi` is there) and whether each music service runs or is loading its model. It is worked out when it is asked and says nothing of other servers (when several share a history, each one counts its own runs). It never names a project in a label, so a project hidden by the content mode is not given away. The `client` label is the name each dashboard gave itself in the generation API (`embedded` for the ordinary projects, `unknown` when none was given); only the 20 clients with most runs are named and the rest add up under `other`. If you built Grafana queries that compare the exact set of labels of `veritmo_runs` or of the histogram, review them: the ones that add up by `state` or by `le` keep working. It needs the same access token as the rest of `/api`.

To have Prometheus read it, put the token (the content of `~/.config/veritmo/token`) in a file on the machine that runs Prometheus, readable only by it, and add a job:

```yaml
scrape_configs:
  - job_name: veritmo
    metrics_path: /api/metrics
    scrape_interval: 30s
    authorization:
      credentials_file: /etc/prometheus/veritmo.token
    static_configs:
      - targets: ['<address of the server>:4666']
        labels: { }
```

Then reload Prometheus. Import [grafana/veritmo-machine.json](grafana/veritmo-machine.json) in Grafana (data source: that Prometheus) for the machine panel; [grafana/README.md](grafana/README.md) explains it and the other one, over the history.

## Limits

The server protects itself from a client that asks for too much. Every limit answers with a plain error that names it.

| What | Limit | Answer |
|---|---|---|
| Time to send the request headers | 15 s (an idle connection is closed after 2 min) | the connection is closed |
| Body of a request | 1 MiB for a JSON message; the endpoints that take uploads, packages or long texts say their own (a source 256 MB, a project package 1 GB, an import text 16 MB) | `413` |
| A `scenes.yaml` sent to the server | 1 MiB | `413` |
| Runs waiting for a GPU | 20 in all, 5 for the same project | `429 queue_full` |
| Model downloads waiting | 20 | `429 queue_full` |
| Drafts of parts written at once | 4 | `429 queue_full` |
| Failed authentications from one address | 10 a minute, then `429 too_many_attempts` with `Retry-After`; a valid token is never limited | `429` |

The failed-authentication limit counts the address of the connection. Behind a reverse proxy every client shares the proxy's address, so put the limit in the proxy as well (Caddy, Apache, Cloudflare). A project package is read from a temporary file and copied to the project file by file, and a program the server starts is stopped with everything it started when its run is cancelled.

## Remote use

To run the UI against a server on another machine, start the server with a
token and allow the UI's origin (a non-loopback `--addr` without a token is
refused):

```bash
export VERITMO_TOKEN="$(openssl rand -hex 24)"
bin/veritmo serve --addr 0.0.0.0:4666 --cors-origin http://my-laptop:4200
```

The server warns when little disk space is left (the Environment screen, the
projects list and the launch form of Runs; a run is never refused for it): the
threshold is 20 GB, or `VERITMO_MIN_FREE_GB` (`0` turns the warning off).

Then open the UI's **Connection** button, enter `http://server:4666` and the
token (or ship defaults as `{"server": "...", "token": "..."}` in a
`config.json` next to the app). Serving the UI from the same server (`--web-dir`) needs
none of this: just browse to it. The token is sent in clear over HTTP, so
prefer a VPN, a TLS proxy or `ssh -L 4666:127.0.0.1:4666 host`. Details,
tickets for SSE/media and the security model: `docs/http-api.md`.

## HTTPS for the microphone (Tailscale)

A browser only lets a page use the microphone on a secure origin: `https://` or `localhost`. Over plain `http://100.x.y.z:4666` the dashboard hides the microphone and says why. If the machine is on a Tailscale network, `tailscale serve` publishes the server there over HTTPS with a valid certificate, without opening anything to the internet:

```bash
export VERITMO_TOKEN="$(openssl rand -hex 24)"
bin/veritmo serve --addr 127.0.0.1:4666        # the token is read from VERITMO_TOKEN
tailscale serve --bg --https=443 http://127.0.0.1:4666
tailscale serve status                               # shows the https://<machine>.<tailnet>.ts.net address
```

Open that address on the phone. **Start the server with the token**: `tailscale serve` reaches it from the same machine, so the server sees a loopback connection and, without a token, would answer anyone on the tailnet. Enter the token in the **Connection** button once. HTTPS must be enabled for the tailnet in the Tailscale admin console (DNS → HTTPS certificates). `tailscale serve reset` undoes it. Exposing the server to the internet (Tailscale Funnel, a Cloudflare tunnel) is not needed for this and is not described here.


Dictation (`POST /api/transcribe`) reads the recording with the same Whisper as the lyrics reader: see below. To try the phone screen on this machine, an Android emulator reaches the server with `adb reverse tcp:4666 tcp:4666` and `http://localhost:4666` (a `localhost` page counts as secure; `10.0.2.2` does not), and the iOS simulator shares the Mac's network, so `http://localhost:4666` works there too.

## Install it as an app on a phone

The dashboard is a Progressive Web App: from a phone it can be installed and then opens full screen, with its own icon, like any app. It needs no store, account or signing, but it **needs HTTPS** (the same address as for the microphone, see above); over plain `http://` it works as a page and the browser does not offer to install it.

- **Android (Chrome):** open the HTTPS address, menu ⋮ → *Install app* (or accept the offer the browser makes). Chrome shares its storage with the installed app, so the token you entered is already there.
- **iPhone (Safari):** open the HTTPS address, Share → *Add to Home Screen*. An installed iOS web app has **its own storage, separate from Safari**: open *Connection* once and enter the token again.

The app keeps only its own files (screens, styles, fonts, icons), so it opens quickly; it never keeps your projects, pictures, songs or the token. If the server cannot be reached it opens anyway and says so, with a *Retry* button. After rebuilding the server (`REBUILD=1 scripts/serve.sh`) the installed app notices the new build the next time it is opened or brought to the front and offers an **Update** button; the connection settings are kept. If an installed copy ever stays on an old build, remove the app and install it again.

## Reading the lyrics of a song

The music studio's **Take it from a video or audio** reads lyrics with whisper.cpp's `whisper-cli` and a Whisper model (`models/whisper/`), on this machine's CPU, so it takes no GPU and sends the audio nowhere. Install both once with `bash scripts/get-whisper.sh` (see `docs/INSTALL.md`); the Environment screen shows whether they are there and the Models screen (Music tab) can install the model. `POST /api/projects/{id}/transcribe` is refused with `409` while a run is queued or active (the model needs memory) and with `503` when the memory is short; `POST /api/projects/{id}/measure`, which estimates the tempo and the key without a model, never waits for runs. See `docs/http-api.md`.

The words of a song of the music motion mode are timed with the same Whisper (the time of each token), and, by default when it is installed (`separate: auto`), on the voice taken out of the song by demucs.cpp (`bin/demucs` and `models/demucs/`, optional, on the CPU, about 6 GB of memory and 0.8 times the length of the song with four threads: `bash scripts/get-demucs.sh --build`); `GET /api/environment` lists `demucs` and its weights (`separation`). See [MUSIC_MODE_MOTION.md](MUSIC_MODE_MOTION.md#timing-the-words).

A spoken request is read with `POST /api/transcribe` (the recording as the body, up to 60 seconds and 5 MB), with the same `whisper-cli` and model, on the CPU, and nothing of it is kept. It does not wait for runs (only for memory) and it does not drop repeated lines. Measured here (large-v3-turbo q5, 8 threads, no GPU): 6 to 10 s for a 3 s sentence and about 12 s for a 9 s one, of which about 6 s is loading the model; giving `?language=` skips the language detection and saves about 3.5 s. A silent recording of 4 s came back as "Thank you.", so that line and a few like it are dropped.

## The browser for motion graphics

Motion-graphic scenes and overlays are drawn with Chrome or Chromium (see `docs/SCENES_YAML.md`). It is found by itself; the **Browser** card of Settings shows the one in use with its version and lets you save the path of another (`PUT /api/settings {"browser": "/usr/bin/chromium"}`; an empty text goes back to searching the `PATH`; a path that is not an executable file is a `400`). The Environment screen lists it as an optional binary (`chrome`). Blender (3D scenes, see `docs/SCENES_YAML.md`) is listed the same way as `blender`, with its version and the devices Cycles can use, or the reason it cannot be used; a `.blend` is uploaded as a source and listed with `kind: blend`. The **Model for motion graphics** card chooses the model that writes the pages (`PUT /api/settings {"motion_model": "gemma4:26b"}`): writing HTML is writing code, so a bigger model than the prompt assistant's pays off; empty uses `gemma4:26b`, or `gemma4:31b`, when the Ollama has it installed (on the same briefs it kept the words and the layout of the description where a coding model cut the text or copied the example) and otherwise the assistant's. Like every AI control, those that ask this model for a page (the *Animated graphic* option, adding an overlay, the *Motion graphics project* button and *Write the page*) are **hidden** when no model can write it; stored pages still play, can be edited by hand (and by the **timeline** panel, which needs no model) and are rendered without it.

The optional **DiffRhythm2** song generator (English lyrics only, no instrumentals; see `docs/MUSIC_MODE.md`) has a card in Settings that works exactly like the HeartMuLa one below (an address, a key, **Test**, the acceptance when the address is another machine) and a box in the Models tab, Music, for its weights (about 9.5 GB). The optional **HeartMuLa** song generator (see `docs/MUSIC_MODE.md`) has its own card in Settings, **HeartMuLa music generator**: the address of a service on another machine and its key, with a **Test** button and, when the address is not this machine, a notice that the lyrics and tags are sent there and a box to accept it (saved only with it ticked). The card also shows whether the engine is local (a service you start from the Environment screen) or external. The **Environment** screen has a **Music services** panel: one row per local generator with its state, the card to start it on, **Start** and **Stop** buttons and the memory it uses; the services stay up between songs, both can be up at once, stopping the server does not stop them, and a run launched through the server while its service is stopped is refused. With no service and no address the generator is hidden from music projects. The **Models** screen, Music tab, shows the weights as a catalog entry. Details in `docs/http-api.md` (`/api/music/engines`, `/api/settings/heartmula/test`).

## A front end from a folder (`--web-dir`)

This repository is the server only. The screens (**Create**, Projects, Runs, Statistics, Gallery, Library, Models,
Environment, API and **Settings**) belong to the dashboards of the `dashboards-lab` repository, which talk to this server
through its OpenAPI contract and are built there. The binary has no front end inside: point it at the folder of any built
front end and it serves that folder at `/`:

```bash
bin/veritmo serve --web-dir ../dashboards-lab/dist/dashboard-full/browser
# or: VERITMO_WEB_DIR=/path/to/build bin/veritmo serve
```

It is the same origin as the API, so the dashboard needs no `--cors-origin`, and the HTTPS and install-as-app notes above
apply unchanged. Files named after their content (`chunk-0i58r8Jb.js`) are cached for a year; `index.html`, the service
worker files and `manifest.webmanifest` are checked again on every visit, so an installed app picks up a new build. A path
that is not a file (a route of the front end) answers `index.html`, while an unknown path under `/api/` stays a JSON `404`.
The folder is read on every request: copy a new build over it and reload, no restart. A `--web-dir` that is not a folder
stops the server at start (exit code 2). `./serve.sh` never passes it: it only starts the server. Without `--web-dir` the
root answers a plain-text note that the server has no front end and where the API is (`/api/`).

## Who made a project, and the projects of the generation API in the listings

`GET /api/projects`, `/api/clips`, `/api/library` and `/api/storage` say who made each item in `client`: the `client:` of the project's `scenes.yaml` (`POST /api/projects` can set it with an optional `client`), else `embedded` for an ordinary project and `unknown` for one made by the generation API without a name. By default the projects of that API (`api-gen-`, `api-edit-`, `api-vid-`, `api-m3d-`) are not listed; `?managed=include` lists them as one more project with their `client` and `kind`, and `?client=a,b` keeps only those clients, so a dashboard can show everything the server holds and filter it by the dashboard that made it. The animations of the music motion engine are never listed. A project of the generation API is deleted with its own endpoint (`DELETE /api/generations/{id}`, `/api/image-edits/{id}`, `/api/video-edits/{id}`, `/api/models3d/generations/{id}`), not with `DELETE /api/projects/{id}`. The disk totals of `GET /api/storage` are the disk's, so including those projects counts nothing twice.
