# storix-updates

The service that answers the Storix update check and counts running servers.

Storix asks whether a newer version has been published. That question is also
the only way this project can tell how many servers are running it, because the
software is downloaded once and then lives on machines nobody else can see.
This service receives that question. It is in the repository so the claims in
`docs/UPDATES.md` can be read rather than taken on faith.

Developed by X Project.

## The protocol

```
POST https://updates.xproject.live/v1/check
Content-Type: application/json
```

Request, and these four values are the whole of it:

```json
{
  "instance": "9f2c41ab7d5e40c8b3a16f0e2d7c8a95",
  "version":  "1.3.0",
  "os":       "linux",
  "arch":     "amd64",
  "channel":  "stable"
}
```

Answer, when something newer exists:

```json
{
  "version":     "1.3.1",
  "notes":       "...",
  "url":         "https://github.com/XProject25/Storix/releases/tag/v1.3.1",
  "asset":       "storix_1.3.1_linux_amd64.tar.gz",
  "assetUrl":    "https://github.com/.../storix_1.3.1_linux_amd64.tar.gz",
  "checksumUrl": "https://github.com/.../checksums.txt",
  "publishedAt": "2026-08-26T12:00:00Z"
}
```

Answer, when the caller already has the newest version:

```json
{ "version": "1.3.0" }
```

Release data comes from the GitHub API and is held in memory for five minutes,
so a thousand servers checking in are one call to GitHub. If GitHub cannot be
reached the last known release is served. If nothing is known yet the answer is
503, never "you are current", so the caller falls back to the GitHub release
API rather than believing it is up to date.

Any non 200, any timeout and any malformed body sends the Storix client to that
same fallback, so an update check never fails because one host is down.

## What it stores

One row per instance:

| column                  | what it is                                  |
| ----------------------- | ------------------------------------------- |
| `instance`              | the 32 hex characters the caller sent        |
| `version`, `os`, `arch` | what that instance last reported             |
| `channel`               | the release track it follows                 |
| `first_seen`            | when it was first counted                    |
| `last_seen`             | when it last checked in                      |
| `checks`                | how many checks that instance has made       |

A later check from the same instance updates that row rather than adding
another. Rows unseen for the retention period, 180 days by default, are deleted
by a janitor that runs daily.

## What it refuses to store

No client address. Not in the database, not in a log line, not in a struct that
outlives the request. There is no `RemoteAddr` in this program, no
`X-Forwarded-For`, and no access log at all. The standard library server log is
silenced for the same reason: its lines carry addresses.

The instance identifier is never logged either. Log output is counts and
errors: a startup line, a summary of totals every six hours, and a line when
the janitor deletes something.

Nothing beyond the five fields above is read from the request. Unknown fields
in the body are ignored rather than kept.

## What an anonymous caller can spend

This service answers anybody on the internet, and it cannot tell a real Storix
server from an invented one: the identifier is a random number, and demanding
proof of anything would mean collecting something about the caller, which is
exactly what the promise above rules out. So the question is not how to refuse
forgeries, it is how to bound what they cost. Read the count as a good estimate
rather than an audited figure.

| what a caller controls | what bounds it                                        |
| ---------------------- | ----------------------------------------------------- |
| body size              | 4 KB, refused with 413 before it is parsed             |
| field lengths          | fixed caps per field, anything longer is a 400         |
| the channel            | an allowlist of `stable` and `beta`, nothing else      |
| new rows               | a bucket of 500, refilling at 200 an hour              |
| total rows             | a ceiling of 250,000                                   |
| calls to GitHub        | one per channel per five minutes, whatever the traffic |

A server the service already knows is never held to the bucket: it owns a row
already, so it only updates it. The bucket applies to identifiers that have
never been seen, which is the only thing an anonymous caller can make this
service spend. A refused caller is not told off and is not an error: it still
gets its answer about the newest release, it simply is not counted.
`refusedNew` in the statistics is how the owner notices somebody is inventing
servers. Zero is the normal reading.

Put a rate limit in front of it as well. The proxy sees the address and this
service never does, so a limit there costs nothing in privacy terms. The
samples below include one.

## Build and run

```
go build -o storix-updates ./cmd/storix-updates
```

| flag         | default              | meaning                                 |
| ------------ | -------------------- | --------------------------------------- |
| `-addr`      | `127.0.0.1:8787`     | listen address, meant to sit behind nginx |
| `-db`        | `/var/lib/storix-updates/updates.db` | SQLite file             |
| `-repo`      | `XProject25/Storix`  | where releases are published            |
| `-retention` | `180`                | delete instances unseen for this many days |

`STORIX_UPDATES_TOKEN` enables `/v1/stats`. Without it that route is refused
entirely rather than served openly.

