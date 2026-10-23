# Changelog

All notable changes to photothing are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

How a release is made: move the entries under "Unreleased" to a new `## [X.Y.Z] - YYYY-MM-DD` section,
commit, then tag that commit `vX.Y.Z` and push the tag. The release workflow checks that the tag is a
valid semantic version and that this file has a section for it, runs the tests, builds the binaries and
the `.deb` and `.rpm` packages, and publishes them with this section as the release notes.
Pre-releases (`v1.0.0-rc.1`) are published as pre-releases.

## [Unreleased]

## [0.1.2] - 2026-10-23

The first version that is published with packages. `0.1.0` and `0.1.1` were tagged, but the release workflow
could not finish for them (it never got past installing ffmpeg from a very slow package mirror), so there is no
release for those two; everything they contain is in this one.

### Changed
- Release workflow: the tests that need ffmpeg, libheif or exiftool run in a job of their own, with retries and
  time limits on the package download. The build no longer waits for that job; the release does.
- Release workflow: a dry run can be started by hand (Actions > Release > Run workflow). It runs all tests and
  builds all packages under a made-up version, and publishes nothing.
- The package test in the workflow installs the `.deb` on a clean machine, starts the service as a user would
  (so the systemd sandbox runs for real), checks that the login page answers, that an account can be made as
  the service user, and that removing the package keeps the configuration and the data.

## [0.1.1] - 2026-10-22

### Changed
- The source address that the About box shows by default, and the place `SECURITY.md` names for reporting a
  vulnerability, are the public GitHub repository. A release built there already did this; now the source says
  so too.
- Database writes no longer wait for the disk on every commit (SQLite in WAL mode with `synchronous=NORMAL`).
  Scanning a library, signing in and the audit log are much faster on a slow disk; the tests of the store went
  from 18 to 1 second on a real disk. A crash of the program loses nothing; a power cut or kernel crash can lose
  the last few commits, but cannot corrupt the database.
- The release workflow has time limits for its jobs and for `go test`, so a test that hangs fails after 12
  minutes with a dump of what every goroutine was doing, instead of running for hours.

## [0.1.0] - 2026-10-22

First version.

### Fixed
- Thumbnails and large pictures that were cached by an earlier version kept being served after how photos are
  turned or scaled had changed, because their file names (and the URLs the pages used) did not change. Cached
  pictures now carry the rendering version in their name and the pages ask for them with `?v=` and the version,
  so a fix shows up at once, in the server's cache and in the browser; outdated cache files are removed at start.
- Photos whose pixels had already been turned upright, but whose EXIF Orientation tag was left behind (what
  some HEIC-to-JPEG conversions produce), were turned a second time and shown sideways. They are recognised
  (tag 5 to 8, portrait pixels, and EXIF dimensions that are the stored ones turned the other way), shown as
  stored and marked in the information panel. HEIC files are never turned twice either: libheif has already
  applied the rotation.

### Security
First review of the code, by the author and by an independent reviewer, with `govulncheck` (no vulnerabilities
in the code that is used). Fixed before release:
- The page to go to after signing in could be made to leave the site with a control character in it
  (`/<tab>/evil.example`, which browsers read as `//evil.example`).
- A video was sent to every viewer as the original file, including the location in its metadata, while the
  photo page hid it. Viewers now get a copy without metadata.
- The audit log could be filled by anybody sending failed sign-ins, and held whatever was typed in the name
  field (people type passwords there). Failed sign-ins are bounded and kept apart from the real history, and
  only existing user names are recorded.
- Second-factor codes could be guessed from many addresses (60 tries per 15 minutes in total): now 10 per
  30 minutes per user. IPv6 clients are limited per /64 block, and the limiter's memory is bounded.
- A small file could describe an image of 100 megapixels, costing more than half a gigabyte to thumbnail, many
  at once. Decoding now works within a pixel budget.
- Recovery codes are stored as an HMAC under a key from `secret.key` instead of an unsalted hash.
- An anonymous visitor could tell which photo numbers exist.
- ffmpeg is held to local files and to the container format the extension promises; exiftool older than 12.24
  (code execution through a crafted file) is refused; external tools only get regular files.
- The systemd units run with no capabilities and a system-call filter (`systemd-analyze security`: 5.4 to 1.3).
- Release workflow: actions pinned to a commit, Dependabot added; `SECURITY.md` describes the model.

### Added
- Gallery: the directories under the photo directory are albums, nested to any depth, shown as a grid
  with a cover per album (automatic, or chosen by an editor). Photo page with the full photo, previous
  and next, and keyboard navigation. Photos are only ever read.
- Photo information: orientation (stored upright, rotated or mirrored, from the EXIF tag), camera, lens, focal length (and 35 mm equivalent), aperture, exposure time, ISO,
  exposure compensation, exposure program and mode, metering, flash, white balance, scene type, colour
  space, software, artist, copyright, description, and GPS position, altitude and direction. GPS is shown
  only to users who may download originals.
- Formats: JPEG, PNG, GIF and WebP; video (mp4, m4v, webm, mov) with `ffmpeg`; HEIC with `heif-convert`;
  RAW previews with `exiftool`. Files that need a missing tool are skipped and counted.
- Captions and tags, edited by editors and admins, and search on file name, caption, album and tag.
- Pagination, sort order (name, natural, date) and language (English, Dutch) chosen and stored per user.
  Light, dark and system themes.
- Accounts with password and optional TOTP two-factor authentication with recovery codes; roles viewer,
  editor and admin; optional anonymous viewing; restricting an album to selected users; a session
  overview; password reset links issued by an admin; an audit log; login limits per address, per
  address and user, and per user.
- Rescans every ten minutes and recognises renamed and moved files, which keep their id, caption, tags
  and covers.
- Backup and restore of the database and key, and optional automatic backups.
- Settings from command-line flags, environment variables or a configuration file (`--config=FILE`), so
  one machine can run several instances; `config.example` documents every setting. A lock keeps two
  servers from using the same data directory.
- Reverse proxy support: `trusted-proxy` makes `X-Forwarded-For` and `X-Forwarded-Proto` count, but only
  from the listed proxies.
- A status line at the bottom of every page with the version, and an About box with the version, the
  address of the source and the author.
- `--version` (and `photothing version`). Versions are semantic versions, taken from the release tag.
- Releases: pushing a tag `vX.Y.Z` runs a GitHub Actions workflow that tests the code, builds linux
  amd64 and arm64 binaries, a `.deb`, an `.rpm` and a tarball for each, and publishes them with the notes of
  this changelog and a `SHA256SUMS` file. Packages install systemd units and a default configuration
  in `/etc/photothing`, create a `photothing` user, and keep configuration and data on removal.
- License: GNU GPL version 3 (`GPL-3.0-only`), with SPDX headers in the source, the license in the About
  box and `--version`, a license field in the packages, and the licenses of the Go modules that are linked
  into the binary shipped with every tarball and package.
- systemd units for one instance and for several (`photothing@NAME`), and an nginx example.
