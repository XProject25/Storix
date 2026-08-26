# Storix API

A practical reference for the Storix HTTP interface. Everything the web
interface does is done through these endpoints, and so can everything a script
of yours does.

Developed by X Project.

## Base

All endpoints live under `/api/v1` on the address Storix is served from, for
example `https://files.example.com/api/v1`. Paths in the tables below are
written without that prefix.

Requests and responses are JSON. Every path parameter is a virtual absolute
path, for example `/var/www/site`, resolved against the folders the calling
account owns. Times are RFC 3339 in UTC. Sizes are bytes.

## Authentication

There are two ways to prove who you are, and a call carries one of them.

**The session cookie.** `POST /auth/login` sets `storix_session` and the
readable `storix_csrf` cookie. Every request that changes something then has to
repeat the CSRF value in an `X-Storix-CSRF` header. This is what the browser
uses.

**A bearer token.** Send `Authorization: Bearer <token>` and no cookie is
needed. Token calls skip the CSRF check, because there is no cookie for another
site to ride on. This is what scripts use. HTTP Basic credentials are accepted
as well, with the token as the password, which is all a WebDAV client can
offer. The username of a Basic pair has to name the account the token belongs
to, so a mount cannot quietly land on somebody else's folders.

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

## Errors

A failure is always the same envelope, with the HTTP status carrying the same
meaning as the code.

```json
{ "error": { "code": "forbidden", "message": "That path is outside the area you can access" } }
```

`detail` is present on some errors and carries a longer explanation. A success
is the payload itself, or `{ "ok": true }` where there is nothing to return.

| Code | Status | Meaning |
| --- | --- | --- |
| `unauthorized` | 401 | No session and no token, or both have expired |
| `invalid_credentials` | 401 | Wrong username or password |
| `totp_required` | 401 | The account has two step verification on |
| `invalid_totp` | 401 | The verification code was not accepted |
| `forbidden` | 403 | Outside the folders the account owns, or a missing permission |
| `denied` | 403 | A protected location such as `/etc/shadow` |
| `read_only` | 403 | The mount, or the token, does not allow writing |
| `csrf` | 403 | The `X-Storix-CSRF` header did not match the cookie |
| `locked` | 403 | Too many failed sign ins, the account is locked for a while |
| `ip_blocked` | 403 | The caller is outside the configured address allowlist |
| `not_found` | 404 | No such file, folder or record |
| `conflict` | 409 | The request contradicts the current state |
| `exists` | 409 | A file with that name is already there |
| `too_large` | 413 | Over the upload limit, or over the storage allowance |
| `unsupported` | 415 | The format is not one Storix handles |
| `unreadable` | 422 | The file could not be decoded |
| `rate_limited` | 429 | Slow down, `Retry-After` says by how long |
| `internal` | 500 | Something failed on the server, the log has the detail |
| `setup_required` | 503 | The first run wizard has not been completed yet |

## System and setup

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /system/status` | nothing, public | `product`, `version`, `setupRequired`, `platform`, `branding` |
| `POST /setup` | `token`, `username`, `password`, `displayName`, `email`, `folders[]`, `domain` | `ok`, `user`, `csrf`, and signs the new administrator in |
| `GET /system/info` | nothing | build details for everyone, host, memory, database and counts for administrators |
| `GET /dashboard` | nothing | the home screen aggregate: storage, recent, favourites, transfers, jobs, trash, mounts |

## Session and account

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `POST /auth/login` | `username`, `password`, `totp` | `ok`, `user`, `csrf`, `mustChangePassword`, and sets the cookies |
| `POST /auth/logout` | nothing | `ok`, and clears the cookies. Signing out twice is not an error |
| `GET /auth/me` | nothing | `user`, `permissions`, `mounts`, `csrf`, `branding`, `preferences`, `limits`, `features` |
| `POST /auth/password` | `current`, `new` | `ok`, and ends every other session of the account |
| `POST /auth/totp/setup` | nothing | `secret`, `uri` for the authenticator app, `recovery[]` shown once |
| `POST /auth/totp/enable` | `code` | `ok` once the code proves the app holds the secret |
| `POST /auth/totp/disable` | `password` | `ok`, and clears the recovery codes |
| `GET /auth/sessions` | nothing | `sessions[]`, with `current` marking this browser |
| `DELETE /auth/sessions/{id}` | nothing | `ok`. A session of another account is refused |
| `POST /auth/preferences` | `theme`, `locale`, `view`, `sort`, `order`, `showHidden`, `foldersFirst` | `ok`, `preferences` as they were stored |
| `GET /roles` | nothing | `roles[]` presets and the `permissions[]` catalogue |

## Access tokens

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /auth/tokens` | nothing | `tokens[]` with `prefix`, `scope`, `lastUsedAt`, `lastUsedIp` and `expired`, plus a `webdav` block holding `url` and the line to run on `windows`, `macos` and `linux` |
| `POST /auth/tokens` | `name` of 1 to 64 characters, `scope` of `read` or `write`, `expiresIn` | `201`, the `token` metadata and `secret`, the only time the token itself is returned |
| `DELETE /auth/tokens/{id}` | nothing | `ok`. The token stops working at once, everything else about the account is untouched |

