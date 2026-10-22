# photothing

A small, self-hosted photo gallery in Go. One binary, SQLite, no external services.

- Albums are the directories under your photo directory, nested to any depth. Photos show in a grid, click for the full photo with its details.
- Users with password, optional TOTP MFA with recovery codes, and role-based access (viewer / editor / admin).
- Optional anonymous viewing, and per-album restrictions for selected users.
- Search on file name, caption, album and tags; captions and tags are edited by editors and admins.
- Interface in English and Dutch, chosen by each user. Light, dark and system themes.
- Pagination with a page size and sort order that each user stores in their own account.
- Photos, WebP, video (and HEIC and RAW previews when the optional tools are installed).

## Run

    make build
    ./photothing -photos /path/to/photos -data ./data user add ton admin   # prompts for a password
    ./photothing -photos /path/to/photos -data ./data serve                # http://127.0.0.1:8080

Settings are command-line flags (before the command). Most can also come from an environment variable
or from a configuration file (next section). Precedence, weakest first: built-in default, environment
variable, configuration file, command-line flag.

| flag | env | default | meaning |
|---|---|---|---|
| `-photos` | `PHOTOTHING_PHOTOS` | `./photos` | the photo directory (only ever read) |
| `-data` | `PHOTOTHING_DATA` | `./data` | database, key and image cache |
| `-addr` | `PHOTOTHING_ADDR` | `127.0.0.1:8080` | listen address |
| `-config` / `--config=FILE` | `PHOTOTHING_CONFIG` | none | read settings from this file |
| `-secure-cookies` | `PHOTOTHING_SECURE_COOKIES=1` | off | always mark cookies Secure (https seen through a trusted proxy does this by itself) |
| `-trusted-proxy` | `PHOTOTHING_TRUSTED_PROXY` | none | addresses/CIDRs of reverse proxies whose `X-Forwarded-For`/`-Proto` are believed |
| `-scan-interval` | `PHOTOTHING_SCAN_INTERVAL` | `10m` | rescan the photo directory this often; `0` = only at start and from the Admin page |
| `-backup-dir` | `PHOTOTHING_BACKUP_DIR` | none | write automatic backups here |
| `-backup-keep` / `-backup-every` | | `7` / `24h` | how many to keep and how often |

Other commands: `scan`, `user add|passwd|mfa-reset|list`, `public on|off`, `backup [file]`, `restore [-force] <file>`,
and `config`, which prints the effective settings and checks them.

## Configuration file and several instances

    photothing --config=/etc/photothing/familie.conf serve
    photothing --config=/etc/photothing/familie.conf user add ton admin

A configuration file is `key = value` lines named like the flags above (`photos`, `data`, `addr`,
`trusted-proxy`, `scan-interval`, `backup-dir`, `backup-keep`, `backup-every`, `secure-cookies`).
`#` and `;` start a comment line, values may be quoted, `yes/no/on/off` work for switches, and a relative
path is relative to the directory of the file. Mistakes are reported with the file name and line number,
and a flag given on the command line always wins over the file. See `config.example`.

An instance is a configuration file with its own `data` directory, `photos` and `addr`; they share
nothing, so one machine can serve any number of galleries, each with its own users. Starting a second
server on the same data directory is refused (and so is `restore` while a server is using it).
For a single instance use `contrib/photothing.service` (reads `/etc/photothing/photothing.conf`); for several,
`contrib/photothing@.service` is a hardened systemd template: `systemctl enable --now photothing@familie`
starts the instance configured in `/etc/photothing/familie.conf`.

## Behind a reverse proxy (nginx)

Run photothing on a loopback port and let nginx handle TLS (`contrib/nginx.conf.example`: a server block
for `/etc/nginx/conf.d/`, plus a redirect from http to https). Every request
then arrives from nginx, so photothing needs to be told which headers to believe:

    trusted-proxy = 127.0.0.1

