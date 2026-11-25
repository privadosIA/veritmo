# hyperframes-catalog Specification

## Purpose
TBD - created by archiving change add-hyperframes-catalog. Update Purpose after archive.

## Requirements

### Requirement: A block is kept whole

The system SHALL store each HyperFrames block as a unit with its HTML, the libraries and assets it declares, its licence and its registry metadata (name, title, tags, dimensions, duration, version), and SHALL record where it came from. A block SHALL NOT be rewritten to fit the part contract.

#### Scenario: Import keeps the block
- **WHEN** a block with a `lib/` folder and an `assets/` folder is imported
- **THEN** all its files are stored together and its licence and origin are listed with it

#### Scenario: Nothing is refused for being a canvas or having assets
- **WHEN** the 210 blocks of the registry are imported
- **THEN** a block is not left out because it uses a canvas, `assets/`, an external script or a shader; it is left out only if a minimum check below fails, and then it is in the report

### Requirement: Libraries live on the server

The libraries a block loads from outside SHALL be downloaded once, pinned by version and sha256, stored locally and served by the server. A page drawn for a block SHALL NOT request anything from the network. The references to a CDN in a block SHALL be resolved to the local copy when it is drawn, without changing the stored block.

#### Scenario: Drawn offline
- **WHEN** a block that uses three and gsap is drawn with the network cut
- **THEN** it draws the same as with the network

#### Scenario: A hash that does not match
- **WHEN** a downloaded library does not match its pinned sha256
- **THEN** it is not installed and the failure names the library

### Requirement: Minimum checks and an import report

A block SHALL pass these checks to be usable: it draws with no network request, two passes at the same moments give the same frames, no script error. It SHALL also warn, without refusing the block, when nothing changes over its length (a lower third or a backdrop may be still by design). The import SHALL write a report with one line per block (ok, or the checks that failed and why) and SHALL NOT stop at the first failure.

#### Scenario: Report of the whole registry
- **WHEN** the 210 blocks are imported
- **THEN** the report has 210 lines and states how many passed and the reason of each that did not

#### Scenario: Two passes differ
- **WHEN** a block gives different frames on two passes at the same moment
- **THEN** it is marked as not usable with the moments that differ

### Requirement: Every block has a thumbnail

Each block SHALL have a thumbnail returned by a route. The server SHALL draw a frame of the block and keep it; the registry's own poster and video (remote addresses) SHALL NOT be fetched. The frame SHALL be taken after the block's first visible moment, not at time zero.

#### Scenario: Thumbnail of a block
- **WHEN** the thumbnail of a block is requested for the first time
- **THEN** a frame drawn by the server is returned and the next request returns the same stored image

### Requirement: Blocks can be listed, edited and previewed

The catalog route SHALL list the blocks with name, title, tags, dimensions, duration, licence, state of the checks and thumbnail, and SHALL filter by tag and text. A block SHALL be editable (its HTML and its variables) with a preview drawn from the edited text before saving; saving SHALL run the minimum checks again and SHALL refuse a block that fails them, naming the check.

#### Scenario: Preview before saving
- **WHEN** an edit is previewed
- **THEN** a frame of the edited block is returned and the stored block has not changed

#### Scenario: A bad edit is refused
- **WHEN** an edit that makes the block request a network address is saved
- **THEN** it is refused naming the check and the stored block stays as it was

### Requirement: A block can be rendered to a clip without a song

A block SHALL be renderable to a video clip with a chosen duration, frame rate and variables, with no song and no beat clock, so that the clip can be used as a scene of a project (a scene's `clip`). The block's own timeline SHALL be sought to the frame being drawn.

#### Scenario: Block as a clip
- **WHEN** a block is rendered with a duration of 4 seconds at 30 fps
- **THEN** a video of 120 frames is produced whose frames follow the block's timeline, and the answer says where it is

#### Scenario: A value for a variable
- **WHEN** a block is rendered with a value for one of the variables it declares
- **THEN** the clip shows that value in place of the default

### Requirement: The libraries are an installable component

The installer SHALL offer the block libraries as an optional component that downloads them with their hashes, and the server SHALL report whether they are installed.

#### Scenario: Not installed
- **WHEN** the libraries are not installed and a block that needs them is drawn
- **THEN** the request fails saying which library is missing, and nothing is fetched from outside

### Requirement: The installer brings every block, optionally

The installer SHALL offer an optional component that brings in all the blocks of the pinned HyperFrames commit together with the libraries they need, importing them whole into the catalog. It SHALL NOT be part of what is installed when the user declines optional components, and re-running it SHALL keep the blocks the user edited.

#### Scenario: Everything in one component
- **WHEN** the component is installed on a machine with the orchestrator built
- **THEN** the libraries are installed and the 210 blocks are in the catalog

#### Scenario: Declined
- **WHEN** the user installs without optional components
- **THEN** no block and no library is downloaded and the rest of the server works as before

### Requirement: The licence travels and is shown in the server and in the dashboard

The server SHALL give, with each block, its licence (as the registry names it), where it comes from and the text of the licence files that came with it. The dashboard SHALL show the licence on the card of the block in the catalog and again in its detail, with the origin.

#### Scenario: Licence files with the block
- **WHEN** the detail of a block that came with licence files (`*LICENSE*`, `*OFL*`) is read
- **THEN** their names and texts are in the answer next to the licence name

### Requirement: A block can be a layer over a verse

A script of a music piece SHALL accept a block as an item of a layer, over a range of the song, with its variables. The block SHALL be drawn inside the page of every piece its range touches, over what is under it and under the layers above it, and its timeline SHALL be sought to the time since the item's own start. A block that is not usable (a check failed, libraries missing) SHALL be refused when the script is saved, naming it.

#### Scenario: Block over a verse
- **WHEN** a script has a layer item that names a block from 12 s to 18 s
- **THEN** the frames between 12 s and 18 s show the block over the verse, and the frames outside do not

#### Scenario: Block that cannot be drawn
- **WHEN** a script names a block that is not in the catalog or has failed its checks
- **THEN** saving the script is refused naming the block and the reason

### Requirement: The record of a block is kept in the database

The server SHALL keep the record of each HyperFrames block (its name, title, description, tags, licence, author, size, duration, kind, family, variables, origin, the names of its files and whatever else its `block.json` holds today) as a row of the database. The files of the block (its HTML and what it loads, its thumbnail) and the clips SHALL stay in the folder of the block, because its page loads them by relative path. The blocks that exist SHALL be imported when the server starts (see `data-store`), and `veritmo blocks` SHALL write the records to the same database. `GET /api/hyperframes/blocks` SHALL be a list that can be paged (see `api-listing`), combined with the filters it has today: sort by `name` or `title`, and `q` searched in the name, the title, the description and the tags. `DELETE /api/hyperframes/blocks/{name}` SHALL delete the record and the folder of the block and answer `204`; it SHALL be `404` for an unknown block and SHALL never delete a part that was made from the block.

#### Scenario: After the import

- **WHEN** the server starts with 210 blocks kept as folders
- **THEN** `GET /api/hyperframes/blocks` answers the same blocks, with the same fields, and each thumbnail is still served

#### Scenario: A block deleted

- **WHEN** a block that was imported as a part is deleted
- **THEN** the block is gone from the list and the part is still in the catalog of parts
