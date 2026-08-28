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
