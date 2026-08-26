# Storix API

A practical reference for the Storix HTTP interface. Everything the web
interface does is done through these endpoints, and so can everything a script
of yours does.

Developed by X Project.

## Base

The JSON API lives under `/api/v1` on the address Storix is served from, for
example `https://files.example.com/api/v1`. Paths in the tables below are
written without that prefix. The network drive is the one part of Storix that
sits outside it, at `/dav/`, and it is described at the end.

Requests and responses are JSON, with three exceptions: `fs/download`,
`fs/download-zip`, `fs/raw` and `fs/thumb` answer with the bytes themselves,
`events` answers with a server sent event stream, and the tus endpoints carry
file bytes and say everything in headers. The public equivalents of those
endpoints behave the same way.

A JSON body is read strictly. A field the endpoint does not know is refused
with `bad_request` rather than ignored, so a misspelled key is visible at once
instead of quietly doing nothing. A JSON body is capped at 16 MiB, which bounds
requests only; an upload carries its bytes through tus and has no such ceiling.

Every path parameter is a virtual absolute path, for example `/var/www/site`,
resolved against the folders the calling account owns. Times are RFC 3339.
Sizes are bytes.

## Authentication

There are two ways to prove who you are, and a call carries one of them.

**The session cookie.** `POST /auth/login` sets `storix_session` and the
readable `storix_csrf` cookie. Every request that changes something then has to
repeat the CSRF value in an `X-Storix-CSRF` header, or in a `_csrf` field when
the body is a form. This is what the browser uses.

**A bearer token.** Send `Authorization: Bearer <token>` and no cookie is
needed. Token calls skip the CSRF check, because there is no cookie for another
site to ride on. This is what scripts use. HTTP Basic credentials are accepted
as well, with the token as the password, which is all a WebDAV client can
offer. The username of a Basic pair has to name the account the token belongs
to, so a mount cannot quietly land on somebody else's folders. On `/api/v1` the
password of a Basic pair must be a token; the account password is accepted over
Basic only on `/dav/`.

A token reads as `sxp_<prefix>_<secret>`: a fixed `sxp_` marker so one that
turns up in a log file or a repository is recognisable for what it is, then an
eight character prefix, then a thirty two character secret, both drawn from an
alphabet that leaves out the characters people misread when copying by hand.
Only the prefix is stored in the clear, which is what the lookup indexes and
what the list screen shows. The secret is kept as a SHA-256 digest, so a copy
of the database cannot be replayed. It is shown in full exactly once, in the
reply that creates it. Losing a token means creating a new one.

A token carries a scope. A `read` token narrows the account to `view` and
`download`, so it can list, download and inspect but never write, delete or
share, whatever the account itself is allowed to do. For an administrator that
narrowing also turns the served folders into read only mounts, so the same tree
stays visible with none of the write paths open. A `write` token acts with the
permissions the account already has. A token never widens an account, and it
never reaches a folder the account does not own.

An account holds up to twenty live tokens. Each records when it was last used
and from which address, written back at most once a minute so a polling script
does not turn every read into a write.

Create a token in the interface, then list a folder with it:

```bash
export STORIX="https://files.example.com"
export STORIX_TOKEN="paste the token you were shown"

curl -s -H "Authorization: Bearer $STORIX_TOKEN" \
  "$STORIX/api/v1/fs/list?path=/var/www/site"
```

The same token authenticates the WebDAV tree at `/dav/`, where it is sent as
the password of an HTTP Basic pair. See [WEBDAV.md](WEBDAV.md).

## Permissions

Every endpoint below is gated by one permission, so a `read` token, which holds
only `view` and `download`, reaches exactly the first two rows.

