# Changelog

All notable changes to Storix are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Developed by X Project.

## [1.2.2] - 2026-08-26

### Fixed

- The 1.2.1 cleanup of the setup token only ran when a wizard completed, so
  every server set up before that release kept a dead credential file in its
  data directory for good. It is now removed on the next start of an already
  configured install, which is the case that actually needed it.

## [1.2.1] - 2026-08-26

A review of the code that landed in 1.2 with the least scrutiny, plus the
documentation checked line by line against what the handlers actually do.

### Fixed

- **The storage allowance was not enforced on drive writes at all.** The
  WebDAV file wrapper embedded `*os.File`, which satisfies `io.ReaderFrom`, so
  the copy that lands a transfer preferred `ReadFrom` and never went through
  the method holding the ceiling. Measured: a 4 KB allowance accepted a 200 KB
  upload in full. The wrapper now does its own `ReadFrom` and the bytes are
  counted again.
- **The drive did not apply the permissions the browser applies.** A mount was
  only marked read only when the account lacked `upload`; nothing consulted
  `delete`, `create`, `rename`, `move` or `copy`. An account holding just view,
  download and upload could delete a file and move a folder over the drive,
  both of which the web interface refuses. Write methods are now gated on the
  same permission the matching endpoint requires, and a MOVE inside one folder
  counts as a rename. This tightens existing accounts: a `user` role has no
  delete, so it can no longer delete over the drive either.
- **One account could hide another's folder.** Slug uniqueness was decided
  with exact map keys while the lookup compared without case, so two labels
  that fold together each got a name in the listing but both resolved to the
  first mount. The second one's files answered 404 from every name shown.
- **A correct password could be rate limited.** The drive spent a sign in
  allowance on every request, so 8 of 16 concurrent listings with the right
  password were refused with 429. Worse, a success cleared the recorded
  failures for the whole address, which made guessing effectively unlimited.
  Failures are now recorded and successes cost nothing.
- **LOCK on a read only mount answered 500.** Windows and the Finder both open
  a copy by locking a name that does not exist yet, so the refusal was correct
  but the status was not. It is a 403 with a sentence.
- **A re-install told a configured server to finish its setup.** The installer
  printed whatever was in the setup token file, which was never removed once
  the wizard had run, so an upgrade greeted the operator with a dead link. The
  server now deletes that file when setup completes, and the installer asks
  the running instance whether it is already configured before deciding what
  to print. The token itself was never accepted after setup.
- The token last used stamp was throttled with a key that was only unique
  within one database, so two servers in one process could silence each other.

### Changed

- `docs/API.md` and `docs/WEBDAV.md` were checked endpoint by endpoint against
  the handlers and corrected. Among other things the drive accepts either an
  access token or the account password, which the documentation had denied,
  and the error envelope never carries the `detail` field it advertised.

### Added

- `CONTRIBUTING.md`, issue templates and a pull request template, so a stranger
  can tell how to build Storix and what is expected of a change.
- The suite is 166 tests.

## [1.2.0] - 2026-08-26

Storix stops being something you can only use in a browser tab.

### Added

- **A network drive.** Storix speaks WebDAV at `/dav/`, so it mounts in
  Windows Explorer, the macOS Finder or a Linux file manager and server files
  behave like local ones. The folders an account may reach appear as the top
  level collections, and everything below goes through the same guarded layer
  the browser uses, so the drive can no more leave a mount than the web
  interface can. Locks are held in memory because Windows and macOS both
  insist on LOCK before they will write.
- **Access tokens.** Scripts, backups, rclone and continuous integration can
  now reach the API without a browser. A token looks like
  `sxp_<prefix>_<secret>`; only the prefix is stored in the clear and the
  secret is kept as a digest, so a leaked database yields nothing usable. A
  read only token narrows the account it belongs to and can never widen it,
  and a token is revoked on its own without changing a password or signing
  anybody out. Tokens also serve as the password a WebDAV client presents.
- **A duplicate finder.** It reports what is wasted rather than merely what is
  big. Candidates are bucketed by size first, so most files are never read,
  then compared on their first 64 KiB, and only the survivors are hashed in
  full. When the interface offers to delete something, "probably identical"
  is not good enough. Removal goes to the recycle bin, and the code refuses to
  preselect every copy in a group.
