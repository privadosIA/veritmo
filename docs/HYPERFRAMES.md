# HyperFrames blocks

The server keeps the blocks of [HyperFrames](https://github.com/heygen-com/hyperframes) (HeyGen, Apache-2.0) **whole**, as the registry has them: title cards, kinetic type, shader backgrounds, 3D, charts and more, 210 in the registry. This is apart from the music-motion *parts* (`veritmo parts import-hyperframes`), which convert some blocks to fit the beat clock; a block here is not rewritten.

Nothing is fetched from outside while a block is drawn. The libraries a block loads from a CDN (gsap, three, d3, topojson, lottie, the Draco decoder, map data, Inter and the Google Fonts it asks for) are downloaded once, pinned by SHA-256, and served to Chrome from the server; any other request fails and is reported.

## Setup

```bash
bash scripts/get-hyperframes-libs.sh   # 86 files (~9 MB) into third_party/hyperframes-libs/, each checked against its SHA-256
git clone https://github.com/heygen-com/hyperframes ~/hyperframes        # any folder
veritmo blocks import-hyperframes ~/hyperframes/registry/blocks     # copy the 210 blocks and check them
```

The blocks live in `hyperframes/blocks/<name>/` inside the server's folder (not committed), or where `HYPERFRAMES_DIR` says. Each has its files untouched, its `registry-item.json`, a `block.json` (title, tags, licence, origin, last check) and, once asked for, `.thumb.jpg`. The list of libraries is `internal/music/hyperframes/libs.txt`; the Google Fonts stylesheets are in `internal/music/hyperframes/fonts/` (latin and latin-ext only). The licences are in [THIRD_PARTY.md](../THIRD_PARTY.md); each block keeps the licence files it came with.

## The checks

A block or component is `ok` when it passes all of these (the same checks run on every edit):

- it draws with nothing asked from outside the block's files and the pinned libraries, and with no script error;
- two passes (the second in a new browser, going forward as a render does) at 0, 2, 10, 25, 50, 75 and 95% of its length give the same frames: byte for byte, or with at most 0.2% of the pixels differing by more than 12 levels, and a moment that differs is drawn a third time and flagged only if no two of the three passes agree. `Math.random` is seeded the same on every load, so a block that scatters things at random (a stagger from random) looks the same on every pass and every render;
- (a warning, not a failure) whether something moves between those moments: a lower third or a backdrop may be still by design, and is listed as `static`;
- every file the registry lists for the block is there (otherwise `needs-files`: the registry lists pictures, videos or audio it does not ship, for example the 27 `carousel-*` blocks).

WebGL and WebGPU are drawn on the CPU (SwiftShader), the same on every machine. `VERITMO_BROWSER_GPU=hardware` in the environment of the server (or of `veritmo blocks`) uses the card of the machine through Vulkan instead (the first NVIDIA adapter; a request for a "high-performance" adapter that finds none falls back to the default one): heavy WebGL and WebGPU scenes then answer in seconds instead of minutes, but the frames can differ a little from one machine to another, so the checks and the thumbnails are made with the CPU. Tried on `frost-sequence-camera-orbit`: with the card it no longer hangs (about 9 s for the first frame) but its pictures stay black even with its real shards atlas and ice texture: its WebGPU renderer starts (it logs its flags and no error) and shows nothing, and it is driven by HyperFrames' own render protocol, which our seek does not speak. It is left as a known limit. The page gets the HyperFrames runtime a block expects: `window.__hyperframes.getVariables()` (the defaults the block declares), its GSAP timelines in `window.__timelines` sought without firing callbacks, `.clip` elements shown over `[data-start, data-start + data-duration)`, and for GPU blocks the time in `__hfThreeTime` and `__hfTypegpuTime` and the `hf-seek` event.

Measured on 2026-11-17 on the registry at commit dbc9c18: of the 448 blocks and components 446 pass (all 238 components, 126 of them with the upstream runtime, and 208 of the 210 blocks, 28 of them with the pictures and clips fetched from HeyGen's host, see below). Two fail: `vfx-magnetic` needs Chrome's experimental HTML-in-canvas API (`texElementImage2D`), which the headless build does not give (the block says itself that no polyfill exists), and `frost-sequence-camera-orbit`, a heavy WebGPU scene that does not answer in 40 s on the CPU (with the card and its real textures it starts and draws, but black: see below).

## Files the registry keeps off git

28 blocks (the carousels, a frosted-glass title, an avatar card) use pictures, clips, a soundtrack and textures that HyperFrames does not keep in its git repository: each `registry-item.json` gives them an address on HeyGen's static host (`static.heygen.ai/hyperframes-oss/registry-assets/<id>.<ext>`), the ones `npx hyperframes add` fetches at install. The file name is the first 16 hex digits of the file's SHA-256, so every download is checked against its own address. The store fetches them **once, when it imports a block** (`veritmo blocks import-hyperframes`, `bash scripts/get-hyperframes-blocks.sh`; `--no-fetch` skips it) and draws the block from its folder, so nothing is fetched while a block is drawn. Only that host is accepted, a file larger than 300 MB or whose hash does not match is refused, and `veritmo blocks fetch-assets [name ...]` fetches them for blocks already in the store (it also replaces the placeholders below with the real files). The files are HeyGen's and the artists' (the carousel pictures are cover photos): the server keeps them in `hyperframes/blocks/`, which is not committed and not redistributed.

When they cannot be fetched (no network, `--no-fetch`) the store makes **placeholders**, listed in `block.json` (`placeholders`) and as a warning of the checks, so none passes for the real one:

- **Pictures:** `bash scripts/make-block-placeholders.sh [GPU]` generates abstract cover art (no people, no text) with the project's own image engine (`bin/sd`, about 12 s each) into `hyperframes/placeholders/`; the store copies the one that has the file's name. Without it a procedural gradient is drawn (a lit sphere for a matcap, a soft noise for a texture).
- **Clips and soundtrack:** a 4 s slow zoom over a generated picture, with a silent track, and 30 s of silence, made with ffmpeg.
- **Fonts:** the open fonts they are (Inter, EB Garamond) from the pinned copies of fontsource that the libraries bring.

`veritmo blocks placeholders [name ...]` makes them for blocks already in the store and checks those blocks again. The store never replaces a file that exists, except a placeholder when the real one is fetched.

## Components

The registry has 238 more pieces, the *components* (a cursor that clicks, a caption effect, a camera rig, a transition...). They are mountable sub-compositions: a host composition mounts them with `data-composition-src` and gives them variables. The catalog keeps each whole as a block of `kind: component`, drawn through its `demo.html` (the registry's own demonstration), with its `family`, `profile`, `jobs` and `sync_points`. `GET /api/hyperframes/blocks?kind=component&family=pointers` lists them; they are thumbnailed, checked, edited, rendered and used over a verse like any block. Import them with `veritmo blocks import-hyperframes --components <registry/components> <registry/blocks>` (`bash scripts/get-hyperframes-blocks.sh` does both).

## The runtime

Two runtimes can draw a page. The server's own small one (variables, GSAP timelines, `.clip` windows, the GPU seek) draws every page that mounts nothing, and the upstream one, HyperFrames' own runtime built from its source (`bash scripts/get-hyperframes-runtime.sh`, needs Node only for that step), draws the pages that mount sub-compositions, which only it can mount. The choice is per page and automatic (`data-composition-src` in the main file and the runtime installed); `veritmo blocks check --runtime upstream|builtin` forces one, and the checks record which drew each block (`runtime`). Forced on every block, the upstream runtime makes some GPU shaders drift between two passes, so it is not the default for them.

## A block over a verse

A layer item of a music piece may name a block instead of a part (`{"block": "lower-third-bild", "params": {…}, "start": 12, "end": 18}`): see "layers" in [MUSIC_MODE_MOTION.md](MUSIC_MODE_MOTION.md). The block is drawn inside an iframe of the page of each piece its range touches, scaled to the frame, and sought to the seconds from the item's start. The server saves the script only if the block is in the catalog, did not fail its checks (`needs-files`, `failed` and `needs-gpu` are refused) and the libraries are installed.

## Command line

```
veritmo blocks import-hyperframes [--only a,b] [--force] [--no-check] [--workers N] <registry/blocks dir>
veritmo blocks check [--workers N] [name ...]
veritmo blocks list [--json]
```

`import-hyperframes` copies and then checks (4 at a time); a block the user edited is kept unless `--force`. Both write `hyperframes/report.json` and print what does not pass and why. Exit code 0 when every block passed, 1 otherwise.

## HTTP API

| Route | What it does |
|---|---|
| `GET /api/hyperframes/blocks[?q=&tag=&state=&kind=&family=&limit=&offset=&sort=]` | The blocks with title, tags, size, length, licence, origin, `state`, `problems` and `thumbnail`; `libs_installed` and `fix`. |
| `DELETE /api/hyperframes/blocks/{name}` | Deletes the record of the block and its folder (`204`, `404` if there is none). A part made from the block is a part of its own and is never deleted with it. |
| `GET /api/hyperframes/blocks/{name}` | A block with the text of its main file (`html`), its files, its `variables` and the text of the licence files that came with it (`licenses`). |
| `GET /api/hyperframes/blocks/{name}/thumbnail` | A JPEG (640 px) of the block at 40% of its length, drawn the first time and kept. |
| `GET /api/hyperframes/blocks/{name}/frame?t=` | The block drawn at `t` seconds, as a PNG. |
| `POST /api/hyperframes/blocks/{name}/preview` | `{html, t?}`: the block drawn with another main file; nothing is stored. |
| `PUT /api/hyperframes/blocks/{name}` | `{html}`: replaces the main file after the checks; `422` with the problems and nothing changes if it fails; the block is marked `edited`. |
| `POST /api/hyperframes/blocks/{name}/check` | Runs the checks again and keeps the result. |
| `POST /api/hyperframes/blocks/{name}/render` | `{duration?, fps?, variables?}`: plays the block to an MP4 with no song (its own timeline; `variables` over the defaults it declares; up to 60 s and 60 fps) into `hyperframes/clips/`. The answer has `path` (use it as the `clip` of a scene) and `url`; the same request is the same file and is not drawn again. |
| `GET /api/hyperframes/clips/{file}` | A clip the render made. |
| `POST /api/hyperframes/blocks/import` | `{dir, only?, force?, check?}`: imports from a clone on the server and checks; the answer has a line per block and the report. |

They are described in the OpenAPI document. The registry's own preview posters and videos are remote addresses: they are kept in `origin` as a reference and never fetched; the thumbnails are drawn by the server.
