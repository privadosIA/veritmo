# Request for the dashboards: a generated video behind a piece, with the lyric as karaoke

> **For the dashboard side (change to open in `dashboards-lab`).** Server side: OpenSpec change `add-video-background-karaoke`. **Contract commit: `6c0ce885` (`internal/server/openapi.yaml`; the routes are in `internal/server/music/musicmotion_backgrounds.go`). Delivered and archived 2026-11-26.** A real run of the whole flow is in the samba folder `video-background-karaoke-pruebas` (`RESULTADOS.md`).

The "make the video clip" flow of a music project makes a **piece** (an animated video made from the song). Until now a piece was made only of animated graphics: it could not show a clip made by a video model, and the generation API could not put the words of a song on a clip. The server now lets a piece of the script play a **video behind it**, with the lyric drawn over it at the time each word is sung (karaoke).

## What the server does now

Check `features` in `GET /api/music-motion/catalog` for `video-background` before showing any of this. Each part in the same answer says `over_video`.

| Step | Route |
|---|---|
| What can make a clip on this machine | `GET /api/generation/options` (the ids, `available`, `clip_seconds` and the `duration` range of each) |
| One clip for a piece | `POST /api/generations` `{prompt, option, duration, aspect_ratio, quality, client}`: `duration` is the length of the piece and `aspect_ratio` the shape of its frame (`16:9` for 1280x720); the generation loops its clip to that length |
| Wait for it | `GET /api/generations/{id}` until `state` is `done` (`failed` says why) |
| Keep a copy in the piece | `POST /api/music-motion/pieces/{id}/backgrounds` `{"from": "generation:<generation id>", "name"?}` → `201 {name, seconds, width, height, fps}` |
| Or upload one | `PUT /api/music-motion/pieces/{id}/backgrounds/{name}` (a raw video, `.mp4`, `.mov`, `.webm`, `.mkv`) |
| List, play, delete | `GET .../backgrounds` (with `used_by`), `GET .../backgrounds/{name}/file` (`Range`, `?ticket=` for a `<video>`), `DELETE .../backgrounds/{name}` |
| Put it behind a piece | `PUT /api/music-motion/pieces/{id}/script` with `background: {clip, clip_start?, clip_end?, clip_speed?, scrim?}` on that piece of the script |
| Karaoke | `PUT .../design` with `verse_style: "karaoke"` (or `"highlight"` or `"glow"`) |
| See it | `POST .../preview` (the scene says `background: {clip, url}`: play `url` and lay the transparent page of the scene over it) and `GET .../frame?t=` (the server's own frame, with the video, drawn exactly as the render draws it) |
| Make the video | `POST .../render` as always |

### The flow the dashboard would offer

1. The person makes the piece as now (with the plan, or by hand) and reaches its script.
2. For each piece of the script that should have a video (or all of them), the dashboard asks the generation API for a clip with a prompt (the prompt assistant can write it from the verse) and the length of the piece, and shows its progress with the generation's own events.
3. When a generation is `done` the dashboard calls `POST .../backgrounds {from}` and then saves the script with that piece's `background`.
4. The person picks the verse style (karaoke), looks at a frame of each piece (`GET .../frame?t=`) and renders.

## What to know

- **Only some parts can go over a video.** A part that paints the whole frame would hide it; the catalog says which parts are `over_video` (28 of the 30 that come with the program). The script is refused (`422` naming the piece and the part) otherwise, so offer only those parts for a piece with a background. `blank` is the one that draws nothing of its own.
- **A clip is short.** Under a piece longer than the part of the clip it uses, the video is slowed by at most 2x and then repeats from its start (there is no chaining of clips), so a long piece shows the same movement more than once. `clip_start`, `clip_end` and `clip_speed` choose the part and the speed (`musicmotion.PieceBackground.ClipTime` is the mapping; the preview's `url` plus those fields is enough to follow it in a `<video>`, and the server's frame is the reference).
- **`scrim`** (0 to 0.8, default 0.3) lays the design's background colour over the video so the words read; a bright clip may need more.
- **The sound of the clip is never in the video;** the song is the soundtrack.
- **A new proposal keeps the videos.** Proposing a script gives each background to the proposed piece that holds the middle of the piece it was on; what cannot go anywhere is named in the `notes` of the answer and its file stays in the piece (`used_by: []` in the list, so the dashboard can offer to delete it). Where a video will play, the proposal uses only `over_video` parts.
- **The pieces are not projects:** the backgrounds are files of the piece. They go with it (deleting the piece removes them).
- **It needs Chrome and ffmpeg and no GPU** to render: the clips are made by the generation API beforehand.
- **A piece can be for adults** (added 2026-11-26, change `add-piece-adult-label`). `POST /api/music-motion/pieces` takes `adult` (it is `true` too when the music project is) and the piece says `adult`. Attaching the video of a generation made with `adult: true` makes the piece one; for an upload, send `PUT .../backgrounds/{name}?adult=true`. On a server that does not keep adult content the creation and that upload are `422` with the code `content_not_allowed`, and a piece for adults is a `404` that the lists leave out, like a project for adults. The label stays when a video is deleted. Nothing to build: show the lists as they come.

## Not yet (do not build on it)

- One call that generates the clips of a whole song (the dashboard makes one request per piece).
- A model that writes the prompt of each clip from its verse (the prompt assistant of the dashboard can).
- Chaining clips to fill a long piece.
- A free video track independent of the pieces (a clip that crosses several pieces, two clips in one piece): that is the change `add-video-track`, which will turn the `background` of a piece into a video item of the script; a script saved with `background` keeps working.
