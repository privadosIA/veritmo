# Request for the server session: serve the `.glb` of a library model

> **Delivered 2026-11-25** by the owner's word, copied into the server's repo as `docs/requests/dashboards-lab-model-file.md` for its session to implement (it is not committed there). Dashboard side: OpenSpec change `model-studio-viewer`.

To be copied into the server session. The dashboards (`@lab/model-library-kit`, hosted by `dashboard-full` and `apps/model-studio`) want a **viewer** for the models of the
3D library, and the contract has no route that returns the model itself.

## What exists today

Checked on 2026-11-25 against the real server (`:4666`, Blender 5.3, TripoSR running), with a model made a minute before (`generated/prueba-cofre`, 48,288 triangles):

- `GET /api/models3d/models/{pack}/{name}` answers the metadata only (`triangles`, `clips`, `tags`, `prompt`, `engine`, ...).
- `GET /api/models3d/models/{pack}/{name}/thumbnail` answers a 256 px Blender render (it works and the chest is recognisable).
- `GET /api/models3d/models/{pack}/{name}/file` and `GET /api/models3d/models/{pack}/{name}.glb` answer `404`.
- The only way to get the `.glb` out is to copy it to a project (`sources/model`) or a piece, which is not a way to look at it.

## What is wanted

`GET /api/models3d/models/{pack}/{name}/file`: the model's binary glTF.

- `200` with `Content-Type: model/gltf-binary`, `Content-Length`, an `ETag` and `Cache-Control: private, max-age=…` (a model of the library does not change until it is removed or replaced).
- Auth as the other library reads: the bearer token, and `?ticket=` too if it is easy (it costs nothing and lets an `<a download>` or a viewer in a plain tag use it).
- `404` with the usual error body for a model that is not in the library.
- `If-None-Match` answered with `304` where cheap.
- The file as stored: the library already refuses a `.glb` that keeps its data in other files, so one response is a complete model.
- CORS as the rest of the API (the dashboards reach the server through a proxy or from `http://localhost:4200`).

Nothing else changes: no new field on `LibraryModel`, no new setting.

## What the dashboards do with it

- The viewer reads the file with the token and shows it in 3D (orbit, animation clips, wireframe, triangle count); until the route exists it shows the thumbnail and says the server does
  not serve the model file yet (a `404` is the signal, no feature flag).
- Nothing is stored in the browser beyond the HTTP cache; with `ETag` the second opening of a model costs nothing.
- When the contract has the route: `npm run contract:sync` and the mock's marker of a pending request is removed.
