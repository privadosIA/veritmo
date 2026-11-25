# storage Specification

## Purpose
TBD - created by archiving change add-storage-cleanup. Update Purpose after archive.

## Requirements

### Requirement: Leftovers of removed scenes are pruned after a run

After a video run the orchestrator SHALL remove from the project's `tmp/` the files named as artifacts of a scene (`scene_<id>` with the extensions `.wav`, `.png`, `.mp4`, `.cut.mp4`, `.init.png`, `.anim.mp4` or `.anim.avi`, or an image variant `_v<N>`) whose scene is no longer in the project, and SHALL NOT touch any other file of `tmp/`.

#### Scenario: A scene is removed
- **WHEN** a scene is deleted from the project and a run finishes
- **THEN** its files are no longer in `tmp/` and the other scenes' files and the manifest are

#### Scenario: Other files stay
- **WHEN** `tmp/` holds the manifest, the concat list and the soundtrack
- **THEN** none of them is removed

### Requirement: The storage of a project is shown part by part

The server SHALL report, for a project, the size and the number of files of each of its parts: cache (`tmp/` without leftovers), leftovers, clips, animations, final (the rest of `out/`), sources, reviews and backups (`scenes.yaml.*`), and the total, without following symbolic links. It SHALL also report the size of every project and the free and total space of the disk that holds them.

#### Scenario: Breakdown
- **WHEN** the storage of a video project is requested
- **THEN** each part comes with its size and the parts add up to the total

#### Scenario: Symbolic links
- **WHEN** a project has a `models` link to a large folder
- **THEN** the folder is not counted

### Requirement: Parts of a project can be cleaned

The user SHALL be able to clean the cache, the leftovers, the clips, the animations, the reviews and the backups of a project, each on its own, and the server SHALL answer how much it freed. The final video and exports and the sources SHALL NOT be cleanable this way. A project with a queued or running run SHALL be refused (409). The dashboard SHALL state what each part costs before cleaning it and ask to confirm.

#### Scenario: Clean the animations
- **WHEN** the user cleans the animations of a project
- **THEN** the `.anim.mp4` files of `out/clips/` are removed, the clips and the final video are not, and the freed bytes are reported

#### Scenario: Not cleanable
- **WHEN** the clean of `final` or `sources` is requested
- **THEN** the server answers 400 and removes nothing

#### Scenario: A run is active
- **WHEN** a run of the project is queued or running
- **THEN** the clean is refused with 409

### Requirement: The projects list shows how much each project takes

The projects list SHALL show the size of each project in a column that can be sorted, and the free space of the disk, and each row SHALL open the storage of its project.

#### Scenario: Sort by size
- **WHEN** the user sorts the list by size
- **THEN** the largest project comes first when descending

### Requirement: A low disk is warned about

The server SHALL report the free and total space of the disk that holds the projects, the threshold below which the space is low (20 GB, or the value of `VERITMO_MIN_FREE_GB` in GB, where 0 disables the warning) and whether it is low, in the storage overview and in the environment report. The Environment screen SHALL show the disk with a warning when it is low, the projects list SHALL mark its free-space line when low, and the launch form of Runs SHALL show a warning before a run when low. The answer to launching a run SHALL carry a `warnings` list with the low-space message when it applies. A low disk SHALL NOT prevent a run from starting.

#### Scenario: Low space
- **WHEN** less than the threshold is free
- **THEN** the Environment screen, the projects list and the launch form say so, and launching a run still works and its answer has the warning

#### Scenario: Enough space
- **WHEN** more than the threshold is free
- **THEN** no warning appears and the launch answer has none

#### Scenario: Changing the threshold
- **WHEN** the server starts with `VERITMO_MIN_FREE_GB=100`
- **THEN** a disk with 80 GB free is reported as low

#### Scenario: Disabled
- **WHEN** `VERITMO_MIN_FREE_GB=0`
- **THEN** the disk is never reported as low

### Requirement: The storage report gives the space of the media disk