`GET /healthz` answers `ok` for a monitor. It needs no token.

## The page

`/v1/stats` is the figure, but reading it means remembering a token and piping
it through something that formats it. `GET /dashboard?k=<key>` is the version
you bookmark: how many servers are running, how many were seen today, this week
and this month, and the split by version and platform.

It is gated by its own key, `STORIX_UPDATES_VIEW_KEY`, not by the statistics
token, so a link sitting in a browser history or pasted to somebody never
carries the credential that reads the API. Without that variable the page
answers 503, and a wrong key gets the same plain 404 as any unknown address, so
the page does not announce that it exists. The key is compared in constant
time.

The page is one document: no script, no font and no image loaded from
anywhere, under a content policy that forbids it. Every label on it comes from
anonymous callers, so all of it is escaped, and there is a test that writes
`<script>` into the table and checks the page comes back with it inert.

## Statistics

```
curl -H "Authorization: Bearer $STORIX_UPDATES_TOKEN" https://updates.xproject.live/v1/stats
```

Totals, activity over 24 hours, 7 days and 30 days, a breakdown by version and
by platform, and the first and last time anything was seen. No identifier
appears in the answer.

## systemd

`/etc/systemd/system/storix-updates.service`:

```ini
[Unit]
Description=Storix update service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=storix-updates
Group=storix-updates
Environment=STORIX_UPDATES_TOKEN=replace-with-a-long-random-string
ExecStart=/usr/local/bin/storix-updates -addr 127.0.0.1:8787 -db /var/lib/storix-updates/updates.db
Restart=on-failure
RestartSec=5

StateDirectory=storix-updates
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/storix-updates

[Install]
WantedBy=multi-user.target
```

```
sudo useradd --system --no-create-home --shell /usr/sbin/nologin storix-updates
sudo systemctl enable --now storix-updates
```

Put the token in a file only root can read if you would rather not have it in
the unit: `EnvironmentFile=/etc/storix-updates.env`, mode 0600.

## nginx

```nginx
# At http level. The address is used to make a decision and then discarded,
# which is nginx's business, not this service's.
limit_req_zone $binary_remote_addr zone=storixcheck:10m rate=1r/s;

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name updates.xproject.live;

    ssl_certificate     /etc/letsencrypt/live/updates.xproject.live/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/updates.xproject.live/privkey.pem;

    # Nothing here needs a client address, so none is recorded or passed on.
    access_log off;

    client_max_body_size 64k;

    location = /v1/check {
        limit_req zone=storixcheck burst=5 nodelay;
        proxy_pass http://127.0.0.1:8787;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_read_timeout 30s;
    }

    location = /v1/stats {
        proxy_pass http://127.0.0.1:8787;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header Authorization $http_authorization;
    }

    location = /healthz {
        proxy_pass http://127.0.0.1:8787;
    }

    location / {
        return 404;
    }
}

server {
    listen 80;
    listen [::]:80;
    server_name updates.xproject.live;
    return 301 https://$host$request_uri;
}
```

The usual `proxy_set_header X-Real-IP` and `X-Forwarded-For` lines are left out
on purpose. The service would not read them, and they should not be sent.

## Apache

The same thing where Apache already holds the certificates:

```apache
<VirtualHost *:443>
    ServerName updates.xproject.live

    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/updates.xproject.live/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/updates.xproject.live/privkey.pem

    # Nothing here needs a client address, so none is recorded or passed on.
    CustomLog /dev/null common

    # mod_ratelimit shapes bandwidth, not requests, so the request limit comes
    # from mod_qos or mod_evasive where they are available. Without one, the
    # bucket inside the service is what bounds the damage.
    ProxyPreserveHost On
    ProxyPass        /v1/check  http://127.0.0.1:8787/v1/check
    ProxyPassReverse /v1/check  http://127.0.0.1:8787/v1/check
    ProxyPass        /v1/stats  http://127.0.0.1:8787/v1/stats
    ProxyPassReverse /v1/stats  http://127.0.0.1:8787/v1/stats
    ProxyPass        /healthz   http://127.0.0.1:8787/healthz

    <Location />
        Require all denied
    </Location>
    <Location /v1/check>
        Require all granted
    </Location>
    <Location /v1/stats>
        Require all granted
    </Location>
    <Location /healthz>
        Require all granted
    </Location>
</VirtualHost>
```

It needs `a2enmod proxy proxy_http ssl` and, because Apache forwards the
authorization header only when told to, `a2enmod headers` is not required but
`SetEnvIf` rules that strip it must not be present, or the statistics endpoint
will answer 401 through the proxy while working on localhost.

## Tests

```
go test ./cmd/storix-updates/...
```
