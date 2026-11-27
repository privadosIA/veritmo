# Request for the dashboards: the video lane of Motion Edit

> **For the dashboard side (change to open in `dashboards-lab`).** Server side: OpenSpec change `add-video-track`. The contract is in `internal/server/openapi.yaml` (the commit that carries it is `b6d0a632`, named again at the end of this file). It builds on `add-video-background-karaoke` (see [dashboards-lab-video-background.md](dashboards-lab-video-background.md)).

The Motion Edit of a music piece edits the pieces of the script (the text track). The server now has a second track under it: a **video track** of clips in the time of the song, independent of the pieces. This asks the dashboard for a **video lane** under the pieces.

Check `features` in `GET /api/music-motion/catalog` for `video-track` before showing any of this.

## What the server does now

| Need | Route |
|---|---|
| The track | `video` in the script (`GET .../pieces/{id}`, `PUT .../pieces/{id}/script`): items `{id, clip, start, end, clip_start?, clip_end?, clip_speed?, scrim?, transition_seconds?}` in seconds of the song, in order, no overlap |
| Where the verse goes | `verse_layout: {position: top\|center\|bottom, scale: 0.6 to 1.6, align: left\|center\|right}` in the design and in a piece's design override |
| The clips of the piece | `GET|PUT|POST|DELETE .../pieces/{id}/backgrounds` (as before: upload or copy a generation) |
| Draw a clip in the lane | `GET .../backgrounds/{name}/thumbs?count=&height=` (one PNG strip; `X-Frame-Count` and `X-Frame-Width` say how to cut it) and `.../frame?t=&width=` (one picture); both accept `?ticket=` |
| Play it | `GET .../backgrounds/{name}/file` (`Range`, `?ticket=`) in a `<video>` |
| Cut and join | `POST .../pieces/{id}/video/split {item, at, ids?}` and `.../video/join {first, second}`: each saves the whole script and answers it |
| See it | `POST .../preview`: `video` lists the track (`id, clip, url, media_type, file_seconds, start, end, build_seconds, scrim, transition_seconds, from, to, speed, repeats`) and each scene says `layer`; `GET .../frame?t=` is the server's composed frame and the reference |
| Render | `POST .../render` as always (Chrome, ffmpeg and ffprobe, no GPU) |

## How the dashboard would use it

1. **The lane.** Under the lane of pieces, draw the items of `video` as blocks over the same time axis, each with the strip of its clip (`thumbs`). Dragging an edge changes `start`/`end` (and `clip_start`/`clip_end` when trimming the clip itself); dragging a block moves it; the cut tool calls `video/split`; two touching blocks of the same clip can be joined with `video/join`. Save with `PUT .../script` (the whole script).
2. **Playing under the text.** The preview gives each piece's page already on a transparent background when it is a layer (`layer: true`). Put a `<video>` under the frame of the pages, and, for the item that holds the audio clock `t`, set `video.currentTime` to the second of the file the render shows: `from + ((t - start) * speed) mod (to - from)` (`speed`, `from` and `to` are in the preview; `repeats` says the part loops). Lay the item's `scrim` over the video as a colour of the design's background at that opacity, and show the design's background colour where no item holds `t`. In a crossfade (`transition_seconds` > 0 at the start of an item) dissolve the two videos. **The server's own frame is the reference**: a dashboard that wants to be exact can ask for it.
3. **The inspector.** A piece's `design` override may carry `verse_layout`, so the verse can be put at the top, resized or aligned for that piece.
4. **Only `over_video` parts** can be used by a piece that touches an item (the catalog says which; the script is `422` otherwise, naming the piece and the part).
5. **Warnings.** After a change of the song's words, `script_warning` names an item that falls outside the song; the script cannot be saved or rendered until it is moved or removed.

## What to know

- A piece's `background` still works: the server converts it to the item `bg-<piece id>` and never returns it, so the dashboard reads the track only.
- The clip's own sound is never in the video: the song is the only soundtrack.
- Out of scope: a second video lane, overlapping pieces, effects per clip and free text.

## Where it is

The routes, the schemas in `internal/server/openapi.yaml` and the pipeline are in commit `b6d0a632` of `veritmo` (`main`, local until it is pushed). The real run that measured it is in the samba folder `video-track-pruebas`: `RESULTADOS.md`, the frames of each piece and the video.

## Reading the verse over a clip (`improve-verse-legibility-over-video`)

Check `features` for `verse-legibility` first.

- `GET .../pieces/{id}` and the answer of `PUT .../pieces/{id}/script` carry `verse_notes`: `{piece, part, band, share, free}` when the verse of a piece falls on a band its part paints by 0.2 or more and the part leaves another band clear (`free` is that band; a part that fills the frame gives no note), and `{item, band, contrast}` when a track item's clip leaves the accent under 3:1. Show them as advice next to the piece or the item; nothing is refused because of them. The fix for the first is `verse_layout` in the piece's `design`; for the second, a higher `scrim` (or a clip with a quieter band behind the verse).
- Each part of the catalog may list `bands: {top, center, bottom}` (0 to 1): a dashboard can draw where a part paints before the author picks `verse_layout`. Absent means unknown.
- `video[].scrim` in the preview is the global scrim of the item (its own or 0.3). When the clip behind the verse leaves the accent under 3:1 the item also lists `video[].local_scrims: [{band, scrim}]` (never saved in the script): strips of the design's colour over the band where the verse is drawn. To draw it over a `<video>`, lay over the item a full-width box on the rows of the band (`top` 6 % to 22 % of the height, `center` 42 % to 58 %, `bottom` 78 % to 94 %) with `scrim` of the colour (the design's background of its default mode) at its flat part and a linear fade to nothing over 6 % of the height above and below. `GET .../frame` shows the same picture, and it is the reference.
- The verse of a layer has a halo; the preview's pages already carry it.

The contract of the notes and `bands` is in `5c756bc7` (`improve-verse-legibility-over-video`, archived). The scrim that follows the clip is now local (`add-local-verse-scrim`, contract in `86e1381a`): the global scrim is no longer raised, and the preview lists `local_scrims` per item.
