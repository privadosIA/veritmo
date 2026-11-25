# prompt-fetch Specification

## Purpose
TBD - created by archiving change add-prompt-fetch-command. Update Purpose after archive.

## Requirements

### Requirement: The command fills the store from the enabled sources
`veritmo prompts fetch` SHALL process every source of the sources file that is enabled (or only `--source id`; an unknown id is an error naming the ids), and write the resulting presets as bundled, read-only presets (`own: false`) into `<MEDIA_DIR>/prompts/<kind>/` (the media directory from `--media-dir`, `MEDIA_DIR`, or the path in `~/.config/veritmo/media-dir`; `prompts/` beside the projects directory when there is none). With `--out file` it SHALL write the `{format: "prompt-presets", version: 1, meta, items}` bundle instead and touch no store. With `--dry-run` it SHALL download and report but write nothing. Running it twice SHALL add nothing the second time (a preset with the same `kind`, `source` and `source_id` is already there).

#### Scenario: First and second run
- **WHEN** the command runs twice with the same sources and seed
- **THEN** the first run writes the presets and the second reports them as already there and writes none

#### Scenario: A bundle file instead of the store
- **WHEN** it runs with `--out bundle.json`
- **THEN** the file holds the presets in bundle format and the store folder is unchanged

### Requirement: The licence is read from the dataset card, not declared
For each enabled source the command SHALL request `https://huggingface.co/api/datasets/<repo>` and take `cardData.license` (a string or a list, lower-cased; absent or empty is `unknown`). The source SHALL be used only when every licence read is in the allow-list (`cc0-1.0`, `cc-by-4.0`, `apache-2.0`, `mit`) and the source's `expected_licence` is among them. Otherwise the source SHALL be refused: nothing of it is downloaded or written, and the report names it with the licence read and the reason. The sha of the dataset from the card SHALL be kept in the report. Every preset written SHALL carry the licence read as its `licence` and the repo as its `source`.

#### Scenario: The card changed
- **WHEN** a source expects `cc0-1.0` and its card now says `cc-by-nc-4.0`
- **THEN** the source is refused with that licence in the report and nothing is written from it

#### Scenario: No licence on the card
- **WHEN** the card declares none
- **THEN** the licence is `unknown` and the source is refused

### Requirement: Only the named files and columns are read
The command SHALL download only the files a source names (`https://huggingface.co/datasets/<repo>/resolve/main/<file>`, with `HF_TOKEN` sent as a bearer token when set and never stored or logged) into `<MEDIA_DIR>/prompts/.cache/` (reused when present, written complete or not at all), and SHALL read only the columns the source names (the prompt, the id, tags, flags and NSFW scores): a column that is not named SHALL never be loaded, by projection at read time and not by filtering afterwards. No image or audio column is named. The cache folder SHALL NOT be read as presets.

#### Scenario: A lyrics column
- **WHEN** a CSV source has a lyrics column that no source entry names
- **THEN** no text of that column reaches a preset, the report or the log

#### Scenario: Reused download
- **WHEN** a file is already in the cache
- **THEN** it is not downloaded again

### Requirement: Rows are cleaned and mapped by fixed rules
Each row SHALL be trimmed with its whitespace collapsed, then dropped, counting each drop under its rule, when: the prompt is empty; a source NSFW score is over the source's threshold (a missing score is not over); it is under 12 or over 2000 characters; it is only a URL; it has no letter once weights and numbers (`(word:1.3)`, `:0.8`) are taken away; for a source with no scores of its own, it contains a word of a short explicit-word list; or its normalised form (lower case, no punctuation, single spaces) was already kept (the first stays). A kept row SHALL become a preset with `title` (the text before the first comma, or 60 characters, without trailing punctuation), `tags` (the fragments after the subject, lower-cased, 2 to 30 characters, no weights, no duplicates, at most 12, plus the source's own categories and flags), the source's `kind` and `engine`, `language` (`es` when common Spanish words dominate, else the source's), `source` (the repo), `source_id` (the source's id column, or a hash of the normalised prompt) and `nsfw: false` when the source had a score.

#### Scenario: The dirty rows
- **WHEN** a source holds a duplicate, a 5-character prompt, a URL-only prompt and one with `prompt_nsfw` 0.9
- **THEN** none is kept and the report counts one drop under each of `duplicate`, `too_short`, `url_only` and `nsfw`

### Requirement: Sampling is seeded and happens after cleaning
After cleaning, each source SHALL be capped to `--max` (default the source's `max_rows`) with a sample chosen by a deterministic generator seeded by `--seed` (default 42), keeping the original order of the rows. The same sources file, seed and rows SHALL give the same list; the result SHALL be ordered by `kind`, `source` and `source_id`. A source SHALL be read as a stream, never loaded whole into memory (DiffusionDB has 2 million rows in one row group).

#### Scenario: Same seed, same bundle
- **WHEN** the command runs twice on the same rows with seed 42
- **THEN** both runs choose the same presets

### Requirement: The run is audited
The command SHALL print and save (next to the cache) a report that lists, per source used, the licence read, the dataset's sha, the rows read, the drops per rule, the rows kept and the rows written; the sources refused with the licence read and the reason; and the sources that are off with their note. A source that is off SHALL be used only when `--source` names it and its note is shown; the Suno music source SHALL stay off in the embedded list, with the reason that it is for strictly local use and never for a shared bundle.

#### Scenario: Off by default
- **WHEN** the command runs with no `--source`
- **THEN** the sources that are off are listed with their notes and none is read

### Requirement: The command works with no network in tests
The sources file, the card and the downloads SHALL be reachable through one seam so that the tests run on fixture files and fake cards: a parquet file with dirty rows (written by the test), a CSV with a lyrics column carrying a marker that must never appear in any output, and cards with `cc0-1.0`, `apache-2.0`, `cc-by-4.0`, `unknown` and a non-commercial licence.

#### Scenario: Offline run
- **WHEN** the tests run without network access
- **THEN** they pass