- **Documentation worth reading:** `docs/API.md` for the HTTP surface with
  working curl recipes, and `docs/WEBDAV.md` for mounting on all three
  platforms, including the Windows registry caveat about Basic authentication
  over plain HTTP.

### Fixed

- A WebDAV path holding `..` was answered with a redirect out of the drive and
  into the web application, because the router cleaned the path before routing
  it. Nothing leaked, but a mounted drive has to stay inside its own name
  space, so those requests are now simply not found. The drive is dispatched
  ahead of the router to keep the raw path intact.

### Internal

- The suite is 156 tests. The new WebDAV tests cover the authentication
  challenge, the mount listing, a byte for byte round trip, folder creation
  and rename, and four ways of trying to climb out of a mount.

## [1.1.1] - 2026-08-26

### Fixed

- The interface opened the server sent event stream twice on every page,
  once from the layout and once from the notification list. Each connection
  costs a goroutine and a subscriber on the server, and a browser allows only
  six connections per host over HTTP/1.1, so two streams took a third of the
  budget for nothing. There is now one shared connection that fans out to
  every listener and closes when the last one goes away.
- `Cross-Origin-Opener-Policy` was sent on plain HTTP, where browsers ignore
  it and log a console error for every page load. Since an install runs on an
  address until a domain is set, that error greeted almost everyone. The
  header is now sent only where it applies.

## [1.1.0] - 2026-08-26

### Added

- **Storage insight.** A Storage screen answers "what is taking up space":
  the biggest folders as a stacked bar you can drill into, the largest
  individual files anywhere in the tree, and a breakdown by file type. The
  whole report comes from one walk rather than one scan per folder.
- **Bulk rename.** Find and replace with optional regular expressions, add a
  prefix or a suffix, number a selection, or change case, all with a live
  preview that shows every old name beside its new one before anything moves.
  Conflicts are detected against both the disk and the rest of the batch, and
  a batch that swaps two names is applied through a temporary name so nothing
  is overwritten. Numbering keeps the file extension.
- **Storage allowances that are enforced.** A per account quota is now checked
  before an upload starts, so a transfer is refused up front instead of
  filling the disk and failing at the end. The figure is measured in the
  background and updated as uploads land, so no request waits on a disk scan.
- **A QR code for every share link,** so a link can be handed to a phone
  without typing it. The encoder is written into Storix, there is no extra
  dependency and the page works offline.

### Changed

- **The interface works on a phone.** Below 1024px the details panel becomes a
  bottom sheet, below 768px the file table folds into two readable lines per
  row with 52px targets, the toolbar scrolls horizontally, and every control
  has a 40px touch floor. No horizontal page scroll at any width.
- The editor now ships only Monaco's base worker. The TypeScript language
  service alone was six megabytes of generated code, and Storix is used to
  edit configuration, YAML, shell scripts and logs, where highlighting is what
  matters. The download is seven megabytes smaller and the build is twice as
  fast.

### Fixed

- Numbering a selection dropped the file extension, so IMG_001.JPG became a
  bare holiday-001 that the system no longer knew how to open. The pattern now
  names the base and the extension follows, unless the pattern supplies one.
- The uploads section of the architecture notes described a staging directory
  that Storix does not use. Partial data is written into the destination as a
  hidden .storix-<id>.part file so finishing is one rename, not a second copy.

### Internal

- `internal/store` and `internal/upload` now have their own tests, the last
  two packages without any. The suite is 134 tests.

## [1.0.1] - 2026-08-26

Fixes found by installing on a live Ubuntu 22.04 server that also runs an
unrelated Docker stack.

### Fixed

- The installer sourced `/etc/os-release`, which defines its own `NAME`,
  `VERSION` and `ID`. That overwrote the variable holding the requested
  version, so the installer asked GitHub for a release tagged
  "22.04.5 LTS (Jammy Jellyfish)" and stopped with a misleading message about
  the internet connection. The values are now read in subshells and the script
  variable can no longer collide.
- Release lookup no longer depends on the GitHub API, which allows only sixty
  unauthenticated calls an hour per address. It falls back to the release page
  redirect and to the predictable asset URLs, and confirms the asset exists
  before touching the system.
- Following a symlink that points outside a mount was correctly refused by the
  kernel, but the refusal surfaced as "Something went wrong on the server".
  It now arrives as a clear refusal that names the reason.