`scope` defaults to `read`. `expiresIn` is one of `30d`, `90d`, `1y` or
`never`, and an absent value means never, because a machine that runs
unattended is a legitimate reason to want no expiry. Expired tokens stay in the
list, marked, so the owner can see what to clear out.

## Browsing

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /fs/list` | `path`, `hidden`, `sort`, `order`, `filter`, `limit` | a listing with `entries[]`, `mount`, `breadcrumbs`, counts and `readOnly`. Without `path` it returns the mount list and `isRoot` |
| `GET /fs/stat` | `path` | one entry, plus `favorite` |
| `GET /fs/tree` | `path`, `depth`, `hidden` | `children[]`, folders only, for the sidebar and the move dialog |
| `GET /fs/search` | `q`, `path`, `kind`, `content`, `hidden`, `limit` | `entries[]`, `scanned`, `elapsedMs`, `truncated` |
| `GET /fs/du` | `path` | `bytes`, `items`, and `partial` when the walk ran out of time |
| `GET /fs/disk` | `path` | usage of the volume holding that path, including inodes |
| `GET /fs/usage` | `path`, `limit` | the storage report: `children[]`, `largest[]`, `byKind[]`, with `message` when the walk stopped early |
| `GET /fs/duplicates` | `path`, `min` smallest file size to consider, 1 KiB by default | `groups[]` of identical files, `wasted`, `scanned`, `hashed`, `truncated`, and `message` when the scan stopped early |

The scan runs in three passes. Files are bucketed by size, the buckets that
still hold more than one file have their first 64 KiB hashed, and only what
survives that is read in full and compared by a SHA-256 of the whole file. A
report therefore never calls two files identical on the strength of their size
alone. It carries up to two hundred groups and gives up after forty five
seconds, reporting what it has with `truncated` set.

## Reading content

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /fs/download` | `path` | the file as an attachment, range aware. A folder redirects to `download-zip` |
| `GET /fs/download-zip` | `path` repeated, or `paths` comma separated, and `name` | a zip built as it is sent, nothing staged on disk |
| `GET /fs/raw` | `path` | the file inline for preview, range aware so video seeks |
| `GET /fs/thumb` | `path`, `size` | a cached thumbnail, generated on first request |
| `GET /fs/text` | `path` | `content`, `language`, `truncated`, `binary`, for the editor |
| `GET /fs/archive` | `path`, `limit` | what is inside an archive, without extracting it |

## Writing

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `POST /fs/mkdir` | `path` parent, `name` | the created entry |
| `POST /fs/touch` | `path` parent, `name`, `content` | the created entry |
| `POST /fs/rename` | `path`, `name` | the renamed entry |
| `PUT /fs/text` | `path`, `content` | the saved entry |
| `POST /fs/move` | `sources[]`, `dest`, `conflict` of `rename`, `overwrite` or `skip` | `202` and the job |
| `POST /fs/copy` | the same as move | `202` and the job |
| `POST /fs/delete` | `paths[]`, `permanent` | `ok`, `deleted`, `errors[]` when the selection is a few plain files, otherwise `202` and a job |
| `POST /fs/compress` | `sources[]`, `dest`, `name`, `format` of `zip`, `tar.gz` or `tar` | `202` and the job |
| `POST /fs/extract` | `path`, `dest` | `202` and the job |
| `POST /fs/rename-bulk/preview` | `paths[]` from one folder, `rule` | every `from` and `to`, with conflicts marked, nothing touched |
| `POST /fs/rename-bulk` | `paths[]`, `rule` | `renamed` and `failed[]` |
| `POST /fs/chmod` | `path`, `mode`, `recursive` | the entry. Refused when advanced tools are off |
| `POST /fs/chown` | `path`, `owner`, `group`, `recursive` | the entry. Administrators only |

## Uploads

Uploads speak tus 1.0.0, so an interrupted transfer resumes at the byte it
stopped at. `Upload-Metadata` is the tus format, a comma separated list of
`key base64value` pairs, and Storix reads `filename`, `dir`, `relativePath` for
folder uploads and `overwrite`.

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `OPTIONS /tus` | nothing | `204` with the protocol version, extensions and maximum size |
| `POST /tus` | `Upload-Length`, `Upload-Metadata` | `201` and a `Location` header naming the new upload. A body sent with `Content-Type: application/offset+octet-stream` is accepted straight away |
| `HEAD /tus/{id}` | nothing | `Upload-Offset` and `Upload-Length`, which is where to resume |
| `PATCH /tus/{id}` | `Upload-Offset` and the chunk, as `application/offset+octet-stream` | `204` and the new `Upload-Offset`. A mismatched offset answers `409` and the real one |
| `DELETE /tus/{id}` | nothing | `204`, and the partial data is removed |
| `GET /uploads` | `all` | the transfers of the caller still in flight, with `active` and `bytes` |

