<!-- Pull request template for Storix. Developed by X Project. -->

## What changed and why

<!-- A few sentences of prose. The why matters more than the what, the diff
     already shows the what. Link the issue if there is one. -->

## Does it touch path handling or authentication

<!-- Answer even if the answer is no. If yes, say which paths reach the file
     system and through which vfs call, and what an account without permission
     sees. These changes are reviewed line by line. -->

- [ ] It touches path handling, sharing or authentication
- [ ] All file access still goes through `internal/vfs` and its `os.Root` handle

## How it was verified

<!-- The commands you ran, the case you tried in the interface, the client you
     tested WebDAV with. Name the new test if there is one. -->

## Checks

- [ ] `make check` passes: `go vet`, `go test ./...` and the TypeScript check
- [ ] A bug fix here comes with a test that fails without it
- [ ] No em-dashes and no emoji, in the code, the copy or the commit messages
- [ ] Docs updated if behaviour or an endpoint changed, `README.md`,
      `docs/ARCHITECTURE.md`, `docs/API.md` or `docs/WEBDAV.md`
