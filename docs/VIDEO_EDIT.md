# Editing a video you already have

The server can do seven things with a video that already exists, each with **one request** (no project, no YAML): make it bigger, give it more frames per second, change its aspect ratio, cut the people out, get its depth, make it longer, or move the movement of some people onto another picture. They are the *Edit video* tools of the dashboard; this page is the same thing for a script or a client of your own. The routes are in [http-api.md](http-api.md) and `internal/server/openapi.yaml`.

| Tool | What it does | Settings | Sound | Needs |
|---|---|---|---|---|
| `scale` | the picture 2 or 4 times as large (up to 8192 px a side) | `factor` 2\|4, `engine` `fast` (lanczos) \|`quality` (Real-ESRGAN) | kept | ffmpeg; `quality`: [the upscalers](INSTALL.md#video-upscalers-real-esrgan-and-rife) |
| `interpolate` | the frames in between: 24, 30 or 60 per second, same length | `fps` | dropped | ffmpeg; RIFE when installed (else `minterpolate`) |
| `reframe` | another aspect ratio (16:9, 9:16, 1:1, 4:5) by `crop` (follows the person) or `pad` (bars) | `aspect_ratio`, `mode`, `color` | kept | ffmpeg; the vision service to follow the person (else centred, with a note) |
| `remove-background` | keeps the people, the rest becomes a colour or transparency (WebM) | `background`, `alpha` | kept | the vision service |
| `depth` | a video of the depth of every frame, near white and far black, on one scale for the whole clip | none | dropped | the vision service |
| `extend` | a new clip that starts at the last frame, joined to the video | `prompt` (required), `seconds` 2 to 10, `engine` `minimax-h3`\|`ltx` | none | MiniMax H3 (with its first-frame bundle) or LTX 2.3 |
| `motion-control` | the people of a picture do the movement of the people of a video (or of a motion of the library) | `prompt`, `strength`, `guidance`, `start`; needs the picture | none | `sd`, Wan 2.1 VACE 1.3B, the vision service, a card with 6 GB |

`GET /api/video-edits/options` lists them with `available` and, when they cannot run, **why** (a missing file, a stopped service, a card too small); what is not installed and how to get it is in each tool's `notes`.

## A request

```bash
# what can this machine do?
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:4666/api/video-edits/options

# make a clip twice as large (multipart: the JSON in `request`, the video in `video`)
curl -H "Authorization: Bearer $TOKEN" \
  -F 'request={"tool":"scale","settings":{"factor":2,"engine":"quality"},"client":"my-script"}' \
  -F video=@clip.mp4 http://127.0.0.1:4666/api/video-edits
# -> 202 {"id":"api-vid-...","state":"running",...}

curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:4666/api/video-edits/api-vid-...    # state, progress, result
curl -H "Authorization: Bearer $TOKEN" -o big.mp4 http://127.0.0.1:4666$RESULT_VIDEO         # `result.video` of a finished one
curl -X DELETE -H "Authorization: Bearer $TOKEN" http://127.0.0.1:4666/api/video-edits/api-vid-...
```

A request the tool does not accept is refused with `422` and the reason (a setting outside its limits, a file that is not a video, a missing or surplus picture, no one in the video for `motion-control`), and `503` when the tool cannot run now; nothing is created in either case. An edit is a hidden project like the generations: it does not appear among yours, takes its turn on the queue (one heavy process at a time, with the usual notice), can be cancelled by removing it, and `client` names the dashboard for the statistics. The result is an MP4 (H.264, `yuv420p`), or a WebM with transparency for `remove-background` with `alpha`; `result` also says its size, rate, length, whether it has sound, the engine that made it and `notes` (a fallback used, nobody found, how many times it has been extended).

## The vision service

`remove-background`, `depth`, `reframe` (to follow someone) and `motion-control` use two small networks (a person mask and a depth estimator) that run **on the CPU** in a service of their own, so they take no memory from the cards and need no CUDA and no Python. Install it with `bash scripts/get-services.sh --only videovision` (about 250 MB), install its two model files from the catalog entry *Vision for editing video* (275 MB, in the Models screen) and start it from the **Environment** screen, or with `POST /api/music/engines/videovision/start`. Until it runs, the tools that need it are listed unavailable with the reason. Measured: 10 seconds of video at 832x480 take 17 s for the person mask, 18 s for the depth and 39 s for the control of `motion-control`. Its source is in the `dotnet/` folder of the private edition, which the public edition does not carry: there these tools are listed unavailable, with the reason.

## `extend`

A new clip is made by an engine that takes a first frame, with the **last frame of the video as that frame** and your prompt, and the two are joined without showing that frame twice, at the size and rate of the video and without sound. One clip is what the engine makes by itself, about 2 to 2.3 seconds on this machine: if you ask for more, the video grows by that and the result says so. The result says how many times the video has been extended from its original (the count travels in the file) and warns that the quality drops with each time. Measured on an RTX 4070: MiniMax H3 about 290 s, LTX 2.3 about 145 s.

## `motion-control`

It takes the movement of the people of a video (33 frames at 16 per second, about 2 seconds, from `start`) and makes the people of a **reference picture** do it, with the picture's background. It is meant for people and it is **soft**: the identity of the face is not guaranteed. The clip has the shape of the picture (832x480, 480x832 or 640x640). It takes about 330 s and 5.5 GB of video memory.

What matters most is the **prompt**: describe the people and the place as the picture shows them, with natural lighting. On the same request, "a woman with red curly hair and a cream sweater dances in a **bright** kitchen" came out burnt white with invented legs; without "bright", in natural light, it kept the hair, the sweater, the jeans and the pose. `strength` (0.2 to 1, default 0.6) is how much the motion rules. `guidance` (1 to 10, default 6, the cfg scale of `sd`) is how closely the prompt is followed: lowering it to about 4 burns the highlights less (36 % to 21 % of the pixels for a person, 24 % to 14 % for two cats) but follows the prompt less, and the cats came out half human, so it is a choice and not the default.

The request is refused with `422` if nobody is in the 33 frames, **before** anything heavy starts: the control (the depth of the people, black elsewhere) is made when the request arrives, in about 8 seconds, and kept for the run.

### The library of motions

A motion is a short video of people moving (a dance, a walk, a gesture) prepared once and applied by name to any picture (its record is a row of the server's database; its control frames and its preview are files in `~/.config/veritmo/motions/<id>/`), so the vision service has nothing left to do: a request that names one is accepted at once (0.2 s instead of 8). The library starts empty; you add what you own or may use. A motion added from an address keeps that address (`source`, shown in the list and the read) so you can tell where it came from; the video itself is never kept.

| | |
|---|---|
| `POST /api/motions` | the video (`multipart/form-data`, a `request` part `{title?, tags?}` and the `video`; or JSON `{source}` with a YouTube address) -> `202`, state `processing`, then `ready` or `failed` with the reason |
| `GET /api/motions`, `GET /api/motions/{id}` | the list, newest first (empty at the start), and one: `{id, title, tags, duration, people, state, preview, error?}`. The list can be paged and searched: `limit`, `offset`, `sort` (`created_at`, `title`), `q` (title and tags) and `state` |
| `PATCH /api/motions/{id}` | `{title?, tags?}`: changes the title and the tags that are sent (`400` for any other field); it can be done while the motion is being prepared |
| `GET /api/motions/{id}/preview` | a small video with the source's picture |
| `DELETE /api/motions/{id}` | stops the preparation if it runs and removes the files |

A video must last from 2.1 to 30 seconds and have somebody in it (`422` otherwise; `503` if the vision service is not running). Only the control (a PNG a frame, in the video's shape) and the preview are kept, never the video. `people` is an estimate: people who touch count as one. To use one, send `motion` instead of the `video` part, with `start` (seconds) to choose which 2 seconds of it; the control is fitted to the picture's shape with black bars.

## Where the files are

Results and sources of an edit live in its hidden project (`api-vid-…`), removed with it. Models: `models/video/` (the vision models, and the three files of Wan 2.1 VACE for `motion-control`, which the catalog installs as one entry). Programs: `bin/` (`sd`, `bin/realesrgan`, `bin/rife`, `bin/services/videovision`). The library of motions is in the settings folder (`~/.config/veritmo/motions/`).

## Trying it on real clips

The tests that need a program, a model or a card (the networks, the vision service, `sd`) are skipped where those are not installed. `VIDEOEDIT_SAMPLES_DIR=/some/folder go test ./internal/video/videoedit -run TestSamples` writes what each tool makes on a real clip into that folder, for you to look at; `VIDEOEDIT_TEST_PERSON_CLIP` points the tests at a clip of a person of your own.
