# The library of 3D models

A 3D scene (a project's scene or a piece of a music motion script) can place models made elsewhere: `.glb` files with their own animation clips (see "Models from files" in [SCENES_YAML.md](SCENES_YAML.md)). The library is where the server keeps **packs** of such models, so that finding a character is a list with pictures and putting it in a project or a piece is one call.

## Where it lives

`models3d/` in the server's working directory, or the folder in `MODELS3D_DIR`. It is not committed. Each pack has its own folder: `pack.json` (what the server knows of it: author, licence, source, hash, the index of its models), `files/` (what was in the zip that a library uses: the `.glb`, the pictures they name, the previews and the licence text, with the pack's own folder structure) and `thumbs/` (pictures drawn by Blender, see below).

Nothing is fetched while a scene is drawn: a model is *copied* into the project or the piece, so removing a pack never breaks them and a project's export and import carry its models like any source.

## The packs

`GET /api/models3d/packs` lists the packs the server knows and the ones you imported, with their `state`: `available`, `installing` (with its `progress`), `installed` or `failed` (with the reason). The list that ships with the server has only **CC0** packs, each with the SHA-256 of the version that was checked:

| Pack | What it has |
|---|---|
| `kenney-mini-characters` | 43 models: six women, six men, a robot, a zombie, a knight, an alien, props; 32 clips each (`idle`, `walk`, `sprint`, `jump`, emotes, attacks...) |
| `kenney-blocky-characters` | 18 blocky characters with 27 clips each, each with its own texture |
| `kenney-furniture-kit` | 140 pieces of furniture and decoration |
| `kenney-food-kit` | 200 models of food and kitchen things |
| `kenney-retro-urban-kit` | 124 pieces to build a retro street |
| `kenney-platformer-kit` | 153 pieces of a platformer level; 13 are animated (a chest that opens, doors, traps, levers) and 5 are characters with 25 clips |

`POST /api/models3d/packs/{id}/install` queues the download and answers `202`; ask `GET /api/models3d/packs/{id}` for the progress. The install:

- fetches the zip **only from the host of the pack's page** (`kenney.nl`), the way the page does: it reads the download id of the page and asks for the signed link with the page's cookies and as coming from the page, and the link must be on the same host; a zip over 200 MB is refused;
- checks the SHA-256 of the zip against the one in the list, and that the licence text of the zip says what the list says (`Creative Commons Zero`): a pack that **changed** is refused with a message that says so and nothing is installed (the list is updated by someone who reads the new version);
- unpacks it into a temporary folder and moves it into place only when it is complete, refusing the whole pack for a link, an absolute path, a `..`, more than 5,000 entries or more than 1 GB unpacked, and keeping only what a library uses (the `.fbx` and `.obj` copies are not kept);
- indexes the models (clips, triangles, the pictures they need, tags); a `.glb` that is not a usable model is left out and named in the pack's `skipped`.

`POST /api/models3d/packs/{id}/cancel` stops an install and `DELETE /api/models3d/packs/{id}` removes a pack (a pack of the list becomes `available` again). If the site changes how it gives its downloads, the install fails saying which step; download the zip in a browser and import it:

`POST /api/models3d/packs/import?id=&name=&author=&licence=&source_url=` with the zip as the body. `licence` is required: what the pack allows, in your words; the server keeps it, shows it with every model and does not judge it (`attribution` is `true` for a licence that does not say CC0). A pack with no `.glb` is a `422`. This is how packs that cannot be fetched by a program, such as Quaternius' (they are on Google Drive), enter the library.

**Kenney asks for donations** on every pack page; the packs are free thanks to them: <https://kenney.nl/support>.

## The pack `generated`