The `X-Forwarded-For` and `X-Forwarded-Proto` headers are then believed, but only on connections from a
listed proxy. Visitors get their own login limits, the audit log and the session list show their real
addresses, cookies become Secure when the visitor used https, and password reset links get the right
scheme. From any other connection the headers are ignored, so a visitor cannot invent an address to escape
the login limits. The visitor is the first address, reading from the right, that is not a trusted proxy;
whatever a visitor put in front of it is not believed. Have nginx set the header with
`proxy_set_header X-Forwarded-For $remote_addr;` (overwrite, not append) as in the example. Without
`trusted-proxy`, photothing sees nginx's address for everybody and all visitors share one login limit.

## Albums

Albums are directories (`vakantie/benidorm_2012`, `etentjes/librije_2013`). A parent album shows its
sub-albums as tiles and counts all photos below it. A tile uses a cover photo: by default the
alphabetically first photo; editors and admins can pick one on the photo page, for the photo's own
album or any parent album. A renamed or moved file is recognised (same size and modification time) and
keeps its id, caption, tags and covers; ambiguous cases (several identical files) are not guessed.

## Roles

| role   | view | download original | edit captions, tags, covers; rescan | users, access, audit log |
|--------|------|-------------------|-------------------------------------|--------------------------|
| viewer | yes  | no                | no                                  | no                       |
| editor | yes  | yes               | yes                                 | no                       |
| admin  | yes  | yes               | yes                                 | yes                      |

Video is streamed to every viewer (a browser must download it to play it). GPS coordinates are shown
only to users who may download originals, since the originals carry them.

**Album access**: an admin can restrict an album (Admin → Album access). It, and everything below it,
is then visible to admins and the selected users only: not in listings, counts, covers, search or tags,
and never to anonymous visitors.

## Photo information

The photo page shows, in this order: camera make and model, lens, focal length (also as 35 mm
equivalent), aperture, exposure time, ISO, exposure compensation, exposure program and mode, metering
mode, flash, white balance, scene type, colour space, software, artist, copyright, description, and GPS
position, altitude and direction; for video the duration and codec. Only fields that are present in the
file are shown, in the user's language; names and descriptions are shown as written.

Location (GPS) fields are shown only to users who may download originals, since the originals carry
them. Not read: IPTC and XMP data (titles, keywords, ratings from Lightroom and the like), maker notes,
and serial numbers.

The details are read when a file is scanned. When a new version reads more, the first scan after the
update reads every file again, once, so existing photos get the new fields (that scan reports them as
"updated"); after that, unchanged files are skipped again. HEIC and RAW files get the same fields via
`exiftool`.

## Accounts and security

- Passwords: at least 12 characters, bcrypt. MFA seeds are encrypted with `data/secret.key`.
- MFA recovery codes: ten single-use codes, shown once when MFA is enabled, regenerated on request.
- No e-mail, so a forgotten password is fixed by an admin: set a new one, or create a one-hour,
  single-use reset link (Admin → Users → Reset link) and pass it on.
- Account page: preferences, password, MFA, and a list of your sessions that you can sign out.
- Failed logins are limited per IP, per IP+username and loosely per username, so guessing at someone's
  account does not lock them out of their own sign-in from another address.
- Everything that matters is written to the audit log (Admin → Audit log): logins and failures, MFA,
  password changes, user management, access rules, scans, edits. Entries are kept for a year.

## Backup

    ./photothing -data ./data backup  [file]       # safe while the server runs
    ./photothing -data ./data restore [-force] file   # stop the server first

An archive holds a consistent snapshot of the database plus `secret.key`. It does not hold the photos
(you have those) or the image cache (rebuilt on demand). It contains password hashes and the key for the
MFA seeds, so store it like a secret. Restoring never overwrites silently: without `-force` it refuses,
with `-force` the replaced files are kept as `*.before-restore`. For automatic backups use
`-backup-dir`. Databases from earlier versions are upgraded automatically when the server starts.

## Optional tools

JPEG, PNG, GIF and WebP work out of the box. These programs add formats; photothing finds them on the
`PATH` at start-up and logs what it found. Files that need a missing tool are skipped and counted in the
scan result.

| format | needs |
|---|---|
| video (mp4, m4v, webm, mov) | `ffmpeg` and `ffprobe` |
| HEIC / HEIF | `heif-convert` (libheif) |
| RAW (dng, cr2, cr3, nef, arw, orf, rw2, raf) | `exiftool`, for the embedded preview image |

