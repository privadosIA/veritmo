# models-directory Specification

## Purpose
Lets the user keep the model files in one directory, possibly on another disk, while every path that names `models/<file>` keeps working.

## Requirements

### Requirement: The models directory is set by a flag, a variable or a user file

The server SHALL take the models directory from `--models-dir`, else from `MODELS_DIR`, else from the first line of `~/.config/veritmo/models-dir` (in `$XDG_CONFIG_HOME` when it is set). With none of them nothing SHALL change: `models/` stays where it is. `MODELS_DIR=off` SHALL ignore the user file.

#### Scenario: Flag wins
- **WHEN** the flag, the variable and the file name different directories
- **THEN** the flag is used

#### Scenario: Nothing set
- **WHEN** no models directory is given
- **THEN** the server runs as before and reports no `models` in the storage answer

### Requirement: `models` is a link to the models directory

With a models directory the repository's `models` SHALL be a symbolic link to it, so that `models/<file>` resolves there for the pipeline, the engines, the installer, and the projects (`models -> ../../models`). At start the server SHALL warn, naming the command, when `models` is a real folder or leads elsewhere, and SHALL keep working with what it finds.

#### Scenario: Linked
- **WHEN** `models` leads to the configured directory
- **THEN** a scene that names `models/<file>` finds the file there and the server starts without a warning

#### Scenario: Not migrated yet
- **WHEN** a models directory is configured and `models` is still a real folder
- **THEN** the server warns that `veritmo models migrate` is pending and keeps using that folder

### Requirement: The models directory is checked and never replaced by the local disk

At start the server SHALL create nothing in it and SHALL report, naming the directory, when it is configured and not available (not there, not a folder or not writable); the listing of models SHALL then say the directory is unavailable instead of showing no models. While it is unavailable no model SHALL be downloaded or converted, and no file of a model SHALL be written to the local disk.

#### Scenario: Disk not mounted
- **WHEN** the directory is configured and its disk is not mounted
- **THEN** the server says the models directory is unavailable, the models are not listed as missing, and installing one is refused

### Requirement: Existing models are migrated without loss

`veritmo models migrate` SHALL copy the contents of `models/` to the models directory, check that every file arrived with its name and size, rename the original to `models.migrated`, create the link and only then delete the old copy. It SHALL skip what is already in the destination with the same size (so an interrupted run resumes), SHALL be repeatable, SHALL support `--dry-run` (what would be copied and the space it needs), SHALL refuse when the destination lacks the space, and SHALL refuse while a server is running unless told otherwise. It SHALL NOT delete the original when the check fails, and SHALL leave `voices` and everything outside `models/` untouched.

#### Scenario: Migration
- **WHEN** `models/` is a real folder and the command runs
- **THEN** every file is in the models directory, `models` is a link to it and a scene naming `models/<file>` reads the same file

#### Scenario: Interrupted
- **WHEN** the copy is cut half way and the command runs again
- **THEN** the files already copied with the same size are skipped and the rest are copied

#### Scenario: Not enough space
- **WHEN** the models need more than the free space of the destination
- **THEN** nothing is copied and the message says how much is needed and how much is free

#### Scenario: Dry run
- **WHEN** the command runs with `--dry-run`
- **THEN** it lists what would be copied and the space needed, and changes nothing
