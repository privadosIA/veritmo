# music-word-timing Specification

## Purpose
Gives the song map of a music piece the real time of every sung word, heard with Whisper and aligned to the written lyrics, so that the text of a piece enters when the voice sings it.

## Requirements

### Requirement: The time of each word is heard in the song

The server SHALL be able to read a song with Whisper by word and give, for each word heard, its text, its start and end in seconds and its confidence (0 to 1). It SHALL use the same `whisper-cli`, model, memory guard and 16 kHz mono conversion as the transcription of a source, SHALL NOT use the network, SHALL carry no previous text into the next window (as that transcription does), and SHALL drop what Whisper invents on silence (a word of no length, a long repetition of the same word, a word whose confidence is below a fixed floor). The times SHALL be the per-token timestamps Whisper computes by dynamic time warping, asked for with the preset that matches the installed model and read from the full JSON output, with the words made from the tokens (a token that begins with a space begins a word): the output SHALL NOT be cut into one-word segments to time it. A model that has no timestamp preset, or a run that returns no token time, SHALL fall back to the one-word-segment call and the result SHALL say which timing it used. A song with no words heard SHALL give an empty list and no error.

#### Scenario: A sung song
- **WHEN** a song with a voice is read by word with a `large-v3-turbo` model
- **THEN** `whisper-cli` is called with `-dtw large.v3.turbo` and the full JSON output, and every word heard has a text, a start, an end and a confidence, in order, and none ends before it starts

#### Scenario: A model without a preset
- **WHEN** the installed model's name matches no timestamp preset
- **THEN** the words are read as before, one per segment, and the result says the timing came from segments

#### Scenario: Words the model is not sure of
- **WHEN** a word's confidence is below the floor
- **THEN** it is not in the list

#### Scenario: An instrumental
- **WHEN** a song with no voice is read by word
- **THEN** the list is empty and there is no error

#### Scenario: No room for the model
- **WHEN** the free memory is below what the Whisper model needs
- **THEN** it is refused with how much is missing, and nothing is read

### Requirement: The heard words are aligned to the written lyrics

When a piece has written lyrics, the server SHALL give each written word the time of the heard word it matches, comparing the words in lower case and without punctuation. The written words, their order, their line breaks and their section tags SHALL NOT change: the heard words only give times. A written word that no heard word matches (Whisper wrote a number, an abbreviation or another spelling, or did not hear it) SHALL get a start and an end between those of its nearest timed neighbours, in proportion to its letters and, when a voice envelope is known, only inside the time where the voice is heard between them. A heard word that matches no written word SHALL be left out, and so SHALL a heard word that lies, for most of its span, where the envelope hears no voice. A heard word's confidence SHALL weigh its claim: a sure word may stand in for a written word spelt another way, an unsure one SHALL NOT displace the written order. Every written word SHALL end with a time, no word SHALL start before the one that precedes it, and the same lyrics, the same heard words and the same envelope SHALL always give the same times.

#### Scenario: Words that match
- **WHEN** the written line is `Monday morning, nine a.m.` and Whisper heard `Monday`, `morning`, `9`, `a.m.`
- **THEN** `Monday`, `morning` and `a.m.` take the times heard, and `nine` takes a time between those of `morning` and `a.m.`

#### Scenario: A word heard over the drums
- **WHEN** a heard word lies where the voice envelope is silent
- **THEN** it is left out and no written word takes its time

#### Scenario: A gap with an instrumental bar in it
- **WHEN** two timed written words are 6 s apart, the voice is heard only in the last 2 s of that gap, and a written word between them was not heard
- **THEN** that word is placed inside those last 2 s

#### Scenario: Lyrics the voice does not follow exactly
- **WHEN** the voice repeats a line the lyrics write once, or skips a word
- **THEN** the written words still come out in their written order, each with a time, and the repeated words are left out

#### Scenario: Same input, same times
- **WHEN** the alignment is made twice from the same lyrics, the same heard words and the same envelope
- **THEN** the two results are equal

### Requirement: The song map is made from the heard words

A song map made from heard words SHALL have, for each written word, its aligned start and end, the global number of its line (the order of the written lines of the whole lyrics, the same number the script uses to name a line), whether it took its time from a heard word (`heard`) and that word's confidence (`confidence`, `0` when it was not heard), SHALL start each section where its first word is sung (a moment before it, never before the end of the section that precedes it), SHALL say where its times came from in `words_source` (`estimated`, `heard`, `heard-voice` when the voice was separated first, `edited` once a word was moved by hand) and SHALL say `words_estimated` is `false`. A map whose words were spread over their sections SHALL keep saying `true` and `words_source: estimated`. When the voice was separated, a heard word whose start is within 120 ms of an attack of the voice SHALL be moved onto that attack, keeping its length, unless that would put its start within 40 ms of the word before or the word after; such a word SHALL say `snapped: "attack"`, and the map SHALL say how many attacks were found (`attacks`). A heard word that was not moved onto an attack and whose start is within 60 ms of a beat (or of the half beat in a song under 100 bpm) SHALL be moved to that beat, keeping its length, unless that would take it before the word that precedes it or after the one that follows it, and SHALL say `snapped: "beat"`; a word moved onto an attack SHALL NOT be moved again. A word that was not heard SHALL NOT be moved. The beats and the loudness of the map SHALL NOT change. A map saved before this requirement SHALL still load, with `words_source` and `heard` derived from `words_estimated`, `confidence` zero, no `snapped` and `attacks` zero.