- `storix doctor` judged the binary by whoever ran the command, so running it
  with sudo always warned that the binary was writable. It now reports the
  owner and mode of the file itself.

## [1.0.0] - 2026-08-26

The first release. One binary, one command to install, a complete file manager
in the browser.

### Files

- Browse with list, grid and gallery views, sorting, hidden file toggle and
  natural name ordering, so `file10` follows `file9`.
- Create, rename, move, copy and delete, with a conflict policy of rename,
  overwrite or skip.
- Drag and drop inside the browser, onto folders and onto breadcrumbs.
- Multi select with click, Ctrl click and Shift ranges, plus keyboard control
  including Ctrl+C, Ctrl+X, Ctrl+V, F2, Delete and Ctrl+A.
- Recursive search by name, optionally through file contents, with a deadline
  so a large volume cannot hang the request.
- Favourites, recent files, folder sizes and volume usage.

### Transfers

- Resumable uploads over the tus 1.0.0 protocol. An interrupted transfer
  continues at the byte it stopped at.
- Pause, resume, retry and cancel, several uploads in parallel, whole folder
  uploads that keep their structure.
- Partial data is written into the destination directory under a hidden name,
  so completing an upload is one atomic rename rather than a second full copy.
- A live transfer panel with speed, remaining time and per file progress.
- Downloads support HTTP range requests, so a large download resumes and video
  seeking works.
- Download a selection as a zip, streamed, with no temporary file on the server.

### Preview and editing

- Inline preview for images, video, audio, PDF, text, Markdown, JSON, YAML,
  logs and source code.
- Thumbnails generated and cached on the server, with single flight so one
  image is never decoded twice at once.
- A full editor with syntax highlighting, saved through a temporary file and an
  atomic rename so a failed save cannot truncate the original.
- Archive contents can be inspected without extracting.

### Archives

- Create zip, tar.gz and tar archives, and extract zip, tar, tar.gz and tar.bz2.
- Already compressed formats are stored rather than deflated, so large media
  does not burn CPU for nothing.
- Extraction refuses entries whose names point outside the destination, refuses
  absolute paths and escaping symlinks, and guards against decompression bombs.

### Sharing

- Public links for a file or a folder with an expiry, an optional password, a
  download limit and a note.
- Upload requests, so someone without an account can send files into one folder.
- Public pages never reveal the absolute server path.

### Accounts

- Roles of administrator, manager, user and read only over a flat permission
  set, with custom combinations.
- Per account folder access, each folder optionally read only.
- Optional TOTP two factor authentication, session list with revoke, and an
  audit log.

### Operations

- Copy, move, delete, compress, extract and update run as background jobs with
  progress, cancellation and live events over server sent events.
- A recycle bin with restore and a retention period.
- `storix doctor` checks the installation and reports problems in plain words.

### Security

- Path containment enforced by the kernel through `os.Root`, closing symlink
  escapes and check then open races.
- Argon2id password hashing, hashed session identifiers, CSRF double submit
  tokens, login rate limiting with account lockout and an optional IP allowlist.
- Protected locations refused even inside a mounted parent.
- Downloads served with `nosniff` and a sandbox content policy.
- Runs as a dedicated service account, with a root owned binary.

### Installation

- One command install for Ubuntu and Debian on amd64, arm64 and arm, with
  checksum verification, service registration and firewall configuration.
- A first run wizard gated by a token, so an instance cannot be claimed by
  whoever reaches the port first.
- Automatic HTTPS through Let's Encrypt once a domain is set.
- In place updates from the interface or with `sudo storix update`, keeping
  accounts, settings and folders.

[1.2.2]: https://github.com/XProject25/Storix/releases/tag/v1.2.2
[1.2.1]: https://github.com/XProject25/Storix/releases/tag/v1.2.1
[1.2.0]: https://github.com/XProject25/Storix/releases/tag/v1.2.0
[1.1.1]: https://github.com/XProject25/Storix/releases/tag/v1.1.1
[1.1.0]: https://github.com/XProject25/Storix/releases/tag/v1.1.0
[1.0.1]: https://github.com/XProject25/Storix/releases/tag/v1.0.1
[1.0.0]: https://github.com/XProject25/Storix/releases/tag/v1.0.0
