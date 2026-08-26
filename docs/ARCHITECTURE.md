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
matched against the `storix_csrf` cookie. A scripted caller sends an
`Authorization: Bearer` token instead and carries no cookie at all.
[API.md](API.md) is the working reference.

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
GET    /api/v1/auth/tokens            own access tokens, and the mount details
POST   /api/v1/auth/tokens            create a token, returned once
DELETE /api/v1/auth/tokens/{id}       revoke a token

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
GET    /api/v1/fs/usage               what is taking up space under a folder
GET    /api/v1/fs/duplicates          identical files under a folder
POST   /api/v1/fs/rename-bulk/preview bulk rename, what would happen
POST   /api/v1/fs/rename-bulk         bulk rename, apply
GET    /api/v1/auth/quota             own storage allowance
GET    /api/v1/users/{id}/quota       one account allowance (admin)
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

OPTIONS  /dav/                        capabilities, class 1 and class 2
PROPFIND /dav/                        the mounts, as one collection each
PROPFIND /dav/{path}                  list a folder, or describe a file
GET      /dav/{path}                  read a file, range aware
HEAD     /dav/{path}                  size and modification time
PUT      /dav/{path}                  write a file
MKCOL    /dav/{path}                  create a folder
COPY     /dav/{path}                  copy, with the Destination header
MOVE     /dav/{path}                  move or rename
DELETE   /dav/{path}                  remove
LOCK     /dav/{path}                  take a write lock
UNLOCK   /dav/{path}                  release one
```

## Uploads

Uploads use the tus 1.0.0 resumable protocol so an interrupted 80 GB transfer
resumes at the byte it stopped at instead of starting over.

Partial data is written straight into the destination directory under a hidden
name, `.storix-<id>.part`, rather than into a staging area. Finishing an upload
is then a single rename on the same volume instead of a second full copy of the
data, which for an 80 GB file is the difference between instant and several
minutes of disk. Those scratch files are filtered out of every listing and
search, and are removed when an upload is abandoned or expires. Folder uploads
carry a `relativePath` metadata field and Storix recreates the tree.

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

## Storage allowance

An account may carry a quota. The figure lives in `user_usage` and is refreshed
by a background walk rather than recomputed on every request, so a listing
never waits on a disk scan. An upload is refused before any bytes are written,
because tus declares the length up front, and a finished upload adds its size
to the running total without a rescan. A quota of zero means no limit.

## Programmatic access

A script authenticates with a token in the `Authorization` header instead of a
session cookie. The token reads as `sxp_<prefix>_<secret>`: a fixed marker that
makes one recognisable in a log file, an eight character prefix and a thirty
two character secret. It is handed over once by `POST /api/v1/auth/tokens` and
is never recoverable afterwards.

Only the prefix is stored in the clear. It is the indexed column the lookup
finds a row by, it is what the list screen and the audit trail name, and it
carries no ability to authenticate on its own. The secret is stored as a
SHA-256 digest and compared in constant time, the same treatment session
identifiers and share tokens get, so a copy of the database cannot be replayed
against a running server. `api_tokens` holds both, along with the scope, the
optional expiry and a last used stamp written back at most once a minute.

A token narrows an account, it never widens one. `tokNarrow` hands a `write`
token the account as loaded and a `read` token a copy carrying only `view` and
`download`. An administrator needs one step more, since that role is allowed
everything without its permissions being consulted: the copy is demoted out of
the role as well, and the served folders are carried across as read only mounts
so the same tree stays visible with none of the write paths open. Revoking a
token leaves the password, the open sessions and the account untouched, which
is the point of having them.

`tokenUser` runs ahead of the session cookie in `authenticate`, so a request
carrying both is treated as the token call it is. It accepts a bearer token,
which is what a script sends, and Basic credentials with the token as the
password, which is all a WebDAV client can offer. A Basic username has to name
the account the token belongs to, so a mount cannot quietly land somewhere
other than where the person typing it expected. Token calls skip the CSRF
double submit check because there is no ambient credential to forge: a page on
another site cannot make the browser attach an `Authorization` header it does
not know. The exemption is keyed on that header being present, not on a route,
so it cannot be inherited by a cookie request.

## Network drive

WebDAV is served at `/dav/` by `golang.org/x/net/webdav`, so PROPFIND bodies,
lock tokens and `Destination` headers are handled by a tested implementation
rather than by hand. Storix supplies the two pieces that are its own: a
`webdav.FileSystem` that resolves every name through the same guarded `vfs`
layer the API uses, and authentication through `TokenAuthUser`, the same
resolver the API middleware runs, reading the Basic password as an access
token. Nothing below it can tell whether a call arrived from the browser or
from Explorer, so containment, read only mounts, the deny list and the audit
trail all apply unchanged.

The top level of the tree is not a directory on disk. It is the mount list,
rendered as one collection per folder the account owns, which is what makes
`/dav/` browsable in a file manager without exposing anything above a mount.

Locks are held in memory, in `webdav.NewMemLS()`. Windows and macOS both take a
LOCK before their first PUT and treat a refusal as a read only volume, so
answering locks is not optional. Keeping them in memory rather than in SQLite
is the right trade for a single process product: a stale lock in a database
outlives the client that took it and has to be reaped, while a restart of the
service is exactly the moment those clients are gone too.

Two guards in `middleware.go` bracket the tree. `setupGate` closes `/dav`
entirely until the first run wizard has completed, and `csrfGuard` lets it
through, since a WebDAV client sends its credentials on every request and never
rides on a cookie.