| Permission | Gates |
| --- | --- |
| `view` | `fs/list`, `fs/stat`, `fs/tree`, `fs/search`, `fs/du`, `fs/disk`, `fs/usage`, `fs/duplicates`, `fs/raw`, `fs/thumb`, `GET fs/text`, `fs/archive` |
| `download` | `fs/download`, `fs/download-zip` |
| `upload` | the tus endpoints and `uploads` |
| `create` | `fs/mkdir`, `fs/touch` |
| `rename` | `fs/rename`, `fs/rename-bulk` and its preview |
| `move` | `fs/move` |
| `copy` | `fs/copy` |
| `delete` | `fs/delete` and everything under `trash` except the listing |
| `archive` | `fs/compress`, `fs/extract` |
| `edit` | `PUT fs/text` |
| `share` | everything under `shares` |
| `advanced` | `fs/chmod`, `fs/chown` |
| `users` | everything under `users`, including another account's quota |
| `settings` | `system/settings`, `system/roots`, `system/browse`, `system/audit` |

Being signed in is enough for the rest: the account endpoints, the tokens,
`dashboard`, `system/info`, `roles`, the trash listing, `favorites`, `recent`,
`jobs`, `events` and `auth/quota`. `system/update/check`, `system/update` and
`system/domain` are restricted to administrators rather than to a permission,
and so is `fs/chown` on top of its `advanced` requirement.

## Errors

A failure from the JSON API is always the same envelope, with the HTTP status
carrying the same meaning as the code. The tus endpoints and the network drive
are the exception: they answer a failure with a plain text line and a status,
because that is what their clients understand.

```json
{ "error": { "code": "forbidden", "message": "That path is outside the area you can access" } }
```

`code` and `message` are the whole of it: the message is written to be shown to
a person as it stands. A success is the payload itself, or `{ "ok": true }`
where there is nothing to return.

| Code | Status | Meaning |
| --- | --- | --- |
| `bad_request` | 400 | A field is missing, unknown or not valid |
| `unauthorized` | 401 | No session and no token, or both have expired |
| `invalid_credentials` | 401 | Wrong username or password |
| `totp_required` | 401 | The account has two step verification on |
| `invalid_totp` | 401 | The verification code was not accepted |
| `password_required` | 401 | The public link is protected by a password |
| `password_invalid` | 401 | The password given for a public link did not match |
| `forbidden` | 403 | Outside the folders the account owns, or a missing permission |
| `denied` | 403 | A protected location such as `/etc/shadow` |
| `read_only` | 403 | The mount, or the token, does not allow writing |
| `csrf` | 403 | The `X-Storix-CSRF` header did not match the cookie |
| `locked` | 403 | Too many failed sign ins, the account is locked for a while |
| `disabled` | 403 | The account is turned off |
| `wrong_password` | 403 | The current password given for a change was not correct |
| `advanced_disabled` | 403 | `chmod` and `chown` are turned off on this server |
| `ip_blocked` | 403 | The caller is outside the configured address allowlist |
| `bad_token` | 403 | That setup link is not valid |
| `not_found` | 404 | No such file, folder or record |
| `gone` | 404 | The public link has expired or was revoked |
| `no_preview` | 404 | A public link was asked for a thumbnail it does not have |
| `conflict` | 409 | The request contradicts the current state |
| `exists` | 409 | A file with that name is already there |
| `setup_completed` | 409 | The first run wizard has already been completed |
| `too_large` | 413 | Over the upload limit, the edit ceiling, or the storage allowance |
| `unsupported` | 415 | There is no preview for this file, or the archive format is not one Storix reads |
| `unreadable` | 422 | The archive could not be read |
| `unsafe_archive` | 422 | The archive expands far beyond its size and was refused |
| `too_many_entries` | 422 | The archive holds too many entries to open |
| `rate_limited` | 429 | Slow down, `Retry-After` says by how long |
| `internal` | 500 | Something failed on the server, the log has the detail |
| `update_check_failed` | 502 | The release service could not be reached |
| `unavailable` | 503 | Thumbnails, live updates or background operations are turned off |
| `updates_unavailable` | 503 | This installation does not manage its own updates |
| `setup_required` | 503 | The first run wizard has not been completed yet |

