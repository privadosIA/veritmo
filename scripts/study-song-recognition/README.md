# Song recognition study (2026-10-21)

Scripts of the study in `docs/SONG_RECOGNITION_STUDY.md`. They are experiments, not part of the application, and
they expect this layout (`$STUDY_DIR`, mounted as `/work` in the containers):

- `c9/w24/<id>.wav`: 25 s of each song of `songs.tsv`, mono 24 kHz (`ffmpeg -ss <start> -t 25 -ac 1 -ar 24000`);
  `c9/w16/` the same at 16 kHz (AST) and `clips48/` at 48 kHz (CLAP).
- `vocab.json`: the tag vocabulary of `internal/assistant/assist/tagvocab.go` as `{category: [tags]}`.
- `muq_genres.py` runs in the `veritmo/diffrhythm2:1` image (it carries MuQ-MuLan; weights of
  `models/music/diffrhythm2` mounted at `/ckpt`); `clap_genres.py` and `ast_tags.py` in `veritmo/clap:1`
  (AST weights: `MIT/ast-finetuned-audioset-10-10-0.4593` in a hub folder mounted at `/ckpt`).
- `llm_variants.py` asks a local Ollama (`qwen3:8b`, CPU) the `listen` task with the facts of each variant of the
  study; it reads `system.txt` (the system prompt of `internal/assistant/assist/listen.go`), `c9/extra.json` (output of
  `extra_features.py`), `c9/ours.txt` (the sound figures of `internal/music/audiomeasure`), `c9/muq.txt` and `c9/ast.txt`.
