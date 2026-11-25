# Releasing

[← Back to the README](../README.md)

A release is a version number (`v0.1.0`) on the public repository, the two images that go with it on GitHub Container Registry
and a GitHub release whose notes are the changelog section of that version. This page is the list of steps; `scripts/` does the
parts that can be scripted.

## Between releases: the changelog

[CHANGELOG.md](../CHANGELOG.md) follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). It is written for a person who
uses the program, grouped by what they can do (music videos and motion graphics, video, images and retouch, music, 3D, editing a
video you have, assistant and providers, dashboard / phone / API, installation and data), with one line per capability, not one
per commit.

**When a change is archived** (`/opsx:archive`), add its line to `## [Unreleased]`, under the group it belongs to, written from
the change's *What changes*. A change with nothing a user could notice (a refactor, tests, documentation of the code) gets no
line. The line goes in the commit that archives the change.

## Making a release

Run these from a clean `main`, in the order given. Version numbers follow [Semantic Versioning](https://semver.org/): while the
major version is 0, a new feature raises the minor number and a fix raises the patch.

1. **Check.** The tests of what changed pass (`go test` of the packages touched, with and without `-tags private`), and
   `bash scripts/check-image.sh` passes on the images built from the tree (see *The images* below).
2. **Date the changelog.** In `CHANGELOG.md` turn `## [Unreleased]` into `## [X.Y.Z] - YYYY-MM-DD`, start a new empty
   `## [Unreleased]` above it, and commit (`Changelog de la X.Y.Z`).
3. **Do not tag `main` with the release name.** The private repository and the public worktree (`../veritmo-public`) are one git
   repository and share one list of tags, and `publish-github.sh` refuses a tag that already exists. The release tag `vX.Y.Z`
   therefore lives only on the public commit (step 5), which is also the one the workflow needs. A binary built from `main`
   says `dev` (`scripts/version.sh` reads the tag it is on or after). To stamp private builds, tag `main` after step 5 with another
   name, for example `git tag -a vX.Y.Z-private -m "Veritmo X.Y.Z (private edition)"`: `version.sh` accepts it, and it is never
   pushed to GitHub.
4. **Sync the public edition.** `bash scripts/sync-public.sh "X.Y.Z"` commits the public tree on `public-release`. Read what it says
   about the README, which the public edition owns.
5. **Publish and tag.** `bash scripts/publish-github.sh --tag vX.Y.Z "Release X.Y.Z"` (first with `--dry-run`). It puts one new commit
   on GitHub and tags it; the push of the tag starts the workflow.
6. **Watch the workflow** `release-images.yml` on GitHub (Actions). It checks that no image holds a model, the source of Stable Fast
   3D or Chrome, builds `veritmo-server` and `veritmo-tools` for `linux/amd64` and pushes them as `X.Y.Z`, `X.Y` and `latest`. If
   the check fails, nothing is pushed: fix it in `main`, delete the tag on GitHub and here (`git tag -d vX.Y.Z` and
   `git push github :refs/tags/vX.Y.Z` from the public worktree), and start again from step 2.
7. **Make the GitHub release.** The notes are the section of this version:

   ```bash
   awk -v v="X.Y.Z" '$0 ~ "^## \\["v"\\]" {p=1; next} p && /^## \[/ {exit} p' CHANGELOG.md > /tmp/notes.md
   gh release create vX.Y.Z --repo privadosIA/veritmo --title "Veritmo X.Y.Z" --notes-file /tmp/notes.md
   ```

8. **First release only:** a package starts private in an organisation: make `veritmo-server` and `veritmo-tools` public in the
   organisation's package settings once.
9. **Try it on a machine that never built them:** follow only the public README (`bash scripts/compose-init.sh`, then
   `docker compose up -d`), and check `GET /api/health`. What got stuck is fixed before the release is announced.
10. **Push the private repository** (`git push origin main public-release github-main --tags`; the release tag goes to Gitea too, on the public commit) and update the site's requirements and its link to the release.

## The images

Only two images are published: `ghcr.io/privadosia/veritmo-server` and `ghcr.io/privadosia/veritmo-tools`. The music, 3D and
compression images are built by whoever uses them (`docker compose build …`), because they carry large stacks or software with
terms of its own.

No published image holds a model, however small. `scripts/check-image.sh <image>` lists the image's files (without running it) and
fails, naming the paths, on a file with a model extension (`.gguf`, `.safetensors`, `.ckpt`, `.pt`, `.pth`, `.onnx`, or `.bin` over
10 MB), any file over 200 MB, anything of Stable Fast 3D, and Chrome for Testing unless [IMAGES.md](IMAGES.md) records that its
terms allow it. The workflow runs the same script before it pushes. What each image contains, with its licence, is in
[IMAGES.md](IMAGES.md).

The version of an image and of the program inside it comes from the tag: the workflow passes it as the build argument `VERSION`,
and `veritmo --version` inside the image prints it.