Saving settings can also answer `no_config_file` or `not_writable` with 409,
`config_not_writable` with 403 and `config_save_failed` with 500, which say
that the configuration file itself is in the way. A caller that hangs up
mid request is recorded as `canceled` with status 499, which nobody receives.

## System and setup

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /system/status` | nothing, public | `product`, `version`, `setupRequired`, `platform`, `branding`, `developer` |
| `POST /setup` | `token`, `username`, `password`, `displayName`, `email`, `folders[]`, `domain` | `ok`, `user`, `csrf`, `warning`, and signs the new administrator in |
| `GET /system/info` | nothing | `build`, `uptime` in seconds and `publicUrl` for everyone, plus `host`, `memory`, `database` and `counts` for administrators |
| `GET /dashboard` | nothing | the home screen aggregate: `greeting`, `user`, `storage`, `recent`, `favorites`, `transfers`, `shares`, `jobs`, `trash`, `mounts`, `version`, `updateAvailable` |

## Session and account

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `POST /auth/login` | `username`, `password`, `totp`, `remember` | `ok`, `user`, `csrf`, `mustChangePassword`, and sets the cookies |
| `POST /auth/logout` | nothing | `ok`, and clears the cookies. Signing out twice is not an error |
| `GET /auth/me` | nothing | `user`, `permissions`, `mounts`, `csrf`, `branding`, `preferences`, `limits`, `features` |
| `POST /auth/password` | `current`, `new` | `ok`, and ends every other session of the account |
| `POST /auth/totp/setup` | nothing | `secret`, `uri` for the authenticator app, `recovery[]` shown once |
| `POST /auth/totp/enable` | `code` | `ok` once the code proves the app holds the secret |
| `POST /auth/totp/disable` | `password` | `ok`, and clears the recovery codes |
| `GET /auth/sessions` | nothing | `sessions[]`, with `current` marking this browser |
| `DELETE /auth/sessions/{id}` | nothing | `ok`. A session of another account answers `forbidden` |
| `POST /auth/preferences` | `theme`, `locale`, `view`, `sort`, `order`, `showHidden`, `foldersFirst` | `ok`, `preferences` as they were stored |
| `GET /roles` | nothing | `roles[]` presets and the `permissions[]` catalogue |

`remember` is accepted by the sign in form and changes nothing: how long a
session lives is a server setting. A value in `preferences` that is not one the
server knows is dropped rather than stored, which is why the reply carries back
what was kept: `theme` is `dark` or `light`, `view` is `list`, `grid` or
`gallery`, `sort` is `name`, `size`, `modified`, `kind` or `ext`, and `order`
is `asc` or `desc`.

## Access tokens

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /auth/tokens` | nothing | `tokens[]` with `id`, `name`, `prefix`, `scope`, `createdAt`, `expiresAt`, `lastUsedAt`, `lastUsedIp` and `expired`, plus a `webdav` block holding `enabled`, `url` and the line to run on `windows`, `macos` and `linux` |
| `POST /auth/tokens` | `name` of 1 to 64 characters, `scope` of `read` or `write`, `expiresIn` | `201`, the `token` metadata and `secret`, the only time the token itself is returned |
| `DELETE /auth/tokens/{id}` | nothing | `ok`. The token stops working at once, everything else about the account is untouched |

`scope` defaults to `read`. `expiresIn` is one of `30d`, `90d`, `1y` or
`never`, and an absent value means never, because a machine that runs
unattended is a legitimate reason to want no expiry. Expired tokens stay in the
list, marked, so the owner can see what to clear out. They do not count towards
the limit of twenty; an account already holding twenty live ones answers
`conflict` until one is revoked.

