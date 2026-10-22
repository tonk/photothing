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

Flags go before the command; most have an environment variable:

| flag | env | default | meaning |
|---|---|---|---|
| `-photos` | `PHOTOTHING_PHOTOS` | `./photos` | the photo directory (only ever read) |
| `-data` | `PHOTOTHING_DATA` | `./data` | database, key and image cache |
| `-addr` | `PHOTOTHING_ADDR` | `127.0.0.1:8080` | listen address |
| `-secure-cookies` | `PHOTOTHING_SECURE_COOKIES=1` | off | mark cookies Secure; use behind HTTPS |
| `-scan-interval` | `PHOTOTHING_SCAN_INTERVAL` | `10m` | rescan the photo directory this often; `0` = only at start and from the Admin page |
| `-backup-dir` | `PHOTOTHING_BACKUP_DIR` | none | write automatic backups here |
| `-backup-keep` / `-backup-every` | | `7` / `24h` | how many to keep and how often |

Other commands: `scan`, `user add|passwd|mfa-reset|list`, `public on|off`, `backup [file]`, `restore [-force] <file>`.

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

## Layout

    main.go                 CLI, server start-up, housekeeping
    internal/store          SQLite schema, upgrades and queries
    internal/auth           passwords, RBAC, TOTP, recovery codes, secret box, throttle
    internal/gallery        directory scan, EXIF, thumbnails, optional converters
    internal/backup         backup and restore archives
    internal/web            handlers, templates, translations, static assets (all embedded)

Add a language by adding it to `langs` and a map next to `i18n_nl.go`; `go test` fails while any
text lacks a translation.

## Known limits

- Behind a reverse proxy, login throttling sees the proxy's address for every visitor. Trusted-proxy
  handling is not built yet.
- There is no TLS: use a reverse proxy and `-secure-cookies`.
- Per-album rules name users, not roles; there are no groups.
- Search is a plain substring match, fine for tens of thousands of photos, not for millions.
- RAW support depends on the camera's embedded preview; it was not tested with real RAW files.
- Video: no transcoding, no seeking thumbnails.