An upload is checked against the storage allowance before the first byte is
accepted, because tus declares the length up front. A refusal is `413`.

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
| `GET /recent` | `limit` | `recent[]`, with entries that no longer resolve left out |

## Share links

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /shares` | `all` for administrators | `shares[]` with the public `url`, and `total` |
| `POST /shares` | `path`, `kind` of `download` or `upload`, `password`, `expiresIn`, `maxDownloads`, `allowDownload`, `allowUpload`, `allowList`, `note` | `201` and the share with its `url` |
| `PATCH /shares/{id}` | any of the same fields, plus `clearPassword` | the updated share. What a link points at never changes |
| `DELETE /shares/{id}` | nothing | `ok` |

`expiresIn` is one of `1h`, `24h`, `7d`, `30d`, `90d` or `never`.

The public side needs no account. It is protected by the unguessable token in
the address, and by the password when the link carries one.

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /public/{token}` | `path` inside a folder share | what a visitor may see: name, capabilities, `entries[]`, breadcrumbs |
| `POST /public/{token}/auth` | `password` | `ok` and `expiresAt`, and unlocks this browser |
| `GET /public/{token}/download` | `path` | the file as an attachment |
| `GET /public/{token}/download-zip` | `path` | the folder as a streamed zip |
| `GET /public/{token}/raw` | `path` | the file inline |
| `GET /public/{token}/thumb` | `path`, `size` | a thumbnail |
| `POST /public/{token}/tus` | as `/tus` | an upload into an upload request |
| `HEAD /public/{token}/tus/{id}` | nothing | the offset to resume from |
| `PATCH /public/{token}/tus/{id}` | as `/tus/{id}` | `204` and the new offset |

## Accounts and allowances

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /users` | nothing | `users[]` with folders and a session count |
| `POST /users` | `username`, `password`, `displayName`, `email`, `role`, `permissions[]`, `mounts[]`, `active`, `mustChangePassword`, `quota` | `201` and the account |
| `PATCH /users/{id}` | any of the same fields, all optional | the updated account |
| `DELETE /users/{id}` | nothing | `ok`, and everything belonging to the account goes with it |
| `GET /auth/quota` | nothing | `limit`, `used`, `files`, `percent`, `remaining`, `computedAt`, `stale` |
| `GET /users/{id}/quota` | nothing | the same, for one account. Needs the users permission |

A `limit` of zero means no allowance, and then `remaining` is `-1`. A figure
that has aged out is answered immediately and a fresh measurement is queued, so
the endpoint never waits on a disk scan.

## Operations

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /jobs` | `limit`, `all` for administrators | `jobs[]`, newest first, and `count` |
| `GET /jobs/{id}` | nothing | one job. A job of another account reads as missing |
| `POST /jobs/{id}/cancel` | nothing | `ok` |
| `GET /events` | nothing | a server sent event stream of `job.*`, `upload.*`, `fs.changed`, `share.changed` and `system.notice` |

## Administration

| Endpoint | Takes | Returns |
| --- | --- | --- |
| `GET /system/settings` | nothing | branding, security, limits, updates, server and trash settings |
| `PUT /system/settings` | any subset of the same | `ok`, `restartRequired`, `changed[]`, `settings` |
| `GET /system/roots` | nothing | `roots[]` with `exists` and volume usage, and `total` |
| `POST /system/roots` | `path`, `label`, `icon`, `readOnly` | `201`, `root` and a message |
| `PATCH /system/roots/{id}` | `label`, `icon`, `readOnly` | `root` and `changed`. A path is never edited, it is added and removed |
| `DELETE /system/roots/{id}` | nothing | `ok`. Nothing on disk is deleted |
| `GET /system/browse` | `path` | folder names on the server for the picker, outside the mount scope, administrators only |
| `GET /system/audit` | `limit`, `offset`, `action`, `q`, `user` as a name or an id | `entries[]`, `total`, `limit`, `offset` |
| `GET /system/update/check` | nothing | the release: `version`, `available`, `notes`, `size`, `writable` |
| `POST /system/update` | nothing | `202`, the `job` doing the update and a message |
| `POST /system/domain` | `domain`, `email`, `enable` | `ok`, `restartRequired`, `url` and what to do next |

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

If the connection drops, ask where it got to and carry on from there.

```bash
OFFSET=$(curl -s -I -X HEAD "$STORIX$LOCATION" \
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

- Mutating calls are rate limited per account. Going over answers `429` with
  `Retry-After`. Upload chunks are exempt, since they legitimately arrive fast.
- Sign in attempts are limited per address, and an account locks itself after
  repeated failures.
- Every sign in, permission change, share, upload and delete leaves an audit
  entry, whether it came from the browser, a token or a WebDAV mount.
- `X-Storix-CSRF` matters only for cookie calls. A request carrying an
  `Authorization` header is not checked for it.