## Browsing

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /fs/list` | `path`, `hidden`, `sort`, `order`, `filter`, `limit` | a listing: `path`, `parent`, `mount`, `entries[]`, `total`, `truncated`, `files`, `folders`, `size`, `hiddenCount`, `readOnly`, plus `favorite`, `canWrite` and `breadcrumbs[]`. Without `path` it returns `mounts` and `isRoot` |
| `GET /fs/stat` | `path` | one entry, plus `favorite` |
| `GET /fs/tree` | `path`, `depth` of 1 to 3, `hidden` | `path` and `children[]` of `name`, `path`, `hasChildren`, `children`, folders only, for the sidebar and the move dialog |
| `GET /fs/search` | `q`, `path`, `kind`, `content`, `hidden`, `limit` | `entries[]`, `scanned`, `elapsedMs`, `truncated`, `query` |
| `GET /fs/du` | `path` | `path`, `bytes`, `items`, and `partial` when the walk ran out of time |
| `GET /fs/disk` | `path` | usage of the volume holding that path: `total`, `free`, `used`, `available`, `percent`, and the same for inodes |
| `GET /fs/usage` | `path`, `limit` of up to 200, 40 by default | the storage report: `bytes`, `files`, `folders`, `children[]`, `largest[]`, `byKind[]`, `scanned`, `truncated`, `elapsedMs`, with `message` when the walk stopped early |
| `GET /fs/duplicates` | `path`, `min` smallest file size to consider, 1 KiB by default | `groups[]` of identical files, `wasted`, `scanned`, `hashed`, `truncated`, `elapsedMs`, and `message` when the scan stopped early |

`path` is required on `fs/stat`, `fs/du`, `fs/disk`, `fs/usage` and
`fs/duplicates`. `fs/list` and `fs/tree` answer with the mount list when it is
left out, and a search without one covers every mount the account owns; what a
search does require is `q`.

`sort` takes `name`, `size`, `modified`, `kind` or `ext`, `order` takes `asc`
or `desc`, and `filter` keeps the entries whose name contains it, ignoring
case. `kind` on a search is a comma separated list of `folder`, `image`,
`video`, `audio`, `pdf`, `archive`, `code`, `text`, `document`, `disk`, `font`,
`binary` and `other`. The `limit` of a listing and of a search are both capped
by the matching server setting.

The duplicate scan runs in three passes. Files are bucketed by size, the
buckets that still hold more than one file have their first 64 KiB hashed, and
only what survives that is read in full and compared by a SHA-256 of the whole
file. A report therefore never calls two files identical on the strength of
their size alone. It carries up to two hundred groups, looks at up to five
hundred thousand files and gives up after forty five seconds, reporting what it
has with `truncated` set. The storage report is fenced in the same way, with
its own ceiling of two million entries and twenty five seconds.

## Reading content

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /fs/download` | `path` | the file as an attachment, range aware. A folder answers `302` to `download-zip` |
| `GET /fs/download-zip` | `path` repeated or comma separated, `paths` the same way, and `name` | a zip built as it is sent, nothing staged on disk |
| `GET /fs/raw` | `path` | the file inline for preview, range aware so video seeks |
| `GET /fs/thumb` | `path`, `size` in pixels, 256 by default | a cached thumbnail, generated on first request |
| `GET /fs/text` | `path` | `path`, `name`, `content`, `language`, `size`, `truncated`, `binary`, `readOnly`, `modified`, for the editor |
| `GET /fs/archive` | `path`, `limit` of up to 5000, 200 by default | `format`, `items[]` and `truncated`, without extracting anything |

A selection for a zip may name up to two thousand paths and they all have to
sit under the same mount. A file Storix will not vouch for is served as a plain
byte stream rather than with its own media type, so a stored page can never
render in the application origin.

