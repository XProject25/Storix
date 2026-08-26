# Changelog

All notable changes to Storix are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Developed by X Project.

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

[1.0.1]: https://github.com/XProject25/Storix/releases/tag/v1.0.1
[1.0.0]: https://github.com/XProject25/Storix/releases/tag/v1.0.0