Browsers play mp4/h264 and webm; other video codecs are listed but may not play in the browser.
HEIC and RAW files are shown as a converted JPEG; the original is downloaded as is.

## Versions, releases and packages

Versions are [semantic versions](https://semver.org): `photothing --version` prints the one in use, and
the footer of every page shows it (the About box also shows the commit, the source address and the
author). The version of a release is its Git tag, `v1.2.3` meaning version `1.2.3`; between tags a build
calls itself `1.2.3-4-g0a1b2c3`. Every change is recorded in `CHANGELOG.md`.

**Making a release.** Move the entries under *Unreleased* in `CHANGELOG.md` to a new
`## [X.Y.Z] - YYYY-MM-DD` section, commit, and push a tag:

    git tag v1.2.3 && git push origin v1.2.3

`.github/workflows/release.yml` then checks that the tag is a valid semantic version and that the changelog
has a section for it, runs the tests, builds for linux/amd64 and linux/arm64, installs the `.deb` on the
runner as a smoke test, and publishes a release with the changelog section as its notes. A tag such as
`v1.2.3-rc.1` becomes a pre-release (`1.2.3~rc.1` in the packages, which sorts before `1.2.3`). Each release
holds, per architecture, `photothing_X.Y.Z_linux_ARCH.tar.gz`, `photothing_X.Y.Z-1_ARCH.deb` and
`photothing-X.Y.Z-1.ARCH.rpm`, plus `SHA256SUMS`. Set the repository variable `MAINTAINER` ("Name <address>")
to fill the maintainer field of the packages. The workflow also sets the source address shown in the
About box to the repository it runs in.

**Installing a package.**

    sudo apt install ./photothing_X.Y.Z-1_amd64.deb      # Debian, Ubuntu
    sudo dnf install ./photothing-X.Y.Z-1.x86_64.rpm     # Fedora, RHEL and relatives

The package installs the binary in `/usr/bin`, the systemd units, a configuration in
`/etc/photothing/photothing.conf` (kept when you upgrade or remove the package) and the documentation in
`/usr/share/doc/photothing`, and creates the system user `photothing`. It does not start anything: set
`photos` in the configuration, then

    sudo systemctl enable --now photothing
    sudo -u photothing photothing --config=/etc/photothing/photothing.conf user add NAME admin

Run commands that write (such as `user add`) as the user `photothing`, so the files belong to the
service. Upgrading restarts a running service. Several instances: `/etc/photothing/NAME.conf` and
`systemctl enable --now photothing@NAME`. ffmpeg, libheif's `heif-convert` and exiftool are suggested, not required.

**Building locally.** `make build` builds with the version from Git, `make version` shows it, and
`make packages` builds the tarball, `.deb` and `.rpm` into `dist/` (it needs
[nfpm](https://nfpm.goreleaser.com): `go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0`).
`VERSION=1.2.3 make build` overrides the version and `REPO_URL=https://github.com/OWNER/photothing make build`
the source address.

## Layout

    main.go, config.go      CLI, settings and configuration file, server start-up, housekeeping
    contrib/                systemd units, nginx configuration (config.example is in the root)
    packaging/              build.sh, version.sh, nfpm.yaml and the install scripts of the .deb and .rpm
    scripts/                what the release workflow runs: tag check, release notes
    .github/workflows/      the release workflow
    internal/store          SQLite schema, upgrades and queries
    internal/auth           passwords, RBAC, TOTP, recovery codes, secret box, throttle
    internal/gallery        directory scan, EXIF, thumbnails, optional converters
    internal/backup         backup and restore archives
    internal/web            handlers, templates, translations, static assets (all embedded)

Add a language by adding it to `langs` and a map next to `i18n_nl.go`; `go test` fails while any
text lacks a translation.

## Known limits

- There is no TLS of its own: use a reverse proxy (see above).
- Login throttling is kept in memory and starts again after a restart.
- Per-album rules name users, not roles; there are no groups.
- Search is a plain substring match, fine for tens of thousands of photos, not for millions.
- RAW support depends on the camera's embedded preview; it was not tested with real RAW files.
- Video: no transcoding, no seeking thumbnails.