#### Scenario: Sections where the voice enters
- **WHEN** the chorus is first sung at 62.4 s
- **THEN** the chorus section starts at or just before 62.4 s and the section before it ends there

#### Scenario: A word near an attack
- **WHEN** the voice was separated, a heard word starts at 33.46 s and an attack of the voice falls at 33.38 s
- **THEN** the word starts at 33.38 s in the map, its length is the same and it says `snapped: "attack"`

#### Scenario: An attack too close to the neighbour
- **WHEN** the nearest attack would put a word's start 20 ms after the previous word's start
- **THEN** the word is not moved onto it

#### Scenario: A word just off the beat
- **WHEN** a heard word with no attack within 120 ms starts at 31.46 s and a beat falls at 31.50 s
- **THEN** the word starts at 31.50 s in the map, its length is the same and it says `snapped: "beat"`

#### Scenario: A word that was interpolated
- **WHEN** a written word took a time between its neighbours and an attack or a beat falls 20 ms from it
- **THEN** it is not moved and `heard` is `false`

#### Scenario: Line numbers do not change
- **WHEN** a map whose times were estimated is rebuilt from heard words
- **THEN** every written line has the same number in both maps, so a script that names lines still names the same ones

#### Scenario: Estimated and heard
- **WHEN** a map is made from heard words on the mix
- **THEN** `words_estimated` is `false`, `words_source` is `heard` and `attacks` is 0; made from the separated voice it is `heard-voice` with `attacks` above 0; made without heard words it is `true` and `estimated`

#### Scenario: An old map
- **WHEN** a map written before this change is loaded
- **THEN** it loads, its times are unchanged, and it carries `words_source` and `heard` consistent with its `words_estimated`

### Requirement: Without written lyrics the lines come from what was heard

When a piece has no written lyrics and words were heard, the server SHALL make the lines of the map from them: a new line starts after a silence of more than 0.8 s between two words, and the map has one section for the song. The words SHALL be the heard ones, as written by Whisper.

#### Scenario: A song with no lyrics given
- **WHEN** words are heard in a song whose piece has no lyrics
- **THEN** the map has lines made from the heard words, split where the voice pauses, and one section

### Requirement: Hearing the words is asked for, not assumed

Creating a piece SHALL accept `transcribe` (a boolean, `false` by default). With `true` the words are heard before the map is made; when Whisper is not installed, does not fit in memory, or a run is in progress, the piece SHALL be created anyway with estimated times and the answer SHALL say why they were not heard. `POST /api/music-motion/pieces/{id}/transcribe` SHALL hear the words of an existing piece and rebuild its map, and SHALL answer the piece: `409` while a run is queued or active (including the drawing of the piece itself), `503` with the reason when Whisper is not installed or does not fit in memory, and `404` for an unknown piece. It SHALL NOT change the design, the level, the style or the frame rate.

#### Scenario: Asked at creation
- **WHEN** a piece is created with `transcribe: true` and Whisper is available
- **THEN** its song map has `words_estimated: false`

#### Scenario: Asked at creation without Whisper
- **WHEN** a piece is created with `transcribe: true` and Whisper is not installed
- **THEN** the piece is created with estimated times and the answer says the words were not heard and why

#### Scenario: Improving a piece
- **WHEN** the route is called for a piece with estimated times and Whisper is available
- **THEN** the answer is the piece with the map rebuilt from heard words

#### Scenario: A run in progress
- **WHEN** the route is called while a run is queued or active
- **THEN** the answer is `409` and the piece is unchanged

#### Scenario: Not by default
- **WHEN** a piece is created with no `transcribe`
- **THEN** Whisper is not run and the times are estimated

### Requirement: The script follows the new sections

When the map of a piece is rebuilt from heard words, a script the user did not edit SHALL be made again with the fixed rule from the new map (keeping the level, the style and the stages of the illustrated style), so its pieces start and end where the voice does. A script the user edited SHALL be kept as it is, and the answer SHALL say that it may need adjusting, naming how many pieces now start or end more than a second from a section boundary. The design SHALL NOT change.

