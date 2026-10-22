# Security

## Reporting a vulnerability

Please do not open a public issue for a security problem. Tell the maintainer directly (once the project is on
GitHub: use "Report a vulnerability" under the Security tab), with what you found and how to reproduce it. Only
the latest release is supported with fixes.

## Security model

PhotoThing is meant for a family or a small organisation. It assumes:

- **The photo directory is trusted to hold photos, not trusted to be harmless.** Files in it are decoded by Go
  libraries and, when the optional tools are installed, by `ffmpeg`/`ffprobe`, `heif-convert` and `exiftool`.
  These programs parse complicated formats and have had vulnerabilities of their own, so keep them up to date.
  PhotoThing helps: it ignores symbolic links, refuses images over 100 megapixels, runs the tools with a time
  limit, hands them only absolute paths of regular files, makes `ffmpeg` read local files and only the container
  format the extension promises, and refuses an `exiftool` older than 12.24 (remote code execution through a
  crafted file). The service is installed with a systemd sandbox (below).
- **Visitors are not trusted.** Anonymous visitors (when public viewing is on) can see photos but not originals.
  Signed-in viewers cannot see originals, GPS data, restricted albums or any administration. A video is shown
  to them as a copy without metadata (made without re-encoding, cached), since a video file carries the place it
  was filmed in; only users who may download originals get the file itself.
- **The administrators are trusted**, including with each other's accounts. An admin can reset anyone's
  password or MFA, which is what makes a forgotten password recoverable without e-mail.

## What it does

- Passwords are at least 12 characters and stored with bcrypt (cost 12). Unknown users cost the same time as
  known ones.
- Sign-in is limited per address (IPv6: per /64 block), per address and user, and loosely per user; the second
  factor has a stricter limit per user (10 wrong codes in 30 minutes, whatever their address). The limits live in
  memory, which is bounded.
- Optional TOTP with replay protection; ten single-use recovery codes, stored as an HMAC under a key from
  `data/secret.key` (a copy of the database alone cannot be used to find them). The TOTP seed is encrypted
  (AES-GCM) with the same file.
- Sessions are random 256-bit tokens, stored hashed, in an `HttpOnly`, `SameSite=Lax` cookie that is `Secure`
  over https. A new token is issued at every sign-in; a password change signs out everywhere. Users can see and
  end their own sessions.
- State-changing requests are `POST` only and protected against cross-site requests (Fetch metadata and
  `Origin`). Pages are escaped by `html/template` and carry a strict Content-Security-Policy without inline
  scripts or styles. Redirect targets after sign-in must be local paths without control characters.
- Photos are opened through `os.Root`, so a path cannot leave the photo directory. All database access uses
  parameters. Restricted albums are enforced on every route that returns photos, counts, covers, tags or
  search results.
- The audit log records sign-ins and failures, MFA, password and role changes, access rules and scans, with the
  address of the client. It never records what was typed in the name field of a failed sign-in for an unknown
  user, mentions a blocked client once per window instead of once per request, and keeps at most 20,000
  failed-sign-in entries, so a flood can neither fill the disk nor push the real history out. Entries are kept for
  a year, which matters if you have to account for personal data.
- Forwarded headers (`X-Forwarded-For`, `X-Forwarded-Proto`) are believed only from the proxies listed in
  `trusted-proxy`.
- Backups contain the database and the key, are written with mode 0600, and are never overwritten. Restore only
  accepts the two expected file names.
- Releases are built by a workflow with read-only permissions except for publishing, from a tag that must be a
  valid semantic version. The actions it uses are pinned to a commit and `nfpm` to a version; Dependabot proposes
  updates. Checksums are published.
- An anonymous visitor gets the same answer for a photo that does not exist and for one they may not see.
- Decoding is limited by a budget of pixels in memory for all requests together (about 850 MB), because a small
  file can describe an enormous image.

## Deploying it safely

- Run it on `127.0.0.1` behind a reverse proxy with TLS (`contrib/nginx.conf.example`), and set `trusted-proxy`
  to that proxy. Without it every visitor shares one sign-in limit and the audit log shows the proxy's address.
- Use the packages or `contrib/photothing.service`: the unit runs as an unprivileged user with no capabilities,
  a read-only file system apart from its state directory, no access to `/home`, devices, kernel logs or the
  clock, and a filter that allows only ordinary system calls (`systemd-analyze security` exposure 1.3, "OK").
  Photos outside `/home` or `/root` only need read permission for the user `photothing`.
- Create users and run `backup` and `restore` as that user (`sudo -u photothing ...`), or the files get the wrong
  owner.
- Keep `ffmpeg`, `heif-convert` (libheif) and `exiftool` current, or do not install them: JPEG, PNG, GIF and WebP
  work without them.
- Treat backup archives as secrets (they hold password hashes and the key to the MFA seeds) and copy them off
  the machine.
- Consider a hard memory limit for the service (`MemoryMax=`, there is a commented line in the units): the
  decoding budget is about 850 MB, and ffmpeg and the other tools have memory needs of their own.

## Known limits

- The sign-in limits are kept in memory and start again after a restart.
- A backup archive holds the database and `secret.key` together (a restore needs both), so whoever steals an
  archive can decrypt the stored TOTP seeds. The archives are not encrypted; keep them where only you can read
  them.
- Releases are published with checksums but are not signed, and carry no build attestation.
- A visitor can lock a user out of signing in for 15 minutes with 200 wrong passwords, which takes at least
  seven addresses; for the second factor the limit is 10 codes and applies even to the user themselves.
- A visitor who can reach the site can ask for thumbnails of any photo they are allowed to see; they are made on
  first request and then cached, so a public gallery with a huge library can be made to do a lot of work (at
  most four at a time, within the memory budget). Put a rate limit in the reverse proxy if that matters (the nginx
  example limits `/login`).
- Photo files are handed to external programs by path, so a file replaced by a symbolic link in the instant
  between the check and the use is not caught. Only trusted people should be able to write to the photo
  directory.
- There is no per-user quota, no CAPTCHA and no alerting on suspicious activity; the audit log has to be read.
