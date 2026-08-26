# Contributing to Storix

Storix is a web file manager for Linux servers: one Go binary that serves the
JSON API and the embedded interface, with SQLite behind it. It runs as a
service with read and write access to real files on somebody's server.

That is the one rule that shapes everything here. A change that touches path
handling is held to a higher standard than the rest of the codebase and is read
line by line. Everything else gets ordinary review.

Developed by X Project.

## Building

Go 1.25 or newer and Node 20 or newer. CI builds with Go 1.27 and Node 22.

```bash
git clone https://github.com/XProject25/Storix.git
cd Storix
make build
```

`make build` is two steps. `make web` installs the npm dependencies if
`web/node_modules` is missing, then compiles the interface with Vite into
`internal/web/dist`. `make backend` compiles `./cmd/storix`, which embeds that
folder through `//go:embed all:dist` in `internal/web/embed.go` and stamps the
version, commit and date into `internal/build`. The result is `./storix`.

The built interface is not committed. A placeholder `internal/web/dist/index.html`
is, so `go build` and `go test` work in a fresh clone without running the web
build first.

```bash
make dev
```

Runs the API on port 8686 against `./storix.dev.yaml` and `./.devdata`, and the
interface on port 5173 with hot reload, with Vite proxying `/api` to the API.
`make dev` does not install npm dependencies, so run `make build` once, or
`npm ci` in `web/`, before the first `make dev`. `make clean` removes the
binary, the built interface and `./.devdata`.

```bash
make check   # go vet, go test ./... and tsc --noEmit
make fmt     # gofmt, CI fails on an unformatted file
```

CI runs the same checks, then cross compiles for linux amd64, arm64 and arm,
runs the full Vite build and shellchecks the installer scripts. `make release`
does that cross compile locally.

## Where things live

| Path | Owns |
| --- | --- |
| `cmd/storix` | the CLI: `serve`, `version`, `user`, `setup-token`, `update`, `doctor`, `config` |
| `internal/api` | HTTP handlers, routing, middleware, and the WebDAV tree at `/dav/` |
| `internal/server` | listener, TLS, graceful shutdown |
| `internal/vfs` | the guarded file system, and the only way to touch a disk |
| `internal/store` | SQLite persistence and schema migrations |
| `internal/auth` | Argon2id passwords, sessions, CSRF, TOTP, rate limiting |
| `internal/archive` | zip and tar creation and extraction, with the safety checks |
| `internal/upload` | tus 1.0.0 resumable uploads |
| `internal/thumbs` | image thumbnails and their on disk cache |
| `internal/jobs` | long running operations with progress and cancellation |
| `internal/events` | the server sent event hub |
| `internal/config` | YAML configuration, defaults, validation |
| `internal/updater` | self update from GitHub releases |
| `internal/build` | compile time version metadata |
| `internal/web` | the embedded interface assets |

The interface is `web/src`: `pages/` one screen each, `components/` the shared
parts, `layout/` the shell, `lib/api.ts` the single API client, `state/` the
stores, `styles.css` the design system.

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) carries the detail: the full route
list, the upload design, permissions, tokens and the WebDAV tree. Keep it
current when you change any of them.

## Path safety

All file access goes through `internal/vfs`. `Resolve`, `ResolveWritable` and
`ResolveChild` map a virtual path onto the most specific mount the acting
account owns and return an `*os.Root` handle plus a mount relative name. Every
read and write runs through that handle, so the kernel refuses anything that
would leave the mount, including a symlink swapped in mid operation. Three
layers apply in order: the configured deny list, the account mount list, and
`os.Root` containment.

A change that reaches the file system any other way will not be merged. In
practice: no `os.Open`, `os.ReadFile`, `os.MkdirAll` or `filepath.Join` on a
path that came in with a request, anywhere above `internal/vfs`. WebDAV is not
an exception, `internal/api/webdav.go` resolves every name through the same
layer.

`internal/vfs/vfs_test.go` is the contract. Read it before changing that
package, in particular `TestResolveStaysInsideTheMount`,
`TestDeniedPathsAreRefused`, `TestSymlinkCannotEscapeTheMount` and
`TestReadOnlyMountRefusesWrites`. If your change makes one of those fail, the
change is wrong, not the test.

## House style

Stated plainly because it is enforced in review.

- Godoc comments on exported Go symbols, starting with the name.
- Comments explain why, not what. If a line needs a comment to say what it
  does, rename something instead.
- Plain calm English in anything a user reads, error messages included. No
  marketing, no exclamation marks.
- No em-dashes and no emoji anywhere: code, comments, interface copy,
  documentation, commit messages.
- Icons are inline SVG in `web/src/components/Icon.tsx`, on the 24 by 24 grid,
  inheriting the current colour. Never an icon font, never an emoji.
- Use the design system classes in `web/src/styles.css`, `sx-panel`, `sx-btn`,
  `sx-input`, `sx-row`, `sx-menu` and the rest, rather than adding one off
  styles.
- Attribution is "Developed by X Project". Do not add personal credits.
- Unix line endings everywhere. `.gitattributes` enforces it, and a shell
  script or systemd unit with CRLF endings does not run on the target servers.

## Tests

`make check` must be clean. The suite was 156 tests at 1.2.0 and is expected to
grow, not to stay that size.

A bug fix comes with a test that fails without it. Put it in the package that
owns the behaviour: `internal/vfs` for path and file semantics, `internal/api`
for request and response behaviour including WebDAV, `internal/auth` for
credentials and sessions, `internal/store` for persistence.

## Commits and pull requests

A short subject line in plain language, a blank line, then prose that explains
why the change exists and what it costs. No conventional commit prefixes, no
`feat:`, `fix:` or `chore:`. `git log` shows the tone.

Open the pull request against `main`. The template asks what changed and why,
whether it touches path handling or authentication, and how you verified it.
Say plainly where you are unsure. A question in the description is much cheaper
than a wrong merge.

## Releases

Pushing a tag matching `v*` runs `.github/workflows/release.yml`, which builds
the interface, cross compiles for linux amd64, arm64 and arm, writes
`checksums.txt` and publishes them to the GitHub release. `scripts/install.sh`
and `storix update` read those assets, so a release that does not publish them
leaves both of those broken. Release notes live in
[CHANGELOG.md](CHANGELOG.md).

## Reporting problems

Use the issue templates. A bug report without the version, the distribution and
the `storix doctor` output usually cannot be acted on.

Security problems go to the advisory page named in [SECURITY.md](SECURITY.md),
never to the issue tracker.