Models made by an engine (`veritmo models3d generate`, see [MODEL_GENERATION.md](MODEL_GENERATION.md)) go to a pack of their own, `generated`
(`source: "generated"`): it is created with the first model, can be removed like any pack (its models go with it) and cannot be imported over (`409`). Each of
its models shows, besides the usual fields, the `prompt` it was made from (or the name of the picture), the `engine`, its `engine_version`, the `licence` of the
engine (`MIT` for `triposr`, the Stability AI Community License for `sf3d`, with `attribution` true) and `generated_at`. The search `q` also looks in the prompt.

## The catalog

`GET /api/models3d/models` lists the models of the installed packs: `{models: [{id, pack, name, tags, clips, triangles, needs, licence, author, attribution}], total}`. `q` is searched in the name and the tags, `pack`, `tag` and `has_clips=true` filter, `limit` and `offset` take a page (`X-Total-Count` and `Link` say where the others are). `GET /api/models3d/models/{pack}/{name}` is one model.

`GET /api/models3d/models/{pack}/{name}/thumbnail` is the picture the pack has for the model (Kenney's `Previews/`, or the `Isometric/` and `Side/` views some kits have) or, when it has none, one 256-pixel frame of the model drawn by Blender on the CPU (about 0.5 s) and kept in `thumbs/`; `503` with how to install Blender when it has none and Blender is missing.

`GET /api/models3d/models/{pack}/{name}/file` is the model's binary glTF as the library stores it (`model/gltf-binary`, with `Content-Length`, an `ETag` and `Cache-Control: private, max-age=3600`; `If-None-Match` is answered with `304` and a `Range` with `206`), for a viewer or a download. It accepts `?ticket=` like the thumbnail. The pictures a model `needs` are not part of the answer. `404` for a model that is not in the library.

## Using a model

- In a project: `POST /api/projects/{id}/sources/model` with `{"from": "kenney-mini-characters/character-female-a", "name"?}`.
- In a piece of a music motion script: `POST /api/music-motion/pieces/{id}/models` with the same body.

The catalog filters by origin: `GET /api/models3d/models?source=catalog|user|generated`, `engine=` and `project=` for the models an engine made (the pack `generated`, see `docs/MODEL_GENERATION.md`), and its `q` also searches the prompt of those. A generated model made by a project (mode `models3d`) or a generation shows its `project`, its `entry` and `adult`; a model marked `adult` is not listed, searched, shown or drawn by a server that hides adult content.

Both copy the `.glb` as `<model>.glb` (or `name`) and the pictures it needs as `<pack>--<picture>`, with the addresses inside the copy rewritten to match: Kenney's kits all call their texture `colormap.png` and they are not the same picture, so a project with a character of one pack and a prop of another keeps both. What is already there with the same content is reused (`200` instead of `201`), another file of the same name is a `409` and nothing is written. From then on it is an ordinary source: `file: "character-female-a.glb"` in a description, the writer is told about it, and it does not depend on the library.

## What it measured

On an isolated server on 2026-11-24 (RTX 4070): the six packs (27 MB in all) installed from `kenney.nl` in about a second each (the largest, 5 MB, in 0.8 s) with no file left out; the 51 models that have clips were found with `has_clips=true`; the thumbnail of a pack with previews is instant and one drawn by Blender took 0.5 to 0.6 s. A project with a character of each of two packs, an animated chest, a pizza and a lamp took 45 s to render (5 s, 1280x720), and a 20 s piece with two 3D choruses that place models of the library took 110 s (the estimate said 105.6 s). The local `gemma4:26b` wrote 3 of 4 descriptions for ideas that use those models (the fourth needed a second try after it put a font the design does not have); the files are in the shared folder `blender-biblioteca/`.

One thing it showed, and the contract now answers: `size` of a `model` is its **height**, so a flat model such as a pizza (3 cm tall, 30 cm wide) given `size: 0.3` came out ten times too big. A model can now say `"fit": "largest"` and `size` is its longest dimension (see "Models from files" in [SCENES_YAML.md](SCENES_YAML.md)); the writer is told when to use it, and the thumbnails of this library are drawn that way.
