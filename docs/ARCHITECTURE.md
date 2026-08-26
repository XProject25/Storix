# Storix architecture

Storix is a single compiled Go binary that serves both the JSON API and the
web interface. There is no PHP, no Node runtime on the server, no external
database and no web server to configure.

Developed by X Project.

## Layout

```
/usr/bin/storix              the whole product, one binary
/etc/storix/config.yaml      configuration
/var/lib/storix/storix.db    SQLite database
/var/lib/storix/uploads      resumable upload chunks
/var/lib/storix/thumbnails   generated previews
/var/lib/storix/trash        recycle bin
/var/log/storix/storix.log   log file
```

## Packages

| Package | Responsibility |
| --- | --- |
| `internal/build` | compile time version metadata |
| `internal/config` | YAML configuration, defaults, validation |
| `internal/store` | SQLite persistence, schema migrations |
| `internal/auth` | Argon2id passwords, sessions, CSRF, TOTP, rate limiting |
| `internal/vfs` | guarded file system built on `os.Root` |
| `internal/archive` | zip and tar creation and extraction |
| `internal/upload` | tus 1.0.0 resumable upload protocol |
| `internal/thumbs` | image thumbnails with an on disk cache |
| `internal/jobs` | background operation manager with progress |
| `internal/events` | server sent events hub |
| `internal/api` | HTTP handlers and routing |
| `internal/server` | listener, TLS, graceful shutdown |
| `internal/updater` | self update from GitHub releases |
| `internal/web` | embedded frontend assets |

## Path safety

Every request path is virtual and absolute, for example `/home/ubuntu/projects`.
`vfs.Resolve` maps it onto the most specific mount the acting user owns and
returns a `*os.Root` handle plus a mount relative path. All I/O then runs
through that handle, so the kernel refuses any traversal that would leave the
mount, including one introduced by a symlink swapped in mid operation.

Three layers apply, in order:

1. the configured deny list (`/etc/shadow`, `/root/.ssh`, `/proc`, and the
   Storix data directory itself),
2. the user mount list, which for non administrators is an explicit allowlist,
3. `os.Root` containment at the syscall level.

Read only mounts refuse every mutating call before it reaches the disk.

## API

All endpoints live under `/api/v1`, speak JSON and authenticate with the
`storix_session` cookie. Mutating requests carry the `X-Storix-CSRF` header,
matched against the `storix_csrf` cookie.

Error shape:

```json
{ "error": { "code": "forbidden", "message": "Path is outside the allowed area" } }
```

Success shape is the payload itself, or `{ "ok": true }` where there is nothing
to return.

### Routes

```
GET    /api/v1/system/status          setup state, version, branding (public)
POST   /api/v1/setup                  first run wizard (public until completed)
POST   /api/v1/auth/login             sign in
POST   /api/v1/auth/logout            sign out
GET    /api/v1/auth/me                current user, permissions, mounts
POST   /api/v1/auth/password          change own password
POST   /api/v1/auth/totp/setup        begin 2FA enrolment
POST   /api/v1/auth/totp/enable       confirm 2FA
POST   /api/v1/auth/totp/disable      turn 2FA off
GET    /api/v1/auth/sessions          list own sessions
DELETE /api/v1/auth/sessions/{id}     revoke a session

GET    /api/v1/fs/list                directory listing
GET    /api/v1/fs/stat                single entry
POST   /api/v1/fs/mkdir               create folder
POST   /api/v1/fs/rename              rename in place
POST   /api/v1/fs/move                move (job)
POST   /api/v1/fs/copy                copy (job)
POST   /api/v1/fs/delete              trash or permanent delete (job)
GET    /api/v1/fs/download            single file, range aware
GET    /api/v1/fs/download-zip        streaming zip of a selection
GET    /api/v1/fs/raw                 inline preview stream
GET    /api/v1/fs/thumb               cached thumbnail
GET    /api/v1/fs/text                editor payload
PUT    /api/v1/fs/text                save editor payload
GET    /api/v1/fs/search              recursive search
GET    /api/v1/fs/du                  recursive size
GET    /api/v1/fs/disk                volume usage
POST   /api/v1/fs/chmod               permissions (advanced)
POST   /api/v1/fs/chown               ownership (advanced)
POST   /api/v1/fs/compress            create archive (job)
POST   /api/v1/fs/extract             extract archive (job)

GET    /api/v1/trash                  recycle bin contents
POST   /api/v1/trash/restore          restore items
POST   /api/v1/trash/delete           delete items permanently
POST   /api/v1/trash/empty            empty the bin

GET    /api/v1/favorites              pinned locations
POST   /api/v1/favorites              pin
DELETE /api/v1/favorites              unpin
GET    /api/v1/recent                 recently opened files

GET    /api/v1/shares                 own links
POST   /api/v1/shares                 create link
PATCH  /api/v1/shares/{id}            update link
DELETE /api/v1/shares/{id}            revoke link

GET    /api/v1/users                  accounts (admin)
POST   /api/v1/users                  create account (admin)
PATCH  /api/v1/users/{id}             update account (admin)
DELETE /api/v1/users/{id}             delete account (admin)
GET    /api/v1/roles                  role presets and permission catalogue

GET    /api/v1/jobs                   running and recent operations
GET    /api/v1/jobs/{id}              one operation
POST   /api/v1/jobs/{id}/cancel       cancel
GET    /api/v1/events                 server sent event stream

POST   /api/v1/tus                    create resumable upload
HEAD   /api/v1/tus/{id}               current offset
PATCH  /api/v1/tus/{id}               append chunk
DELETE /api/v1/tus/{id}               abort upload

GET    /api/v1/dashboard              home screen aggregate
GET    /api/v1/system/info            version, host, storage
GET    /api/v1/system/settings        settings (admin)
PUT    /api/v1/system/settings        save settings (admin)
GET    /api/v1/system/roots           mounted trees (admin)
POST   /api/v1/system/roots           add tree (admin)
DELETE /api/v1/system/roots/{id}      remove tree (admin)
GET    /api/v1/system/audit           audit log (admin)
GET    /api/v1/system/update/check    release check (admin)
POST   /api/v1/system/update          self update (admin)

GET    /api/v1/public/{token}         public share metadata and listing
POST   /api/v1/public/{token}/auth    unlock a password protected share
GET    /api/v1/public/{token}/download
GET    /api/v1/public/{token}/raw
GET    /api/v1/public/{token}/thumb
POST   /api/v1/public/{token}/tus     upload request, tus create
HEAD   /api/v1/public/{token}/tus/{id}
PATCH  /api/v1/public/{token}/tus/{id}
```

## Uploads

Uploads use the tus 1.0.0 resumable protocol so an interrupted 80 GB transfer
resumes at the byte it stopped at instead of starting over. Chunks land in
`/var/lib/storix/uploads` and are moved into place only once the declared
length has been received. Folder uploads carry a `relativePath` metadata field
and Storix recreates the tree.

## Background jobs

Copy, move, delete, compress and extract run as jobs. Each job reports bytes,
item counts and the file currently being processed. The UI subscribes to
`/api/v1/events` and renders live progress, and every job can be cancelled.

## Permissions

Roles are presets over a flat permission set: `view`, `download`, `upload`,
`create`, `rename`, `move`, `copy`, `delete`, `share`, `archive`, `edit`,
`advanced`, `users`, `settings`. `admin` implies all of them. The UI presents
the simple question "who can access this" and keeps the Unix mode bits behind
an Advanced disclosure.