## Writing

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `POST /fs/mkdir` | `path` parent, `name` | the created entry |
| `POST /fs/touch` | `path` parent, `name`, `content` | the created entry |
| `POST /fs/rename` | `path`, `name` | the renamed entry |
| `PUT /fs/text` | `path`, `content` | the saved entry |
| `POST /fs/move` | `sources[]`, `dest`, `conflict` of `rename`, `overwrite`, `skip` or `fail` | `202` and the job |
| `POST /fs/copy` | the same as move | `202` and the job |
| `POST /fs/delete` | `paths[]`, `permanent` | `ok`, `deleted`, `permanent`, `errors[]` when the selection is a few plain files, otherwise `202` and a job |
| `POST /fs/compress` | `sources[]`, `dest`, `name`, `format` of `zip`, `tar.gz` or `tar` | `202` and the job |
| `POST /fs/extract` | `path`, `dest` | `202` and the job |
| `POST /fs/rename-bulk/preview` | `paths[]` from one folder, `rule` | `changes[]` of `path`, `from`, `to`, `conflict`, `unchanged` and `reason`, with the `conflicts`, `unchanged` and `valid` counts. Nothing is touched |
| `POST /fs/rename-bulk` | `paths[]`, `rule` | `renamed` and `failed[]` of `path` and `reason` |
| `POST /fs/chmod` | `path`, `mode`, `recursive` | the entry. Refused when advanced tools are off |
| `POST /fs/chown` | `path`, `owner`, `group`, `recursive` | the entry. Administrators only |

A delete of up to fifty plain files runs inside the request so the interface
reacts at once. A folder, or a longer selection, becomes a job. A move, a copy
or a delete may name up to ten thousand paths.

A rename `rule` carries a `mode` and only the fields that mode reads. `replace`
takes `find`, `replace`, `regex` and `caseSensitive`; `prefix` and `suffix`
take `text`; `number` takes `pattern` with `{n}` for the counter and `{name}`
for the original base name, plus `start` and `padding`; `case` takes `casing`
of `lower`, `upper` or `title`. `keepExtension` applies to all of them. One
batch covers up to five thousand items, and they all have to sit in one folder.

## Uploads

Uploads speak tus 1.0.0, so an interrupted transfer resumes at the byte it
stopped at. `Upload-Metadata` is the tus format, a comma separated list of
`key base64value` pairs, and Storix reads `filename`, `dir`, `relativePath` for
folder uploads and `overwrite`. `filename` and `dir` are both required.

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `OPTIONS /tus`, `OPTIONS /tus/{id}` | nothing, no credentials needed | `204` with `Tus-Resumable`, `Tus-Version`, `Tus-Extension`, and `Tus-Max-Size` when the server sets a ceiling |
| `POST /tus` | `Upload-Length`, `Upload-Metadata` | `201` and a `Location` header naming the new upload. A body sent with `Content-Type: application/offset+octet-stream` is accepted straight away and answered with `Upload-Offset` |
| `HEAD /tus/{id}` | nothing | `200` with `Upload-Offset` and `Upload-Length`, which is where to resume |
| `PATCH /tus/{id}` | `Upload-Offset` and the chunk, as `application/offset+octet-stream` | `204` and the new `Upload-Offset`. A mismatched offset answers `409` and the real one |
| `DELETE /tus/{id}` | nothing | `204`, and the partial data is removed |
| `GET /uploads` | `all`, which here adds the transfers that already finished rather than those of other accounts | `uploads[]` of the caller's transfers, with `active` and `bytes` |

An upload is checked against the storage allowance before the first byte is
accepted, because tus declares the length up front. A refusal is `413`, as is
one over the server's upload ceiling. A client that hangs up mid chunk is
answered `204` with the offset that did land, so it can carry on from there.
The extensions offered are `creation`, `creation-with-upload`, `termination`
and `expiration`; a transfer that is never finished expires and is swept away.
Storix does not refuse a request that leaves `Tus-Resumable` off, but a client
that sends it is the one speaking the protocol properly.

