# Request for the dashboards: use the client label as a visual category

> **For the dashboard side (change to open in `dashboards-lab`).** Server side: OpenSpec change `add-client-labels`. The contract is in `internal/server/openapi.yaml` (the commit that carries it is `d5722b93`).

The `client` label (`dashboard-full`, `video-studio`, `claude-test`, `embedded`, `unknown`) says where something came from, and the owner finds it very useful as a visual category. The server now gives what a dashboard needs to use it as one. Everything here is additive: nothing that answers today changes without a new parameter or a new route.

## What the server does now

| Need | Call |
|---|---|
| Draw the filter chips | `GET /api/clients` → `[{name, color, counts: {projects, animations, generations, image_edits, video_edits, model_generations}, total, last_used}]`, newest use first. Only items the server shows are counted. |
| The colour of a label | `color` in that list: the one the owner set, or a default worked out from the name (`PUT /api/clients/{name}` documents the rule, so it can be computed locally for a label the list does not have yet). |
| Let the owner choose a colour | `PUT /api/clients/{name}` with `{color: "#rrggbb"}`, or `{color: null}` for the default. The server keeps it, so every dashboard shows the same colour for a label. |
| Change the label of something | `PATCH {client}` on the project, the animation, the generation, the image edit, the video edit or the model generation (`null` removes it). `409` while it runs (an animation's change of `client` too: it is written in its project as well). |
| See the animations next to the rest | `?managed=all` on `GET /api/projects`, `/clips`, `/library` and `/storage`: they come with `kind: music_motion` (`project_kind` in the clips), their `client` and their size. |

## What to know

- **Opt in with `managed=all`.** `managed=include` answers what it always did and never lists an animation, so a dashboard that does not know `music_motion` is not surprised. Draw the new kind before sending `managed=all`.
- **Use the route of each kind to relabel.** `PATCH /api/projects/{id}` with `client` is only for ordinary projects: for `api-gen-`, `api-edit-`, `api-vid-`, `api-m3d-` and `api-mm-` it answers `400` and names the route to use (the item keeps its label in its own record too).
- **The label is only visual.** It gives no permission and changes no behaviour; two dashboards may use the same label.
- **The runs keep the label they were made with.** After a relabel, the project, the generation and `GET /api/clients` show the new one; the runs already made keep the old one (they are a record of who asked), so a filter by label on the runs and on the projects may differ for what was relabelled.
- **An animation is in the project-based listings from its first render** (it has no project before). `GET /api/clients` counts every animation from its creation.
- **Deleting an animation from the project routes is refused** (`DELETE /api/projects/{id}` answers `400` and names `DELETE /api/music-motion/pieces/{id}`), so its history and drafts go with it.
- The chips the dashboard draws should use `color` as given; the default colour comes from the name, so it is stable across dashboards and restarts.

## Where it is

The routes, the schemas in `internal/server/openapi.yaml` and the tests are in commit `d5722b93` of `veritmo`. A real check on an isolated server is in the samba folder `client-labels-pruebas`.
