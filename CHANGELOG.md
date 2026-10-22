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

## [0.1.0] - 2026-10-22

First version.

### Added
- Gallery: the directories under the photo directory are albums, nested to any depth, shown as a grid
  with a cover per album (automatic, or chosen by an editor). Photo page with the full photo, previous
  and next, and keyboard navigation. Photos are only ever read.
- Photo information: camera, lens, focal length (and 35 mm equivalent), aperture, exposure time, ISO,
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
- systemd units for one instance and for several (`photothing@NAME`), and an nginx example.