#### Scenario: A script nobody edited
- **WHEN** the words of a piece with the proposed script are heard
- **THEN** the script is made again from the new sections and its pieces cover the song with no gap or overlap

#### Scenario: A script the user edited
- **WHEN** the words of a piece whose script the user edited are heard
- **THEN** the script is the same as before and the answer warns that it may need adjusting

### Requirement: The voice can be separated first, on the CPU

When the voice separator (spec `song-analysis`) and its model are installed, hearing the words SHALL be able to run on the separated voice instead of the mix: the song is separated on the CPU, the voice stem is converted to 16 kHz mono and read by Whisper, the attacks and the envelope of the voice are found from it and kept for the alignment. When the piece has written lyrics and the words Whisper heard on the stem match fewer than 75 % of the written words, the server SHALL also read the mix with Whisper and keep the hearing that matches more written words (the voice on a tie), while the envelope and the attacks SHALL stay the stem's in either case; the map SHALL still say `words_source: heard-voice`, and when the mix was kept the answer's note SHALL say so with both counts. Without written lyrics this test SHALL NOT run. The stem SHALL be kept with the piece so that hearing the words again does not separate again, and SHALL go when the piece goes. Creating a piece and `POST /api/music-motion/pieces/{id}/transcribe` SHALL accept `separate`: `auto` (the default: separate when the separator is installed and fits in memory, else hear the mix and say why), `true` (`503` with the reason when it cannot), `false` (never use the voice). The default SHALL be the one the bench decides (see the requirement "The timing is measured against the voice"). Separation SHALL wait like Whisper does: not while a run is queued or active, not when the model does not fit in memory. A stem in which the voice is heard for less than 5 % of the song SHALL be treated as no stem, and the mix heard instead, with a note. Without the separator installed nothing SHALL change from hearing the mix.

#### Scenario: Installed, auto
- **WHEN** a piece is created with `transcribe: true` (`separate` absent or `auto`) and the separator is installed and fits
- **THEN** the voice is separated, Whisper reads the stem, and the map says `words_source: heard-voice` with `attacks` above 0

#### Scenario: The stem loses words
- **WHEN** the lyrics have 95 written words, Whisper hears 53 of them on the stem and 91 on the mix
- **THEN** the words of the map come from the mix, placed with the stem's envelope and attacks, the map says `heard-voice`, and the note says the voice heard 53 of 95 and the mix 91

#### Scenario: The stem hears enough
- **WHEN** Whisper hears 110 of 112 written words on the stem
- **THEN** the mix is not read and nothing changes from today

#### Scenario: No lyrics
- **WHEN** the piece has no written lyrics
- **THEN** the stem's hearing is kept as it is and the mix is not read for comparison

#### Scenario: Not installed, auto
- **WHEN** `separate` is `auto` (or absent) and `demucs` is not installed
- **THEN** the mix is heard, the map says `heard`, `attacks` is 0, and the answer says the voice was not separated and how to install the separator

#### Scenario: No room, auto
- **WHEN** `separate` is `auto` and the free memory is below what the separator needs
- **THEN** the mix is heard and the answer says how much memory is missing

#### Scenario: Not installed, required
- **WHEN** `separate: true` and `demucs` is not installed
- **THEN** the answer is `503` with the command that installs it and the piece is unchanged

#### Scenario: Heard again
- **WHEN** the words of a piece that was separated are heard again
- **THEN** the kept stem is used and `demucs` is not run

#### Scenario: A stem with no voice
- **WHEN** the voice stem is voiced for less than 5 % of the song
- **THEN** the mix is heard instead and the answer says so

### Requirement: A word can be moved by hand

`PATCH /api/music-motion/pieces/{id}/words` `{"words": [{"index", "start", "end"?}]}` SHALL move the named words of the song map to the given times, in song seconds: `index` is the word's position in `song_map.words`; a missing `end` keeps the word's length. It SHALL answer `422` for an unknown index, a time outside the song, an end before the start plus the shortest word, or a start that would put a word before the one that precedes it or after the one that follows it (words are moved, never reordered); `409` while a run is queued or active; `404` for an unknown piece. On success the map SHALL say `words_source: edited`, the moved words SHALL say `heard: false` and `confidence: 0`, the sections SHALL start again where their first word is sung, and the script SHALL follow the same rule as when the words are heard: a script the user did not edit is proposed again from the new map, a script the user edited is kept and the answer warns that it may need adjusting. The design, the level, the style and the frame rate SHALL NOT change. The same body sent twice SHALL give the same map.

#### Scenario: One word moved
- **WHEN** word 12 is moved from 31.5 s to 31.2 s and word 11 ends at 30.9 s
- **THEN** word 12 starts at 31.2 s with its length, `words_source` is `edited`, and the unedited script is proposed again