## Recycle bin, pins and history

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /trash` | `all` for administrators, `limit` | `items[]`, `count`, `bytes`, `retentionDays` |
| `POST /trash/restore` | `ids[]` | `restored` and `failed[]`. A name taken again gets a numbered suffix |
| `POST /trash/delete` | `ids[]` | `ok`, `deleted`, `failed[]` |
| `POST /trash/empty` | `allUsers` for administrators | `ok`, `emptied`, `failed`, `bytes` freed |
| `GET /favorites` | nothing | `favorites[]`, `count` |
| `POST /favorites` | `path` | the pin. The path has to resolve first |
| `DELETE /favorites` | `path` | `ok`. Unpinning something that was not pinned still succeeds |
| `GET /recent` | `limit` of up to 500, 50 by default | `recent[]`, `count`, with entries that no longer resolve left out |

## Share links

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /shares` | `all` for administrators | `shares[]` with the public `url`, `total`, and `all` echoing whether the whole server was listed |
| `POST /shares` | `path`, `name`, `kind` of `download` or `upload`, `password`, `expiresIn`, `maxDownloads`, `allowDownload`, `allowUpload`, `allowList`, `note` | `201` and the share with its `url` |
| `PATCH /shares/{id}` | any of the same fields, plus `clearPassword` | the updated share. What a link points at never changes, and a `path` sent here is ignored |
| `DELETE /shares/{id}` | nothing | `ok` |

`expiresIn` is one of `1h`, `24h`, `7d`, `30d`, `90d` or `never`.

The public side needs no account. It is protected by the unguessable token in
the address, and by the password when the link carries one.

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /public/{token}` | `path` inside a folder share, `sort`, `order` | what a visitor may see: `name`, `kind`, `isDir`, the `allow` flags, `note`, `owner`, `entries[]`, `breadcrumbs[]` |
| `POST /public/{token}/auth` | `password` | `ok` and `expiresAt`, and unlocks this browser for twelve hours |
| `GET /public/{token}/download` | `path` | the file as an attachment |
| `GET /public/{token}/download-zip` | `path`, and `item` repeated to pick single names inside it | the folder as a streamed zip |
| `GET /public/{token}/raw` | `path` | the file inline |
| `GET /public/{token}/thumb` | `path`, `size` | a thumbnail |
| `OPTIONS /public/{token}/tus`, `OPTIONS /public/{token}/tus/{id}` | nothing | the tus discovery answer |
| `POST /public/{token}/tus` | as `/tus`, and the destination is fixed by the link | an upload into an upload request |
| `HEAD /public/{token}/tus/{id}` | nothing | the offset to resume from |
| `PATCH /public/{token}/tus/{id}` | as `/tus/{id}` | `204` and the new offset |

A link that is locked answers `GET /public/{token}` with `401` and a body
holding `error`, `name`, `kind` and `hasPassword`, which is everything a
visitor may know before unlocking. A link that has expired, run out of
downloads or been revoked answers `gone`, and so does one that never existed.

## Accounts and allowances

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /users` | nothing | `users[]` with folders and a session count |
| `POST /users` | `username`, `password`, `displayName`, `email`, `role`, `permissions[]`, `mounts[]`, `active`, `mustChangePassword`, `quota` | `201` and the account |
| `PATCH /users/{id}` | any of the same fields, all optional | the updated account |
| `DELETE /users/{id}` | nothing | `ok`, and everything belonging to the account goes with it |
| `GET /auth/quota` | nothing | `limit`, `used`, `files`, `percent`, `remaining`, `computedAt`, `stale` |
| `GET /users/{id}/quota` | nothing | the same, for one account. Needs the `users` permission |

A `mounts[]` entry is an object of `path`, `label`, `icon`, `readOnly` and
`create`, the last of which asks for the folder to be made if it is not there.

A `limit` of zero means no allowance, and then `remaining` is `-1`. A figure
that has aged out is answered immediately with `stale` set and a fresh
measurement is queued, so the endpoint never waits on a disk scan.

