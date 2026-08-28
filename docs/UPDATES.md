# The update check

Storix checks whether a newer version has been published. That check is also
how the project knows roughly how many servers are running it, because there
is no other way to tell: the software is downloaded once and then lives on
machines nobody else can see.

This page says exactly what is sent, what is kept, and how to turn it off.

Developed by X Project.

## What is sent

One request, to `https://updates.xproject.live/v1/check`, carrying five
values, and this is the whole of it:

```json
{
  "instance": "9f2c41ab7d5e40c8b3a16f0e2d7c8a95",
  "version":  "1.3.0",
  "os":       "linux",
  "arch":     "amd64",
  "channel":  "stable"
}
```

- **instance** is 32 random hex characters, generated on this server the first
  time Storix starts and stored in its own database. It is not derived from
  anything: not the host name, not the address, not the licence, not the
  hardware. It exists only so two checks from the same server are not counted
  as two servers.
- **version**, **os** and **arch** are what the binary already advertises in
  its user agent when it talks to GitHub.
- **channel** is stable or beta, so the answer names a release this server
  would actually install.

That is the whole payload. There is no account name, no folder, no file name,
no path, no size, no user count, no host name and no address.

## What is kept

The receiving service records one row per instance, holding exactly what was
sent and nothing derived from the connection: the identifier, the last version,
platform and channel it reported, when it was first seen, when it was last
seen, and how many times it has checked. A later check from the same instance updates that row rather than adding
another.

**Client addresses are not recorded.** They are not written to the access log,
not stored in the database and not kept in memory beyond the request. The
service that receives this is in this repository, under `cmd/storix-updates`,
so the claim can be read rather than taken on faith.

Rows that have not been seen for 180 days are deleted.

## What it is used for

Counting running servers, seeing which versions are still out there so an old
one is not broken carelessly, and deciding whether a platform is worth
building for. Nothing else, and nothing is shared with anyone.

## Turning it off

In Settings, Updates, clear "Check for new versions". Or in
`/etc/storix/config.yaml`:

```yaml
updates:
  check: false
```

With the check off, Storix never contacts the update service, and the
interface stops offering to update. `sudo storix update` still works when you
run it yourself, because at that point you have asked for it.

To keep the update check but send nothing countable, point it back at GitHub:

```yaml
updates:
  endpoint: "https://api.github.com/repos/XProject25/Storix/releases/latest"
```

Storix falls back to that address on its own if the update service cannot be
reached, so a check never fails just because one host is down.

## When it runs

At most once every six hours, and only while the server is running. It happens
on its own, not only when somebody opens the interface, because a server that
is quietly doing its job should still learn that a release exists. The first
check is delayed by a random part of an hour and later ones are spread a
little, so a fleet upgraded in one afternoon does not all knock at the same
second.

It is a single request with a short timeout, and a failure is silent: an
update check that cannot reach anything is not an error worth waking anyone
for. With the check switched off, the loop does not run at all.