#### Scenario: A move that crosses a neighbour
- **WHEN** word 12 is moved to 30.5 s while word 11 starts at 30.7 s
- **THEN** the answer is `422` and the map is unchanged

#### Scenario: While drawing
- **WHEN** the route is called while the piece's render is queued or active
- **THEN** the answer is `409` and the map is unchanged

### Requirement: The frame data names the word and the line being sung

The `beat` object a page receives on every frame (`window.__partSeek(t, beat)`) SHALL also carry `word` and `line`: the written word being sung at that frame (`{text, index, line, start, end, position}`, `position` from 0 at its start to 1 at its end, in the time of the page like `beat.time`) and the written line it belongs to (`{index, text, start, end, position}`), or `null` for each when no word or no line is being sung. The words of `window.__scene.lines[].words[]` SHALL carry `heard`. A part that ignores the fields SHALL be unchanged. The instruction given to the writer of scene parts SHALL name the two fields.

#### Scenario: Inside a word
- **WHEN** a frame falls at 40 % of the span of the word `billing` in line 3
- **THEN** `beat.word` is that word with `position` 0.4 and `beat.line` is line 3 with its position

#### Scenario: Between lines
- **WHEN** a frame falls in an instrumental break
- **THEN** `beat.word` and `beat.line` are `null`

### Requirement: The timing is measured against the voice

The server SHALL ship a command, `veritmo words bench <project-dir> [--songs n] [--out file]`, that takes up to `n` songs of a music project with written lyrics and, for each, separates the voice once, finds its attacks, and hears the words three ways (one word per segment on the mix, token times on the mix, token times on the separated voice). The stick SHALL be the attacks: for the heard words that lie inside the written sections (from the first to the last written word, a second each side), the signed offset from the word's start to the nearest attack within 300 ms. Per way it SHALL report: the words, the heard ones, those with an attack within 300 ms, the median absolute offset in milliseconds, the signed median (the lag), the shares within 50, 100 and 150 ms, the attacks inside the span with no word start within 300 ms, the share of word starts within 60 ms of a beat next to the share a uniformly random start would get at that tempo, and the seconds taken; for the voice way it SHALL also report the shares within 50, 100 and 150 ms after the words are moved onto the attacks. The tempo is measured on the song when the manifest has none, as the server does. Without the separator the command SHALL say it cannot measure and exit 0. The JSON written with `--out` SHALL carry, per song and way, every word with its index, text, start, end, `heard`, `confidence`, offset and `snapped`, and the attacks, so that a report can be measured again without the audio.

The decision: `separate: auto` separates by default only if, on the median song over at least 8 distinct songs (songs with different lyrics, not variants of one track), moving the words onto the attacks lifts the share of heard words within 50 ms of an attack by at least 20 points, and the voice way takes under three times the length of the song. With fewer songs, or songs that share lyrics, the command SHALL print the numbers and `decision: not decided` and the default SHALL NOT change. Before the change is archived the command SHALL have been run on at least 8 distinct songs and its numbers, the machine, the date and the decision written in `docs/MUSIC_MODE_MOTION.md`, replacing the earlier table with one sentence saying that its stick counted phrase starts.

#### Scenario: The report
- **WHEN** the command runs on a project with 10 distinct songs with lyrics and the separator installed
- **THEN** it prints one table per song and a summary with the medians per way, the shares after snapping for the voice way, a decision, and writes the words and the attacks to `--out` when given

#### Scenario: Too few songs
- **WHEN** the command runs on 6 songs, four of which share their lyrics
- **THEN** it prints the numbers for the 3 distinct ones and `decision: not decided`

#### Scenario: Without the separator
- **WHEN** the separator is not installed
- **THEN** the command says it cannot measure the words against the voice and exits 0

#### Scenario: The documentation
- **WHEN** the change is archived
- **THEN** `docs/MUSIC_MODE_MOTION.md` states the songs measured, the numbers, the decision and why the earlier table was replaced

### Requirement: The attacks of the voice are found

From the separated voice the server SHALL find the attacks of the voice: the moments where its sound starts or changes sharply (a syllable), found as peaks of the spectral flux of the voice above an adaptive threshold, at least 80 ms apart and only where the voice envelope hears voice. On a sung song this SHALL give about one attack per syllable (a 120-second song with 129 words gives a few hundred, not a few dozen). The same stem SHALL always give the same attacks. Without a separated voice there are no attacks and nothing that depends on them runs.

#### Scenario: A sung line
- **WHEN** the voice stem of a line of five syllables sung clearly is analysed
- **THEN** about five attacks are found inside the line and none in the silence before it

#### Scenario: Same stem, same attacks
- **WHEN** the attacks are found twice from the same stem
- **THEN** the two lists are equal