## Operations

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /jobs` | `limit`, `all` for administrators | `jobs[]`, newest first, and `count` |
| `GET /jobs/{id}` | nothing | one job. A job of another account reads as missing |
| `POST /jobs/{id}/cancel` | nothing | `ok` |
| `GET /events` | nothing | a server sent event stream |

A job carries `id`, `type`, `status` of `queued`, `running`, `done`, `failed`
or `canceled`, `title`, `message`, `total`, `done`, `totalItems`, `doneItems`,
`cancellable`, the timestamps, and `result` once it has finished.

The stream opens with a `hello` event carrying `userId` and the caller's recent
jobs, so a page that reconnects catches up in the same request. After that it
carries `job.created`, `job.progress`, `job.done`, `job.failed`,
`upload.progress`, `upload.done`, `fs.changed`, `share.created`,
`share.updated`, `share.revoked`, `share.upload` and `system.notice`. A comment
line is sent every twenty five seconds so an idle connection is not closed by a
proxy.

## Administration

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /system/settings` | nothing | `branding`, `security`, `limits`, `updates`, `server` and `trash` |
| `PUT /system/settings` | any subset of the same | `ok`, `restartRequired`, `changed[]`, `settings` |
| `GET /system/roots` | nothing | `roots[]` with `exists` and volume `usage`, and `total` |
| `POST /system/roots` | `path`, `label`, `icon`, `readOnly` | `201`, `root` and a message |
| `PATCH /system/roots/{id}` | `label`, `icon`, `readOnly` | `root` and `changed`. A path is never edited, it is added and removed |
| `DELETE /system/roots/{id}` | nothing | `ok` and a message. Nothing on disk is deleted |
| `GET /system/browse` | `path` | `path`, `parent`, `dirs[]` and `truncated`: folder names on the server for the picker. It reads outside the mount scope on purpose, which is why it sits behind the `settings` permission |
| `GET /system/audit` | `limit` of up to 500, `offset`, `action`, `q`, `user` as a name or an id | `entries[]`, `total`, `limit`, `offset` |
| `GET /system/update/check` | nothing | the release: `version`, `current`, `available`, `notes`, `size`, `writable` |
| `POST /system/update` | nothing | `202`, the `job` doing the update and a message |
| `POST /system/domain` | `domain`, `email`, `enable` | `ok`, `restartRequired`, `url` and what to do next |

## The network drive

`/dav/` serves the same folders over WebDAV, so Storix can be mapped as a drive
in Windows Explorer, the macOS Finder or a Linux file manager. It is not part
of `/api/v1` and speaks no JSON: methods are the WebDAV ones, `PROPFIND`,
`GET`, `PUT`, `MKCOL`, `MOVE`, `COPY`, `DELETE`, `PROPPATCH`, `LOCK` and
`UNLOCK`, and a refusal is a status with a plain sentence.

Credentials go on every request as an HTTP Basic pair, because a mounted drive
has no session cookie and nowhere to put a CSRF header. The password is either
an access token or, when the account has no two step verification, the account
password. An account with two step verification on, or one still waiting to
change its password, can only mount with a token. Refused credentials answer
`401` with a `WWW-Authenticate` challenge, and repeated failures from one
address are slowed down exactly as the sign in form is.

The drive needs `view` to open at all, and `upload` to write: an account
without it sees every mount as read only, and so does anyone mounting with a
`read` token.

The top of the tree lists the mounts the account owns, one collection each,
named after the mount label reduced to a URL safe slug, so `Web files` answers
at `/dav/web-files/`. Everything below resolves through the same guarded layer
the browser uses, so containment, protected paths and read only mounts behave
identically. A write into a read only mount, or into the top of the tree, is
refused with `403` and a sentence saying which of the two it was.

```bash
curl -u alice:$STORIX_TOKEN -X PROPFIND -H "Depth: 1" "$STORIX/dav/"
```

The mounting instructions for each operating system are in
[WEBDAV.md](WEBDAV.md).

## Recipes

Each of these assumes `$STORIX` is the address and `$STORIX_TOKEN` is a token
with the `write` scope, except where reading is enough.