`GET /api/storage` SHALL include `media` with the configured `dir` (the absolute path, as the server sees it, because a client may write files there too), the `folder` of each project inside it and the `disk` of that directory (`free_bytes`, `total_bytes`, `warn_below_bytes`, `low`), so a client can compute how much is left to write. Without a media directory `media` SHALL be absent and `disk` keeps describing the disk of the projects. The low-space warning when launching a run SHALL use the disk where the results are written.

#### Scenario: Free space of the media disk
- **WHEN** the server runs with `--media-dir /srv/media`
- **THEN** `GET /api/storage` answers `media.dir` and `media.disk.free_bytes` of the disk of that directory

#### Scenario: A client that writes there
- **WHEN** a dashboard that writes into the media directory asks `GET /api/storage` before and after writing
- **THEN** `media.dir` is the same path and `media.disk.free_bytes` reflects the space left at that moment (it is read on every request, never cached)

#### Scenario: Not configured
- **WHEN** no media directory is configured
- **THEN** the answer has no `media` and `disk` is the disk of the projects

### Requirement: The size of a project counts the files behind the media links

The storage of a project SHALL count the files behind the links into the media directory (`scenes.yaml`, `sources`, `.reviews`, `out`, `tmp`) and only those; other symbolic links such as `models` stay uncounted, so every part and the cleaning of each part work the same.

#### Scenario: Project in the media directory
- **WHEN** the files of a project are in the media directory
- **THEN** the report shows the same parts and sizes as if the files were inside the project

### Requirement: The storage report gives the models directory and its disk

When a models directory is configured, `GET /api/storage` SHALL include `models` with its absolute `dir` and the `disk` of that directory (`free_bytes`, `total_bytes`, `warn_below_bytes`, `low`), read on every request. `GET /api/environment` SHALL carry the same `dir`. Without a models directory `models` SHALL be absent. A project's storage SHALL keep not following its `models` and `voices` links.

#### Scenario: Space of the models disk
- **WHEN** the server runs with a models directory
- **THEN** `GET /api/storage` answers `models.dir` and the free space of that disk

#### Scenario: Project links
- **WHEN** a project has the `models` link
- **THEN** the models are not counted in the size of the project

### Requirement: The storage list says who made each project and can include the managed ones

Each project of `GET /api/storage` SHALL carry `client` (as in the project list) and, for a managed project, `kind`. The request SHALL accept `managed=include` (the managed projects appear as one more project; any other value is a `400`) and `client=a,b` (only those clients). Without them the list is what it was.

#### Scenario: Included with a client
- **WHEN** `GET /api/storage?managed=include` and a managed project of `video-studio` exists
- **THEN** it is in `projects` with its size, `client: "video-studio"` and its `kind`

### Requirement: The totals of the disk are the disk's

The `disk` and `media.disk` of `GET /api/storage` SHALL be the free and total space of the disk, never a sum of the projects listed, so listing managed projects SHALL NOT change them and SHALL NOT count anything twice. `GET /api/projects/{id}/storage` of a managed project SHALL work as for any project.

#### Scenario: Same totals
- **WHEN** the storage is read with and without `managed=include`
- **THEN** `disk` is the same in both and only `projects` differs

### Requirement: The backups of a definition are its versions

The part `backups` of the storage of a project SHALL be the versions of its definition kept in the database: the report SHALL give how many there are and their bytes, and cleaning `backups` SHALL delete all of them but the newest. A `scenes.yaml.bak` file left in the directory after the import SHALL be counted and cleaned with them.

#### Scenario: Cleaning the backups

- **WHEN** a project with 8 versions has its `backups` cleaned
- **THEN** it has 1 version and its definition is unchanged

### Requirement: The list of projects does not measure the disk

The size the list of projects shows for each project SHALL be a value kept with the project, measured when a run of the project ends, when its storage is cleaned, when its sources change and when it is imported. `GET /api/projects/{id}/storage` SHALL keep measuring the disk when it is asked, and SHALL update the kept value.

#### Scenario: A list of many projects

- **WHEN** the list of projects is asked
- **THEN** no directory of a project is walked to answer it

#### Scenario: After a run

- **WHEN** a run ends and the list is asked
- **THEN** the size of that project is the one after the run