**Upload a file with tus.** Create the transfer, then send the bytes. The
metadata values are base64.

```bash
FILE=backup.tar.gz
SIZE=$(stat -c %s "$FILE")
DIR=$(printf '/var/www/site' | base64 -w0)
NAME=$(printf '%s' "$FILE" | base64 -w0)

LOCATION=$(curl -s -D - -o /dev/null -X POST "$STORIX/api/v1/tus" \
  -H "Authorization: Bearer $STORIX_TOKEN" \
  -H "Tus-Resumable: 1.0.0" \
  -H "Upload-Length: $SIZE" \
  -H "Upload-Metadata: filename $NAME,dir $DIR" \
  | tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')

curl -s -X PATCH "$STORIX$LOCATION" \
  -H "Authorization: Bearer $STORIX_TOKEN" \
  -H "Tus-Resumable: 1.0.0" \
  -H "Content-Type: application/offset+octet-stream" \
  -H "Upload-Offset: 0" \
  --data-binary "@$FILE"
```

`$LOCATION` comes back as a path, `/api/v1/tus/<id>`, which is why it is used
with `$STORIX` in front of it.

If the connection drops, ask where it got to and carry on from there.

```bash
OFFSET=$(curl -sI "$STORIX$LOCATION" \
  -H "Authorization: Bearer $STORIX_TOKEN" -H "Tus-Resumable: 1.0.0" \
  | tr -d '\r' | awk '/^[Uu]pload-[Oo]ffset:/ {print $2}')

tail -c +$((OFFSET + 1)) "$FILE" | curl -s -X PATCH "$STORIX$LOCATION" \
  -H "Authorization: Bearer $STORIX_TOKEN" \
  -H "Tus-Resumable: 1.0.0" \
  -H "Content-Type: application/offset+octet-stream" \
  -H "Upload-Offset: $OFFSET" \
  --data-binary @-
```

**Download a folder as a zip.** The zip is built while it is sent, so nothing
is staged on the server first.

```bash
curl -s -o site.zip -G "$STORIX/api/v1/fs/download-zip" \
  -H "Authorization: Bearer $STORIX_TOKEN" \
  --data-urlencode "path=/var/www/site" \
  --data-urlencode "name=site.zip"
```

Pass `--data-urlencode "path=..."` more than once to pack a selection.

**Create a share link.** The reply carries the public address in `url`.

```bash
curl -s -X POST "$STORIX/api/v1/shares" \
  -H "Authorization: Bearer $STORIX_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"path":"/var/www/site/report.pdf","kind":"download","expiresIn":"7d","maxDownloads":25}'
```

**Check storage use.** The allowance of the calling account:

```bash
curl -s -H "Authorization: Bearer $STORIX_TOKEN" "$STORIX/api/v1/auth/quota"
```

What is filling one folder, biggest first:

```bash
curl -s -H "Authorization: Bearer $STORIX_TOKEN" -G "$STORIX/api/v1/fs/usage" \
  --data-urlencode "path=/var/www" --data-urlencode "limit=20"
```

And the volume behind it:

```bash
curl -s -H "Authorization: Bearer $STORIX_TOKEN" -G "$STORIX/api/v1/fs/disk" \
  --data-urlencode "path=/var/www"
```

## Notes

- Mutating calls are rate limited, per account where there is one and per
  address otherwise. Going over answers `429` with `Retry-After`. Upload chunks
  are exempt, since they legitimately arrive fast.
- Sign in attempts are limited per address, and an account locks itself after
  repeated failures.
- Every sign in, permission change, share, upload and delete made through the
  API leaves an audit entry, whether it came from the browser or from a token.
  A WebDAV mount records refused credentials but not each file operation, so
  the audit trail of a drive is thinner than the one of the interface.
- `X-Storix-CSRF` matters only for cookie calls. A request carrying an
  `Authorization` header is not checked for it.
